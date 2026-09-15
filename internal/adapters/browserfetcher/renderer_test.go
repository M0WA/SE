package browserfetcher_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"searchengine/internal/adapters/browserfetcher"
	"searchengine/internal/ports"
)

// requireBrowserTests skips unless SE_BROWSER_TESTS=1 is set. A Renderer
// test launches a real headless browser -- the first run on any given
// machine downloads Playwright's own managed Chromium/Firefox binary (a
// few hundred MB), which would make an ordinary `go test ./...` (and CI)
// slow and network-dependent by default. Set SE_BROWSER_TESTS=1 to run
// these for real (once Playwright's browsers are installed -- see
// Renderer.ensureBrowser's doc comment -- the first run pays the download
// cost, every run after is fast).
func requireBrowserTests(t *testing.T) {
	t.Helper()
	if os.Getenv("SE_BROWSER_TESTS") == "" {
		t.Skip("skipping: set SE_BROWSER_TESTS=1 to run real headless-browser tests (downloads Playwright's Chromium on first run)")
	}
}

// skipIfNoUsableSandbox skips the test (rather than failing it) when err is
// Chromium's own "No usable sandbox!" failure -- Renderer deliberately
// enables ChromiumSandbox (see ensureBrowser's doc comment), which requires
// the host to support unprivileged user namespaces. Some hosts (e.g.
// Ubuntu 23.10+ with its AppArmor restriction on unprivileged userns, or
// this project's own CI/dev sandboxes) don't, and that's a fact about the
// machine running the test, not a bug for these tests to catch -- the
// production behavior for a host that genuinely can't sandbox is to fail
// loudly with this same diagnostic (surfaced as the crawl job's
// fetch_failed error), by design.
func skipIfNoUsableSandbox(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "No usable sandbox") {
		t.Skip("skipping: this host can't do unprivileged-namespace Chromium sandboxing (see the error for why) -- see skipIfNoUsableSandbox")
	}
}

func TestRenderer_ExecutesJavaScriptAndWaitsForLoad(t *testing.T) {
	requireBrowserTests(t)
	r := browserfetcher.New("chromium")
	defer r.Close()

	url := "data:text/html," + `<html><body><div id="x">before</div>` +
		`<script>document.getElementById('x').textContent='after-js'</script></body></html>`
	html, err := r.Render(context.Background(), url, ports.FetchOptions{})
	skipIfNoUsableSandbox(t, err)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(html, "after-js") {
		t.Errorf("expected the rendered HTML to reflect the JS-driven DOM change, got: %s", html)
	}
	if strings.Contains(html, ">before<") {
		t.Errorf("expected the pre-JS content to be gone from the rendered HTML, got: %s", html)
	}
}

func TestRenderer_RespectsMaxResponseBytes(t *testing.T) {
	requireBrowserTests(t)
	r := browserfetcher.New("chromium")
	defer r.Close()

	html, err := r.Render(context.Background(), "data:text/html,<html><body>hello world</body></html>", ports.FetchOptions{MaxResponseBytes: 10})
	skipIfNoUsableSandbox(t, err)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(html) != 10 {
		t.Errorf("expected the rendered HTML truncated to 10 bytes, got %d bytes: %q", len(html), html)
	}
}

// alwaysAllowURL is a Renderer.AllowURL override for tests that render an
// httptest.Server -- a loopback address the real SSRF guard
// (netguard.URLAllowed) rejects by design. The guard itself is exercised
// separately, in TestRenderer_BlocksLoopbackNavigationTarget.
func alwaysAllowURL(string) bool { return true }

func TestRenderer_SetsUserAgentAndCookie(t *testing.T) {
	requireBrowserTests(t)
	var gotUA, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotUA = req.UserAgent()
		if c, err := req.Cookie("session"); err == nil {
			gotCookie = c.Value
		}
		fmt.Fprint(w, "<html><body>ok</body></html>")
	}))
	defer srv.Close()

	r := browserfetcher.New("chromium")
	r.AllowURL = alwaysAllowURL
	defer r.Close()

	_, err := r.Render(context.Background(), srv.URL, ports.FetchOptions{
		UserAgent: "searchengine-test-agent/1.0",
		Cookie:    "session=abc123",
	})
	skipIfNoUsableSandbox(t, err)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUA != "searchengine-test-agent/1.0" {
		t.Errorf("expected the custom User-Agent to reach the server, got %q", gotUA)
	}
	if gotCookie != "abc123" {
		t.Errorf("expected the cookie to reach the server, got %q", gotCookie)
	}
}

func TestRenderer_SetsBasicAuth(t *testing.T) {
	requireBrowserTests(t)
	var gotUser, gotPass string
	var gotOK bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// A browser only sends Basic Auth credentials after being
		// challenged with a 401 + WWW-Authenticate -- unlike the plain
		// httpfetcher.Fetcher, which sets the Authorization header
		// preemptively via req.SetBasicAuth. Without this challenge, the
		// browser's first (and only) request here would carry no
		// credentials at all, regardless of whether Renderer set them up
		// correctly.
		user, pass, ok := req.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotUser, gotPass, gotOK = user, pass, ok
		fmt.Fprint(w, "<html><body>ok</body></html>")
	}))
	defer srv.Close()

	r := browserfetcher.New("chromium")
	r.AllowURL = alwaysAllowURL
	defer r.Close()

	_, err := r.Render(context.Background(), srv.URL, ports.FetchOptions{
		BasicAuthUser: "admin", BasicAuthPass: "hunter2",
	})
	skipIfNoUsableSandbox(t, err)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotOK || gotUser != "admin" || gotPass != "hunter2" {
		t.Errorf("expected basic auth admin/hunter2 to reach the server, got ok=%v user=%q pass=%q", gotOK, gotUser, gotPass)
	}
}

// TestRenderer_ContextCancellationInterruptsRender proves cancelling ctx
// stops an in-flight render promptly, the same way it stops a plain HTTP
// fetch -- needed for Handler.CancelCrawlJob to work for a rendered crawl,
// not just an unrendered one.
func TestRenderer_ContextCancellationInterruptsRender(t *testing.T) {
	requireBrowserTests(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-block // never responds until the test unblocks it
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	r := browserfetcher.New("chromium")
	r.AllowURL = alwaysAllowURL
	defer r.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := r.Render(ctx, srv.URL, ports.FetchOptions{})
	if err == nil {
		t.Fatal("expected an error from the cancelled render")
	}
}

// TestRenderer_BlocksLoopbackNavigationTarget exercises the real SSRF guard
// (Renderer.AllowURL left at its default, netguard.URLAllowed) against a
// loopback navigation target -- the browser's very first request (the page
// navigation itself) must be aborted before it ever reaches the target.
func TestRenderer_BlocksLoopbackNavigationTarget(t *testing.T) {
	requireBrowserTests(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "<html><body>should never be reached</body></html>")
	}))
	defer srv.Close()

	r := browserfetcher.New("chromium")
	defer r.Close()

	_, err := r.Render(context.Background(), srv.URL, ports.FetchOptions{})
	skipIfNoUsableSandbox(t, err)
	if err == nil {
		t.Fatal("expected the loopback navigation target to be blocked by the SSRF guard")
	}
}
