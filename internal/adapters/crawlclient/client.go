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
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{}}
}

type startJobResponse struct {
	JobID string `json:"job_id"`
}

func (c *Client) StartCrawlJob(ctx context.Context, opts ports.CrawlOptions) (string, error) {
	body, err := json.Marshal(opts)
	if err != nil {
		return "", fmt.Errorf("encoding crawl request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/crawl", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building crawl request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.do(req)
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted {
		return "", fmt.Errorf("crawl server returned %d: %s", status, bytes.TrimSpace(respBody))
	}

	var out startJobResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decoding crawl server response: %w", err)
	}
	return out.JobID, nil
}

func (c *Client) ListCrawlJobs(ctx context.Context) ([]domain.CrawlJobSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/jobs", nil)
	if err != nil {
		return nil, fmt.Errorf("building jobs request: %w", err)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/jobs/"+url.PathEscape(jobID), nil)
	if err != nil {
		return domain.CrawlJob{}, fmt.Errorf("building job request: %w", err)
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

// do sends req and returns its body and status code, or an error if the
// request couldn't be made or its response body couldn't be read.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
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
