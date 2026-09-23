package httpfetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type Fetcher struct {
	Client   *http.Client
	settings *domain.OperationalSettings
}

// New builds a Fetcher whose timeout/User-Agent are read live from
// settings (nil uses built-in defaults). Its Transport routes every dial,
// including redirects, through netguard.SafeDialContext, refusing a
// loopback/private/reserved target at the TCP layer.
func New(settings *domain.OperationalSettings) *Fetcher {
	return &Fetcher{Client: &http.Client{Transport: netguard.Transport()}, settings: settings}
}

func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	return f.FetchWithOptions(ctx, rawURL, ports.FetchOptions{})
}

func (f *Fetcher) FetchWithOptions(ctx context.Context, rawURL string, opts ports.FetchOptions) (string, error) {
	v := f.settings.Get()
	timeout := v.FetchTimeout
	if opts.FetchTimeoutSeconds > 0 {
		timeout = time.Duration(opts.FetchTimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	userAgent := v.UserAgent
	if opts.UserAgent != "" {
		userAgent = opts.UserAgent
	}
	req.Header.Set("User-Agent", userAgent)
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

	if ct := resp.Header.Get("Content-Type"); ct != "" && !looksTextual(ct) {
		return "", fmt.Errorf("fetch %s: unsupported content-type %q", rawURL, ct)
	}

	maxResponseBytes := v.MaxResponseBytes
	if opts.MaxResponseBytes > 0 {
		maxResponseBytes = opts.MaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxResponseBytes)))
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}
	return string(body), nil
}

// looksTextual reports whether a Content-Type looks like parseable text
// rather than a binary format we have no use for. Allows "xml" alongside
// "html"/"text" since sitemap.xml is commonly served as application/xml.
func looksTextual(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "html") || strings.Contains(ct, "text") || strings.Contains(ct, "xml")
}
