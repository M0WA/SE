// Package httpembed implements ports.EmbeddingProvider by calling an
// OpenAI-compatible embeddings HTTP endpoint -- a local inference server
// (Ollama, llama.cpp, LM Studio, ...) or a hosted API -- so a deployment can
// opt into a real trained embedding model without this binary itself taking
// on an ML runtime dependency (no bundled model weights, no ONNX runtime).
// See internal/domain/settings.go's OperationalSettingsValues.
// EmbeddingProvider for how a deployment opts into this over the default
// hashembed.Embedder.
package httpembed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds a single embeddings call -- generous enough for a
// slow local CPU-only inference server, but never so long that a stuck
// endpoint hangs a search request indefinitely. Unlike httpfetcher's
// timeout, this isn't admin-configurable: the request shape here is a
// short, fixed-size text-to-vector call, not an open-ended page fetch, so
// one sane built-in value covers every deployment.
const requestTimeout = 30 * time.Second

// maxResponseBytes caps how much of the HTTP response body is ever read --
// an embedding response is normally a few KB even for a few-thousand-
// dimensional vector, so this is purely a safety bound against a
// misbehaving or malicious endpoint, not a real limit in practice.
const maxResponseBytes = 1 << 20

// defaultDimensions matches hashembed's own default, used only when Config
// leaves Dimensions unset (<=0) -- callers driven by
// domain.OperationalSettingsValues never hit this, since Set already
// defaults EmbeddingHTTPDimensions itself, but a directly-constructed
// Embedder (e.g. in a test) still gets a sane, non-zero value.
const defaultDimensions = 128

// Config configures a new Embedder. BaseURL and APIKey are read verbatim
// from the admin-configured EmbeddingHTTPBaseURL/EmbeddingHTTPAPIKey
// settings; APIKey is never logged by this package.
type Config struct {
	// BaseURL is the embeddings API's base URL, e.g.
	// "http://localhost:11434/v1" for a local Ollama server -- Embed POSTs
	// to "<BaseURL>/embeddings" (a trailing slash on BaseURL is tolerated).
	BaseURL string
	// APIKey, when non-empty, is sent as "Authorization: Bearer <APIKey>"
	// on every request. Optional -- many local inference servers need none.
	APIKey string
	// Model is sent as the request body's "model" field.
	Model string
	// Dimensions is the expected embedding vector length; Embed errors
	// clearly if a response's actual vector length differs, rather than
	// silently returning a mis-sized vector that would corrupt every
	// downstream cosine-similarity calculation. <=0 falls back to
	// defaultDimensions.
	Dimensions int
}

// Embedder calls an OpenAI-compatible POST {base_url}/embeddings endpoint:
// request body {"input": text, "model": "..."}, response body
// {"data":[{"embedding":[...]}]}.
type Embedder struct {
	baseURL string
	apiKey  string
	model   string
	dims    int
	client  *http.Client
}

func New(cfg Config) *Embedder {
	dims := cfg.Dimensions
	if dims <= 0 {
		dims = defaultDimensions
	}
	return &Embedder{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		dims:    dims,
		client:  &http.Client{},
	}
}

func (e *Embedder) Dimensions() int { return e.dims }

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed calls the configured embeddings endpoint for text and returns its
// vector. A network failure, non-2xx status, malformed JSON response, empty
// result, or a response vector whose length doesn't match the configured
// Dimensions all return a clear, wrapped error -- never a panic, and never a
// silently zero-valued or mis-sized vector.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	reqBody, err := json.Marshal(embeddingRequest{Input: text, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("httpembed: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpembed: calling embeddings endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("httpembed: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("httpembed: embeddings endpoint returned status %d: %s", resp.StatusCode, truncate(redact(body, e.apiKey)))
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("httpembed: decoding response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("httpembed: embeddings endpoint returned no embedding data")
	}
	vec := parsed.Data[0].Embedding
	if len(vec) != e.dims {
		return nil, fmt.Errorf("httpembed: expected %d-dimensional embedding, got %d -- check the configured dimensions match the model actually serving %s", e.dims, len(vec), e.baseURL)
	}
	return vec, nil
}

// modelsResponse mirrors the OpenAI-compatible GET {base_url}/models
// response: {"object":"list","data":[{"id":"...", ...}]}.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListModels calls the configured endpoint's GET {base_url}/models and
// returns every model ID it reports -- used by the admin Settings page to
// prefill the embedding model field's suggestions, so an admin doesn't have
// to already know (or guess/mistype) a valid model ID for whatever provider
// they've pointed this at. Not part of ports.EmbeddingProvider: unlike
// Embed, this isn't something every embedding provider implementation can
// support (hashembed has no remote catalog to list), so callers that want
// it type-assert for it specifically (see
// restapi.handleAdminEmbeddingsModels). Some OpenAI-compatible servers
// don't implement /models at all -- a 404 there surfaces as a normal
// non-2xx error, same as any other endpoint failure.
func (e *Embedder) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpembed: calling models endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("httpembed: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("httpembed: models endpoint returned status %d: %s", resp.StatusCode, truncate(redact(body, e.apiKey)))
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("httpembed: decoding response: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// truncate bounds how much of a non-2xx response body an error message
// carries, so a large HTML error page doesn't blow up a log line.
func truncate(s string) string {
	const max = 500
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// redact removes every occurrence of apiKey from body before it's ever
// included in an error -- this error is not just logged, it's persisted to
// a crawl job's record and shown back in the admin UI (see
// crawl_loop.go -> the crawl job store -> the admin crawl-job endpoints),
// so an embeddings endpoint that's malicious, misconfigured, or simply
// echoes request headers back in its own error bodies (some gateways do)
// could otherwise round-trip the "Authorization: Bearer <apiKey>" header
// this same request just sent right back through this app's own
// error/logging path. An empty apiKey (no key configured) is a no-op.
func redact(body []byte, apiKey string) string {
	if apiKey == "" {
		return string(body)
	}
	return strings.ReplaceAll(string(body), apiKey, "[REDACTED]")
}
