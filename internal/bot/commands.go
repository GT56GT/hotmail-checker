package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const usageText = `Send me Microsoft cookies and I'll tell you which ones still work.

*How to send*
• Paste cookie string(s), one account per line
• Or upload a .txt file
• No batch limit — send as many as you want

*Accepted formats*
• Raw header — ` + "`MSPAuth=…; MSPProf=…; ANON=…`" + `
• JSON export — ` + "`{\"MSPAuth\":\"…\", …}`" + `
• Netscape cookies.txt — one file per account

*Required cookies*
MSPAuth · MSPProf · ANON

*Inbox search*
` + "`/search invoice`" + ` — then send cookies to search every live account's inbox for that keyword.

*Your own proxies*
` + "`/proxies add`" + ` — then paste your list. All formats accepted. Your checks run through your proxies.

*Commands*
/start — this message
/search <term> — arm an inbox search
/proxies — manage your proxy list
/cancel — stop a running job
/stats — live service status`

const searchUsage = `*Inbox search*

` + "`/search <keyword>`" + `

Then send the cookie(s). Every live account in that batch is searched and you get back matching messages — subject, sender, date, preview and a link.

Search runs after the check, so dead cookies cost nothing.`

const proxiesUsage = `*Your proxies*

` + "`/proxies add`" + ` — paste a list (all formats)
` + "`/proxies list`" + ` — see what's saved
` + "`/proxies test`" + ` — check reachability and latency
` + "`/proxies del <n>`" + ` — remove one by index
` + "`/proxies clear`" + ` — remove all

*Accepted formats*
` + "```" + `
host:port
host:port:user:pass
user:pass@host:port
socks5://host:port
socks5://user:pass@host:port
http://host:port
https://host:port
[2001:db8::1]:8080
` + "```" + `
Separate with newlines, commas or semicolons. Comments with ` + "`#`" + ` are ignored.

Your checks use your proxies. Without them, the shared pool is used.`

const maxDocBytes = 20 << 20

func (b *Bot) cmdStart(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	_, err := m.Reply(bot, usageText, &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
			IsDisabled: true,
		},
	})
	return err
}

func (b *Bot) cmdSearch(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if m == nil || user == nil {
		return nil
	}
	if b.cfg.IsBanned(user.Id) {
		return nil
	}

	body := commandBody(m.Text, "/search")
	if body == "" {
		_, err := m.Reply(bot, searchUsage, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return err
	}

	// Inline form: "/search term" followed by cookies on the next lines.
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		term := strings.TrimSpace(body[:idx])
		rest := strings.TrimSpace(body[idx+1:])
		if term != "" && looksLikeCookie(rest) {
			return b.startJob(bot, m, user.Id, rest, term)
		}
	}

	term := strings.TrimSpace(strings.SplitN(body, "\n", 2)[0])
	if term == "" {
		_, err := m.Reply(bot, searchUsage, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return err
	}
	if runes := []rune(term); len(runes) > b.cfg.MaxSearchTermLen {
		term = string(runes[:b.cfg.MaxSearchTermLen])
	}

	b.setPending(user.Id, pending{kind: pendingSearch, term: term})

	_, err := m.Reply(bot, fmt.Sprintf(
		"Search armed for \"%s\".\n\nNow send the cookie(s). Only live accounts are searched.",
		term), nil)
	return err
}

func (b *Bot) cmdCancel(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if user == nil {
		return nil
	}

	b.clearPending(user.Id)

	if !b.reg.Cancel(user.Id) {
		_, err := m.Reply(bot, "Nothing is running.", nil)
		return err
	}
	_, err := m.Reply(bot, "Cancelling…", nil)
	return err
}

func (b *Bot) cmdStats(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if m == nil || user == nil {
		return nil
	}

	ctxBg := context.Background()
	var sb strings.Builder
	sb.WriteString("*Service status*\n\n")
	fmt.Fprintf(&sb, "Active jobs: `%d`\n", b.reg.Count())
	fmt.Fprintf(&sb, "Concurrency: `%d`\n", b.cfg.Concurrency)

	if b.cfg.MaxCookiesPerJob > 0 {
		fmt.Fprintf(&sb, "Batch limit: `%d` accounts\n", b.cfg.MaxCookiesPerJob)
	} else {
		sb.WriteString("Batch limit: `none`\n")
	}

	globalTotal, globalHealthy := b.pools.Global().Stats()
	fmt.Fprintf(&sb, "Shared proxies: `%d` healthy / `%d` total\n", globalHealthy, globalTotal)

	own := b.pools.URLs(ctxBg, user.Id)
	if len(own) > 0 {
		fmt.Fprintf(&sb, "Your proxies: `%d` configured\n", len(own))
	} else {
		sb.WriteString("Your proxies: `none` — using shared pool\n")
	}

	if !b.pools.Persistent() {
		sb.WriteString("\n_No database configured — proxy lists reset on restart._\n")
	}

	_, err := m.Reply(bot, sb.String(), &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
			IsDisabled: true,
		},
	})
	return err
}

// --- pending state -------------------------------------------------------

func (b *Bot) setPending(userID int64, p pending) {
	p.expires = time.Now().Add(5 * time.Minute)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[userID] = p
}

// takePending consumes whatever is armed for this user, if not expired.
func (b *Bot) takePending(userID int64) pending {
	b.mu.Lock()
	defer b.mu.Unlock()

	p, ok := b.pending[userID]
	if !ok {
		return pending{kind: pendingNone}
	}
	delete(b.pending, userID)

	if time.Now().After(p.expires) {
		return pending{kind: pendingNone}
	}
	return p
}

func (b *Bot) clearPending(userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, userID)
}

// commandBody strips the command name and an optional @botname suffix.
func commandBody(text, cmd string) string {
	if !strings.HasPrefix(text, cmd) {
		return ""
	}
	body := strings.TrimSpace(text[len(cmd):])
	if strings.HasPrefix(body, "@") {
		if sp := strings.IndexFunc(body, func(r rune) bool { return r == ' ' || r == '\n' }); sp >= 0 {
			body = strings.TrimSpace(body[sp+1:])
		} else {
			body = ""
		}
	}
	return body
}
