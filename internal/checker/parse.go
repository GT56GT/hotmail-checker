package checker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

// Cookie is the shared provider type.
type Cookie = provider.Cookie

// Required lists the cookies a Microsoft session must carry to authenticate.
var Required = []string{"MSPAuth", "MSPProf", "ANON"}

// markers identify a Microsoft consumer session in free-form text.
var markers = []string{"MSPAuth", "MSPProf", "__Host-MSAAUTHP", "RPSSecAuth"}

// Detect reports whether text looks like a Microsoft cookie block.
func Detect(text string) bool {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// ParseCookies parses an upload that may contain one account or many.
func ParseCookies(text string) ([]*Cookie, []error) {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, []error{fmt.Errorf("empty input")}
	}

	if len(text) > 0 && (text[0] == '{' || text[0] == '[') {
		c, err := parseJSONCookie(text)
		if err == nil {
			return []*Cookie{c}, nil
		}
	}

	if looksNetscape(text) {
		c, err := parseNetscape(text)
		if err == nil {
			return []*Cookie{c}, nil
		}
	}

	var cookies []*Cookie
	var issues []error

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		c, err := parseRawCookie(line)
		if err != nil {
			issues = append(issues, fmt.Errorf("line %d: %w", i+1, err))
			continue
		}
		cookies = append(cookies, c)
	}

	if len(cookies) == 0 && len(issues) == 0 {
		issues = append(issues, fmt.Errorf("no cookies found"))
	}
	return cookies, issues
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

func parseRawCookie(s string) (*Cookie, error) {
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

	if len(values) == 0 {
		return nil, fmt.Errorf("no key=value pairs")
	}
	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &Cookie{Values: values, Raw: s, Format: "raw"}, nil
}

func parseNetscape(text string) (*Cookie, error) {
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
		name := strings.TrimSpace(fields[5])
		value := strings.Join(fields[6:], "\t")
		if name == "" {
			continue
		}
		values[name] = value
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("netscape block had no cookies")
	}
	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &Cookie{Values: values, Raw: text, Format: "netscape"}, nil
}

func parseJSONCookie(text string) (*Cookie, error) {
	values := map[string]string{}

	var flat map[string]string
	if err := json.Unmarshal([]byte(text), &flat); err == nil && len(flat) > 0 {
		for k, v := range flat {
			values[k] = v
		}
	} else {
		var arr []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(text), &arr); err != nil {
			return nil, fmt.Errorf("unrecognised JSON format")
		}
		for _, c := range arr {
			if c.Name != "" {
				values[c.Name] = c.Value
			}
		}
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("empty JSON cookie set")
	}
	if err := requireKeys(values); err != nil {
		return nil, err
	}
	return &Cookie{Values: values, Raw: text, Format: "json"}, nil
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
