// Package httpembed implements ports.EmbeddingProvider by calling an
// OpenAI-compatible embeddings HTTP endpoint, so a deployment can opt into
// a real trained model without this binary taking on an ML runtime
// dependency (see domain.OperationalSettingsValues.EmbeddingProvider).
//
// BaseURL/TokenizeURL are admin-configured, so every call goes through
// netguard's more permissive AllowedConfiguredEndpointIP policy (see
// checkEndpointURL): private/loopback stays allowed (a self-hosted backend
// commonly lives there), but cloud metadata and a few other classes are
// still blocked. Mirrors httpchat's identical SSRF guard.
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
	"sync"
	"time"
	"unicode/utf8"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/domain"
)

// requestTimeout bounds a single embeddings call -- generous for a slow
// CPU-only server, but bounded so a stuck endpoint can't hang a search
// indefinitely. Not admin-configurable: unlike a page fetch, this is a
// short fixed-size call, so one built-in value covers every deployment.
const requestTimeout = 30 * time.Second

// bearerPrefix is the HTTP Authorization header scheme this client sends
// e.apiKey under, on every request that carries one.
const bearerPrefix = "Bearer "

// maxResponseBytes caps how much of the HTTP response body is ever read --
// an embedding response is normally a few KB even for a few-thousand-
// dimensional vector, so this is purely a safety bound against a
// misbehaving or malicious endpoint, not a real limit in practice.
const maxResponseBytes = 1 << 20

// defaultDimensions matches hashembed's default, used only when Config
// leaves Dimensions unset -- domain.OperationalSettingsValues-driven
// callers never hit this (Set already defaults it), but a directly
// constructed Embedder (e.g. in a test) still gets a sane value.
const defaultDimensions = 128

// maxTokenizeSplitDepth bounds how many times fitChunkToTokenBudget
// recursively halves a chunk that verified over budget -- a hard backstop
// normal input shouldn't approach; a pathological input is used as one
// (possibly still slightly over-budget) chunk rather than recursing forever.
const maxTokenizeSplitDepth = 4

// rateLimitMaxRetries bounds Embed/ListModels' retries on a rate-limited
// response -- otherwise a persistently throttled call could stall a caller
// like RunEmbeddingRecomputeJob on one document forever.
const rateLimitMaxRetries = 5

// rateLimitInitialBackoff/rateLimitMaxBackoff bound the exponential
// backoff after a 429, per IONOS's AI Model Hub rate-limit guidance --
// 429 carries no server-given delay, unlike 529 (see retryDelay below).
const (
	rateLimitInitialBackoff = 500 * time.Millisecond
	rateLimitMaxBackoff     = 30 * time.Second
)

// statusOverloaded is IONOS's "platform overloaded" status -- distinct
// from 429, and the one case that carries a Retry-After to honor exactly
// rather than backing off on our own schedule. Not in net/http (not a
// standard status).
const statusOverloaded = 529

// checkEndpointURL rejects a BaseURL/TokenizeURL-derived URL that
// netguard.AllowedConfiguredEndpointIP blocks (link-local, e.g. cloud
// metadata; multicast; unspecified). Guards against ever pointing the
// embeddings endpoint at its own cloud metadata service, by admin mistake
// or a compromised session -- mirrors httpchat's identically-named helper.
func checkEndpointURL(rawURL string) error {
	if !netguard.ConfiguredEndpointURLAllowed(rawURL) {
		return fmt.Errorf("httpembed: endpoint URL is not allowed: %s", rawURL)
	}
	return nil
}

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

