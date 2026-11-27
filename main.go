// Command hotmail-checker runs a Telegram bot that validates Microsoft
// session cookies and returns the ones that still authenticate.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/bot"
	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/config"
	"github.com/GT56GT/hotmail-checker/internal/instagram"
	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/netflix"
	"github.com/GT56GT/hotmail-checker/internal/provider"
	"github.com/GT56GT/hotmail-checker/internal/proxy"
	"github.com/GT56GT/hotmail-checker/internal/store"
)

func main() {
	selftest := flag.Bool("selftest", false, "run one diagnostic request and exit")
	dbcheck := flag.Bool("dbcheck", false, "verify DATABASE_URL and the schema, then exit")
	checkPath := flag.String("check", "", "read cookies from a file (or - for stdin), check them and exit")
	flag.Parse()

	if *selftest {
		runSelftest()
		return
	}
	if *dbcheck {
		runDBCheck()
		return
	}
	if *checkPath != "" {
		runCheck(*checkPath)
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

	var checkers []provider.Checker
	for _, name := range cfg.Providers {
		switch name {
		case "microsoft":
			checkers = append(checkers, checker.New(cfg.Timeout, cfg.Concurrency))
		case "netflix":
			nf := netflix.New(cfg.Timeout)
			nf.LoginLinks = cfg.NFLoginLinks
			checkers = append(checkers, nf)
		case "instagram":
			checkers = append(checkers, instagram.New(cfg.Timeout))
		default:
			slog.Warn("unknown provider, ignoring", "name", name)
		}
	}
	if len(checkers) == 0 {
		slog.Error("no providers enabled — set PROVIDERS", "value", cfg.Providers)
		os.Exit(1)
	}

	b, err := bot.New(cfg, checkers, pools, reg)
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

	// Netflix: an invalid cookie must land on /login and read as dead.
	fmt.Println("\nissuing one Netflix check (expected: dead)…")
	nf := netflix.New(25 * time.Second)
	nf.LoginLinks = false
	nres := nf.Check(ctx, &provider.Cookie{Values: map[string]string{
		"NetflixId":       "selftest-invalid",
		"SecureNetflixId": "selftest-invalid",
	}}, "")
	fmt.Printf("verdict: %s\nreason:  %s\nelapsed: %s\n",
		nres.Verdict, orNone(nres.Reason), nres.Elapsed.Round(time.Millisecond))

	// Instagram: an invalid sessionid must redirect to /accounts/login.
	fmt.Println("\nissuing one Instagram check (expected: dead)…")
	ig := instagram.New(25 * time.Second)
	ires := ig.Check(ctx, &provider.Cookie{Values: map[string]string{
		"sessionid":  "selftest-invalid",
		"ds_user_id": "0",
		"csrftoken":  "selftestcsrftoken",
	}}, "")
	fmt.Printf("verdict: %s\nreason:  %s\nelapsed: %s\n",
		ires.Verdict, orNone(ires.Reason), ires.Elapsed.Round(time.Millisecond))
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// runCheck runs one real check against cookies in a file. Useful for verifying
// a single account without going through the bot:
//
//	bin/hotmail-checker -check cookies.txt
//	cat cookies.txt | bin/hotmail-checker -check -
func runCheck(path string) {
	var data []byte
	var err error

	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fmt.Printf("read: %v\n", err)
		os.Exit(1)
	}

	timeout := 25 * time.Second
	checkers := []provider.Checker{
		checker.New(timeout, 1),
		netflix.New(timeout),
		instagram.New(timeout),
	}

	text := string(data)

	var chk provider.Checker
	for _, c := range checkers {
		if c.Detect(text) {
			chk = c
			break
		}
	}
	if chk == nil {
		fmt.Println("no provider recognised these cookies")
		os.Exit(1)
	}

	cookies, issues := chk.ParseBlock(text)
	for _, issue := range issues {
		fmt.Printf("issue: %v\n", issue)
	}
	if len(cookies) == 0 {
		fmt.Println("no cookie sets parsed")
		os.Exit(1)
	}

	fmt.Printf("provider: %s\n", chk.Label())
	fmt.Printf("parsed:   %d cookie set(s)\n", len(cookies))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for i, ck := range cookies {
		fmt.Printf("\n--- set %d ---\n", i+1)
		res := chk.Check(ctx, ck, "")

		fmt.Printf("verdict: %s\n", res.Verdict)
		if res.Email != "" {
			fmt.Printf("email:   %s\n", res.Email)
		}
		if res.Name != "" {
			fmt.Printf("name:    %s\n", res.Name)
		}
		if res.Reason != "" {
			fmt.Printf("reason:  %s\n", res.Reason)
		}
		fmt.Printf("elapsed: %s\n", res.Elapsed.Round(time.Millisecond))

		keys := make([]string, 0, len(res.Meta))
		for k := range res.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-13s %s\n", k, res.Meta[k])
		}
	}
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
