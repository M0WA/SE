package domain

import (
	"regexp"
	"time"
)

// EmbeddingHTTPEndpoint is one admin-configured HTTP embeddings endpoint
// (OpenAI-compatible, local or hosted), independent of every other endpoint
// and the built-in EmbeddingProviderHash. Multiple may be enabled at once,
// each kept warm, so switching which one search uses needs no recompute.
type EmbeddingHTTPEndpoint struct {
	// ID is this endpoint's provider key -- the document_embeddings.provider
	// value and pgvector column/index name suffix, so must match
	// EmbeddingEndpointIDPattern. Minted once from Name, never changed.
	ID   string
	Name string
	// BaseURL is the OpenAI-compatible embeddings API's base URL --
	// httpembed.Embedder POSTs to "<BaseURL>/embeddings".
	BaseURL string
	// APIKey is sent as "Authorization: Bearer <key>", optional for a local
	// server. Encrypted at rest, never echoed to the admin UI; empty on
	// update leaves the stored value as-is.
	APIKey string
	// Model is sent as the embeddings request body's "model" field.
	Model string
	// Dimensions is the expected embedding vector length -- httpembed.
	// Embedder errors on a length mismatch; EnableANN sizes this
	// provider's pgvector column from it.
	Dimensions int
	// RateLimitPerSecond caps real HTTP requests/sec against this endpoint
	// (per request, not per Embed call -- ChunkSizeTokens can fire many per
	// call). 0 means unlimited.
	RateLimitPerSecond float64
	// Enabled controls whether this endpoint's embedding gets kept current;
	// disabling it never deletes stored document_embeddings rows.
	Enabled bool
	// ChunkSizeTokens bounds text per Embed call; longer text is split,
	// each chunk embedded and mean-pooled into one vector. 0 disables
	// chunking. Measured via TokenizeURL when set, else a char-count estimate.
	ChunkSizeTokens int
	// TokenizeURL, when non-empty, is a separate endpoint (e.g. vLLM's
	// "/tokenize") for an exact token count instead of an estimate.
	// Unused when ChunkSizeTokens is 0.
	TokenizeURL string
	CreatedAt   time.Time
}

// EmbeddingEndpointIDPattern is every valid EmbeddingHTTPEndpoint.ID --
// lowercase alphanumeric/underscore, 1-20 chars, short enough to keep
// sqlrepo's derived identifiers within Postgres's 63-byte limit.
var EmbeddingEndpointIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

// NewEmbeddingEndpointID derives an ID from a display name, appending the
// shortest numeric suffix that avoids colliding with existing (every other
// endpoint's ID, plus "hash").
func NewEmbeddingEndpointID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "ep")
}

// ReconcileSearchWeights keeps only entries naming a still-enabled
// provider; if nothing survives, falls back to {hash: 1} if hashEnabled,
// else the first enabled endpoint, else {hash: 1} regardless.
func ReconcileSearchWeights(weights map[string]float64, hashEnabled bool, endpoints []EmbeddingHTTPEndpoint) map[string]float64 {
	enabled := make(map[string]bool, len(endpoints)+1)
	if hashEnabled {
		enabled[EmbeddingProviderHash] = true
	}
	for _, e := range endpoints {
		if e.Enabled {
			enabled[e.ID] = true
		}
	}

	survivors := make(map[string]float64, len(weights))
	for provider, w := range weights {
		if enabled[provider] {
			survivors[provider] = w
		}
	}
	if len(survivors) > 0 {
		return survivors
	}
	if hashEnabled {
		return map[string]float64{EmbeddingProviderHash: 1}
	}
	for _, e := range endpoints {
		if e.Enabled {
			return map[string]float64{e.ID: 1}
		}
	}
	return map[string]float64{EmbeddingProviderHash: 1}
}
