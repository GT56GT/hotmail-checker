// Command hotmail-checker runs a Telegram bot that validates Microsoft
// session cookies and returns the ones that still authenticate.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/bot"
	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/config"
	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/proxy"
)

func main() {
	selftest := flag.Bool("selftest", false, "run one diagnostic request and exit")
	flag.Parse()

	if *selftest {
		runSelftest()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	setupLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := proxy.New(4)
	if len(cfg.Proxies) > 0 {
		pool.Load(cfg.Proxies)
		slog.Info("proxies loaded from env", "count", len(cfg.Proxies))
	}

	if cfg.ProxyListURL != "" {
		if err := pool.Fetch(ctx, cfg.ProxyListURL); err != nil {
			slog.Warn("initial proxy fetch failed", "err", err)
		}
		go pool.RefreshLoop(ctx, cfg.ProxyListURL, cfg.ProxyRefresh)
	}

	if pool.Len() == 0 {
		slog.Warn("no proxies configured — checks run direct and will be rate limited quickly")
	}

	reg := job.NewRegistry(cfg.Cooldown)
	chk := checker.New(cfg.Timeout, cfg.Concurrency)

	b, err := bot.New(cfg, chk, pool, reg)
	if err != nil {
		slog.Error("bot init", "err", err)
		os.Exit(1)
	}

	slog.Info("starting", "concurrency", cfg.Concurrency, "proxies", pool.Len())
	if err := b.Run(ctx); err != nil {
		slog.Error("bot run", "err", err)
		os.Exit(1)
	}
	slog.Info("stopped")
}

// runSelftest proves the TLS impersonation layer reaches Microsoft. With a
// deliberately invalid cookie a working stack returns a redirect to signin
// (dead). A 403 or handshake failure means the fingerprint is wrong.
//
//	heroku run --type=worker bin/hotmail-checker -selftest
func runSelftest() {
	chk := checker.New(25*time.Second, 1)
	ck := &checker.Cookie{Values: map[string]string{
		"MSPAuth": "selftest-invalid",
		"MSPProf": "selftest-invalid",
		"ANON":    "selftest-invalid",
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	fmt.Println("issuing one check against outlook.live.com (direct, no proxy)…")
	res := chk.Check(ctx, ck, "")

	fmt.Printf("verdict: %s\n", res.Verdict)
	if res.Reason != "" {
		fmt.Printf("reason:  %s\n", res.Reason)
	}
	fmt.Printf("elapsed: %s\n", res.Elapsed.Round(time.Millisecond))

	switch res.Verdict {
	case checker.Dead:
		fmt.Println("\nOK — TLS impersonation works and the endpoint answered.")
	default:
		fmt.Println("\nWARNING — expected dead (redirect to signin). Investigate before deploying.")
	}
}

func setupLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: l,
	})))
}
