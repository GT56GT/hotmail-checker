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
	fmt.Fprintf(&sb, "# job      %s\n", j.ID)
	fmt.Fprintf(&sb, "# finished %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "# duration %s\n", j.Elapsed().Round(time.Second))
	fmt.Fprintf(&sb, "# checked  %d\n", j.Total)
	fmt.Fprintf(&sb, "# live     %d\n", len(hits))
	fmt.Fprintf(&sb, "# dead     %d\n", len(dead))
	fmt.Fprintf(&sb, "# challenge %d\n", len(challenged))
	fmt.Fprintf(&sb, "# error    %d\n", len(errs))

	if len(hits) > 0 {
		sb.WriteString("\n## LIVE — email|name|proxy\n")
		for _, h := range hits {
			email := h.Email
			if email == "" {
				email = "unknown"
			}
			fmt.Fprintf(&sb, "%s|%s|%s\n", email, sanitiseField(h.Name), h.Proxy)
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
