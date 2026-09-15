package httpembed_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"searchengine/internal/adapters/httpembed"
)

func TestEmbedder_Dimensions(t *testing.T) {
	e := httpembed.New(httpembed.Config{Dimensions: 64})
	if e.Dimensions() != 64 {
		t.Errorf("expected 64, got %d", e.Dimensions())
	}
}

func TestEmbedder_DefaultDimensions(t *testing.T) {
	e := httpembed.New(httpembed.Config{Dimensions: 0})
	if e.Dimensions() != 128 {
		t.Errorf("expected default 128, got %d", e.Dimensions())
	}
	e = httpembed.New(httpembed.Config{Dimensions: -1})
	if e.Dimensions() != 128 {
		t.Errorf("expected default 128 for negative input, got %d", e.Dimensions())
	}
}

func TestEmbedder_SuccessReturnsVector(t *testing.T) {
	var gotBody map[string]interface{}
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, APIKey: "secret-key", Model: "test-model", Dimensions: 3})
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 || vec[1] != 0.2 || vec[2] != 0.3 {
		t.Errorf("unexpected vector: %v", vec)
	}
	if gotPath != "/embeddings" {
		t.Errorf("expected POST to /embeddings, got %q", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected Authorization header 'Bearer secret-key', got %q", gotAuth)
	}
	if gotBody["input"] != "hello world" || gotBody["model"] != "test-model" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
}

func TestEmbedder_BaseURLTrailingSlashTolerated(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL + "/", Dimensions: 1})
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/embeddings" {
		t.Errorf("expected /embeddings, got %q", gotPath)
	}
}

func TestEmbedder_NoAPIKeyOmitsAuthorizationHeader(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1})
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawHeader {
		t.Error("expected no Authorization header when APIKey is unset")
	}
}

func TestEmbedder_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("model not found"))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !containsAll(err.Error(), "500", "model not found") {
		t.Errorf("expected error to mention status and body, got: %v", err)
	}
}

func TestEmbedder_NonOKStatusBodyIsTruncated(t *testing.T) {
	huge := make([]byte, 10000)
	for i := range huge {
		huge[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(huge)
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
	if len(err.Error()) > 700 {
		t.Errorf("expected the huge response body to be truncated in the error, got %d chars", len(err.Error()))
	}
}

// TestEmbedder_NonOKStatusBodyRedactsAPIKey proves the configured API key
// never appears verbatim in the error Embed returns -- this error is
// persisted to a crawl job's record and shown in the admin UI, so if a
// malicious/misconfigured embeddings endpoint echoes the request's own
// Authorization header back in its error body (some gateways do), that
// shouldn't round-trip the real key back out through this app.
func TestEmbedder_NonOKStatusBodyRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`invalid request, got Authorization: Bearer sk-super-secret-key`))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, APIKey: "sk-super-secret-key", Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if strings.Contains(err.Error(), "sk-super-secret-key") {
		t.Errorf("expected the API key to be redacted from the error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("expected a [REDACTED] marker in place of the API key, got: %v", err)
	}
}

// TestEmbedder_NonOKStatusBodyUnaffectedWithNoAPIKey proves redact is a
// no-op when no API key is configured -- nothing to scrub, the body passes
// through unchanged.
func TestEmbedder_NonOKStatusBodyUnaffectedWithNoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream unavailable`))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
	if !strings.Contains(err.Error(), "upstream unavailable") {
		t.Errorf("expected the response body preserved when no API key is configured, got: %v", err)
	}
}

func TestEmbedder_MalformedJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestEmbedder_EmptyDataReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error when the response carries no embedding data")
	}
}

func TestEmbedder_DimensionMismatchReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 2, 3}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 128})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected a dimension-mismatch error")
	}
	if !containsAll(err.Error(), "128", "3") {
		t.Errorf("expected error to mention both dimension counts, got: %v", err)
	}
}

func TestEmbedder_InvalidBaseURLReturnsError(t *testing.T) {
	e := httpembed.New(httpembed.Config{BaseURL: "://invalid", Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error building a request against an invalid base URL")
	}
}

func TestEmbedder_BodyReadErrorReturnsError(t *testing.T) {
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

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error when the response body can't be fully read")
	}
}

func TestEmbedder_NetworkErrorReturnsError(t *testing.T) {
	e := httpembed.New(httpembed.Config{BaseURL: "http://127.0.0.1:1", Dimensions: 8})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error connecting to an unreachable address")
	}
}

func TestEmbedder_ContextDeadlineExceededReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := e.Embed(ctx, "x")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
