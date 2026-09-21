package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/domain"
)

// newTestFetcher mirrors internal/adapters/httpfetcher/fetcher_test.go's own
// helper: swaps the production SSRF guard (netguard.Transport) back out for
// the plain default transport, since every test here fetches from an
// httptest.Server on a loopback address the guard would otherwise block.
func newTestFetcher() *httpfetcher.Fetcher {
	f := httpfetcher.New(domain.DefaultOperationalSettings())
	f.Client.Transport = http.DefaultTransport
	return f
}

// connectedTestServer wires newServer's mcp.Server to a real mcp.Client over
// an in-memory transport (mcp.NewInMemoryTransports), so a test can exercise
// the two tool handlers exactly as a real chat turn would -- through
// CallTool, not by calling webSearch/truncate directly.
func connectedTestServer(t *testing.T, searxBaseURL string, resultCount int, userAgent string) *mcp.ClientSession {
	t.Helper()
	server := newServer(searxBaseURL, resultCount, userAgent, newTestFetcher())
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d: %+v", len(result.Content), result.Content)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return tc.Text
}

func TestWebSearchTool_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("expected /search, got %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "golang release notes" {
			t.Errorf("expected q=golang release notes, got %q", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("expected format=json, got %q", got)
		}
		w.Write([]byte(`{"results":["go 1.26 release notes"]}`))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, srv.URL, 0, "")
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_search", Arguments: map[string]any{"query": "golang release notes"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	if got := textContent(t, result); got != `{"results":["go 1.26 release notes"]}` {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestWebSearchTool_UpstreamErrorSurfacesAsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cs := connectedTestServer(t, srv.URL, 0, "")
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_search", Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatalf("unexpected transport-level error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error result for a 500 upstream, got %+v", result)
	}
	if !strings.Contains(textContent(t, result), "500") {
		t.Errorf("expected the status code in the error text, got %q", textContent(t, result))
	}
}

func TestWebFetchTool_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body><p>hello from the page</p></body></html>"))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, "http://unused.example", 0, "")
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_fetch", Arguments: map[string]any{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	if !strings.Contains(textContent(t, result), "hello from the page") {
		t.Errorf("expected the fetched page's text, got %q", textContent(t, result))
	}
}

// TestWebSearchTool_ResultCountCapsResults proves a positive resultCount
// (from domain.ChatEndpoint.WebSearchResultCount, delivered as
// WEB_SEARCH_RESULT_COUNT -- see main's own doc comment) actually reaches
// the tool call and truncates the returned "results" array, exercised
// through CallTool exactly as a real chat turn would invoke it.
func TestWebSearchTool_ResultCountCapsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"a"},{"title":"b"},{"title":"c"}],"query":"x"}`))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, srv.URL, 2, "")
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_search", Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := textContent(t, result)
	if !strings.Contains(got, `"title":"a"`) || !strings.Contains(got, `"title":"b"`) {
		t.Errorf("expected the first 2 results kept, got %q", got)
	}
	if strings.Contains(got, `"title":"c"`) {
		t.Errorf("expected the 3rd result dropped, got %q", got)
	}
	if !strings.Contains(got, `"query":"x"`) {
		t.Errorf("expected other top-level fields preserved, got %q", got)
	}
}

// TestWebFetchTool_UsesConfiguredUserAgent proves a non-empty userAgent
// (from application.ChatOptions.UserAgent, delivered as
// WEB_FETCH_USER_AGENT) actually reaches the outgoing fetch request.
func TestWebFetchTool_UsesConfiguredUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, "http://unused.example", 0, "custom-agent/9.0")
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_fetch", Arguments: map[string]any{"url": srv.URL},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUserAgent != "custom-agent/9.0" {
		t.Errorf("expected the configured user agent, got %q", gotUserAgent)
	}
}

func TestWebFetchTool_FetchErrorSurfacesAsToolError(t *testing.T) {
	cs := connectedTestServer(t, "http://unused.example", 0, "")
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "web_fetch", Arguments: map[string]any{"url": "not a url"},
	})
	if err != nil {
		t.Fatalf("unexpected transport-level error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error result for an invalid URL, got %+v", result)
	}
}

// TestWebSearch_NonSuccessStatusIncludesTruncatedBody calls webSearch
// directly (rather than through the tool, like the other tests in this
// file) specifically to prove the long-body branch: a 500-status body
// longer than truncate's 500-char cutoff must come back truncated with a
// note, not verbatim -- TestWebSearchTool_UpstreamErrorSurfacesAsToolError
// only exercises a short body, which never reaches that branch.
func TestWebSearch_NonSuccessStatusIncludesTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("x", 600)))
	}))
	defer srv.Close()

	_, err := webSearch(context.Background(), srv.URL, "x", 0)
	if err == nil {
		t.Fatal("expected an error for a non-2xx status")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected the status code in the error, got %v", err)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("expected the long body truncated in the error, got %v", err)
	}
}

// TestWebSearch_RequestBuildError proves a malformed base URL is reported
// as a build error rather than panicking or silently no-op'ing.
func TestWebSearch_RequestBuildError(t *testing.T) {
	_, err := webSearch(context.Background(), "://not a url", "x", 0)
	if err == nil {
		t.Fatal("expected an error for a malformed base URL")
	}
}

// TestWebSearch_ConnectionError proves a base URL nothing is listening on
// surfaces as an error rather than hanging or panicking.
func TestWebSearch_ConnectionError(t *testing.T) {
	_, err := webSearch(context.Background(), "http://127.0.0.1:1", "x", 0)
	if err == nil {
		t.Fatal("expected an error when nothing is listening")
	}
}

func TestCapResults_ZeroOrNegativeMeansNoCap(t *testing.T) {
	body := []byte(`{"results":[1,2,3]}`)
	if got := capResults(body, 0); got != string(body) {
		t.Errorf("expected body unchanged for resultCount=0, got %q", got)
	}
	if got := capResults(body, -1); got != string(body) {
		t.Errorf("expected body unchanged for resultCount=-1, got %q", got)
	}
}

func TestCapResults_FewerResultsThanCapLeavesBodyUnchanged(t *testing.T) {
	body := []byte(`{"results":[1,2]}`)
	if got := capResults(body, 5); got != string(body) {
		t.Errorf("expected body unchanged when under the cap, got %q", got)
	}
}

func TestCapResults_MalformedJSONPassesThroughUnchanged(t *testing.T) {
	body := []byte(`not json`)
	if got := capResults(body, 1); got != string(body) {
		t.Errorf("expected malformed body passed through, got %q", got)
	}
}

func TestCapResults_MissingResultsKeyPassesThroughUnchanged(t *testing.T) {
	body := []byte(`{"query":"x"}`)
	if got := capResults(body, 1); got != string(body) {
		t.Errorf("expected body without a results key passed through, got %q", got)
	}
}

func TestCapResults_ResultsNotAnArrayPassesThroughUnchanged(t *testing.T) {
	body := []byte(`{"results":"not an array"}`)
	if got := capResults(body, 1); got != string(body) {
		t.Errorf("expected body with a non-array results field passed through, got %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("expected a short string unchanged, got %q", got)
	}
	if got := truncate("this is long", 4); got != "this... (truncated)" {
		t.Errorf("expected a truncation note appended, got %q", got)
	}
}