// waitForRetry blocks for wait, or returns ctx's error if cancelled first
// -- shared by Embed/ListModels so a long backoff never outlives ctx.
func waitForRetry(ctx context.Context, wait time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// rateLimiter paces real HTTP requests against one configured endpoint
// (Config.RateLimitPerSecond) so their combined rate, across every chunk
// of every document, respects the endpoint's requests-per-second cap.
type rateLimiter struct {
	mu   sync.Mutex
	next time.Time
}

// wait blocks until this call's reserved slot arrives, or returns early on
// ctx cancellation. ratePerSecond <= 0 disables pacing. Each call
// atomically reserves the next slot before sleeping outside the lock, so
// concurrent callers queue up correctly spaced.
func (r *rateLimiter) wait(ctx context.Context, ratePerSecond float64) {
	if ratePerSecond <= 0 {
		return
	}
	interval := time.Second / time.Duration(ratePerSecond)

	r.mu.Lock()
	now := time.Now()
	start := r.next
	if start.Before(now) {
		start = now
	}
	r.next = start.Add(interval)
	r.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
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
	// RateLimitMaxRetries/RateLimitInitialBackoff override the package
	// defaults -- a test-only hook to exercise 429/529 retry behavior
	// without waiting out real backoff delays. Leave zero in production.
	RateLimitMaxRetries     int
	RateLimitInitialBackoff time.Duration
	// ChunkSizeTokens and TokenizeURL mirror domain.EmbeddingHTTPEndpoint's
	// same-named fields. Zero value disables chunking, preserving the
	// original single-call-per-Embed behavior.
	ChunkSizeTokens int
	TokenizeURL     string
	// RateLimitPerSecond mirrors domain.EmbeddingHTTPEndpoint's same-named
	// field: max rate of real HTTP requests (embeddings + tokenize) this
	// Embedder issues. <=0 (the zero value) disables pacing entirely.
	RateLimitPerSecond float64
}

// Embedder calls an OpenAI-compatible POST {base_url}/embeddings endpoint:
// request body {"input": text, "model": "..."}, response body
// {"data":[{"embedding":[...]}]}.
type Embedder struct {
	baseURL string
	apiKey  string
	model   string
	dims    int
	// client's Transport routes every dial through
	// netguard.ConfiguredEndpointDialContext, so a redirect hop or a
	// changed DNS answer can't land on a blocked address --
	// checkEndpointURL's per-request calls are the pre-request layer of
	// the same belt-and-suspenders guard httpchat uses.
	client              *http.Client
	rateLimitMaxRetries int
	rateLimitBackoff    time.Duration
	chunkSizeTokens     int
	tokenizeURL         string
	// rateLimitPerSecond is Config.RateLimitPerSecond, read by rate.wait on
	// every real request. rate is always non-nil; <=0 just makes rate.wait
	// a no-op.
	rateLimitPerSecond float64
	rate               *rateLimiter
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
		client:              &http.Client{Transport: netguard.ConfiguredEndpointTransport()},
		rateLimitMaxRetries: maxRetries,
		rateLimitBackoff:    backoff,
		chunkSizeTokens:     cfg.ChunkSizeTokens,
		tokenizeURL:         cfg.TokenizeURL,
		rateLimitPerSecond:  cfg.RateLimitPerSecond,
		rate:                &rateLimiter{},
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
// vector. When chunkSizeTokens is 0 (the default), this is one call to
// embedChunk. Otherwise text is split into chunks (chunkText), each
// embedded separately and mean-pooled (combineVectors) -- so a document
// exceeding the model's context length still gets a whole-document vector.
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

// embedChunk calls the configured embeddings endpoint for one chunk (or
// the whole text, if chunking is disabled) and returns its vector. Any
// failure returns a clear wrapped error, never a mis-sized vector. A
// 429/529 is retried with backoff up to rateLimitMaxRetries times.
func (e *Embedder) embedChunk(ctx context.Context, text string) ([]float32, error) {
	// Paced once, before the first attempt only -- a retry is already
	// paced by the backoff below, so pacing again would double-wait.
	e.rate.wait(ctx, e.rateLimitPerSecond)

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

// embedOnce makes a single attempt against the embeddings endpoint. resp
// is non-nil whenever a real HTTP response was received (even non-2xx), so
// the retry loop can inspect it; nil only for a build/network failure,
// which is never retried.
func (e *Embedder) embedOnce(ctx context.Context, reqBody []byte) ([]float32, *http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	url := e.baseURL + "/embeddings"
	if err := checkEndpointURL(url); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", bearerPrefix+e.apiKey)
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
		return nil, resp, fmt.Errorf("httpembed: embeddings endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), e.apiKey), 500))
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

// chunkText splits text into pieces each estimated (or, with tokenizeURL
// configured, confirmed) to fit e.chunkSizeTokens. Breaks on whitespace
// where possible; an over-budget "word" (CJK, or a long URL/base64 blob)
// is split on rune boundaries (splitByRuneBudget). Unchanged if chunking
// is disabled or it already fits.
func (e *Embedder) chunkText(ctx context.Context, text string) ([]string, error) {
	if e.chunkSizeTokens <= 0 {
		return []string{text}, nil
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{text}, nil
	}

	charBudget := e.chunkSizeTokens * domain.ApproxCharsPerToken
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
		wRunes := utf8.RuneCountInString(w)
		wLen := wRunes + 1 // +1 for the joining space
		if currentLen > 0 && currentLen+wLen > charBudget {
			flush()
		}
		if wRunes > charBudget {
			// No whitespace to split on (CJK, or a long URL/base64 blob) --
			// flush pending text first, then split on rune boundaries so a
			// multi-byte UTF-8 sequence is never severed.
			flush()
			chunks = append(chunks, splitByRuneBudget(w, charBudget)...)
			continue
		}
		current = append(current, w)
		currentLen += wLen
	}
	flush()

	if e.tokenizeURL == "" {
		return chunks, nil
	}
	// Exact mode: the character estimate is conservative but not infallible
	// (CJK tokenizes denser than domain.ApproxCharsPerToken assumes) --
	// verify each chunk against the real tokenizer and split (never
	// trim/discard) anything over budget.
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

// fitChunkToTokenBudget ensures text fits e.chunkSizeTokens per
// e.tokenizeURL's exact count, recursively halving when it doesn't (never
// trimming/discarding). Prefers splitting on words, falling back to rune
// count when there's no whitespace left. Bounded by maxTokenizeSplitDepth.
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
		// No whitespace left (CJK, or an oversized token) -- split by rune
		// count in half instead, still bounded by maxTokenizeSplitDepth.
		runes := []rune(text)
		if len(runes) < 2 {
			return []string{text}, nil
		}
		mid := len(runes) / 2
		left, err := e.fitChunkToTokenBudget(ctx, string(runes[:mid]), depth+1)
		if err != nil {
			return nil, err
		}
		right, err := e.fitChunkToTokenBudget(ctx, string(runes[mid:]), depth+1)
		if err != nil {
			return nil, err
		}
		return append(left, right...), nil
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

// splitByRuneBudget splits s into consecutive pieces of at most maxRunes
// runes each, via []rune(s) slicing rather than raw byte offsets so a
// multi-byte UTF-8 sequence is never severed mid-character.
func splitByRuneBudget(s string, maxRunes int) []string {
	if maxRunes <= 0 {
		return []string{s}
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return []string{s}
	}
	pieces := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		pieces = append(pieces, string(runes[start:end]))
	}
	return pieces
}

// tokenizeRequest/tokenizeResponse mirror vLLM's POST /tokenize contract
// ({"model":..., "prompt": text} -> {"count": N, ...}) -- the one backend
// confirmed to expose this (see domain.EmbeddingHTTPEndpoint.TokenizeURL
// on why it's explicit admin config, never auto-detected).
type tokenizeRequest struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
}

// Count is a pointer so a missing field (misconfigured tokenize_url) is
// distinguishable from a genuine 0 -- a plain int would silently read as
// 0, which fitChunkToTokenBudget would wrongly treat as "fits".
type tokenizeResponse struct {
	Count *int `json:"count"`
}

// countTokens calls e.tokenizeURL for text's exact token count. Unlike
// embedChunk/listModelsOnce, this never retries on 429/529 -- it's an
// internal accuracy step, not worth the same backoff complexity.
func (e *Embedder) countTokens(ctx context.Context, text string) (int, error) {
	e.rate.wait(ctx, e.rateLimitPerSecond)

	reqBody, err := json.Marshal(tokenizeRequest{Model: e.model, Prompt: text})
	if err != nil {
		return 0, fmt.Errorf("httpembed: encoding tokenize request: %w", err)
	}
	if err := checkEndpointURL(e.tokenizeURL); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenizeURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("httpembed: building tokenize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", bearerPrefix+e.apiKey)
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
		return 0, fmt.Errorf("httpembed: tokenize endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), e.apiKey), 500))
	}
	var parsed tokenizeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("httpembed: decoding tokenize response: %w", err)
	}
	if parsed.Count == nil {
		return 0, fmt.Errorf("httpembed: tokenize response missing \"count\" field: %s", domain.TruncateWithEllipsis(domain.RedactSecret(string(body), e.apiKey), 500))
	}
	return *parsed.Count, nil
}

// combineVectors mean-pools multiple chunk vectors into one vector
// representing the whole document. Only ever called with 2+ vectors --
// Embed calls embedChunk directly for the single-chunk case.
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

// ListModels calls GET {base_url}/models and returns every model ID, used
// by the admin Settings page to prefill model suggestions. Not part of
// ports.EmbeddingProvider (hashembed has no remote catalog to list) --
// callers type-assert for it (see restapi.handleAdminEmbeddingsModels).
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

	url := e.baseURL + "/models"
	if err := checkEndpointURL(url); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("httpembed: building request: %w", err)
	}
	if e.apiKey != "" {
		req.Header.Set("Authorization", bearerPrefix+e.apiKey)
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
		return nil, resp, fmt.Errorf("httpembed: models endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), e.apiKey), 500))
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
