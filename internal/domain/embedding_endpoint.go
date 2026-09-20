package domain

import (
	"regexp"
	"time"
)

// EmbeddingHTTPEndpoint is one admin-configured HTTP embeddings endpoint
// (an OpenAI-compatible API, local or hosted) with its own embedding
// computed per document, independent of every other endpoint and the
// built-in EmbeddingProviderHash. Multiple may be enabled at once, each
// kept warm, so switching which one search uses never needs a recompute.
type EmbeddingHTTPEndpoint struct {
	// ID is this endpoint's provider key -- the document_embeddings.provider
	// value and the pgvector column/index name suffix, so it must match
	// EmbeddingEndpointIDPattern. Minted once from Name at creation and
	// never changed afterward, even if Name is edited later.
	ID   string
	Name string
	// BaseURL is the OpenAI-compatible embeddings API's base URL --
	// httpembed.Embedder POSTs to "<BaseURL>/embeddings".
	BaseURL string
	// APIKey is sent as an "Authorization: Bearer <key>" header -- optional
	// for a local server. Encrypted at rest (settingscrypto), never echoed
	// back to the admin UI; empty on update leaves the stored value as-is.
	APIKey string
	// Model is sent as the embeddings request body's "model" field.
	Model string
	// Dimensions is the expected embedding vector length -- httpembed.
	// Embedder errors if a response's actual length doesn't match, and
	// EnableANN uses it to size this provider's own pgvector column.
	Dimensions int
	// RateLimitPerSecond caps real HTTP requests/sec against this endpoint
	// specifically (enforced in httpembed.Embedder, per real request, not
	// per Embed call -- one Embed call can fire many once ChunkSizeTokens
	// splits a document). 0 means unlimited (fine for a local server).
	RateLimitPerSecond float64
	// Enabled controls whether this endpoint's embedding gets kept current
	// for every document -- disabling it never deletes already-stored
	// document_embeddings rows, it just stops updating them.
	Enabled bool
	// ChunkSizeTokens bounds how much text one Embed call sends; longer
	// text is split into chunks, each embedded and mean-pooled into one
	// vector, instead of failing against a model's context limit. 0
	// disables chunking (text sent whole). Measured via TokenizeURL's
	// exact tokenizer when set, else an approximation from character count.
	ChunkSizeTokens int
	// TokenizeURL, when non-empty, is a separate endpoint (e.g. vLLM's own
	// "/tokenize") POSTed {"model":..., "prompt": text} for an exact token
	// count instead of an estimate -- opt-in, never guessed from BaseURL.
	// Unused when ChunkSizeTokens is 0.
	TokenizeURL string
	CreatedAt   time.Time
}

// EmbeddingEndpointIDPattern is every valid EmbeddingHTTPEndpoint.ID --
// lowercase alphanumeric/underscore, 1-20 characters. Short enough that
// sqlrepo's derived index/column names stay within Postgres's 63-byte
// identifier limit, and safe to concatenate into a SQL identifier since
// nothing outside this pattern is ever accepted as an ID.
var EmbeddingEndpointIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

// NewEmbeddingEndpointID derives an ID from a display name (lowercased,
// non-alphanumeric runs collapsed, trimmed to fit EmbeddingEndpointIDPattern)
// and appends the shortest numeric suffix that avoids colliding with a key
// in existing (every other configured endpoint's ID, plus "hash"). Falls
// back to a timestamp-derived ID if name has no alphanumeric characters.
func NewEmbeddingEndpointID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "ep")
}

// ReconcileSearchWeights keeps only entries naming a still-enabled
// provider; if nothing survives, falls back to {hash: 1} if hashEnabled,
// else the first enabled endpoint, else {hash: 1} regardless.
// OperationalSettings.Set can't do this itself since validity depends on
// the dynamic endpoints table -- callers apply it where both are at hand.
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
