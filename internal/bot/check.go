package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/job"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func (b *Bot) onMessage(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if m == nil || user == nil {
		return nil
	}
	if b.cfg.IsBanned(user.Id) {
		return nil
	}

	text := strings.TrimSpace(m.Text)
	if !looksLikeCookie(text) {
		_, err := m.Reply(bot, usageText, &gotgbot.SendMessageOpts{
			ParseMode: "Markdown",
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
				IsDisabled: true,
			},
		})
		return err
	}

	return b.startJob(bot, m, user.Id, text)
}

func (b *Bot) onDocument(bot *gotgbot.Bot, ctx *ext.Context) error {
	m := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if m == nil || user == nil || m.Document == nil {
		return nil
	}
	if b.cfg.IsBanned(user.Id) {
		return nil
	}
	if m.Document.FileSize > maxDocBytes {
		_, err := m.Reply(bot, "File too large. Limit is 5 MB.", nil)
		return err
	}

	data, err := b.downloadFile(m.Document.FileId)
	if err != nil {
		slog.Warn("document download failed", "err", err)
		_, rerr := m.Reply(bot, "Could not download that file.", nil)
		return rerr
	}

	return b.startJob(bot, m, user.Id, string(data))
}

func (b *Bot) startJob(bot *gotgbot.Bot, m *gotgbot.Message, userID int64, text string) error {
	cookies, issues := checker.ParseBlock(text)

	if len(cookies) == 0 {
		msg := "No valid cookies found."
		if len(issues) > 0 {
			msg += "\n\n" + summariseIssues(issues)
		}
		_, err := m.Reply(bot, msg, nil)
		return err
	}

	if len(cookies) > b.cfg.MaxCookiesPerJob {
		_, err := m.Reply(bot, fmt.Sprintf(
			"Too many accounts: %d. Limit is %d per job.", len(cookies), b.cfg.MaxCookiesPerJob), nil)
		return err
	}

	j, err := b.reg.Begin(context.Background(), userID, len(cookies))
	if err != nil {
		_, rerr := m.Reply(bot, friendlyRegistryError(err), nil)
		return rerr
	}
	defer b.reg.Finish(userID)

	status, err := m.Reply(bot, fmt.Sprintf("Queued %d account(s). Starting…", len(cookies)), nil)
	if err != nil {
		return err
	}

	b.runJob(bot, j, cookies, status)
	return nil
}

func (b *Bot) runJob(bot *gotgbot.Bot, j *job.Job, cookies []*checker.Cookie, status *gotgbot.Message) {
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		b.progressLoop(bot, j, status, progressDone)
	}()

	work := make(chan *checker.Cookie)
	var workers sync.WaitGroup

	n := b.cfg.Concurrency
	if n > len(cookies) {
		n = len(cookies)
	}
	if n < 1 {
		n = 1
	}

	for i := 0; i < n; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ck := range work {
				if j.Ctx.Err() != nil {
					return
				}
				b.checkOne(j, ck)

				select {
				case <-time.After(jitter()):
				case <-j.Ctx.Done():
					return
				}
			}
		}()
	}

feed:
	for _, ck := range cookies {
		select {
		case work <- ck:
		case <-j.Ctx.Done():
			break feed
		}
	}
	close(work)
	workers.Wait()

	close(progressDone)
	progressWG.Wait()
	j.Done()

	b.deliver(bot, j, status)
}

func (b *Bot) checkOne(j *job.Job, ck *checker.Cookie) {
	ctx, cancel := context.WithTimeout(j.Ctx, b.cfg.Timeout+10*time.Second)
	defer cancel()

	entry, release, err := b.pool.Acquire(ctx)
	if err != nil {
		j.AddErr("pool: " + err.Error())
		return
	}
	defer release()

	res := b.chk.Check(ctx, ck, entry.URL)

	switch res.Verdict {
	case checker.Live:
		b.pool.Report(entry, true)
		j.AddHit(job.Hit{
			Email:  res.Email,
			Name:   res.Name,
			Cookie: ck.Header(),
			Proxy:  res.Proxy,
		})

	case checker.Dead:
		b.pool.Report(entry, true)
		j.AddDead(*ck)

	case checker.Challenged:
		// The session authenticated but needs consent or re-auth, so the proxy
		// is working — do not penalise it.
		b.pool.Report(entry, true)
		j.AddChallenged(res.Reason)

	case checker.RateLimited:
		b.pool.Report(entry, false)
		j.AddErr("rate limited")

	default:
		b.pool.Report(entry, false)
		j.AddErr(res.Reason)
	}
}

