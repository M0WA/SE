package crawlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"searchengine/internal/ports"
)

// Client implements ports.CrawlerService by delegating the actual crawl to
// a separate crawl-server process over HTTP -- admin-server itself never
// fetches pages, checks robots.txt, or writes documents.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{}}
}

type crawlResponse struct {
	CrawledCount int `json:"crawled_count"`
}

func (c *Client) Crawl(ctx context.Context, opts ports.CrawlOptions) (int, error) {
	body, err := json.Marshal(opts)
	if err != nil {
		return 0, fmt.Errorf("encoding crawl request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/crawl", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("building crawl request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("calling crawl server: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading crawl server response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("crawl server returned %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}

	var out crawlResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("decoding crawl server response: %w", err)
	}
	return out.CrawledCount, nil
}
