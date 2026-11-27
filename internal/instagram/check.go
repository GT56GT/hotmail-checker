package instagram

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

const (
	// formURL is the primary probe: a dead session redirects to /accounts/login.
	formURL = "https://www.instagram.com/api/v1/accounts/edit/web_form_data/"

	// currentURL is the JSON fallback: 401 with require_login when invalid.
	currentURL = "https://www.instagram.com/api/v1/accounts/current_user/"

	// profileURL adds follower counts and flags. Login-gated, so best effort.
	profileURL = "https://www.instagram.com/api/v1/users/web_profile_info/?username="

	// appID is Instagram's public web app id, sent with every API call.
	appID = "936619743392459"
	// asbdID matches the value the web client sends.
	asbdID = "198387"

	bodyLimit = 4 << 20
)

// Checker validates Instagram session cookies.
type Checker struct {
	pool *clientPool
}

// New builds a Checker.
func New(timeout time.Duration) *Checker {
	return &Checker{pool: newClientPool(timeout)}
}

func (c *Checker) Name() string  { return "instagram" }
func (c *Checker) Label() string { return "Instagram" }

func (c *Checker) Detect(text string) bool { return Detect(text) }

func (c *Checker) ParseBlock(text string) ([]*provider.Cookie, []error) {
	return ParseCookies(text)
}

// Check validates one Instagram cookie set.
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

	// --- primary: the edit form endpoint --------------------------------
	resp, body, err := c.get(ctx, client, formURL, ck)
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

	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		if isLoginURL(loc) {
			res.Verdict = provider.Dead
			res.Reason = "session expired"
			return res
		}
		if isCheckpoint(body) || isCheckpoint([]byte(loc)) {
			res.Verdict = provider.Challenged
			res.Reason = "challenge: checkpoint required"
			return res
		}
		res.Verdict = provider.Dead
		res.Reason = "redirected to " + shorten(loc, 60)
		return res

	case resp.StatusCode == fhttp.StatusOK:
		if isCheckpoint(body) {
			res.Verdict = provider.Challenged
			res.Reason = "challenge: checkpoint required"
			return res
		}
		d, ok := parseFormData(body)
		if !ok {
			// 200 but no form data: fall through to the JSON probe.
			break
		}
		c.enrich(ctx, client, &d, ck)
		res.Verdict = provider.Live
		res.Email = d.Email
		res.Name = firstNonEmpty(d.FullName, d.Username)
		res.Meta = d.asMeta()
		return res

	case resp.StatusCode == fhttp.StatusUnauthorized || resp.StatusCode == fhttp.StatusForbidden:
		if !requiresLogin(body) && resp.StatusCode == fhttp.StatusForbidden {
			// 403 without a login marker is Instagram's generic block, not a
			// verdict on the cookie.
			res.Verdict = provider.Failed
			res.Reason = "403 forbidden (edge block)"
			return res
		}
		res.Verdict = provider.Dead
		res.Reason = "session expired"
		return res
	}

	// --- fallback: current_user JSON ------------------------------------
	resp2, body2, err := c.get(ctx, client, currentURL, ck)
	if err != nil {
		res.Verdict = provider.Failed
		res.Reason = classifyNetErr(err)
		return res
	}

	switch {
	case resp2.StatusCode == fhttp.StatusTooManyRequests:
		res.Verdict = provider.RateLimited
		res.Reason = "rate limited"

	case resp2.StatusCode == fhttp.StatusOK:
		if isCheckpoint(body2) {
			res.Verdict = provider.Challenged
			res.Reason = "challenge: checkpoint required"
			return res
		}
		d, ok := parseCurrentUser(body2)
		if !ok {
			d, ok = parseFormData(body2)
		}
		if !ok {
			res.Verdict = provider.Failed
			res.Reason = "http 200 without account data"
			return res
		}
		c.enrich(ctx, client, &d, ck)
		res.Verdict = provider.Live
		res.Email = d.Email
		res.Name = firstNonEmpty(d.FullName, d.Username)
		res.Meta = d.asMeta()

	case resp2.StatusCode == fhttp.StatusUnauthorized || resp2.StatusCode == fhttp.StatusForbidden:
		if requiresLogin(body2) || resp2.StatusCode == fhttp.StatusUnauthorized {
			res.Verdict = provider.Dead
			res.Reason = "session expired"
			return res
		}
		res.Verdict = provider.Failed
		res.Reason = "403 forbidden (edge block)"

	default:
		res.Verdict = provider.Failed
		res.Reason = fmt.Sprintf("http %d", resp2.StatusCode)
	}

	return res
}

// enrich adds follower counts and account flags from the profile endpoint.
func (c *Checker) enrich(ctx context.Context, client tls_client.HttpClient, d *detail, ck *provider.Cookie) {
	username := strings.TrimSpace(d.Username)
	if username == "" {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, body, err := c.get(ctx, client, profileURL+url.QueryEscape(username), ck)
	if err != nil || resp.StatusCode != fhttp.StatusOK {
		return
	}
	mergeProfile(d, body)
}

// get performs one request without following redirects — the Location header
// on a 3xx is the dead-session signal.
func (c *Checker) get(ctx context.Context, client tls_client.HttpClient, target string, ck *provider.Cookie) (*fhttp.Response, []byte, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header = headers(ck)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	_ = resp.Body.Close()
	return resp, body, nil
}

func headers(ck *provider.Cookie) fhttp.Header {
	h := fhttp.Header{
		"accept":           {"*/*"},
		"accept-language":  {"en-US,en;q=0.9"},
		"user-agent":       {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"},
		"x-ig-app-id":      {appID},
		"x-asbd-id":        {asbdID},
		"x-ig-www-claim":   {"0"},
		"x-requested-with": {"XMLHttpRequest"},
		"origin":           {"https://www.instagram.com"},
		"referer":          {"https://www.instagram.com/"},
		"sec-fetch-site":   {"same-origin"},
		"sec-fetch-mode":   {"cors"},
		"sec-fetch-dest":   {"empty"},
	}

	if csrf := strings.TrimSpace(ck.Values["csrftoken"]); csrf != "" {
		h["x-csrftoken"] = []string{csrf}
	}
	if id := strings.TrimSpace(ck.Values["ds_user_id"]); id != "" {
		h["ig-intended-user-id"] = []string{id}
	}
	if raw := ck.Header(); raw != "" {
		h["cookie"] = []string{raw}
	}

	h[fhttp.HeaderOrderKey] = []string{
		"accept", "accept-language", "user-agent",
		"x-ig-app-id", "x-asbd-id", "x-ig-www-claim", "x-requested-with",
		"x-csrftoken", "ig-intended-user-id",
		"origin", "referer",
		"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
		"cookie",
	}
	return h
}

func isLoginURL(loc string) bool {
	l := strings.ToLower(loc)
	return strings.Contains(l, "/accounts/login") || strings.Contains(l, "/login")
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
