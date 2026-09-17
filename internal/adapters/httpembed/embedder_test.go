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

func TestEmbedder_ListModelsSuccess(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{"id": "intfloat/e5-large-v2", "object": "model"},
				{"id": "Qwen/Qwen3-VL-Embedding-8B", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, APIKey: "secret-key"})
	ids, err := e.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("expected GET /models, got %q", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected Authorization header 'Bearer secret-key', got %q", gotAuth)
	}
	if len(ids) != 2 || ids[0] != "intfloat/e5-large-v2" || ids[1] != "Qwen/Qwen3-VL-Embedding-8B" {
		t.Errorf("unexpected model list: %v", ids)
	}
}

func TestEmbedder_ListModelsNoAPIKeyOmitsAuthorizationHeader(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL})
	if _, err := e.ListModels(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawHeader {
		t.Error("expected no Authorization header when APIKey is unset")
	}
}

func TestEmbedder_ListModelsEmptyDataReturnsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": []map[string]interface{}{}})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL})
	ids, err := e.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected an empty (not nil-panicking) slice, got %v", ids)
	}
}

func TestEmbedder_ListModelsNonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, APIKey: "secret-key"})
	_, err := e.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("expected the API key redacted from the error, got: %v", err)
	}
}

func TestEmbedder_ListModelsMalformedJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL})
	if _, err := e.ListModels(context.Background()); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestEmbedder_ListModelsNetworkErrorReturnsError(t *testing.T) {
	e := httpembed.New(httpembed.Config{BaseURL: "http://127.0.0.1:1"})
	if _, err := e.ListModels(context.Background()); err == nil {
		t.Fatal("expected an error connecting to an unreachable address")
	}
}

// TestEmbedder_ListModelsBodyReadErrorReturnsError mirrors
// TestEmbedder_BodyReadErrorReturnsError for ListModels.
func TestEmbedder_ListModelsBodyReadErrorReturnsError(t *testing.T) {
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
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL})
	_, err := e.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error when the response body can't be fully read")
	}
}

// TestEmbedder_RetriesOn429ThenSucceeds proves the core rate-limit
// retry behavior IONOS's own docs call for (see
// docs.ionos.com/cloud/ai/ai-model-hub/how-tos/rate-limits): a 429 is
// retried, not treated as a terminal failure, and a subsequent success is
// returned to the caller as if it had succeeded on the first try.
func TestEmbedder_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 2, 3}}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 3, RateLimitInitialBackoff: time.Millisecond})
	vec, err := e.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("unexpected vector: %v", vec)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (1 rate-limited, 1 success), got %d", calls)
	}
}

// TestEmbedder_RetriesExhaustedReturnsRateLimitError proves Embed gives up
// after RateLimitMaxRetries and surfaces the underlying 429 error, rather
// than retrying forever and stalling a caller like
// application.RunEmbeddingRecomputeJob.
func TestEmbedder_RetriesExhaustedReturnsRateLimitError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 3, RateLimitMaxRetries: 2, RateLimitInitialBackoff: time.Millisecond})
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if !containsAll(err.Error(), "429") {
		t.Errorf("expected the final error to mention the 429 status, got: %v", err)
	}
	if calls != 3 { // the initial attempt plus 2 retries
		t.Errorf("expected exactly 3 calls (1 initial + 2 retries), got %d", calls)
	}
}

// TestEmbedder_Retries529HonorsRetryAfter proves a 529's own Retry-After
// header is honored exactly (per IONOS's "retry only after the indicated
// delay" guidance), not overridden by the exponential backoff sequence
// used for 429.
func TestEmbedder_Retries529HonorsRetryAfter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(529)
			_, _ = w.Write([]byte("overloaded"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	// A large initial backoff proves the wait actually came from
	// Retry-After (0s) rather than this exponential schedule -- if the
	// call took anywhere near this long, the fallback path was used
	// instead.
	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1, RateLimitInitialBackoff: 5 * time.Second})
	start := time.Now()
	if _, err := e.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotWait := time.Since(start)
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls, got %d", calls)
	}
	if gotWait > time.Second {
		t.Errorf("expected Retry-After: 0 to be honored (a near-instant retry), took %v", gotWait)
	}
}

