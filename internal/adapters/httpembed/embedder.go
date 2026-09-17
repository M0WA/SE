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
	"strconv"
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

// approxCharsPerToken estimates a chunk's token count from its character
// count when no TokenizeURL is configured -- this package has no real
// tokenizer of its own (see the package doc comment). Deliberately low
// (real English text averages closer to 4 chars/token) so the estimate
// errs toward *smaller* chunks: undercounting tokens (and so overflowing
// the configured budget) is the failure mode chunking exists to prevent,
// so this constant is tuned to make that rare rather than to be precise.
const approxCharsPerToken = 3

// maxTokenizeSplitDepth bounds how many times fitChunkToTokenBudget
// recursively halves a chunk that verified over budget via TokenizeURL --
// a hard backstop, not something normal input should ever approach (the
// character estimate that produced the chunk in the first place is
// already conservative), so a pathological input is used as one
// (possibly still slightly over-budget) chunk rather than recursing
// indefinitely.
const maxTokenizeSplitDepth = 4

// rateLimitMaxRetries bounds how many times Embed/ListModels retries a
// rate-limited response before giving up and returning the error to the
// caller -- otherwise a single persistently-throttled call could retry
// forever and stall a caller like application.RunEmbeddingRecomputeJob
// (which needs to eventually move on and count a document as failed
// rather than block the entire corpus behind one document).
const rateLimitMaxRetries = 5

// rateLimitInitialBackoff/rateLimitMaxBackoff bound the exponential
// backoff used after a 429 -- IONOS's AI Model Hub rate-limit guidance
// (docs.ionos.com/cloud/ai/ai-model-hub/how-tos/rate-limits) calls for
// exponential backoff there specifically because 429 carries no
// server-given delay (unlike 529 -- see retryDelay below).
const (
	rateLimitInitialBackoff = 500 * time.Millisecond
	rateLimitMaxBackoff     = 30 * time.Second
)

// statusOverloaded is IONOS's "the platform overall is overloaded"
// status -- distinct from the contract-specific 429, and the one case
// that does carry a Retry-After the client is expected to honor exactly
// rather than backing off on its own schedule. Not a named constant in
// net/http (529 isn't part of the standard HTTP status registry).
const statusOverloaded = 529

// isRateLimitStatus reports whether status is one of the two codes
// IONOS's rate-limit docs describe as retryable. Any other non-2xx status
// (400, 401, 404, ...) means retrying would just fail identically again,
// so those are never retried.
func isRateLimitStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == statusOverloaded
}

