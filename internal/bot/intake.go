package bot

import (
	"crypto/sha1"
	"fmt"
	"strings"

	"github.com/GT56GT/hotmail-checker/internal/archive"
	"github.com/GT56GT/hotmail-checker/internal/checker"
)

// intake is the result of reading a paste, a text file or a ZIP archive.
type intake struct {
	cookies []*checker.Cookie
	source  string
	issues  []error
	skipped []string
}

// parseIntake accepts raw text or a ZIP of logs and returns every distinct
// cookie set it can find.
func parseIntake(name string, data []byte) intake {
	var in intake

	label := strings.TrimSpace(name)
	if label == "" {
		label = "pasted"
	}

	if archive.LooksZipped(data) {
		res, err := archive.Extract(data, archive.DefaultLimits())
		if err != nil {
			in.issues = append(in.issues, err)
			return in
		}

		in.skipped = res.Skipped
		in.source = fmt.Sprintf("%s (%d file(s) in zip)", label, len(res.Files))

		seen := make(map[string]struct{}, len(res.Files))
		for _, f := range res.Files {
			cookies, issues := checker.ParseBlock(string(f.Data))
			for _, issue := range issues {
				in.issues = append(in.issues, fmt.Errorf("%s: %w", f.Name, issue))
			}
			for _, c := range cookies {
				key := cookieKey(c)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				in.cookies = append(in.cookies, c)
			}
		}
		return in
	}

	cookies, issues := checker.ParseBlock(string(data))
	in.cookies = cookies
	in.issues = issues
	in.source = label
	return in
}

// cookieKey identifies a session so overlapping logs collapse to one entry.
func cookieKey(c *checker.Cookie) string {
	sum := sha1.Sum([]byte(c.Header()))
	return string(sum[:])
}
