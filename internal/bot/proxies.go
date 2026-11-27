package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/proxy"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const (
	proxyTestTarget     = "https://login.live.com/"
	proxyTestTimeout    = 15 * time.Second
	proxyTestWorkers    = 5
	maxProxiesDisplayed = 60
)

func (b *Bot) cmdProxies(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if m == nil || user == nil {
		return nil
	}
	if b.cfg.IsBanned(user.Id) {
		return nil
	}

	sub, rest := splitFirstWord(commandBody(m.Text, "/proxies"))

	switch sub {
	case "":
		return b.proxyOverview(bot, m, user.Id)
	case "add":
		if rest == "" {
			b.setPending(user.Id, pending{kind: pendingProxyAdd})
			_, err := m.Reply(bot,
				"Send your proxy list now — paste it or upload a .txt.\n\nAll formats accepted. Separate with newlines, commas or semicolons.",
				nil)
			return err
		}
		return b.saveProxies(bot, m, user.Id, rest)
	case "list", "ls":
		return b.proxyList(bot, m, user.Id)
	case "del", "remove", "rm":
		return b.proxyDel(bot, m, user.Id, rest)
	case "clear", "reset":
		return b.proxyClear(bot, m, user.Id)
	case "test":
		return b.proxyTest(bot, m, user.Id)
	default:
		_, err := m.Reply(bot, proxiesUsage, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return err
	}
}

func (b *Bot) proxyOverview(bot *gotgbot.Bot, m *gotgbot.Message, userID int64) error {
	own := b.pools.URLs(context.Background(), userID)
	globalTotal, globalHealthy := b.pools.Global().Stats()

	var sb strings.Builder
	sb.WriteString("*Your proxies*\n\n")

	if len(own) > 0 {
		fmt.Fprintf(&sb, "Saved: `%d`\n", len(own))
	} else {
		sb.WriteString("Saved: `0` — checks use the shared pool\n")
	}

	fmt.Fprintf(&sb, "Shared pool: `%d` healthy / `%d` total\n", globalHealthy, globalTotal)

	if own != nil && !b.pools.Persistent() {
		sb.WriteString("\n_No database configured — your list resets when the bot restarts._\n")
	}

	sb.WriteString("\n" + proxiesUsage)

	_, err := m.Reply(bot, sb.String(), &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
			IsDisabled: true,
		},
	})
	return err
}

