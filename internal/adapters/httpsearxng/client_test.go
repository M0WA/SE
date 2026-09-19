package httpsearxng_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"searchengine/internal/adapters/httpsearxng"
)

// erroringBody is an io.ReadCloser whose Read always fails, so a test can
// exercise Search's "reading response body" error branch without a real
// network fault.
type erroringBody struct{}

func (erroringBody) Read(p []byte) (int, error) { return 0, errors.New("simulated read failure") }
func (erroringBody) Close() error               { return nil }

// erroringBodyTransport wraps a real transport but swaps the response body
// for one that always fails to read.
type erroringBodyTransport struct{ base http.RoundTripper }

func (t erroringBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = erroringBody{}
	return resp, nil
}

func TestSearch_Success(t *testing.T) {
	var gotPath, gotQuery, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Go", "url": "https://go.dev", "content": "The Go language"},
				{"title": "Go wiki", "url": "https://go.dev/wiki", "content": "Wiki"},
			},
		})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	results, err := c.Search(context.Background(), srv.URL, "golang", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/search" {
		t.Errorf("path = %q, want /search", gotPath)
	}
	if gotQuery != "golang" {
		t.Errorf("q = %q, want golang", gotQuery)
	}
	if gotFormat != "json" {
		t.Errorf("format = %q, want json", gotFormat)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Go" || results[0].URL != "https://go.dev" || results[0].Snippet != "The Go language" {
		t.Errorf("unexpected first result: %+v", results[0])
	}
}

func TestSearch_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	if _, err := c.Search(context.Background(), srv.URL+"/", "q", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/search" {
		t.Errorf("path = %q, want /search", gotPath)
	}
}

func TestSearch_CountLimitsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "a", "url": "https://a.example", "content": "a"},
				{"title": "b", "url": "https://b.example", "content": "b"},
				{"title": "c", "url": "https://c.example", "content": "c"},
			},
		})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	results, err := c.Search(context.Background(), srv.URL, "q", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected count to cap results at 2, got %d", len(results))
	}
}

func TestSearch_CountAboveResultLengthReturnsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "a", "url": "https://a.example", "content": "a"},
			},
		})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	results, err := c.Search(context.Background(), srv.URL, "q", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected all 1 result when count exceeds available, got %d", len(results))
	}
}

func TestSearch_NonPositiveCountReturnsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "a", "url": "https://a.example", "content": "a"},
				{"title": "b", "url": "https://b.example", "content": "b"},
			},
		})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	results, err := c.Search(context.Background(), srv.URL, "q", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected all results for a non-positive count, got %d", len(results))
	}
}

func TestSearch_EmptyResultsIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := httpsearxng.New()
	results, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %v", results)
	}
}

func TestSearch_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := httpsearxng.New()
	_, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
}

func TestSearch_NonSuccessStatusLongBodyIsTruncated(t *testing.T) {
	longBody := strings.Repeat("x", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(longBody))
	}))
	defer srv.Close()

	c := httpsearxng.New()
	_, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if strings.Contains(err.Error(), longBody) {
		t.Errorf("expected truncated error message, got full length body")
	}
}

func TestSearch_MalformedJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := httpsearxng.New()
	_, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err == nil {
		t.Fatal("expected error for malformed JSON body")
	}
}

func TestSearch_RequestBuildError(t *testing.T) {
	c := httpsearxng.New()
	_, err := c.Search(context.Background(), "http://\x7f invalid", "q", 5)
	if err == nil {
		t.Fatal("expected error for invalid base URL")
	}
}

func TestSearch_ReadBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := &httpsearxng.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: http.DefaultTransport}}}
	_, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err == nil {
		t.Fatal("expected error when reading the response body fails")
	}
	if !strings.Contains(err.Error(), "reading response body") {
		t.Errorf("error = %v, want it to mention reading response body", err)
	}
}

func TestSearch_NetworkError(t *testing.T) {
	c := httpsearxng.New()
	// Nothing listens here -- connection refused.
	_, err := c.Search(context.Background(), "http://127.0.0.1:1", "q", 5)
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}

func TestNew_DefaultHTTPClient(t *testing.T) {
	c := httpsearxng.New()
	if c.HTTPClient == nil {
		t.Fatal("expected New() to set a default HTTPClient")
	}
	if c.HTTPClient.Timeout <= 0 {
		t.Errorf("expected a positive default timeout, got %v", c.HTTPClient.Timeout)
	}
}

func TestSearch_NilHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "a", "url": "https://a.example", "content": "a"},
			},
		})
	}))
	defer srv.Close()

	c := &httpsearxng.Client{} // HTTPClient left nil
	results, err := c.Search(context.Background(), srv.URL, "q", 5)
	if err != nil {
		t.Fatalf("unexpected error with nil HTTPClient: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}
