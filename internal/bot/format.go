package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/job"
)

// buildCookieFile renders working cookies only — one per line, ready to be fed
// into another checker without transformation.
func buildCookieFile(hits []job.Hit) string {
	var sb strings.Builder
	for _, h := range hits {
		sb.WriteString(h.Cookie)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildReport renders the full outcome: hits with identity, expired cookies,
// challenged sessions, and grouped failure reasons.
func buildReport(hits []job.Hit, dead []checker.Cookie, challenged, errs []string, j *job.Job) string {
	var sb strings.Builder

	sb.WriteString("# hotmail-checker report\n")
	fmt.Fprintf(&sb, "# job       %s\n", j.ID)
	fmt.Fprintf(&sb, "# finished  %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "# duration  %s\n", j.Elapsed().Round(time.Second))
	fmt.Fprintf(&sb, "# checked   %d\n", j.Total)
	fmt.Fprintf(&sb, "# live      %d\n", len(hits))
	fmt.Fprintf(&sb, "# dead      %d\n", len(dead))
	fmt.Fprintf(&sb, "# challenge %d\n", len(challenged))
	fmt.Fprintf(&sb, "# error     %d\n", len(errs))

	if len(hits) > 0 {
		sb.WriteString("\n## LIVE — email|name|proxy\n")
		for _, h := range hits {
			fmt.Fprintf(&sb, "%s|%s|%s\n", orUnknown(h.Email), sanitiseField(h.Name), h.Proxy)
		}

		sb.WriteString("\n## LIVE COOKIES\n")
		for _, h := range hits {
			sb.WriteString(h.Cookie)
			sb.WriteByte('\n')
		}
	}

	if len(challenged) > 0 {
		sb.WriteString("\n## CHALLENGED — valid session, needs consent or re-auth\n")
		for _, c := range challenged {
			fmt.Fprintf(&sb, "%s\n", c)
		}
	}

	if len(dead) > 0 {
		sb.WriteString("\n## DEAD\n")
		for _, c := range dead {
			fmt.Fprintf(&sb, "%s\n", c.Label())
		}
	}

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

	return sb.String()
}

// buildSearchFile renders mailbox matches per account.
func buildSearchFile(searches []searchOutcome, term string) string {
	var sb strings.Builder

	sb.WriteString("# inbox search\n")
	fmt.Fprintf(&sb, "# term      %s\n", term)
	fmt.Fprintf(&sb, "# accounts  %d\n", len(searches))
	fmt.Fprintf(&sb, "# matches   %d\n", countMatches(searches))

	if len(searches) == 0 {
		sb.WriteString("\nNo live accounts to search.\n")
		return sb.String()
	}

	for _, s := range searches {
		if len(s.Items) == 0 {
			reason := s.Reason
			if reason == "" {
				reason = "no matches"
			}
			fmt.Fprintf(&sb, "\n## %s — %s\n", orUnknown(s.Email), reason)
			continue
		}

		fmt.Fprintf(&sb, "\n## %s — %d match(es) via %s\n", orUnknown(s.Email), len(s.Items), s.Method)
		for _, m := range s.Items {
			date := "unknown date"
			if !m.Date.IsZero() {
				date = m.Date.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(&sb, "%s | %s | %s\n", date, orDash(m.From), orDash(m.Subject))
			if m.Preview != "" {
				fmt.Fprintf(&sb, "    %s\n", m.Preview)
			}
			if m.Link != "" {
				fmt.Fprintf(&sb, "    %s\n", m.Link)
			}
		}
	}

	return sb.String()
}

// bar renders a fixed-width progress bar.
func bar(done, total int) string {
	const width = 20
	if total <= 0 {
		return strings.Repeat("░", width)
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	pct := done * 100 / total
	return fmt.Sprintf("%s%s %3d%%",
		strings.Repeat("█", filled),
		strings.Repeat("░", width-filled),
		pct)
}

func sanitiseField(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