func (b *Bot) saveProxies(bot *gotgbot.Bot, m *gotgbot.Message, userID int64, text string) error {
	parsed, rejected := proxy.ParseList(text)
	if len(parsed) == 0 {
		msg := "No valid proxies found."
		if len(rejected) > 0 {
			msg += "\n\nRejected:\n" + previewLines(rejected, 3)
		}
		msg += "\n\n" + proxiesUsage
		_, err := m.Reply(bot, msg, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	existing := b.pools.URLs(ctx, userID)
	merged := mergeUnique(existing, parsed)

	truncated := false
	if len(merged) > b.cfg.MaxProxiesPerUser {
		merged = merged[:b.cfg.MaxProxiesPerUser]
		truncated = true
	}

	if err := b.pools.Save(ctx, userID, merged); err != nil {
		_, rerr := m.Reply(bot, "Could not save your proxies: "+err.Error(), nil)
		return rerr
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Saved. You now have *%d* prox%s.\n", len(merged), plural(len(merged), "y", "ies"))
	fmt.Fprintf(&sb, "Added %d new, %d duplicate(s) skipped.\n", len(merged)-len(existing), len(parsed)-len(merged)+len(existing))
	if truncated {
		fmt.Fprintf(&sb, "\nCapped at %d.\n", b.cfg.MaxProxiesPerUser)
	}
	if len(rejected) > 0 {
		fmt.Fprintf(&sb, "\n%d entr%s could not be parsed.\n", len(rejected), plural(len(rejected), "y", "ies"))
	}
	if !b.pools.Persistent() {
		sb.WriteString("\n_No database configured — this list resets when the bot restarts._\n")
	}

	_, err := m.Reply(bot, strings.TrimSpace(sb.String()), &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
	return err
}

func (b *Bot) proxyList(bot *gotgbot.Bot, m *gotgbot.Message, userID int64) error {
	list := b.pools.URLs(context.Background(), userID)
	if len(list) == 0 {
		_, err := m.Reply(bot, "No proxies saved. Use /proxies add.", nil)
		return err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "*Your proxies* (%d)\n\n", len(list))

	shown := len(list)
	if shown > maxProxiesDisplayed {
		shown = maxProxiesDisplayed
	}
	for i := 0; i < shown; i++ {
		fmt.Fprintf(&sb, "%d. `%s`\n", i+1, proxy.Label(list[i]))
	}
	if len(list) > shown {
		fmt.Fprintf(&sb, "…and %d more\n", len(list)-shown)
	}

	_, err := m.Reply(bot, sb.String(), &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
	return err
}

func (b *Bot) proxyDel(bot *gotgbot.Bot, m *gotgbot.Message, userID int64, arg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	list := b.pools.URLs(ctx, userID)
	if len(list) == 0 {
		_, err := m.Reply(bot, "No proxies saved.", nil)
		return err
	}

	var idx int
	if _, err := fmt.Sscanf(strings.TrimSpace(arg), "%d", &idx); err != nil {
		_, rerr := m.Reply(bot, "Usage: /proxies del <number>", nil)
		return rerr
	}
	if idx < 1 || idx > len(list) {
		_, err := m.Reply(bot, fmt.Sprintf("Pick a number between 1 and %d.", len(list)), nil)
		return err
	}

	removed := list[idx-1]
	next := make([]string, 0, len(list)-1)
	next = append(next, list[:idx-1]...)
	next = append(next, list[idx:]...)

	if err := b.pools.Save(ctx, userID, next); err != nil {
		_, rerr := m.Reply(bot, "Could not save: "+err.Error(), nil)
		return rerr
	}

	_, err := m.Reply(bot, fmt.Sprintf("Removed `%s`. %d left.", proxy.Label(removed), len(next)), nil)
	return err
}

func (b *Bot) proxyClear(bot *gotgbot.Bot, m *gotgbot.Message, userID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := b.pools.Delete(ctx, userID); err != nil {
		_, err := m.Reply(bot, "Could not clear: "+err.Error(), nil)
		return err
	}
	_, err := m.Reply(bot, "Your proxy list is cleared. Checks use the shared pool again.", nil)
	return err
}

func (b *Bot) proxyTest(bot *gotgbot.Bot, m *gotgbot.Message, userID int64) error {
	list := b.pools.URLs(context.Background(), userID)
	if len(list) == 0 {
		_, err := m.Reply(bot, "No proxies saved. Use /proxies add.", nil)
		return err
	}

	status, err := m.Reply(bot, fmt.Sprintf("Testing %d prox%s…", len(list), plural(len(list), "y", "ies")), nil)
	if err != nil {
		return err
	}

	type outcome struct {
		url    string
		ok     bool
		status int
		ms     int64
		err    string
	}
	results := make([]outcome, len(list))

	sem := make(chan struct{}, proxyTestWorkers)
	var wg sync.WaitGroup

	for i, raw := range list {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ok, code, ms, terr := probeProxy(raw)
			o := outcome{url: raw, ok: ok, status: code, ms: ms}
			if terr != nil {
				o.err = terr.Error()
			}
			results[i] = o
		}(i, raw)
	}
	wg.Wait()

	good, bad := 0, 0
	var sb strings.Builder
	fmt.Fprintf(&sb, "*Proxy test* — %d checked\n\n", len(results))

	shown := len(results)
	if shown > maxProxiesDisplayed {
		shown = maxProxiesDisplayed
	}
	for i := 0; i < shown; i++ {
		r := results[i]
		if r.ok {
			good++
			fmt.Fprintf(&sb, "✅ `%s` — %dms (http %d)\n", proxy.Label(r.url), r.ms, r.status)
		} else {
			bad++
			reason := r.err
			if reason == "" {
				reason = fmt.Sprintf("http %d", r.status)
			}
			fmt.Fprintf(&sb, "❌ `%s` — %s\n", proxy.Label(r.url), shortenText(reason, 60))
		}
	}
	for i := shown; i < len(results); i++ {
		if results[i].ok {
			good++
		} else {
			bad++
		}
	}

	fmt.Fprintf(&sb, "\n*%d working · %d failing*", good, bad)
	if len(results) > shown {
		fmt.Fprintf(&sb, "\n(only first %d shown)", shown)
	}

	_, _, err = status.EditText(bot, &gotgbot.EditMessageTextOpts{
		Text:      sb.String(),
		ParseMode: "Markdown",
	})
	return err
}

// probeProxy checks whether a proxy can carry traffic to Microsoft. Any HTTP
// response counts as success — even a 403 proves the proxy is working; the
// fingerprint is a separate concern.
func probeProxy(raw string) (ok bool, status int, ms int64, err error) {
	u, perr := url.Parse(raw)
	if perr != nil {
		return false, 0, 0, perr
	}

	transport := &http.Transport{
		Proxy:               http.ProxyURL(u),
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	client := &http.Client{Transport: transport, Timeout: proxyTestTimeout}

	ctx, cancel := context.WithTimeout(context.Background(), proxyTestTimeout)
	defer cancel()

	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, proxyTestTarget, nil)
	if rerr != nil {
		return false, 0, 0, rerr
	}

	start := time.Now()
	resp, derr := client.Do(req)
	ms = time.Since(start).Milliseconds()
	if derr != nil {
		return false, 0, ms, derr
	}
	defer resp.Body.Close()

	return true, resp.StatusCode, ms, nil
}

func splitFirstWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t'
	}); i >= 0 {
		return strings.ToLower(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:])
	}
	return strings.ToLower(s), ""
}

func mergeUnique(existing, add []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(add))
	out := make([]string, 0, len(existing)+len(add))
	for _, s := range append(append([]string{}, existing...), add...) {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func previewLines(lines []string, max int) string {
	var sb strings.Builder
	for i, l := range lines {
		if i >= max {
			fmt.Fprintf(&sb, "…and %d more\n", len(lines)-max)
			break
		}
		sb.WriteString("• " + l + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortenText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
