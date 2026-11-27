// Package provider defines the contract every cookie checker implements.
//
// The bot is provider-agnostic: it detects which service a cookie belongs to,
// routes it to the matching checker, and renders whatever metadata that
// checker returns. Adding a service means adding one package that satisfies
// Checker.
package provider

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Verdict is the outcome of a single check.
type Verdict int

const (
	Live Verdict = iota
	Dead
	Challenged // authenticated but needs consent or re-authentication
	RateLimited
	Failed
)

func (v Verdict) String() string {
	switch v {
	case Live:
		return "live"
	case Dead:
		return "dead"
	case Challenged:
		return "challenged"
	case RateLimited:
		return "rate-limited"
	default:
		return "error"
	}
}

// Cookie is one account's cookie set, normalised.
type Cookie struct {
	Values map[string]string
	Raw    string
	Format string // raw | netscape | json
}

// Header renders the set as an HTTP Cookie header value.
func (c *Cookie) Header() string {
	if c == nil {
		return ""
	}
	keys := make([]string, 0, len(c.Values))
	for k := range c.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(c.Values[k])
	}
	return sb.String()
}

// Has reports whether a cookie name is present and non-empty.
func (c *Cookie) Has(name string) bool {
	if c == nil {
		return false
	}
	v, ok := c.Values[name]
	return ok && strings.TrimSpace(v) != ""
}

// Label returns a short human identifier for the session.
func (c *Cookie) Label() string {
	if c == nil {
		return "cookie"
	}
	for _, k := range []string{"__Host-MSAAUTHP", "NetflixId", "MSPAuth", "RPSSecAuth"} {
		if v := c.Values[k]; len(v) > 12 {
			return v[:12] + "…"
		}
	}
	return "cookie"
}

// Result carries everything a report needs about one check.
type Result struct {
	Verdict Verdict
	Email   string
	Name    string
	Reason  string
	Proxy   string
	Elapsed time.Duration
	// Meta holds provider-specific detail for the report, rendered as
	// "key : value" lines under each account. Keys are already humanised.
	Meta map[string]string
}

// SearchItem is one matching message.
type SearchItem struct {
	Subject string
	From    string
	Date    time.Time
	Preview string
	Link    string
}

// SearchOutcome is the result of searching one account.
type SearchOutcome struct {
	Items   []SearchItem
	Method  string // graph | rest
	Reason  string
	Elapsed time.Duration
}

// Checker is one service.
type Checker interface {
	// Name is the internal identifier: "microsoft", "netflix".
	Name() string
	// Label is the display name: "Microsoft", "Netflix".
	Label() string
	// Detect reports whether this block of text looks like this service.
	Detect(text string) bool
	// ParseBlock extracts every distinct cookie set from text.
	ParseBlock(text string) ([]*Cookie, []error)
	// Check validates one cookie set through the given proxy ("" = direct).
	Check(ctx context.Context, ck *Cookie, proxyURL string) Result
}
