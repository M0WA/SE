// Package browserfetcher implements ports.Renderer by driving a real,
// headless browser through Playwright: it executes a page's JavaScript in a
// sandboxed process before extracting the client-rendered HTML, for sites a
// plain HTTP GET (internal/adapters/httpfetcher) can never fully see.
package browserfetcher

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/mxschmitt/playwright-go"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/ports"
)

// Renderer wraps one browser engine (Chromium or Firefox). Safe for
// concurrent use: each Render call opens its own isolated browser context,
// while the underlying browser/driver starts at most once, lazily.
type Renderer struct {
	// Engine is "chromium" or "firefox" (see domain.Renderer* constants).
	Engine string

	// AllowURL decides whether a request (navigation or subresource) may go
	// out -- see Render's SSRF-guard comment. Defaults to
	// netguard.URLAllowed when nil; overridable so tests can render an
	// httptest.Server, whose loopback address the default guard rejects.
	AllowURL func(rawURL string) bool

	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser
}

// New returns a Renderer for the given engine ("chromium" or "firefox").
// It starts no browser process yet -- that only happens on the first
// Render call, so a crawl-server process that never actually enables
// rendering never pays Playwright's driver/browser startup cost.
func New(engine string) *Renderer {
	return &Renderer{Engine: engine}
}

// ensureBrowser lazily installs (if needed) and launches this renderer's
// browser engine, memoizing it for reuse -- deferred to first crawl rather
// than startup, so a deployment that never enables rendering never pays
// Playwright's install cost.
func (r *Renderer) ensureBrowser() (playwright.Browser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.browser != nil {
		return r.browser, nil
	}

	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{r.Engine}}); err != nil {
		return nil, fmt.Errorf("installing playwright browser %q: %w", r.Engine, err)
	}
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("starting playwright driver: %w", err)
	}

	// Playwright defaults ChromiumSandbox to false, but this crawler renders
	// untrusted sites, so turn it on explicitly. Firefox has no equivalent
	// toggle -- its sandboxing is always active.
	launchOpts := playwright.BrowserTypeLaunchOptions{}
	browserType := pw.Chromium
	if r.Engine == "firefox" {
		browserType = pw.Firefox
	} else {
		launchOpts.ChromiumSandbox = playwright.Bool(true)
	}
	browser, err := browserType.Launch(launchOpts)
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("launching %s: %w", r.Engine, err)
	}

	r.pw = pw
	r.browser = browser
	return r.browser, nil
}

