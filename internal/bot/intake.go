package bot

import (
	"crypto/sha1"
	"fmt"
	"strings"

	"github.com/GT56GT/hotmail-checker/internal/archive"
	"github.com/GT56GT/hotmail-checker/internal/provider"
)

// tagged pairs a parsed cookie set with the checker that understands it.
type tagged struct {
	checker provider.Checker
	cookie  *provider.Cookie
}

// intake is the result of reading a paste, a text file or a ZIP archive.
type intake struct {
	cookies []tagged
	source  string
	issues  []error
	skipped []string
	// services counts how many cookie sets each provider contributed.
	services map[string]int
}

// parseIntake accepts raw text or a ZIP of logs, detects the service for each
// block, and returns every distinct cookie set it can find.
func parseIntake(name string, data []byte, checkers []provider.Checker) intake {
	in := intake{services: map[string]int{}}

	label := strings.TrimSpace(name)
	if label == "" {
		label = "pasted"
	}

	parse := func(fileName, text string) {
		chk, ok := detect(checkers, text)
		if !ok {
			if strings.TrimSpace(text) != "" {
				in.issues = append(in.issues, fmt.Errorf("%s: unrecognised cookie format", fileName))
			}
			return
		}

		cookies, issues := chk.ParseBlock(text)
		for _, issue := range issues {
			in.issues = append(in.issues, fmt.Errorf("%s: %w", fileName, issue))
		}
		for _, c := range cookies {
			in.cookies = append(in.cookies, tagged{checker: chk, cookie: c})
			in.services[chk.Name()]++
		}
	}

	if archive.LooksZipped(data) {
		res, err := archive.Extract(data, archive.DefaultLimits())
		if err != nil {
			in.issues = append(in.issues, err)
			return in
		}

		in.skipped = res.Skipped
		in.source = fmt.Sprintf("%s (%d file(s) in zip)", label, len(res.Files))

		for _, f := range res.Files {
			parse(f.Name, string(f.Data))
		}
	} else {
		in.source = label
		parse(label, string(data))
	}

	in.cookies = dedupe(in.cookies)
	in.services = recount(in.cookies)
	return in
}

func detect(checkers []provider.Checker, text string) (provider.Checker, bool) {
	for _, c := range checkers {
		if c.Detect(text) {
			return c, true
		}
	}
	return nil, false
}

// dedupe collapses the same session appearing in several files of one archive.
func dedupe(in []tagged) []tagged {
	seen := make(map[string]struct{}, len(in))
	out := make([]tagged, 0, len(in))

	for _, t := range in {
		key := t.checker.Name() + "|" + cookieKey(t.cookie)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

func recount(in []tagged) map[string]int {
	m := make(map[string]int, len(in))
	for _, t := range in {
		m[t.checker.Name()]++
	}
	return m
}

// cookieKey identifies a session so overlapping logs collapse to one entry.
func cookieKey(c *provider.Cookie) string {
	sum := sha1.Sum([]byte(c.Header()))
	return string(sum[:])
}

// serviceSummary renders "Netflix 42 · Microsoft 3" for the headline.
func (in intake) serviceSummary() string {
	if len(in.services) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, len(in.services))
	for _, name := range []string{"netflix", "microsoft"} {
		if n, ok := in.services[name]; ok {
			parts = append(parts, fmt.Sprintf("%s %d", displayName(name), n))
		}
	}
	if len(parts) == 0 {
		for name, n := range in.services {
			parts = append(parts, fmt.Sprintf("%s %d", displayName(name), n))
		}
	}
	return strings.Join(parts, " · ")
}

func displayName(name string) string {
	switch name {
	case "netflix":
		return "Netflix"
	case "microsoft":
		return "Microsoft"
	}
	return name
}