// TestEmbedder_NonRateLimitStatusIsNeverRetried proves an ordinary
// non-2xx failure (one retrying could never fix) returns immediately on
// the first attempt.
func TestEmbedder_NonRateLimitStatusIsNeverRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1, RateLimitInitialBackoff: time.Millisecond})
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for a non-retryable status, got %d", calls)
	}
}

// TestEmbedder_RetryAbortsOnContextCancellation proves a long backoff
// wait doesn't outlive the caller's own context.
func TestEmbedder_RetryAbortsOnContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1, RateLimitInitialBackoff: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.Embed(ctx, "x"); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("expected the context's own timeout to cut the backoff short, took %v", elapsed)
	}
}

func TestListModels_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"id": "model-a"}},
		})
	}))
	defer srv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, RateLimitInitialBackoff: time.Millisecond})
	ids, err := e.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "model-a" {
		t.Errorf("unexpected models: %v", ids)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls, got %d", calls)
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

// embedInput is the one field these chunking tests need from a real
// {"input": "...", "model": "..."} embeddings request body.
type embedInput struct {
	Input string `json:"input"`
}

func TestEmbedder_ChunkSizeZeroSendsTextWholeRegardlessOfLength(t *testing.T) {
	var calls int
	var gotInput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body embedInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotInput = body.Input
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 2}}},
		})
	}))
	defer srv.Close()

	longText := strings.Repeat("word ", 500) // far more than any small ChunkSizeTokens budget would allow in one chunk
	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 2, ChunkSizeTokens: 0})
	vec, err := e.Embed(context.Background(), longText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 embeddings call with chunking disabled, got %d", calls)
	}
	if gotInput != longText {
		t.Errorf("expected the full, unmodified text sent whole, got %q", gotInput)
	}
	if vec[0] != 1 || vec[1] != 2 {
		t.Errorf("expected the single call's own vector returned unchanged, got %v", vec)
	}
}

func TestEmbedder_ChunksLongTextAndMeanPoolsVectors(t *testing.T) {
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs = append(inputs, body.Input)
		// A distinct, checkable vector per call: (n, 2n) for the nth call.
		n := float32(len(inputs))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{n, 2 * n}}},
		})
	}))
	defer srv.Close()

	// ChunkSizeTokens=2, approxCharsPerToken=3 -> a 6-character budget per
	// chunk. Each word below is 10 characters (over budget on its own), so
	// the first word in every chunk is always accepted regardless (a chunk
	// is never left empty just because one word doesn't fit), giving
	// exactly one word per chunk here -- three words, three chunks.
	text := "aaaaaaaaaa bbbbbbbbbb cccccccccc"
	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 2, ChunkSizeTokens: 2})
	vec, err := e.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("expected 3 chunked embeddings calls, got %d: %v", len(inputs), inputs)
	}
	if inputs[0] != "aaaaaaaaaa" || inputs[1] != "bbbbbbbbbb" || inputs[2] != "cccccccccc" {
		t.Errorf("expected one word per chunk, in order, got %v", inputs)
	}
	// Mean of (1,2), (2,4), (3,6) is (2,4).
	if vec[0] != 2 || vec[1] != 4 {
		t.Errorf("expected the mean-pooled vector (2,4), got %v", vec)
	}
}

func TestEmbedder_ChunkingPacksMultipleShortWordsPerChunk(t *testing.T) {
	var inputs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs = append(inputs, body.Input)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 0}}},
		})
	}))
	defer srv.Close()

	// ChunkSizeTokens=4 -> 12-character budget. Six 2-character words ("aa
	// bb cc dd ee ff", each contributing 3 chars incl. its joining space)
	// pack four per chunk (4*3=12, exactly the budget) before overflowing.
	text := "aa bb cc dd ee ff"
	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 2, ChunkSizeTokens: 4})
	if _, err := e.Embed(context.Background(), text); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("expected 2 chunks (4 words + 2 words), got %d: %v", len(inputs), inputs)
	}
	if inputs[0] != "aa bb cc dd" || inputs[1] != "ee ff" {
		t.Errorf("expected chunks [%q, %q], got %v", "aa bb cc dd", "ee ff", inputs)
	}
}

