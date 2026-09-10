package httpfetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type Fetcher struct {
	Client   *http.Client
	settings *domain.OperationalSettings
}

// New builds a Fetcher whose timeout and User-Agent are read from settings
// on every request, so they can be changed live from the admin panel. A
// nil settings uses the built-in defaults (see domain.OperationalSettings).
func New(settings *domain.OperationalSettings) *Fetcher {
	return &Fetcher{Client: &http.Client{}, settings: settings}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	return f.FetchWithOptions(ctx, rawURL, ports.FetchOptions{})
}

func (f *Fetcher) FetchWithOptions(ctx context.Context, rawURL string, opts ports.FetchOptions) (string, error) {
	v := f.settings.Get()
	ctx, cancel := context.WithTimeout(ctx, v.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", v.UserAgent)
	if opts.Cookie != "" {
		req.Header.Set("Cookie", opts.Cookie)
	}
	if opts.BasicAuthUser != "" || opts.BasicAuthPass != "" {
		req.SetBasicAuth(opts.BasicAuthUser, opts.BasicAuthPass)
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}
	return string(body), nil
}
