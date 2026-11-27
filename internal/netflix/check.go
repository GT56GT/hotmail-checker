package netflix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

const (
	// accountURL is the validation target. /browse is deliberately avoided:
	// it redirects to locale roots when profile state is odd, which reads as
	// dead when the session is actually fine.
	accountURL = "https://www.netflix.com/YourAccount"
	detailURL  = "https://www.netflix.com/account"

	bodyLimit = 8 << 20
	maxHops   = 6
)

// Checker validates Netflix session cookies.
type Checker struct {
	pool       *clientPool
	LoginLinks bool
}

// New builds a Checker.
func New(timeout time.Duration) *Checker {
	return &Checker{pool: newClientPool(timeout), LoginLinks: true}
}

func (c *Checker) Name() string  { return "netflix" }
func (c *Checker) Label() string { return "Netflix" }

func (c *Checker) Detect(text string) bool { return Detect(text) }

func (c *Checker) ParseBlock(text string) ([]*provider.Cookie, []error) {
	return ParseCookies(text)
}

// Check validates one Netflix cookie set.
func (c *Checker) Check(ctx context.Context, ck *provider.Cookie, proxyURL string) (res provider.Result) {
	start := time.Now()
	res.Proxy = proxyLabel(proxyURL)
	res.Meta = map[string]string{}
	defer func() { res.Elapsed = time.Since(start) }()

	client, err := c.pool.get(proxyURL)
	if err != nil {
		res.Verdict = provider.Failed
		res.Reason = "client: " + err.Error()
		return res
	}

	resp, body, finalURL, err := c.fetch(ctx, client, accountURL, ck.Values)
	if err != nil {
		res.Verdict = provider.Failed
		res.Reason = classifyNetErr(err)
		return res
	}

	switch {
	case resp.StatusCode == fhttp.StatusTooManyRequests:
		res.Verdict = provider.RateLimited
		res.Reason = "rate limited"
		return res

	case resp.StatusCode == fhttp.StatusForbidden:
		// Netflix blocks some networks at the edge; that says nothing about
		// the cookie.
		res.Verdict = provider.Failed
		res.Reason = "403 forbidden (network)"
		return res

	case resp.StatusCode != fhttp.StatusOK:
		res.Verdict = provider.Dead
		res.Reason = fmt.Sprintf("http %d", resp.StatusCode)
		return res
	}

	lower := strings.ToLower(finalURL)
	if strings.Contains(lower, "/login") {
		res.Verdict = provider.Dead
		res.Reason = "session expired"
		return res
	}

	alive := strings.Contains(lower, "netflix.com/browse") ||
		strings.Contains(lower, "/youraccount") ||
		strings.Contains(lower, "netflix.com/account") ||
		strings.Contains(string(body), "Sign Out")

	if !alive {
		res.Verdict = provider.Dead
		res.Reason = "redirected to " + shorten(finalURL, 60)
		return res
	}

	res.Verdict = provider.Live

	if _, accountBody, _, err := c.fetch(ctx, client, detailURL, ck.Values); err == nil {
		d := parseAccount(string(accountBody))
		res.Email = d.Email
		res.Name = d.ProfileName
		res.Meta = d.asMeta()
	}

	if c.LoginLinks {
		for k, v := range c.loginLinks(ctx, ck.Values["NetflixId"], proxyURL) {
			res.Meta[k] = v
		}
	}

	return res
}

// fetch follows redirects by hand so the final URL is known — that URL is the
// live/dead signal.
func (c *Checker) fetch(ctx context.Context, client tls_client.HttpClient, target string, cookies map[string]string) (*fhttp.Response, []byte, string, error) {
	current := target

	for hop := 0; hop < maxHops; hop++ {
		req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, current, nil)
		if err != nil {
			return nil, nil, current, err
		}
		req.Header = headers(cookies)

		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, current, err
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()

			if loc == "" {
				return nil, nil, current, errors.New("redirect without location")
			}

			base, err := url.Parse(current)
			if err != nil {
				return nil, nil, current, err
			}
			next, err := url.Parse(loc)
			if err != nil {
				return nil, nil, current, err
			}
			current = base.ResolveReference(next).String()
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
		_ = resp.Body.Close()
		return resp, body, current, nil
	}

	return nil, nil, current, errors.New("too many redirects")
}

func headers(cookies map[string]string) fhttp.Header {
	h := fhttp.Header{
		"user-agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"},
		"accept-language":           {"en-US,en;q=0.9"},
		"cache-control":             {"no-cache"},
		"pragma":                    {"no-cache"},
		"sec-ch-ua":                 {`"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {`"Windows"`},
		"sec-fetch-dest":            {"document"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-user":            {"?1"},
		"upgrade-insecure-requests": {"1"},
	}
	if len(cookies) > 0 {
		h["cookie"] = []string{cookieHeader(cookies)}
	}
	h[fhttp.HeaderOrderKey] = []string{
		"accept", "accept-language", "cache-control", "pragma",
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user",
		"upgrade-insecure-requests", "cookie",
	}
	return h
}

func cookieHeader(cookies map[string]string) string {
	keys := make([]string, 0, len(cookies))
	for k := range cookies {
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
		sb.WriteString(cookies[k])
	}
	return sb.String()
}

func proxyLabel(proxyURL string) string {
	if proxyURL == "" {
		return "direct"
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return "proxy"
	}
	return u.Host
}

func classifyNetErr(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "proxy"):
		return "proxy failure"
	case strings.Contains(s, "tls") || strings.Contains(s, "handshake"):
		return "tls failure"
	case strings.Contains(s, "no such host") || strings.Contains(s, "dns"):
		return "dns failure"
	case strings.Contains(s, "connection reset") || strings.Contains(s, "unexpected eof"):
		return "connection reset"
	}
	return "network: " + shorten(err.Error(), 80)
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// clientPool keeps one tls-client per proxy so connection pools are reused.
type clientPool struct {
	mu      sync.Mutex
	timeout time.Duration
	clients map[string]tls_client.HttpClient
}

func newClientPool(timeout time.Duration) *clientPool {
	return &clientPool{timeout: timeout, clients: make(map[string]tls_client.HttpClient)}
}

func (p *clientPool) get(proxyURL string) (tls_client.HttpClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.clients[proxyURL]; ok {
		return c, nil
	}

	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(int(p.timeout.Seconds())),
		tls_client.WithClientProfile(profiles.Chrome_124),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithRandomTLSExtensionOrder(),
	}
	if proxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxyURL))
	}

	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	p.clients[proxyURL] = c
	return c, nil
}