func TestEmbedder_TokenizeURLSplitsChunkThatMeasuresOverBudget(t *testing.T) {
	var embedInputs []string
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		embedInputs = append(embedInputs, body.Input)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 0}}},
		})
	}))
	defer embedSrv.Close()

	var tokenizeCalls int
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		tokenizeCalls++
		// Pretend every word costs 2 real tokens -- double the character
		// estimate's assumption for these 2-character words -- so the
		// initial character-based chunk (4 words, "costed" at ~4 tokens by
		// the estimate) actually measures 8 tokens, over the 4-token
		// budget, forcing a split.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 2 * len(strings.Fields(body.Prompt))})
	}))
	defer tokenizeSrv.Close()

	// Same word/character shape as the packing test above: "aa bb cc dd ee
	// ff" char-chunks into ["aa bb cc dd", "ee ff"] first.
	text := "aa bb cc dd ee ff"
	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, Dimensions: 2, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	if _, err := e.Embed(context.Background(), text); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "aa bb cc dd" (measures 8 > 4) splits into "aa bb" (4) and "cc dd"
	// (4), both within budget; "ee ff" (measures 4 <= 4) stays whole.
	want := []string{"aa bb", "cc dd", "ee ff"}
	if len(embedInputs) != len(want) {
		t.Fatalf("expected %d embed calls after tokenize-verified splitting, got %d: %v", len(want), len(embedInputs), embedInputs)
	}
	for i, w := range want {
		if embedInputs[i] != w {
			t.Errorf("chunk %d: expected %q, got %q (all: %v)", i, w, embedInputs[i], embedInputs)
		}
	}
	if tokenizeCalls == 0 {
		t.Error("expected at least one call to the configured TokenizeURL")
	}
}

func TestEmbedder_TokenizeURLErrorPropagatesFromEmbed(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("embeddings endpoint should never be called when tokenize verification fails first")
	}))
	defer embedSrv.Close()

	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, Dimensions: 2, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	_, err := e.Embed(context.Background(), "aa bb cc dd ee ff")
	if err == nil {
		t.Fatal("expected an error when the configured TokenizeURL fails")
	}
	if !strings.Contains(err.Error(), "tokenize") {
		t.Errorf("expected the error to mention the tokenize endpoint, got %v", err)
	}
}

// TestEmbedder_TokenizeURLUnsplittableChunkNeverLoops proves
// fitChunkToTokenBudget's recursion actually terminates for a single
// "word" (no whitespace to split on) that a misbehaving/unusual tokenizer
// reports as perpetually over budget -- it must be embedded once, as-is,
// rather than recursing forever or panicking.
func TestEmbedder_TokenizeURLUnsplittableChunkNeverLoops(t *testing.T) {
	var embedCalls, tokenizeCalls int
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embedCalls++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1, 0}}},
		})
	}))
	defer embedSrv.Close()

	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenizeCalls++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 999999})
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, Dimensions: 2, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	if _, err := e.Embed(context.Background(), "oneunsplittableword"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedCalls != 1 {
		t.Errorf("expected exactly 1 embeddings call for the single unsplittable chunk, got %d", embedCalls)
	}
	if tokenizeCalls != 1 {
		t.Errorf("expected exactly 1 tokenize call (no whitespace to split on, so no recursion), got %d", tokenizeCalls)
	}
}

