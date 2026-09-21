package httpchat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"searchengine/internal/adapters/httpchat"
	"searchengine/internal/domain"
)

// erroringBody is an io.ReadCloser whose Read always fails, so a test can
// exercise Complete's "reading response body" error branch without a real
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

func TestComplete_Success(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": "hello there"}},
			},
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, APIKey: "secret-key", Model: "test-model"}
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}

	answer, err := c.Complete(context.Background(), endpoint, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "hello there" {
		t.Errorf("answer = %q, want %q", answer, "hello there")
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
	if gotBody["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", gotBody["model"])
	}
}

func TestComplete_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL + "/", Model: "m"}
	if _, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
}

func TestComplete_NoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	var gotAuth string
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, sawHeader = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"} // APIKey left empty
	if _, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawHeader {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestComplete_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m", APIKey: "secret-key"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("error leaked api key: %v", err)
	}
}

func TestComplete_NonSuccessStatusLongBodyNoAPIKey(t *testing.T) {
	longBody := strings.Repeat("x", 600)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(longBody))
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"} // no APIKey
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 600)) {
		t.Errorf("expected truncated error message, got full length body")
	}
}

func TestComplete_MalformedJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for malformed JSON body")
	}
}

func TestComplete_EmptyChoicesArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for empty choices array")
	}
}

func TestComplete_RequestBuildError(t *testing.T) {
	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: "http://\x7f invalid", Model: "m"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for invalid base URL")
	}
}

func TestComplete_ReadBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := &httpchat.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: http.DefaultTransport}}}
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error when reading the response body fails")
	}
	if !strings.Contains(err.Error(), "reading response body") {
		t.Errorf("error = %v, want it to mention reading response body", err)
	}
}

func TestComplete_NetworkError(t *testing.T) {
	c := httpchat.New()
	// Nothing listens here -- connection refused.
	endpoint := domain.ChatEndpoint{BaseURL: "http://127.0.0.1:1", Model: "m"}
	_, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}

func TestNew_DefaultHTTPClient(t *testing.T) {
	c := httpchat.New()
	if c.HTTPClient == nil {
		t.Fatal("expected New() to set a default HTTPClient")
	}
	if c.HTTPClient.Timeout <= 0 {
		t.Errorf("expected a positive default timeout, got %v", c.HTTPClient.Timeout)
	}
}

func TestModelMaxContextTokens_MatchesRequestedModel(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "other-model", "max_model_len": 4096},
				{"id": "test-model", "max_model_len": 32768},
			},
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, APIKey: "secret-key", Model: "test-model"}
	tokens, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || tokens != 32768 {
		t.Errorf("ModelMaxContextTokens = (%d, %v), want (32768, true)", tokens, ok)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-key")
	}
}

func TestModelMaxContextTokens_FallsBackToFirstEntryWhenNoIDMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "some-other-alias", "max_model_len": 8192},
			},
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "requested-model"}
	tokens, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok || tokens != 8192 {
		t.Errorf("ModelMaxContextTokens = (%d, %v), want (8192, true)", tokens, ok)
	}
}

func TestModelMaxContextTokens_NoPositiveValueReportsNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"id": "m"}}, // max_model_len omitted/zero
		})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	tokens, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || tokens != 0 {
		t.Errorf("ModelMaxContextTokens = (%d, %v), want (0, false)", tokens, ok)
	}
}

func TestModelMaxContextTokens_EmptyDataReportsNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	tokens, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || tokens != 0 {
		t.Errorf("ModelMaxContextTokens = (%d, %v), want (0, false)", tokens, ok)
	}
}

func TestModelMaxContextTokens_NoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"} // APIKey left empty
	if _, _, err := c.ModelMaxContextTokens(context.Background(), endpoint); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawHeader {
		t.Error("expected no Authorization header")
	}
}

func TestModelMaxContextTokens_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid api key"))
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m", APIKey: "secret-key"}
	_, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if ok {
		t.Error("expected ok=false alongside the error")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Errorf("error leaked api key: %v", err)
	}
}

func TestModelMaxContextTokens_MalformedJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	if _, _, err := c.ModelMaxContextTokens(context.Background(), endpoint); err == nil {
		t.Fatal("expected error for malformed JSON body")
	}
}

func TestModelMaxContextTokens_RequestBuildError(t *testing.T) {
	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: "http://\x7f invalid", Model: "m"}
	if _, _, err := c.ModelMaxContextTokens(context.Background(), endpoint); err == nil {
		t.Fatal("expected error for invalid base URL")
	}
}

func TestModelMaxContextTokens_NetworkError(t *testing.T) {
	c := httpchat.New()
	endpoint := domain.ChatEndpoint{BaseURL: "http://127.0.0.1:1", Model: "m"}
	if _, _, err := c.ModelMaxContextTokens(context.Background(), endpoint); err == nil {
		t.Fatal("expected error for unreachable endpoint")
	}
}

func TestModelMaxContextTokens_ReadBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{}})
	}))
	defer srv.Close()

	c := &httpchat.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: http.DefaultTransport}}}
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	_, _, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err == nil {
		t.Fatal("expected error when reading the response body fails")
	}
	if !strings.Contains(err.Error(), "reading models response body") {
		t.Errorf("error = %v, want it to mention reading models response body", err)
	}
}

func TestModelMaxContextTokens_NilHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"id": "m", "max_model_len": 2048}},
		})
	}))
	defer srv.Close()

	c := &httpchat.Client{} // HTTPClient left nil
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	tokens, ok, err := c.ModelMaxContextTokens(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("unexpected error with nil HTTPClient: %v", err)
	}
	if !ok || tokens != 2048 {
		t.Errorf("ModelMaxContextTokens = (%d, %v), want (2048, true)", tokens, ok)
	}
}

func TestComplete_NilHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer srv.Close()

	c := &httpchat.Client{} // HTTPClient left nil
	endpoint := domain.ChatEndpoint{BaseURL: srv.URL, Model: "m"}
	answer, err := c.Complete(context.Background(), endpoint, []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("unexpected error with nil HTTPClient: %v", err)
	}
	if answer != "ok" {
		t.Errorf("answer = %q, want %q", answer, "ok")
	}
}
