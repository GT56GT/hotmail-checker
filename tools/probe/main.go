// Command probe is an endpoint diagnostic, not part of the bot.
//
// Microsoft changes OAuth scopes and endpoint behaviour without notice. When
// checks or searches start failing, run this with an invalid cookie to see
// which endpoints still discriminate and which now answer everyone:
//
//	go run ./tools/probe
//
// A healthy result is: every scope returns error=login_required, and every
// protected endpoint returns 401.
package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const (
	redirect = "https%3A%2F%2Flogin.live.com%2Foauth20_desktop.srf"
	base     = "https://login.live.com/oauth20_authorize.srf?client_id=000000004C12AE6F&response_type=token&display=touch&locale=en&prompt=none&redirect_uri=" + redirect + "&scope="
)

var scopes = map[string]string{
	"mbi (current)":   "service%3A%3Aoutlook.office.com%3A%3AMBI_SSL",
	"graph Mail.Read": "https%3A%2F%2Fgraph.microsoft.com%2FMail.Read",
	"outlook mail":    "https%3A%2F%2Foutlook.office.com%2Fmail.read",
	"graph+outlook":   "https%3A%2F%2Fgraph.microsoft.com%2FMail.Read%20https%3A%2F%2Foutlook.office.com%2Fmail.read",
}

var endpoints = []string{
	"https://graph.microsoft.com/v1.0/me/messages?$search=%22invoice%22&$top=1",
	"https://outlook.office.com/api/v2.0/me/messages?$search=%22invoice%22&$top=1",
	"https://outlook.live.com/owa/service.svc?action=FindItem&app=Mail",
}

func main() {
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(25),
		tls_client.WithClientProfile(profiles.Chrome_124),
		tls_client.WithNotFollowRedirects(),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println("### SCOPE ACCEPTANCE (implicit flow, no session)")
	for label, sc := range scopes {
		u := base + sc
		req, _ := fhttp.NewRequest(fhttp.MethodGet, u, nil)
		req.Header = fhttp.Header{
			"accept":          {"text/html,*/*"},
			"accept-language": {"en-US,en;q=0.9"},
			"cookie":          {"MSPAuth=x; MSPProf=x; ANON=x"},
		}
		req.Header[fhttp.HeaderOrderKey] = []string{"accept", "accept-language", "cookie"}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  %-18s ERR %v\n", label, err)
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		loc := resp.Header.Get("Location")
		parsed, _ := url.Parse(loc)
		params, _ := url.ParseQuery(parsed.Fragment)
		errv := params.Get("error")
		if errv == "" {
			errv = "(none)"
		}
		fmt.Printf("  %-18s status=%-3d error=%s\n", label, resp.StatusCode, errv)
	}

	fmt.Println("\n### ENDPOINT SHAPE (no token, no session)")
	for _, u := range endpoints {
		req, _ := fhttp.NewRequest(fhttp.MethodGet, u, nil)
		req.Header = fhttp.Header{
			"accept":          {"application/json"},
			"accept-language": {"en-US,en;q=0.9"},
			"cookie":          {"MSPAuth=x; MSPProf=x; ANON=x"},
		}
		req.Header[fhttp.HeaderOrderKey] = []string{"accept", "accept-language", "cookie"}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  %s\n    ERR %v\n", trunc(u, 90), err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		fmt.Printf("  %s\n    status=%d www-auth=%q\n    body=%s\n",
			trunc(u, 90), resp.StatusCode,
			trunc(resp.Header.Get("WWW-Authenticate"), 80),
			trunc(strings.Join(strings.Fields(string(body)), " "), 200))
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