// TestEmbedder_ChunkTextWhitespaceOnlyReturnsItUnchanged covers chunkText's
// "no words at all" branch: whitespace-only text (strings.Fields returns
// nothing) is sent to embedChunk exactly as given, rather than chunkText
// producing an empty chunk list.
func TestEmbedder_ChunkTextWhitespaceOnlyReturnsItUnchanged(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body embedInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Input != "   " {
			t.Errorf("expected the original whitespace-only text sent unchanged, got %q", body.Input)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer embedSrv.Close()

	e := httpembed.New(httpembed.Config{BaseURL: embedSrv.URL, Dimensions: 1, ChunkSizeTokens: 4})
	if _, err := e.Embed(context.Background(), "   "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmbedder_ChunkEmbedFailurePropagatesWithChunkIndex(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	// Same one-word-per-chunk shape as TestEmbedder_ChunksLongTextAndMeanPoolsVectors.
	e := httpembed.New(httpembed.Config{BaseURL: srv.URL, Dimensions: 1, ChunkSizeTokens: 2})
	_, err := e.Embed(context.Background(), "aaaaaaaaaa bbbbbbbbbb cccccccccc")
	if err == nil {
		t.Fatal("expected an error when a later chunk's embed call fails")
	}
	if !strings.Contains(err.Error(), "chunk 2/3") {
		t.Errorf("expected the error to name which chunk failed (\"chunk 2/3\"), got %v", err)
	}
}

func TestEmbedder_FitChunkToTokenBudgetPropagatesLeftHalfTokenizeError(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("embeddings endpoint should never be called when a recursive tokenize check fails")
	}))
	defer embedSrv.Close()

	var tokenizeCalls int
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenizeCalls++
		if tokenizeCalls == 1 {
			// The whole chunk: report it over budget, forcing a split into
			// left/right halves.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 999})
			return
		}
		// The left half's own verification call (evaluated before the
		// right half's, per fitChunkToTokenBudget's call order): fail it.
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	_, err := e.Embed(context.Background(), "aa bb cc dd")
	if err == nil {
		t.Fatal("expected an error when the left half's tokenize verification fails")
	}
	if tokenizeCalls != 2 {
		t.Errorf("expected exactly 2 tokenize calls (whole chunk, then the failing left half), got %d", tokenizeCalls)
	}
}

func TestEmbedder_FitChunkToTokenBudgetPropagatesRightHalfTokenizeError(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("embeddings endpoint should never be called when a recursive tokenize check fails")
	}))
	defer embedSrv.Close()

	var tokenizeCalls int
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenizeCalls++
		switch tokenizeCalls {
		case 1:
			// The whole chunk: over budget, forcing a split.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 999})
		case 2:
			// The left half: within budget, no further recursion.
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 1})
		default:
			// The right half's own verification call: fail it.
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	_, err := e.Embed(context.Background(), "aa bb cc dd")
	if err == nil {
		t.Fatal("expected an error when the right half's tokenize verification fails")
	}
	if tokenizeCalls != 3 {
		t.Errorf("expected exactly 3 tokenize calls (whole chunk, left half, then the failing right half), got %d", tokenizeCalls)
	}
}

func TestEmbedder_CountTokensSendsAuthorizationHeaderWhenAPIKeySet(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer embedSrv.Close()

	var gotAuth string
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 1})
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: embedSrv.URL, APIKey: "secret-key", Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	if _, err := e.Embed(context.Background(), "aa bb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected the tokenize call to carry the same Authorization header as embeddings calls, got %q", gotAuth)
	}
}

func TestEmbedder_CountTokensInvalidURLReturnsError(t *testing.T) {
	e := httpembed.New(httpembed.Config{
		BaseURL: "http://unused.invalid", Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: "://invalid",
	})
	_, err := e.Embed(context.Background(), "aa bb")
	if err == nil {
		t.Fatal("expected an error building a request against an invalid TokenizeURL")
	}
}

func TestEmbedder_CountTokensNetworkErrorReturnsError(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tokenizeURL := unreachable.URL
	unreachable.Close() // closed before use: connections to it now fail outright

	e := httpembed.New(httpembed.Config{
		BaseURL: "http://unused.invalid", Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeURL,
	})
	_, err := e.Embed(context.Background(), "aa bb")
	if err == nil {
		t.Fatal("expected an error when the tokenize endpoint can't be reached")
	}
}

func TestEmbedder_CountTokensBodyReadErrorReturnsError(t *testing.T) {
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("expected a hijackable response writer")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: "http://unused.invalid", Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	_, err := e.Embed(context.Background(), "aa bb")
	if err == nil {
		t.Fatal("expected an error when the tokenize response body can't be fully read")
	}
}

func TestEmbedder_CountTokensMalformedJSONReturnsError(t *testing.T) {
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer tokenizeSrv.Close()

	e := httpembed.New(httpembed.Config{
		BaseURL: "http://unused.invalid", Dimensions: 1, ChunkSizeTokens: 4, TokenizeURL: tokenizeSrv.URL,
	})
	_, err := e.Embed(context.Background(), "aa bb")
	if err == nil {
		t.Fatal("expected an error decoding a malformed tokenize response")
	}
}
