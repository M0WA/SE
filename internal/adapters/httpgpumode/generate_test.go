package httpgpumode_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"searchengine/internal/adapters/httpgpumode"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestGenerate_Success(t *testing.T) {
	var gotPath, gotToken, gotPrompt, gotAspectRatio string
	var gotDurationSeconds int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		var body struct {
			Prompt          string `json:"prompt"`
			AspectRatio     string `json:"aspect_ratio"`
			DurationSeconds int    `json:"duration_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPrompt = body.Prompt
		gotAspectRatio = body.AspectRatio
		gotDurationSeconds = body.DurationSeconds
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"prompt_id": "abc-123"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	id, err := c.Generate(context.Background(), cfgFor(srv.URL), "a cat", "9:16 (Portrait Widescreen)", 8)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if id != "abc-123" {
		t.Fatalf("expected abc-123, got %q", id)
	}
	if gotPath != "/gpu/api/generate" {
		t.Fatalf("expected POST /gpu/api/generate, got %s", gotPath)
	}
	if gotToken != "s3cr3t" {
		t.Fatalf("expected X-Internal-Token forwarded, got %q", gotToken)
	}
	if gotPrompt != "a cat" {
		t.Fatalf("expected the prompt forwarded, got %q", gotPrompt)
	}
	if gotAspectRatio != "9:16 (Portrait Widescreen)" || gotDurationSeconds != 8 {
		t.Fatalf("expected aspect_ratio/duration_seconds forwarded, got %q/%d", gotAspectRatio, gotDurationSeconds)
	}
}

func TestGenerate_ConflictReturnsErrGPUGenerateNotInVisionMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the GPU is not in vision mode", http.StatusConflict)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.Generate(context.Background(), cfgFor(srv.URL), "a cat", "", 0)
	if !errors.Is(err, ports.ErrGPUGenerateNotInVisionMode) {
		t.Fatalf("expected ErrGPUGenerateNotInVisionMode, got %v", err)
	}
}

func TestGenerate_ServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.Generate(context.Background(), cfgFor(srv.URL), "a cat", "", 0)
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if errors.Is(err, ports.ErrGPUGenerateNotInVisionMode) {
		t.Fatal("a 500 must not be reported as ErrGPUGenerateNotInVisionMode")
	}
}

func TestGenerate_RejectsDisallowedURL(t *testing.T) {
	c := &httpgpumode.Client{}
	_, err := c.Generate(context.Background(), cfgFor("http://169.254.169.254"), "a cat", "", 0)
	if err == nil {
		t.Fatal("expected the link-local control URL to be rejected")
	}
}

func TestGenerate_MalformedJSONResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.Generate(context.Background(), cfgFor(srv.URL), "a cat", "", 0)
	if err == nil {
		t.Fatal("expected an error decoding a malformed response")
	}
}

func TestGenerateResult_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "done", "view_url": "/gpu/api/view?filename=out.mp4"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	result, err := c.GenerateResult(context.Background(), cfgFor(srv.URL), "abc-123")
	if err != nil {
		t.Fatalf("GenerateResult: %v", err)
	}
	if gotPath != "/gpu/api/generate/abc-123" {
		t.Fatalf("expected GET /gpu/api/generate/abc-123, got %s", gotPath)
	}
	if result.Status != "done" || result.ViewURL != "/gpu/api/view?filename=out.mp4" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestGenerateResult_ServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.GenerateResult(context.Background(), cfgFor(srv.URL), "abc-123")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

func TestGenerateResult_RejectsDisallowedURL(t *testing.T) {
	c := &httpgpumode.Client{}
	_, err := c.GenerateResult(context.Background(), cfgFor("http://169.254.169.254"), "abc-123")
	if err == nil {
		t.Fatal("expected the link-local control URL to be rejected")
	}
}

func TestGenerateResult_MalformedJSONResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not valid json"))
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.GenerateResult(context.Background(), cfgFor(srv.URL), "abc-123")
	if err == nil {
		t.Fatal("expected an error decoding a malformed response")
	}
}

func TestViewAsset_Success(t *testing.T) {
	var gotPath, gotQuery, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotToken = r.Header.Get("X-Internal-Token")
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	ct, body, err := c.ViewAsset(context.Background(), cfgFor(srv.URL), "/gpu/api/view?filename=out.mp4")
	if err != nil {
		t.Fatalf("ViewAsset: %v", err)
	}
	defer body.Close()
	if ct != "video/mp4" {
		t.Fatalf("expected video/mp4, got %q", ct)
	}
	data, _ := io.ReadAll(body)
	if string(data) != "video-bytes" {
		t.Fatalf("expected the upstream body streamed through, got %q", data)
	}
	if gotPath != "/gpu/api/view" || gotQuery != "filename=out.mp4" {
		t.Fatalf("unexpected upstream request: path=%q query=%q", gotPath, gotQuery)
	}
	if gotToken != "s3cr3t" {
		t.Fatalf("expected X-Internal-Token forwarded, got %q", gotToken)
	}
}

func TestViewAsset_RejectsUnexpectedURLPrefix(t *testing.T) {
	c := &httpgpumode.Client{}
	_, _, err := c.ViewAsset(context.Background(), cfgFor("http://example.com"), "/some/other/path")
	if err == nil {
		t.Fatal("expected an error for a viewURL outside the expected prefix")
	}
}

func TestViewAsset_NonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, _, err := c.ViewAsset(context.Background(), cfgFor(srv.URL), "/gpu/api/view?filename=out.mp4")
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestViewAsset_RejectsDisallowedURL(t *testing.T) {
	c := &httpgpumode.Client{}
	_, _, err := c.ViewAsset(context.Background(), cfgFor("http://169.254.169.254"), "/gpu/api/view?filename=out.mp4")
	if err == nil {
		t.Fatal("expected the link-local control URL to be rejected")
	}
}

func TestGenerate_ExpiredContextIsError(t *testing.T) {
	c := &httpgpumode.Client{HTTPClient: http.DefaultClient}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := c.Generate(ctx, domain.GPUModeSettings{ControlBaseURL: "http://127.0.0.1", ControlAPIKey: "x"}, "a cat", "", 0); err == nil {
		t.Fatal("expected an error for an already-expired context")
	}
}

func TestGenerate_ReadingResponseBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"prompt_id": "abc"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: srv.Client().Transport}}}
	if _, err := c.Generate(context.Background(), cfgFor(srv.URL), "a cat", "", 0); err == nil {
		t.Fatal("expected an error when the response body fails to read")
	}
}

func TestGenerateResult_ExpiredContextIsError(t *testing.T) {
	c := &httpgpumode.Client{HTTPClient: http.DefaultClient}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := c.GenerateResult(ctx, domain.GPUModeSettings{ControlBaseURL: "http://127.0.0.1", ControlAPIKey: "x"}, "abc-123"); err == nil {
		t.Fatal("expected an error for an already-expired context")
	}
}

func TestGenerateResult_ReadingResponseBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: srv.Client().Transport}}}
	if _, err := c.GenerateResult(context.Background(), cfgFor(srv.URL), "abc-123"); err == nil {
		t.Fatal("expected an error when the response body fails to read")
	}
}

func TestViewAsset_ExpiredContextIsError(t *testing.T) {
	c := &httpgpumode.Client{HTTPClient: http.DefaultClient}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, _, err := c.ViewAsset(ctx, domain.GPUModeSettings{ControlBaseURL: "http://127.0.0.1", ControlAPIKey: "x"}, "/gpu/api/view?filename=out.mp4"); err == nil {
		t.Fatal("expected an error for an already-expired context")
	}
}

func TestViewAsset_ReadingErrorBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: srv.Client().Transport}}}
	if _, _, err := c.ViewAsset(context.Background(), cfgFor(srv.URL), "/gpu/api/view?filename=out.mp4"); err == nil {
		t.Fatal("expected an error for a non-200 response whose body also fails to read")
	}
}