// retryDelay picks how long to wait before the next attempt: a 529
// carries its own Retry-After (seconds) that must be honored exactly per
// IONOS's guidance ("retry only after the indicated delay"); a 429 never
// carries one, so that case falls back to the caller's own exponential
// backoff sequence instead.
func retryDelay(resp *http.Response, backoff time.Duration) time.Duration {
	if resp.StatusCode == statusOverloaded {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return backoff
}

// waitForRetry blocks for wait, or returns ctx's error if it's cancelled
// first -- shared by Embed/ListModels' retry loops so a long backoff
// never outlives the caller's own context.
func waitForRetry(ctx context.Context, wait time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

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
	// RateLimitMaxRetries/RateLimitInitialBackoff override
	// rateLimitMaxRetries/rateLimitInitialBackoff -- a test-only hook so
	// the retry-on-429/529 behavior can be exercised without a real test
	// waiting out multi-second production backoff delays. Production
	// callers should leave both at their zero value.
	RateLimitMaxRetries     int
	RateLimitInitialBackoff time.Duration
	// ChunkSizeTokens and TokenizeURL mirror domain.EmbeddingHTTPEndpoint's
	// same-named fields -- see that type's doc comments for the full
	// rationale. 0/"" (the zero value) disables chunking entirely,
	// preserving this package's original single-call-per-Embed behavior
	// exactly.
	ChunkSizeTokens int
	TokenizeURL     string
}

// Embedder calls an OpenAI-compatible POST {base_url}/embeddings endpoint:
// request body {"input": text, "model": "..."}, response body
// {"data":[{"embedding":[...]}]}.
type Embedder struct {
	baseURL             string
	apiKey              string
	model               string
	dims                int
	client              *http.Client
	rateLimitMaxRetries int
	rateLimitBackoff    time.Duration
	chunkSizeTokens     int
	tokenizeURL         string
}

func New(cfg Config) *Embedder {
	dims := cfg.Dimensions
	if dims <= 0 {
		dims = defaultDimensions
	}
	maxRetries := cfg.RateLimitMaxRetries
	if maxRetries <= 0 {
		maxRetries = rateLimitMaxRetries
	}
	backoff := cfg.RateLimitInitialBackoff
	if backoff <= 0 {
		backoff = rateLimitInitialBackoff
	}
	return &Embedder{
		baseURL:             strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:              cfg.APIKey,
		model:               cfg.Model,
		dims:                dims,
		client:              &http.Client{},
		rateLimitMaxRetries: maxRetries,
		rateLimitBackoff:    backoff,
		chunkSizeTokens:     cfg.ChunkSizeTokens,
		tokenizeURL:         cfg.TokenizeURL,
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
// vector. When chunkSizeTokens is 0 (the default), this is exactly one
// call to embedChunk -- everything below is unchanged from before
// chunking existed. Otherwise, text is split into chunks (see chunkText),
// each embedded separately, and mean-pooled into one final vector (see
// combineVectors) -- so a document whose token count would otherwise
// exceed the model's context length still gets a real, whole-document
// vector rather than a hard error.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	chunks, err := e.chunkText(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 1 {
		return e.embedChunk(ctx, chunks[0])
	}
	vecs := make([][]float32, len(chunks))
	for i, c := range chunks {
		vec, err := e.embedChunk(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("httpembed: embedding chunk %d/%d: %w", i+1, len(chunks), err)
		}
		vecs[i] = vec
	}
	return combineVectors(vecs), nil
}

// embedChunk calls the configured embeddings endpoint for one chunk of
// text (or the whole text, when chunking is disabled) and returns its
// vector. A network failure, non-2xx status, malformed JSON response, empty
// result, or a response vector whose length doesn't match the configured
// Dimensions all return a clear, wrapped error -- never a panic, and never a
// silently zero-valued or mis-sized vector. A 429/529 response is retried
// with backoff (see isRateLimitStatus/retryDelay) up to rateLimitMaxRetries
// times before its error is finally returned.
func (e *Embedder) embedChunk(ctx context.Context, text string) ([]float32, error) {
	reqBody, err := json.Marshal(embeddingRequest{Input: text, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("httpembed: encoding request: %w", err)
	}

	backoff := e.rateLimitBackoff
	for attempt := 0; ; attempt++ {
		vec, resp, err := e.embedOnce(ctx, reqBody)
		if err == nil {
			return vec, nil
		}
		if resp == nil || !isRateLimitStatus(resp.StatusCode) || attempt >= e.rateLimitMaxRetries {
			return nil, err
		}
		if waitErr := waitForRetry(ctx, retryDelay(resp, backoff)); waitErr != nil {
			return nil, waitErr
		}
		backoff *= 2
		if backoff > rateLimitMaxBackoff {
			backoff = rateLimitMaxBackoff
		}
	}
}

// embedOnce makes a single attempt against the embeddings endpoint.
// resp is non-nil whenever a real HTTP response was received (even a
// non-2xx one), so Embed's retry loop can inspect its status/headers;
// it's nil only for a request-building or network-level failure, which
// Embed never retries.
func (e *Embedder) embedOnce(ctx context.Context, reqBody []byte) ([]float32, *http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: calling embeddings endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp, fmt.Errorf("httpembed: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp, fmt.Errorf("httpembed: embeddings endpoint returned status %d: %s", resp.StatusCode, truncate(redact(body, e.apiKey)))
	}

	var parsed embeddingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, resp, fmt.Errorf("httpembed: decoding response: %w", err)
	}
	if len(parsed.Data) == 0 {
		return nil, resp, fmt.Errorf("httpembed: embeddings endpoint returned no embedding data")
	}
	vec := parsed.Data[0].Embedding
	if len(vec) != e.dims {
		return nil, resp, fmt.Errorf("httpembed: expected %d-dimensional embedding, got %d -- check the configured dimensions match the model actually serving %s", e.dims, len(vec), e.baseURL)
	}
	return vec, resp, nil
}

// chunkText splits text into pieces for Embed to embed separately (see
// combineVectors), each estimated -- or, with tokenizeURL configured,
// confirmed -- to be at or under e.chunkSizeTokens tokens. Breaks only on
// whitespace, so a chunk never splits a word in half. Returns text
// unchanged as the only element when chunkSizeTokens is 0 (chunking
// disabled), text has no whitespace to split on at all, or text already
// fits in one chunk.
func (e *Embedder) chunkText(ctx context.Context, text string) ([]string, error) {
	if e.chunkSizeTokens <= 0 {
		return []string{text}, nil
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}, nil
	}

	charBudget := e.chunkSizeTokens * approxCharsPerToken
	var chunks []string
	var current []string
	currentLen := 0
	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, strings.Join(current, " "))
			current = nil
			currentLen = 0
		}
	}
	for _, w := range words {
		wLen := len(w) + 1 // +1 for the joining space
		if currentLen > 0 && currentLen+wLen > charBudget {
			flush()
		}
		current = append(current, w)
		currentLen += wLen
	}
	flush()

	if e.tokenizeURL == "" {
		return chunks, nil
	}
	// Exact mode: the character estimate above is only a starting point,
	// deliberately conservative but not infallible (dense-script text like
	// CJK tokenizes far denser than approxCharsPerToken assumes) -- verify
	// each chunk against the endpoint's own tokenizer and split further
	// (never trim/discard) anything that measures over budget.
	verified := make([]string, 0, len(chunks))
	for _, c := range chunks {
		fitted, err := e.fitChunkToTokenBudget(ctx, c, 0)
		if err != nil {
			return nil, err
		}
		verified = append(verified, fitted...)
	}
	return verified, nil
}

