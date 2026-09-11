package httpfetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestFetcher_Fetch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("expected User-Agent header to be set")
		}
		w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()

	f := httpfetcher.New(nil)
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

	f := httpfetcher.New(nil)
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Error("expected error for 404 status")
	}
}

func TestFetcher_Fetch_InvalidURL(t *testing.T) {
	f := httpfetcher.New(nil)
	if _, err := f.Fetch(context.Background(), "://ungueltig"); err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestFetcher_Fetch_ConnectionError(t *testing.T) {
	f := httpfetcher.New(nil)
	if _, err := f.Fetch(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Error("expected error for unreachable host")
	}
}

func TestFetcher_FetchWithOptions_SetsCookieAndBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "session=abc123" {
			t.Errorf("expected Cookie header %q, got %q", "session=abc123", got)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "hunter2" {
			t.Errorf("expected basic auth alice/hunter2, got %q/%q (ok=%v)", user, pass, ok)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := httpfetcher.New(nil)
	_, err := f.FetchWithOptions(context.Background(), srv.URL, ports.FetchOptions{
		Cookie:        "session=abc123",
		BasicAuthUser: "alice",
		BasicAuthPass: "hunter2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetcher_FetchWithOptions_NoOptionsMeansNoHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("expected no Cookie header, got %q", got)
		}
		if _, _, ok := r.BasicAuth(); ok {
			t.Error("expected no basic auth")
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := httpfetcher.New(nil)
	if _, err := f.FetchWithOptions(context.Background(), srv.URL, ports.FetchOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetcher_FetchWithOptions_UsesSettingsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		FetchTimeout: 5 * time.Millisecond,
	})
	f := httpfetcher.New(settings)
	if _, err := f.FetchWithOptions(context.Background(), srv.URL, ports.FetchOptions{}); err == nil {
		t.Error("expected timeout error")
	}
}

func TestFetcher_FetchWithOptions_UsesSettingsUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "custom-agent/9.0" {
			t.Errorf("expected custom User-Agent, got %q", got)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		UserAgent: "custom-agent/9.0",
	})
	f := httpfetcher.New(settings)
	if _, err := f.FetchWithOptions(context.Background(), srv.URL, ports.FetchOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetcher_FetchWithOptions_PerRequestUserAgentOverridesSettings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "per-crawl-agent/1.0" {
			t.Errorf("expected per-request User-Agent to win, got %q", got)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		UserAgent: "process-default-agent/1.0",
	})
	f := httpfetcher.New(settings)
	if _, err := f.FetchWithOptions(context.Background(), srv.URL, ports.FetchOptions{UserAgent: "per-crawl-agent/1.0"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetcher_Fetch_BodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("expected a hijackable response writer")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		// Promise more body than is actually sent, then close the
		// connection early -- io.ReadAll sees an unexpected EOF.
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer srv.Close()

	f := httpfetcher.New(nil)
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Error("expected an error when the response body can't be fully read")
	}
}
