// Command probe is an endpoint diagnostic, not part of the bot.
//
// Every provider can drift: endpoints start returning 200 to everyone, scopes
// get retired, logins move behind new walls. When checks start failing, run
// this with invalid cookies to see which endpoints still discriminate.
//
//	go run ./tools/probe
//
// A healthy run looks like:
//
//	Microsoft  silent OAuth      302 -> #error=login_required
//	Netflix    /YourAccount      302 -> /login
//	Instagram  web_form_data     302 -> /accounts/login/
package main

import (
	"fmt"
	"io"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

type probe struct {
	service string
	label   string
	url     string
	headers map[string]string
	cookie  string
}

var probes = []probe{
	{
		service: "Microsoft",
		label:   "silent OAuth",
		url: "https://login.live.com/oauth20_authorize.srf?client_id=000000004C12AE6F" +
			"&response_type=token&redirect_uri=https%3A%2F%2Flogin.live.com%2Foauth20_desktop.srf" +
			"&display=touch&locale=en&prompt=none&scope=service%3A%3Aoutlook.office.com%3A%3AMBI_SSL",
		cookie: "MSPAuth=invalid; MSPProf=invalid; ANON=invalid",
	},
	{
		service: "Microsoft",
		label:   "owa config",
		url:     "https://outlook.live.com/owa/service.svc?action=GetOwaUserConfiguration",
		cookie:  "MSPAuth=invalid; MSPProf=invalid; ANON=invalid",
	},
	{
		service: "Microsoft",
		label:   "mail inbox",
		url:     "https://outlook.live.com/mail/0/inbox",
		cookie:  "MSPAuth=invalid; MSPProf=invalid; ANON=invalid",
	},
	{
		service: "Netflix",
		label:   "/YourAccount",
		url:     "https://www.netflix.com/YourAccount",
		cookie:  "NetflixId=invalid; SecureNetflixId=invalid",
	},
	{
		service: "Instagram",
		label:   "web_form_data",
		url:     "https://www.instagram.com/api/v1/accounts/edit/web_form_data/",
		headers: map[string]string{
			"x-ig-app-id":      "936619743392459",
			"x-asbd-id":        "198387",
			"x-requested-with": "XMLHttpRequest",
		},
		cookie: "sessionid=invalid; ds_user_id=0; csrftoken=invalid",
	},
	{
		service: "Instagram",
		label:   "current_user",
		url:     "https://www.instagram.com/api/v1/accounts/current_user/",
		headers: map[string]string{
			"x-ig-app-id":      "936619743392459",
			"x-asbd-id":        "198387",
			"x-requested-with": "XMLHttpRequest",
		},
		cookie: "sessionid=invalid; ds_user_id=0; csrftoken=invalid",
	},
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

	lastService := ""

	for _, p := range probes {
		if p.service != lastService {
			fmt.Printf("\n=== %s ===\n", p.service)
			lastService = p.service
		}

		req, _ := fhttp.NewRequest(fhttp.MethodGet, p.url, nil)
		req.Header = fhttp.Header{
			"accept":          {"*/*"},
			"accept-language": {"en-US,en;q=0.9"},
			"user-agent":      {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"},
			"cookie":          {p.cookie},
		}
		for k, v := range p.headers {
			req.Header[k] = []string{v}
		}
		req.Header[fhttp.HeaderOrderKey] = []string{
			"accept", "accept-language", "user-agent",
			"x-ig-app-id", "x-asbd-id", "x-requested-with", "cookie",
		}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  %-14s ERR %v\n", p.label, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		loc := resp.Header.Get("Location")
		fmt.Printf("  %-14s status=%-3d len=%-6d", p.label, resp.StatusCode, len(body))
		if loc != "" {
			fmt.Printf(" -> %s", trunc(loc, 70))
		}
		fmt.Println()
	}
}

func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