// fitChunkToTokenBudget ensures text fits within e.chunkSizeTokens tokens
// per e.tokenizeURL's own exact count, recursively splitting it into two
// halves (by words) and re-verifying each when it doesn't -- rather than
// trimming and discarding the excess, which would silently drop part of
// the document from ever being embedded. Bounded by
// maxTokenizeSplitDepth so a pathological chunk that still measures over
// budget after repeated halving can't recurse forever; it's used as one
// (possibly still slightly over-budget) chunk at that point rather than
// looping indefinitely, same as a chunk with no whitespace left to split
// on (len(words) < 2).
func (e *Embedder) fitChunkToTokenBudget(ctx context.Context, text string, depth int) ([]string, error) {
	count, err := e.countTokens(ctx, text)
	if err != nil {
		return nil, err
	}
	if count <= e.chunkSizeTokens || depth >= maxTokenizeSplitDepth {
		return []string{text}, nil
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		return []string{text}, nil
	}
	mid := len(words) / 2
	left, err := e.fitChunkToTokenBudget(ctx, strings.Join(words[:mid], " "), depth+1)
	if err != nil {
		return nil, err
	}
	right, err := e.fitChunkToTokenBudget(ctx, strings.Join(words[mid:], " "), depth+1)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// tokenizeRequest/tokenizeResponse mirror vLLM's own POST /tokenize
// contract ({"model":..., "prompt": text} -> {"count": N, ...}) -- the one
// HTTP embedding backend this package has confirmed exposes an equivalent
// route (see domain.EmbeddingHTTPEndpoint.TokenizeURL's doc comment on
// why this is explicit admin config, never auto-detected/guessed).
type tokenizeRequest struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
}

type tokenizeResponse struct {
	Count int `json:"count"`
}

// countTokens calls e.tokenizeURL for text's exact token count. Unlike
// embedChunk/listModelsOnce, this never retries on 429/529 -- it's an
// internal accuracy step inside chunking, not a user-facing operation
// worth the same backoff complexity, and a persistently rate-limited
// tokenizer endpoint should surface as a clear failure rather than
// silently stall Embed.
func (e *Embedder) countTokens(ctx context.Context, text string) (int, error) {
	reqBody, err := json.Marshal(tokenizeRequest{Model: e.model, Prompt: text})
	if err != nil {
		return 0, fmt.Errorf("httpembed: encoding tokenize request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenizeURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("httpembed: building tokenize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("httpembed: calling tokenize endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, fmt.Errorf("httpembed: reading tokenize response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("httpembed: tokenize endpoint returned status %d: %s", resp.StatusCode, truncate(redact(body, e.apiKey)))
	}
	var parsed tokenizeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("httpembed: decoding tokenize response: %w", err)
	}
	return parsed.Count, nil
}

// combineVectors mean-pools multiple chunk vectors (see chunkText) into
// one final vector representing the whole document -- the standard,
// simplest way to combine several same-length embeddings of different
// parts of one text into a single one for downstream cosine-similarity
// search. Only ever called with 2+ vectors: Embed calls embedChunk
// directly for the single-chunk case, never combineVectors.
func combineVectors(vecs [][]float32) []float32 {
	dims := len(vecs[0])
	out := make([]float32, dims)
	for _, v := range vecs {
		for i, x := range v {
			out[i] += x
		}
	}
	inv := 1 / float32(len(vecs))
	for i := range out {
		out[i] *= inv
	}
	return out
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
	backoff := e.rateLimitBackoff
	for attempt := 0; ; attempt++ {
		ids, resp, err := e.listModelsOnce(ctx)
		if err == nil {
			return ids, nil
		}
		if resp == nil || !isRateLimitStatus(resp.StatusCode) || attempt >= e.rateLimitMaxRetries {
			return nil, err
		}
		if waitErr := waitForRetry(ctx, retryDelay(resp, backoff)); waitErr != nil {
			return nil, waitErr
		}
		backoff *= 2
		if backoff > rateLimitMaxBackoff {
			backoff = rateLimitMaxBackoff
		}
	}
}

// listModelsOnce makes a single attempt against the models endpoint --
// see embedOnce's identical doc comment on why resp is returned alongside
// the error.
func (e *Embedder) listModelsOnce(ctx context.Context) ([]string, *http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/models", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: calling models endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp, fmt.Errorf("httpembed: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp, fmt.Errorf("httpembed: models endpoint returned status %d: %s", resp.StatusCode, truncate(redact(body, e.apiKey)))
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, resp, fmt.Errorf("httpembed: decoding response: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return ids, resp, nil
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