// Render implements ports.Renderer: opens a fresh, isolated browser
// context, navigates to url (opts.FetchTimeoutSeconds bounds it), and
// returns the rendered DOM's HTML. ctx cancellation closes the in-flight
// page promptly by racing ctx.Done() against the navigation goroutine,
// since Playwright takes no context.Context directly.
func (r *Renderer) Render(ctx context.Context, url string, opts ports.FetchOptions) (string, error) {
	browser, err := r.ensureBrowser()
	if err != nil {
		return "", err
	}

	bctx, err := browser.NewContext(buildContextOptions(opts))
	if err != nil {
		return "", fmt.Errorf("creating browser context: %w", err)
	}
	defer bctx.Close()

	// A rendered page's JS can issue fetch()/XHR anywhere, so route every
	// request through the same loopback/private-IP guard as the plain
	// fetcher -- a DNS lookup here, not a connect-time hook, since
	// Playwright exposes no dial-level control (a narrow resolve-then-connect
	// gap vs. the plain fetcher). data: URLs are always allowed.
	allowURL := r.AllowURL
	if allowURL == nil {
		allowURL = netguard.URLAllowed
	}
	if err := bctx.Route("**/*", ssrfRouteHandler(allowURL)); err != nil {
		return "", fmt.Errorf("installing SSRF request guard: %w", err)
	}

	if opts.Cookie != "" {
		if err := setCookies(bctx, url, opts.Cookie); err != nil {
			return "", fmt.Errorf("setting cookies: %w", err)
		}
	}

	page, err := bctx.NewPage()
	if err != nil {
		return "", fmt.Errorf("opening page: %w", err)
	}

	gotoOpts := playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateLoad}
	if opts.FetchTimeoutSeconds > 0 {
		gotoOpts.Timeout = playwright.Float(float64(opts.FetchTimeoutSeconds) * 1000)
	}

	type result struct {
		html string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if _, err := page.Goto(url, gotoOpts); err != nil {
			done <- result{err: fmt.Errorf("rendering %s: %w", url, err)}
			return
		}
		// "load" fires once static resources finish, often before a SPA's
		// own JS has rendered real content (confirmed: DOM empty at
		// `load`, populated ~1s later). networkidle is a proxy for "JS
		// done fetching"; a page with continuous background requests just
		// times out here (not an error) and rendering proceeds regardless.
		_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State:   playwright.LoadStateNetworkidle,
			Timeout: gotoOpts.Timeout,
		})
		html, err := page.Content()
		if err != nil {
			done <- result{err: fmt.Errorf("reading rendered content of %s: %w", url, err)}
			return
		}
		done <- result{html: html}
	}()

	select {
	case <-ctx.Done():
		_ = page.Close()
		return "", ctx.Err()
	case res := <-done:
		if res.err != nil {
			return "", res.err
		}
		html := res.html
		if opts.MaxResponseBytes > 0 && len(html) > opts.MaxResponseBytes {
			html = html[:opts.MaxResponseBytes]
		}
		return html, nil
	}
}

// buildContextOptions maps FetchOptions' identity knobs (user agent, basic
// auth) onto a fresh browser context's options -- pulled out of Render to
// keep this field mapping off its cognitive complexity.
func buildContextOptions(opts ports.FetchOptions) playwright.BrowserNewContextOptions {
	contextOpts := playwright.BrowserNewContextOptions{}
	if opts.UserAgent != "" {
		contextOpts.UserAgent = playwright.String(opts.UserAgent)
	}
	if opts.BasicAuthUser != "" || opts.BasicAuthPass != "" {
		contextOpts.HttpCredentials = &playwright.HttpCredentials{
			Username: opts.BasicAuthUser,
			Password: opts.BasicAuthPass,
		}
	}
	return contextOpts
}

// ssrfRouteHandler returns the playwright.Route callback Render installs:
// blocks any request allowURL rejects (see Render's SSRF-guard comment) --
// its own function so the branching doesn't add to Render's complexity.
func ssrfRouteHandler(allowURL func(string) bool) func(playwright.Route) {
	return func(route playwright.Route) {
		reqURL := route.Request().URL()
		if strings.HasPrefix(reqURL, "data:") || allowURL(reqURL) {
			_ = route.Continue()
			return
		}
		_ = route.Abort("blockedbyclient")
	}
}

// setCookies parses opts.Cookie (a raw "name=value; ..." Cookie header)
// into individual cookies scoped to url, using net/http's own header
// parser rather than hand-rolling one.
func setCookies(bctx playwright.BrowserContext, url, cookieHeader string) error {
	req := &http.Request{Header: http.Header{"Cookie": []string{cookieHeader}}}
	parsed := req.Cookies()
	if len(parsed) == 0 {
		return nil
	}
	cookies := make([]playwright.OptionalCookie, len(parsed))
	for i, c := range parsed {
		cookies[i] = playwright.OptionalCookie{Name: c.Name, Value: c.Value, URL: playwright.String(url)}
	}
	return bctx.AddCookies(cookies)
}

// Close shuts down this renderer's browser and Playwright driver, if one
// was ever started. Not called anywhere in cmd/crawl's lifecycle (it runs
// until killed), but kept as an explicit release path for tests.
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.browser != nil {
		_ = r.browser.Close()
		r.browser = nil
	}
	if r.pw != nil {
		err := r.pw.Stop()
		r.pw = nil
		return err
	}
	return nil
}

var _ ports.Renderer = (*Renderer)(nil)
