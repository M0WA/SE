package httpfetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Fetcher struct {
	Client    *http.Client
	UserAgent string
}

func New() *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: 8 * time.Second},
		UserAgent: "EigeneSuchmaschine/1.0 (+educational)",
	}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("Request bauen: %w", err)
	}
	req.Header.Set("User-Agent", f.UserAgent)

	resp, err := f.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch %s: unerwarteter Status %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Body lesen: %w", err)
	}
	return string(body), nil
}
