package bot

import (
	"fmt"
	"strings"
)

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
