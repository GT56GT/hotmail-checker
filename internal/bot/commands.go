package bot

import (
	"fmt"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

const usageText = `Send me Microsoft cookies and I'll tell you which ones still work.

*How to send*
• Paste cookie string(s), one account per line
• Or upload a .txt file

*Accepted formats*
• Raw header — ` + "`MSPAuth=…; MSPProf=…; ANON=…`" + `
• JSON export — ` + "`{\"MSPAuth\":\"…\", …}`" + `
• Netscape cookies.txt — one file per account

*Required cookies*
MSPAuth · MSPProf · ANON

*Commands*
/start — this message
/cancel — stop a running job
/stats — live service status

Results arrive as two files: working cookies only, and a full report.`

const maxDocBytes = 5 << 20

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

func (b *Bot) cmdCancel(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if user == nil {
		return nil
	}

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
