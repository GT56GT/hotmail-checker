package bot

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/archive"
	"github.com/GT56GT/hotmail-checker/internal/job"
)

// bundle holds every result artefact for one job.
type bundle struct {
	report   string
	cookies  string
	accounts string
	search   string
	dead     string
}

// buildBundle renders the consolidated report plus machine-readable exports.
// searches is index-aligned with hits (the first len(searches) entries).
func buildBundle(hits []job.Hit, dead, challenged, errs []string, searches []searchOutcome, term string, j *job.Job) bundle {
	var b bundle

	var sb strings.Builder

	sb.WriteString("# hotmail-checker results\n")
	fmt.Fprintf(&sb, "# job       %s\n", j.ID)
	fmt.Fprintf(&sb, "# source    %s\n", orUnknown(j.Source))
	fmt.Fprintf(&sb, "# finished  %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "# duration  %s\n", j.Elapsed().Round(time.Second))
	fmt.Fprintf(&sb, "# checked   %d\n", j.Total)
	fmt.Fprintf(&sb, "# live      %d\n", len(hits))
	fmt.Fprintf(&sb, "# dead      %d\n", len(dead))
	fmt.Fprintf(&sb, "# challenge %d\n", len(challenged))
	fmt.Fprintf(&sb, "# error     %d\n", len(errs))
	if term != "" {
		fmt.Fprintf(&sb, "# search    %q\n", term)
	}

	// --- LIVE, per account ------------------------------------------------
	sb.WriteString("\n## LIVE\n")
	if len(hits) == 0 {
		sb.WriteString("\nnone\n")
	}
	for i, h := range hits {
		fmt.Fprintf(&sb, "\n### %d. %s\n", i+1, orUnknown(h.Email))
		sb.WriteString("status : live\n")
		fmt.Fprintf(&sb, "name   : %s\n", orDash(h.Name))
		fmt.Fprintf(&sb, "proxy  : %s\n", orDash(h.Proxy))

		if i < len(searches) {
			s := searches[i]
			switch {
			case len(s.Items) > 0:
				fmt.Fprintf(&sb, "search : %d match(es) for %q via %s\n", len(s.Items), term, s.Method)
			case s.Reason != "":
				fmt.Fprintf(&sb, "search : %s\n", s.Reason)
			default:
				fmt.Fprintf(&sb, "search : no matches for %q\n", term)
			}

			for _, m := range s.Items {
				date := "unknown date"
				if !m.Date.IsZero() {
					date = m.Date.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(&sb, "\n  %s | %s | %s\n", date, orDash(m.From), orDash(m.Subject))
				if m.Preview != "" {
					fmt.Fprintf(&sb, "      %s\n", m.Preview)
				}
				if m.Link != "" {
					fmt.Fprintf(&sb, "      %s\n", m.Link)
				}
			}
		}
	}

	// --- CHALLENGED -------------------------------------------------------
	if len(challenged) > 0 {
		sb.WriteString("\n## CHALLENGED — session valid, needs consent or re-auth\n")
		for _, c := range challenged {
			fmt.Fprintf(&sb, "- %s\n", c)
		}
	}

	// --- DEAD -------------------------------------------------------------
	if len(dead) > 0 {
		sb.WriteString("\n## DEAD\n")
		for _, d := range dead {
			fmt.Fprintf(&sb, "%s\n", d)
		}
	}

	// --- ERRORS -----------------------------------------------------------
	if len(errs) > 0 {
		sb.WriteString("\n## ERRORS (grouped)\n")
		counts := make(map[string]int, len(errs))
		order := make([]string, 0, len(errs))
		for _, e := range errs {
			if _, seen := counts[e]; !seen {
				order = append(order, e)
			}
			counts[e]++
		}
		for _, e := range order {
			fmt.Fprintf(&sb, "%4d × %s\n", counts[e], e)
		}
	}

	b.report = sb.String()

	// --- cookies.txt ------------------------------------------------------
	var ck strings.Builder
	for _, h := range hits {
		ck.WriteString(h.Cookie)
		ck.WriteByte('\n')
	}
	b.cookies = ck.String()

	// --- accounts.csv -----------------------------------------------------
	var acct strings.Builder
	aw := csv.NewWriter(&acct)
	_ = aw.Write([]string{"email", "name", "status", "proxy", "search_matches"})
	for i, h := range hits {
		matches := 0
		if i < len(searches) {
			matches = len(searches[i].Items)
		}
		_ = aw.Write([]string{
			h.Email, h.Name, "live", h.Proxy, strconv.Itoa(matches),
		})
	}
	for range challenged {
		_ = aw.Write([]string{"", "", "challenged", "", "0"})
	}
	aw.Flush()
	b.accounts = acct.String()

	// --- search.csv -------------------------------------------------------
	var srch strings.Builder
	sw := csv.NewWriter(&srch)
	_ = sw.Write([]string{"email", "date", "from", "subject", "link"})
	for _, s := range searches {
		for _, m := range s.Items {
			date := ""
			if !m.Date.IsZero() {
				date = m.Date.Format(time.RFC3339)
			}
			_ = sw.Write([]string{s.Email, date, m.From, m.Subject, m.Link})
		}
	}
	sw.Flush()
	b.search = srch.String()

	// --- dead.txt ---------------------------------------------------------
	b.dead = strings.Join(dead, "\n")
	if b.dead != "" {
		b.dead += "\n"
	}

	return b
}

// zipBundle packages every artefact into one archive.
func zipBundle(b bundle, term string) ([]byte, error) {
	files := []archive.File{
		{Name: "report.txt", Data: []byte(b.report)},
		{Name: "cookies.txt", Data: []byte(b.cookies)},
		{Name: "accounts.csv", Data: []byte(b.accounts)},
	}
	if strings.TrimSpace(b.dead) != "" {
		files = append(files, archive.File{Name: "dead.txt", Data: []byte(b.dead)})
	}
	if term != "" && strings.TrimSpace(b.search) != "" {
		files = append(files, archive.File{Name: "search.csv", Data: []byte(b.search)})
	}
	return archive.Build(files)
}
