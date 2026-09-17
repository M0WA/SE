package crawlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// Client implements ports.CrawlJobService by delegating to a separate
// crawl-server process over HTTP -- admin-server itself never fetches
// pages, checks robots.txt, or writes documents; it only starts jobs on
// crawl-server and polls their progress.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Token, when set, is sent as the X-Internal-Token header on every
	// request -- crawl-server's own opt-in shared-secret check (see
	// restapi.Handler.requireCrawlInternalToken). Left empty, requests
	// carry no such header, matching a crawl-server that hasn't been
	// given CRAWL_INTERNAL_TOKEN either.
	Token string
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{}, Token: token}
}

// newRequest builds a request against path relative to c.BaseURL, wrapping
// the (rare, usually invalid-BaseURL) construction error consistently
// across every method below.
func (c *Client) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building %s %s request: %w", method, path, err)
	}
	return req, nil
}

func (c *Client) ListCrawlJobs(ctx context.Context) ([]domain.CrawlJobSummary, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs")
	if err != nil {
		return nil, err
	}

	respBody, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("crawl server returned %d: %s", status, bytes.TrimSpace(respBody))
	}

	var out []domain.CrawlJobSummary
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decoding crawl server response: %w", err)
	}
	return out, nil
}

func (c *Client) GetCrawlJob(ctx context.Context, jobID string) (domain.CrawlJob, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/jobs/"+url.PathEscape(jobID))
	if err != nil {
		return domain.CrawlJob{}, err
	}

	respBody, status, err := c.do(req)
	if err != nil {
		return domain.CrawlJob{}, err
	}
	if status == http.StatusNotFound {
		return domain.CrawlJob{}, ports.ErrCrawlJobNotFound
	}
	if status != http.StatusOK {
		return domain.CrawlJob{}, fmt.Errorf("crawl server returned %d: %s", status, bytes.TrimSpace(respBody))
	}

	var out domain.CrawlJob
	if err := json.Unmarshal(respBody, &out); err != nil {
		return domain.CrawlJob{}, fmt.Errorf("decoding crawl server response: %w", err)
	}
	return out, nil
}

// CancelCrawlJob asks crawl-server to stop a queued or running job.
func (c *Client) CancelCrawlJob(ctx context.Context, jobID string) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/jobs/"+url.PathEscape(jobID)+"/cancel")
	if err != nil {
		return err
	}

	respBody, status, err := c.do(req)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ports.ErrCrawlJobNotFound
	case http.StatusConflict:
		return ports.ErrCrawlJobNotRunning
	default:
		return fmt.Errorf("crawl server returned %d: %s", status, bytes.TrimSpace(respBody))
	}
}

// DeleteEndedCrawlJobs asks crawl-server to delete every done/failed/
// cancelled job, returning how many were removed.
func (c *Client) DeleteEndedCrawlJobs(ctx context.Context) (int, error) {
	req, err := c.newRequest(ctx, http.MethodDelete, "/jobs")
	if err != nil {
		return 0, err
	}

	respBody, status, err := c.do(req)
	if err != nil {
		return 0, err
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("crawl server returned %d: %s", status, bytes.TrimSpace(respBody))
	}

	var out struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("decoding crawl server response: %w", err)
	}
	return out.Removed, nil
}

// do sends req and returns its body and status code, or an error if the
// request couldn't be made or its response body couldn't be read.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
	if c.Token != "" {
		req.Header.Set("X-Internal-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling crawl server: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("reading crawl server response: %w", err)
	}
	return body, resp.StatusCode, nil
}
