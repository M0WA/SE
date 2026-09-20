// Package httpsearxng implements ports.WebSearcher by calling a self-hosted
// SearXNG instance's JSON search API, mirroring httpchat/httpembed's own
// house style for calling an HTTP endpoint the admin configures a base URL
// for.
package httpsearxng

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"searchengine/internal/domain"
)

// requestTimeout is the default HTTP client timeout used when Client is
// constructed via New() with no HTTPClient override -- short, since a
// SearXNG instance fans a query out to several upstream engines in
// parallel and this is meant to be one input to a chat turn, not something
// worth waiting a long time on.
const requestTimeout = 10 * time.Second

// maxResponseBytes caps how much of the HTTP response body is ever read --
// a safety bound against a misbehaving instance, not a real limit in
// practice for an ordinary search result page.
const maxResponseBytes = 1 << 20

// Client calls a SearXNG instance's GET {base_url}/search?q=...&format=json
// endpoint. Implements ports.WebSearcher.
type Client struct {
	HTTPClient *http.Client
}

// New returns a Client with a sane default timeout when HTTPClient is left
// nil by the caller.
func New() *Client {
	return &Client{HTTPClient: &http.Client{Timeout: requestTimeout}}
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

// Search GETs strings.TrimRight(baseURL, "/") + "/search" with q=query and
// format=json, returning up to count results in whatever relevance order
// SearXNG itself already returned them in. A non-2xx status or an
// unparseable body is an error; count <= 0 returns every result SearXNG
// gave back rather than none, since a caller with no real preference
// shouldn't get an empty result set.
func (c *Client) Search(ctx context.Context, baseURL, query string, count int) ([]domain.WebSearchResult, error) {
	reqURL := strings.TrimRight(baseURL, "/") + "/search?" + url.Values{
		"q":      {query},
		"format": {"json"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("httpsearxng: building request: %w", err)
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpsearxng: calling search endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("httpsearxng: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("httpsearxng: search endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(string(body), 500))
	}

	var parsed searxngResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("httpsearxng: decoding response: %w", err)
	}

	if count <= 0 || count > len(parsed.Results) {
		count = len(parsed.Results)
	}
	results := make([]domain.WebSearchResult, count)
	for i, r := range parsed.Results[:count] {
		results[i] = domain.WebSearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content}
	}
	return results, nil
}
