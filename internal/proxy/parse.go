package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Supported input forms, all accepted in one pasted list:
//
//	host:port
//	host:port:user:pass
//	user:pass@host:port
//	scheme://host:port
//	scheme://user:pass@host:port
//	[ipv6]:port
//	scheme://[ipv6]:port
//
// Separators may be newlines, commas or semicolons. Lines beginning with
// '#' or '//' are ignored.
var supportedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks4":  true,
	"socks4a": true,
	"socks5":  true,
	"socks5h": true,
}

// ParseList normalises a free-form proxy list. It returns accepted URLs and
// the raw entries it could not understand.
func ParseList(text string) (urls []string, rejected []string) {
	seen := make(map[string]struct{})

	for _, raw := range splitEntries(text) {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}

		u, err := Normalise(s)
		if err != nil {
			rejected = append(rejected, s)
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls, rejected
}

func splitEntries(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
}

// Normalise converts one entry into a canonical proxy URL.
func Normalise(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty")
	}

	scheme := "http"
	rest := s

	if i := strings.Index(s, "://"); i >= 0 {
		scheme = strings.ToLower(s[:i])
		rest = s[i+3:]
	}

	if !supportedSchemes[scheme] {
		return "", fmt.Errorf("unsupported scheme %q", scheme)
	}

	user, pass, hostport, err := splitCredentials(rest)
	if err != nil {
		return "", err
	}

	host, port, err := splitHostPort(hostport)
	if err != nil {
		return "", err
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, port),
	}
	if user != "" {
		if pass != "" {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}
	return u.String(), nil
}

// splitCredentials separates optional user:pass from host:port.
func splitCredentials(rest string) (user, pass, hostport string, err error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", "", fmt.Errorf("empty")
	}

	// user:pass@host:port
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		creds := rest[:at]
		hostport = rest[at+1:]
		user, pass, _ = strings.Cut(creds, ":")
		if hostport == "" {
			return "", "", "", fmt.Errorf("missing host")
		}
		return user, pass, hostport, nil
	}

	// [ipv6]:port — never a colon-separated credential form
	if strings.HasPrefix(rest, "[") {
		return "", "", rest, nil
	}

	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 2:
		// host:port
		return "", "", rest, nil
	case 4:
		// host:port:user:pass
		return parts[2], parts[3], parts[0] + ":" + parts[1], nil
	default:
		return "", "", "", fmt.Errorf("unrecognised format %q", rest)
	}
}

func splitHostPort(s string) (host, port string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty host")
	}

	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return "", "", fmt.Errorf("unclosed ipv6 %q", s)
		}
		host = s[1:end]
		rest := strings.TrimPrefix(s[end+1:], ":")
		if rest == "" {
			return "", "", fmt.Errorf("missing port %q", s)
		}
		return host, rest, nil
	}

	host, port, ok := strings.Cut(s, ":")
	if !ok || host == "" || port == "" {
		return "", "", fmt.Errorf("missing port %q", s)
	}
	return host, port, nil
}

// Label renders a proxy for display with credentials removed.
func Label(proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return proxyURL
	}
	label := u.Host
	if u.Scheme != "" && u.Scheme != "http" {
		label = u.Scheme + "://" + label
	}
	if u.User != nil {
		label += " (auth)"
	}
	return label
}
