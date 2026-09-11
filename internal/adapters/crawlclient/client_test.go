package crawlclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/ports"
)

func TestClient_Crawl_Success(t *testing.T) {
	var gotOpts ports.CrawlOptions
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/crawl" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotOpts); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"crawled_count": 4})
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	count, err := c.Crawl(context.Background(), ports.CrawlOptions{
		SeedURLs: []string{"http://a"},
		MaxPages: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 4 {
		t.Errorf("expected count=4, got %d", count)
	}
	if len(gotOpts.SeedURLs) != 1 || gotOpts.SeedURLs[0] != "http://a" || gotOpts.MaxPages != 5 {
		t.Errorf("unexpected options sent to crawl server: %+v", gotOpts)
	}
}

func TestClient_Crawl_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "crawl failed", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}); err == nil {
		t.Error("expected error for a non-200 response")
	}
}

func TestClient_Crawl_ConnectionError(t *testing.T) {
	c := crawlclient.New("http://127.0.0.1:1")
	if _, err := c.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}); err == nil {
		t.Error("expected error for an unreachable crawl server")
	}
}

func TestClient_Crawl_InvalidBaseURL(t *testing.T) {
	c := crawlclient.New("http://\x7f")
	if _, err := c.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}); err == nil {
		t.Error("expected error when the base URL contains an invalid control character")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type erroringReadCloser struct{}

func (erroringReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (erroringReadCloser) Close() error             { return nil }

func TestClient_Crawl_ResponseBodyReadError(t *testing.T) {
	c := crawlclient.New("http://crawl-server.invalid")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: erroringReadCloser{}, Header: make(http.Header)}, nil
	})}
	if _, err := c.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}); err == nil {
		t.Error("expected error when the response body fails to read")
	}
}

func TestClient_Crawl_InvalidResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}); err == nil {
		t.Error("expected error for an invalid response body")
	}
}