func (b *Bot) progressLoop(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, done <-chan struct{}) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()

	var lastEdit time.Time

	for {
		select {
		case <-done:
			return
		case <-t.C:
			// Telegram allows roughly one edit per second per chat; stay under
			// it so the bot is not throttled mid-job.
			if time.Since(lastEdit) < 2*time.Second {
				continue
			}

			c := j.Counts()
			text := fmt.Sprintf(
				"Checking…\n`%s` %d/%d\n\nlive %d · dead %d · challenge %d · error %d\nelapsed %s",
				bar(c.Done(), j.Total), c.Done(), j.Total,
				c.Live, c.Dead, c.Challenged, c.Errors,
				j.Elapsed().Round(time.Second),
			)

			if _, _, err := status.EditText(bot, &gotgbot.EditMessageTextOpts{
				Text:      text,
				ParseMode: "Markdown",
			}); err == nil {
				lastEdit = time.Now()
			}
		}
	}
}

func (b *Bot) deliver(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message) {
	hits, dead, challenged, errs := j.Results()

	summary := fmt.Sprintf(
		"*Done* in %s\n\n✅ live %d\n❌ dead %d\n🔒 challenge %d\n⚠️ error %d\n\n%d checked",
		j.Elapsed().Round(time.Second), len(hits), len(dead), len(challenged), len(errs), j.Total,
	)

	if _, _, err := status.EditText(bot, &gotgbot.EditMessageTextOpts{
		Text:      summary,
		ParseMode: "Markdown",
	}); err != nil {
		slog.Debug("status edit failed", "err", err)
	}

	chatID := status.Chat.Id

	if len(hits) > 0 {
		_, err := bot.SendDocument(chatID,
			gotgbot.InputFileByReader(fmt.Sprintf("cookies_%s.txt", j.ID), strings.NewReader(buildCookieFile(hits))),
			&gotgbot.SendDocumentOpts{
				Caption: fmt.Sprintf("%d working cookie(s) — drop-in ready", len(hits)),
			})
		if err != nil {
			slog.Warn("send cookies file failed", "err", err)
		}
	} else {
		_, _ = bot.SendMessage(chatID, "No working cookies in this batch.", nil)
	}

	report := buildReport(hits, dead, challenged, errs, j)
	_, err := bot.SendDocument(chatID,
		gotgbot.InputFileByReader(fmt.Sprintf("report_%s.txt", j.ID), strings.NewReader(report)),
		&gotgbot.SendDocumentOpts{
			Caption: "Full report",
		})
	if err != nil {
		slog.Warn("send report failed", "err", err)
	}
}

func (b *Bot) downloadFile(fileID string) ([]byte, error) {
	f, err := b.client.GetFile(fileID, nil)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.client.Token, f.FilePath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("getFile returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDocBytes))
}

func looksLikeCookie(s string) bool {
	for _, marker := range []string{"MSPAuth", "MSPProf", "ANON=", "RPSSecAuth", "__Host-MSAAUTHP"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func jitter() time.Duration {
	return time.Duration(200+rand.Intn(500)) * time.Millisecond
}

func friendlyRegistryError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "cooldown"):
		return "Cooling down. " + msg
	case strings.Contains(msg, "already running"):
		return "A job is already running. Send /cancel to stop it."
	}
	return msg
}

func summariseIssues(issues []error) string {
	const max = 4
	var sb strings.Builder
	for i, err := range issues {
		if i >= max {
			fmt.Fprintf(&sb, "…and %d more\n", len(issues)-max)
			break
		}
		sb.WriteString("• " + err.Error() + "\n")
	}
	return strings.TrimSpace(sb.String())
}
