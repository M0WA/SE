// Package browserfetcher implements ports.Renderer by driving a real,
// headless browser through Playwright: it executes a page's JavaScript in
// a sandboxed browser process and waits for the page's load event before
// extracting the final, client-side-rendered HTML -- for a site whose real
// content only exists after that rendering happens, which a plain HTTP GET
// (see internal/adapters/httpfetcher) can never see.
package browserfetcher

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/mxschmitt/playwright-go"

	"searchengine/internal/ports"
)

// Renderer wraps one browser engine (Chromium or Firefox -- see
// domain.RendererChromium/RendererFirefox, the names crawl-server's wiring
// keys its Renderer map by). It's safe for concurrent use: each Render call
// opens its own isolated browser context (cookies, credentials, and the
// page itself never leak between concurrent crawls sharing one Renderer),
// while the underlying browser process and Playwright driver are started
// at most once, lazily, on first actual use.
type Renderer struct {
	// Engine is "chromium" or "firefox" (see domain.Renderer* constants).
	Engine string

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
// browser engine, memoizing it for reuse across every subsequent Render
// call. Installing downloads Playwright's own managed browser binary the
// first time this engine is ever used on this machine (a few hundred MB) --
// slow, but idempotent, and it happens on this engine's very first crawl
// rather than at process startup, so a deployment that never enables
// rendering never triggers it.
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

	// Playwright's own documented default for ChromiumSandbox is false --
	// it launches with --no-sandbox unless told otherwise, favoring broad
	// compatibility (works even where the OS-level sandbox mechanism
	// Chromium wants isn't available) over defense in depth. This crawler
	// runs pages from arbitrary, untrusted sites, so that tradeoff is wrong
	// here: explicitly turn Chromium's own sandbox on. Firefox has no
	// equivalent Playwright toggle -- its content-process sandboxing is
	// always active regardless of launch options.
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

// Render implements ports.Renderer: it opens a fresh, isolated browser
// context for this fetch alone (so concurrent crawls sharing a Renderer
// never see each other's cookies or credentials), navigates to url,
// waits for the page's load event (opts.FetchTimeoutSeconds bounds it,
// falling back to Playwright's own 30s default when unset), and returns
// the fully rendered DOM's HTML.
//
// ctx cancellation (e.g. Handler.CancelCrawlJob) closes the in-flight page
// to interrupt navigation promptly, the same way a cancelled context
// aborts a plain HTTP fetch -- Playwright's own API has no direct
// context.Context parameter, so this is done by racing ctx.Done() against
// the navigation in a goroutine.
func (r *Renderer) Render(ctx context.Context, url string, opts ports.FetchOptions) (string, error) {
	browser, err := r.ensureBrowser()
	if err != nil {
		return "", err
	}

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
	bctx, err := browser.NewContext(contextOpts)
	if err != nil {
		return "", fmt.Errorf("creating browser context: %w", err)
	}
	defer bctx.Close()

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

// setCookies parses opts.Cookie (a raw "name=value; name2=value2" Cookie
// header, exactly what ports.FetchOptions.Cookie already carries for the
// plain HTTP fetcher) into individual cookies scoped to url, using
// net/http's own header parser rather than hand-rolling one.
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

// Close shuts down this renderer's browser and Playwright driver, if a
// Render call ever actually started one. Not currently called anywhere in
// this codebase's process lifecycle (cmd/crawl has no graceful-shutdown
// path at all -- it runs until killed, same as its DB connection and every
// other resource), but kept as a clean, explicit release path for tests
// and any future caller that does manage its own shutdown.
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
