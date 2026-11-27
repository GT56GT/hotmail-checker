package netflix

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

const (
	argoURL = "https://ios.prod.ftl.netflix.com/iosui/user/15.48"
	argoESN = "NFAPPL-02-IPHONE8=1-PXA-02026U9VV5O8AUKEAEO8PUJETCGDD4PQRI9DEB3MDLEMD0EACM4CS78LMD334MN3MQ3NMJ8SU9O9MVGS6BJCURM1PH1MUTGDPF4S4200"
)

// loginLinks converts a NetflixId into one-click sign-in URLs.
//
// Best effort: this is a private iOS endpoint with a fixed device identity, so
// any failure returns nil and the check result stands on its own.
func (c *Checker) loginLinks(ctx context.Context, netflixID, proxyURL string) map[string]string {
	if strings.TrimSpace(netflixID) == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	client, err := c.pool.get(proxyURL)
	if err != nil {
		return nil
	}

	q := url.Values{}
	q.Set("appVersion", "15.48.1")
	q.Set("device_type", "NFAPPL-02-")
	q.Set("esn", argoESN)
	q.Set("idiom", "phone")
	q.Set("iosVersion", "15.8.5")
	q.Set("isTablet", "false")
	q.Set("languages", "en-US")
	q.Set("locale", "en-US")
	q.Set("maxDeviceWidth", "375")
	q.Set("model", "saget")
	q.Set("modelType", "IPHONE8-1")
	q.Set("odpAware", "true")
	q.Set("path", `["account","token","default"]`)
	q.Set("pathFormat", "graph")
	q.Set("pixelDensity", "2.0")
	q.Set("progressive", "false")
	q.Set("responseFormat", "json")

	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet, argoURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil
	}
	req.Header = fhttp.Header{
		"user-agent":                         {"Argo/15.48.1 (iPhone; iOS 15.8.5; Scale/2.00)"},
		"accept-language":                    {"en-US;q=1"},
		"x-netflix.request.attempt":          {"1"},
		"x-netflix.request.routing":          {`{"path":"/nq/mobile/nqios/~15.48.0/user","control_tag":"iosui_argo"}`},
		"x-netflix.context.app-version":      {"15.48.1"},
		"x-netflix.argo.translated":          {"true"},
		"x-netflix.context.form-factor":      {"phone"},
		"x-netflix.context.sdk-version":      {"2012.4"},
		"x-netflix.client.appversion":        {"15.48.1"},
		"x-netflix.context.max-device-width": {"375"},
		"x-netflix.client.type":              {"argo"},
		"x-netflix.client.ftl.esn":           {argoESN},
		"x-netflix.context.locales":          {"en-US"},
		"x-netflix.client.iosversion":        {"15.8.5"},
		"cookie":                             {"NetflixId=" + netflixID},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != fhttp.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}

	var payload struct {
		Value struct {
			Account struct {
				Token struct {
					Default struct {
						Token string `json:"token"`
					} `json:"default"`
				} `json:"token"`
			} `json:"account"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil
	}

	tok := payload.Value.Account.Token.Default.Token
	if tok == "" {
		return nil
	}

	return map[string]string{
		"PC login":    "https://www.netflix.com/browse?nftoken=" + tok,
		"TV login":    "https://www.netflix.com/tv2?nftoken=" + tok,
		"Phone login": "https://www.netflix.com/unsupported?nftoken=" + tok,
	}
}
