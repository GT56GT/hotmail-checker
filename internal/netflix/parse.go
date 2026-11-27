// Package netflix validates Netflix session cookies.
//
// Validation endpoint is /YourAccount, not /browse: /browse redirects to
// locale roots (/in/, /us/) when profile state is odd, which produces false
// negatives. Account data lives on /account inside window.netflix.reactContext.
package netflix

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

// Required lists the cookies a Netflix session must carry.
var Required = []string{"NetflixId"}

var (
	idRx = regexp.MustCompile(`(?i)NetflixId\s*=\s*([^\s;'"]+)`)

	markers = []string{"NetflixId", "SecureNetflixId"}
)

// Detect reports whether text looks like a Netflix cookie block.
func Detect(text string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// ParseCookies extracts every distinct Netflix cookie set from text.
func ParseCookies(text string) ([]*provider.Cookie, []error) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, []error{fmt.Errorf("empty input")}
	}

	if text[0] == '[' || text[0] == '{' {
		if c, err := parseJSON(text); err == nil {
			return []*provider.Cookie{c}, nil
		}
	}

	if looksNetscape(text) {
		if c, err := parseNetscape(text); err == nil {
			return []*provider.Cookie{c}, nil
		}
	}

	var out []*provider.Cookie
	var issues []error

	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		// A single line can hold several accounts stacked together.
		for _, chunk := range splitStacked(line) {
			c, err := parseRaw(chunk)
			if err != nil {
				issues = append(issues, fmt.Errorf("line %d: %w", i+1, err))
				continue
			}
			out = append(out, c)
		}
	}

	if len(out) == 0 && len(issues) == 0 {
		issues = append(issues, fmt.Errorf("no Netflix cookies found"))
	}
	return out, issues
}

// splitStacked breaks a line containing several NetflixId values into one
// chunk per session. "SecureNetflixId" also contains "NetflixId", so a match
// preceded by a word character is rejected.
func splitStacked(line string) []string {
	var starts []int

	for _, m := range idRx.FindAllStringSubmatchIndex(line, -1) {
		start := m[0]
		if start > 0 && isWordByte(line[start-1]) {
			continue
		}
		starts = append(starts, start)
	}

	if len(starts) <= 1 {
		return []string{line}
	}

	out := make([]string, 0, len(starts))
	for i, start := range starts {
		end := len(line)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		out = append(out, line[start:end])
	}
	return out
}

func isWordByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_':
		return true
	}
	return false
}

func parseRaw(s string) (*provider.Cookie, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "cookie:") {
		s = strings.TrimSpace(s[len("cookie:"):])
	}

	values := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		values[k] = v
	}

	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &provider.Cookie{Values: values, Raw: s, Format: "raw"}, nil
}

func parseJSON(text string) (*provider.Cookie, error) {
	values := map[string]string{}

	var arr []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
		for _, c := range arr {
			if c.Name == "" {
				continue
			}
			if c.Domain != "" && !strings.Contains(c.Domain, "netflix") {
				continue
			}
			values[c.Name] = c.Value
		}
	} else {
		var flat map[string]string
		if err := json.Unmarshal([]byte(text), &flat); err != nil {
			return nil, fmt.Errorf("unrecognised JSON format")
		}
		for k, v := range flat {
			values[k] = v
		}
	}

	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &provider.Cookie{Values: values, Raw: text, Format: "json"}, nil
}

func looksNetscape(text string) bool {
	total, tabbed := 0, 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		total++
		if strings.Count(line, "\t") >= 6 {
			tabbed++
		}
	}
	return total > 0 && tabbed*2 >= total
}

func parseNetscape(text string) (*provider.Cookie, error) {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := fields[0]
		name := strings.TrimSpace(fields[5])
		if name == "" {
			continue
		}
		if domain != "" && !strings.Contains(domain, "netflix") {
			continue
		}
		values[name] = strings.Join(fields[6:], "\t")
	}

	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &provider.Cookie{Values: values, Raw: text, Format: "netscape"}, nil
}

func requireKeys(values map[string]string) error {
	var missing []string
	for _, k := range Required {
		if strings.TrimSpace(values[k]) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required cookie(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
