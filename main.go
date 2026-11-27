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
	"strings"
	"syscall"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/bot"
	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/config"
	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/proxy"
	"github.com/GT56GT/hotmail-checker/internal/store"
)

func main() {
	selftest := flag.Bool("selftest", false, "run one diagnostic request and exit")
	dbcheck := flag.Bool("dbcheck", false, "verify DATABASE_URL and the schema, then exit")
	flag.Parse()

	if *selftest {
		runSelftest()
		return
	}
	if *dbcheck {
		runDBCheck()
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

	// Optional persistence for user proxy lists. Without it, lists live only
	// for the life of this process.
	var persistence proxy.Store
	if cfg.DatabaseURL != "" {
		pg, err := store.OpenPostgres(ctx, cfg.DatabaseURL)
		if err != nil {
			slog.Error("database", "err", err)
			os.Exit(1)
		}
		defer pg.Close()
		persistence = pg
		slog.Info("database connected — user proxies persist across restarts")
	} else {
		slog.Warn("DATABASE_URL not set — user proxy lists reset on restart")
	}

	pools := proxy.NewManager(4, persistence)

	if len(cfg.Proxies) > 0 {
		pools.Global().Load(cfg.Proxies)
		slog.Info("shared proxies loaded from env", "count", pools.Global().Len())
	}

	if cfg.ProxyListURL != "" {
		if err := pools.Global().Fetch(ctx, cfg.ProxyListURL); err != nil {
			slog.Warn("initial shared proxy fetch failed", "err", err)
		}
		go pools.Global().RefreshLoop(ctx, cfg.ProxyListURL, cfg.ProxyRefresh)
	}

	reg := job.NewRegistry(cfg.Cooldown)
	chk := checker.New(cfg.Timeout, cfg.Concurrency)

	b, err := bot.New(cfg, chk, pools, reg)
	if err != nil {
		slog.Error("bot init", "err", err)
		os.Exit(1)
	}

	slog.Info("starting",
		"concurrency", cfg.Concurrency,
		"shared_proxies", pools.Global().Len(),
		"persistence", pools.Persistent(),
	)
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

	// Exercise the search path too. With an invalid cookie it must degrade to
	// "search unavailable" rather than panic — that proves the token chain and
	// both API decoders are wired.
	fmt.Println("\nissuing one inbox search (expected: unavailable)…")
	sres := chk.Search(ctx, ck, "", "invoice", 5)
	fmt.Printf("method: %s\nitems:  %d\nreason: %s\nelapsed: %s\n",
		orNone(sres.Method), len(sres.Items), orNone(sres.Reason), sres.Elapsed.Round(time.Millisecond))
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// runDBCheck verifies DATABASE_URL end to end: connect, migrate, query.
//
//	heroku run --type=worker bin/hotmail-checker -dbcheck
func runDBCheck() {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fmt.Println("DATABASE_URL is not set")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	pg, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		fmt.Printf("connect failed: %v\n", err)
		os.Exit(1)
	}
	defer pg.Close()

	n, err := pg.Count(ctx)
	if err != nil {
		fmt.Printf("query failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("connected, schema applied")
	fmt.Printf("user_proxies rows: %d\n", n)
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
