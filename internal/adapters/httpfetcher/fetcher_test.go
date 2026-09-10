package httpfetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"searchengine/internal/adapters/httpfetcher"
)

func TestFetcher_Fetch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("expected User-Agent header to be set")
		}
		w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()

	f := httpfetcher.New()
	body, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(body, "hello") {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestFetcher_Fetch_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := httpfetcher.New()
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Error("expected error for 404 status")
	}
}

func TestFetcher_Fetch_InvalidURL(t *testing.T) {
	f := httpfetcher.New()
	if _, err := f.Fetch(context.Background(), "://ungueltig"); err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestFetcher_Fetch_ConnectionError(t *testing.T) {
	f := httpfetcher.New()
	if _, err := f.Fetch(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Error("expected error for unreachable host")
	}
}
