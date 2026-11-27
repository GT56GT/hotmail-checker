package bot

import (
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
• Up to 2000 accounts per job

*Accepted formats*
• Raw header — ` + "`MSPAuth=…; MSPProf=…; ANON=…`" + `
• JSON export — ` + "`{\"MSPAuth\":\"…\", …}`" + `
• Netscape cookies.txt — one file per account

*Required cookies*
MSPAuth · MSPProf · ANON

*Inbox search*
` + "`/search invoice`" + ` — then send cookies to search every live account's inbox for that keyword. Results include subject, sender, date and a preview.

*Commands*
/start — this message
/search <term> — arm an inbox search
/cancel — stop a running job
/stats — live service status

Results arrive as files: working cookies, a full check report, and a search report when searching.`

const searchUsage = `*Inbox search*

` + "`/search <keyword>`" + `

Then send the cookie(s). Every live account in that batch is searched and you get back matching messages — subject, sender, date, preview and a link.

Search runs after the check, so dead cookies cost nothing.`

const maxDocBytes = 10 << 20

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
		_, err := m.Reply(bot, searchUsage, &gotgbot.SendMessageOpts{
			ParseMode: "Markdown",
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
				IsDisabled: true,
			},
		})
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

	b.setPending(user.Id, term)

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
	total, healthy := b.pool.Stats()

	var sb strings.Builder
	sb.WriteString("*Service status*\n\n")
	fmt.Fprintf(&sb, "Active jobs: `%d`\n", b.reg.Count())
	fmt.Fprintf(&sb, "Concurrency: `%d`\n", b.cfg.Concurrency)
	fmt.Fprintf(&sb, "Batch limit: `%d` accounts\n", b.cfg.MaxCookiesPerJob)
	fmt.Fprintf(&sb, "Search per job: `%d` accounts\n", b.cfg.MaxSearchAccounts)
	fmt.Fprintf(&sb, "Proxies: `%d` healthy / `%d` total\n", healthy, total)
	if total == 0 {
		sb.WriteString("\n_No proxies configured — throughput will be limited._\n")
	}

	_, err := m.Reply(bot, sb.String(), &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
			IsDisabled: true,
		},
	})
	return err
}

func (b *Bot) cmdProxies(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if user == nil || !b.cfg.IsAdmin(user.Id) {
		return nil
	}

	total, healthy := b.pool.Stats()
	text := fmt.Sprintf("*Proxy pool*\n\nTotal `%d`\nHealthy `%d`\nQuarantined `%d`",
		total, healthy, total-healthy)

	_, err := m.Reply(bot, text, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
	return err
}

// --- pending search state -----------------------------------------------

func (b *Bot) setPending(userID int64, term string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[userID] = pendingSearch{term: term, expires: time.Now().Add(5 * time.Minute)}
}

// takePending consumes the armed search term, if any and not expired.
func (b *Bot) takePending(userID int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	p, ok := b.pending[userID]
	if !ok {
		return ""
	}
	delete(b.pending, userID)

	if time.Now().After(p.expires) {
		return ""
	}
	return p.term
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
