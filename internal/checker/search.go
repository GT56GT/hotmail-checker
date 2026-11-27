package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

const (
	// Graph is the modern path: OData $search against /me/messages.
	graphSearchBase = "https://graph.microsoft.com/v1.0/me/messages" +
		"?$top=%d&$select=subject,from,receivedDateTime,bodyPreview,webLink&$search="

	// Outlook REST v2 is the fallback for accounts whose Graph scope is not
	// consented. Same data, PascalCase fields.
	restSearchBase = "https://outlook.office.com/api/v2.0/me/messages" +
		"?$top=%d&$select=Subject,From,ReceivedDateTime,BodyPreview,WebLink&$search="

	// searchBodyLimit caps the response we are willing to decode.
	searchBodyLimit = 4 << 20
)

// SearchItem and SearchOutcome are shared with the report layer.
type SearchItem = provider.SearchItem
type SearchOutcome = provider.SearchOutcome

// Search queries one account's mailbox for a keyword.
//
// The token chain is Graph -> Outlook REST (Graph scope) -> Outlook REST (mail
// scope). Any of the three working is enough; the first that answers wins.
func (c *Checker) Search(ctx context.Context, ck *Cookie, proxyURL, term string, limit int) (out SearchOutcome) {
	start := time.Now()
	defer func() { out.Elapsed = time.Since(start) }()

	if limit < 1 || limit > 200 {
		limit = 25
	}
	term = strings.TrimSpace(term)
	if term == "" {
		out.Reason = "empty search term"
		return
	}

	client, err := c.pool.get(proxyURL)
	if err != nil {
		out.Reason = "client: " + err.Error()
		return
	}

	header := ck.Header()
	q := url.QueryEscape(`"` + term + `"`)

	// Graph first.
	if tok, err := c.token(ctx, client, header, ScopeGraph); err == nil {
		if items, err := c.graphSearch(ctx, client, tok, q, limit); err == nil {
			out.Items, out.Method = items, "graph"
			return
		}
	}

	// Outlook REST with the Graph token, then with the mail-scope token.
	for _, scope := range []string{ScopeGraph, ScopeREST, ScopeMail} {
		tok, err := c.token(ctx, client, header, scope)
		if err != nil {
			continue
		}
		if items, err := c.restSearch(ctx, client, tok, q, limit); err == nil {
			out.Items, out.Method = items, "rest"
			return
		}
	}

	out.Reason = "search unavailable for this account"
	return
}

func (c *Checker) graphSearch(ctx context.Context, client tls_client.HttpClient, token, q string, limit int) ([]SearchItem, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet,
		fmt.Sprintf(graphSearchBase, limit)+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header = browserHeaders("")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != fhttp.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("graph %d", resp.StatusCode)
	}

	var payload struct {
		Value []struct {
			Subject          string `json:"subject"`
			BodyPreview      string `json:"bodyPreview"`
			WebLink          string `json:"webLink"`
			ReceivedDateTime string `json:"receivedDateTime"`
			From             struct {
				EmailAddress struct {
					Address string `json:"address"`
					Name    string `json:"name"`
				} `json:"emailAddress"`
			} `json:"from"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchBodyLimit)).Decode(&payload); err != nil {
		return nil, err
	}

	items := make([]SearchItem, 0, len(payload.Value))
	for _, m := range payload.Value {
		items = append(items, SearchItem{
			Subject: sanitiseSpace(m.Subject),
			From:    sanitiseSpace(firstNonEmpty(m.From.EmailAddress.Address, m.From.EmailAddress.Name)),
			Date:    parseTime(m.ReceivedDateTime),
			Preview: truncateText(sanitiseSpace(m.BodyPreview), 180),
			Link:    m.WebLink,
		})
	}
	return items, nil
}

func (c *Checker) restSearch(ctx context.Context, client tls_client.HttpClient, token, q string, limit int) ([]SearchItem, error) {
	req, err := fhttp.NewRequestWithContext(ctx, fhttp.MethodGet,
		fmt.Sprintf(restSearchBase, limit)+q, nil)
	if err != nil {
		return nil, err
	}
	req.Header = browserHeaders("")
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != fhttp.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("rest %d", resp.StatusCode)
	}

	var payload struct {
		Value []struct {
			Subject          string `json:"Subject"`
			BodyPreview      string `json:"BodyPreview"`
			WebLink          string `json:"WebLink"`
			ReceivedDateTime string `json:"ReceivedDateTime"`
			From             struct {
				EmailAddress struct {
					Address string `json:"Address"`
					Name    string `json:"Name"`
				} `json:"EmailAddress"`
			} `json:"From"`
		} `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchBodyLimit)).Decode(&payload); err != nil {
		return nil, err
	}

	items := make([]SearchItem, 0, len(payload.Value))
	for _, m := range payload.Value {
		items = append(items, SearchItem{
			Subject: sanitiseSpace(m.Subject),
			From:    sanitiseSpace(firstNonEmpty(m.From.EmailAddress.Address, m.From.EmailAddress.Name)),
			Date:    parseTime(m.ReceivedDateTime),
			Preview: truncateText(sanitiseSpace(m.BodyPreview), 180),
			Link:    m.WebLink,
		})
	}
	return items, nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
