package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EmbeddingHTTPEndpoint is one admin-configured HTTP embeddings endpoint --
// an OpenAI-compatible embeddings API (a local inference server such as
// Ollama/llama.cpp/LM Studio, or a hosted provider) that gets its own
// embedding computed and stored for every document, independently of every
// other configured endpoint and of the built-in EmbeddingProviderHash.
// Multiple endpoints may be enabled at once, each keeping its own vectors
// warm -- switching OperationalSettingsValues.EmbeddingProvider between
// EmbeddingProviderHash and any enabled endpoint's ID never needs a
// recompute, the same "keep every enabled provider's vectors warm so
// switching is instant" design the old single-HTTP-endpoint feature
// established, generalized from exactly one HTTP endpoint to any number.
type EmbeddingHTTPEndpoint struct {
	// ID is this endpoint's provider key -- the document_embeddings.provider
	// value and the pgvector ANN column/index name suffix (sqlrepo's
	// vectorColumnNameFor/vectorIndexNameFor), so it must stay a short, safe
	// SQL identifier fragment matching EmbeddingEndpointIDPattern. Minted
	// once from Name at creation (NewEmbeddingEndpointID) and never changed
	// afterward, even if Name is edited later.
	ID   string
	Name string
	// BaseURL is the OpenAI-compatible embeddings API's base URL --
	// httpembed.Embedder POSTs to "<BaseURL>/embeddings".
	BaseURL string
	// APIKey is sent as an "Authorization: Bearer <key>" header -- optional,
	// since a local inference server often needs none. A real credential,
	// handled the same way ScheduledCrawl.Cookie/BasicAuthPass are: encrypted
	// at rest (see internal/adapters/settingscrypto), never echoed back to
	// the admin UI, and an empty value on update leaves whatever's already
	// stored unchanged rather than clearing it.
	APIKey string
	// Model is sent as the embeddings request body's "model" field.
	Model string
	// Dimensions is the expected embedding vector length -- httpembed.
	// Embedder errors clearly if an API response's actual vector length
	// doesn't match, and it's what EnableANN uses to size this provider's
	// own pgvector column.
	Dimensions int
	// RateLimitPerSecond caps how many real HTTP requests per second this
	// process issues against this endpoint specifically -- enforced inside
	// httpembed.Embedder itself (see its rateLimiter), once per real
	// embeddings/tokenize request rather than once per Embed call, since a
	// single Embed call can fire many real requests once ChunkSizeTokens
	// splits a document into multiple chunks. Each configured endpoint may
	// be a genuinely different external service with its own rate limit,
	// so unlike the old single shared EmbeddingRateLimitPerSecond setting
	// this replaces, the limit is per-endpoint. 0 means unlimited (fine for
	// a local server with no rate limit of its own, e.g. Ollama).
	RateLimitPerSecond float64
	// Enabled controls whether this endpoint's embedding actually gets
	// computed and stored for every document -- disabling it (like
	// EmbeddingHashEnabled going false) never deletes its already-stored
	// document_embeddings rows or ANN column, it just stops keeping them
	// current until re-enabled or a recompute catches them up.
	Enabled bool
	// ChunkSizeTokens bounds how much text a single Embed call sends to
	// this endpoint -- text longer than this is split into multiple
	// chunks, each embedded separately and mean-pooled into one final
	// vector (see httpembed.Embedder.Embed), instead of failing outright
	// against a model's context-length limit. 0 disables chunking
	// entirely (the default for every endpoint created before this
	// existed): text is sent whole, exactly like before.
	//
	// Always a token count, but how exactly that's measured depends on
	// TokenizeURL: exact (via that endpoint's own tokenizer) when it's
	// set, or an approximation from character count otherwise -- this
	// package has no real tokenizer of its own (see httpembed's package
	// doc comment on why: no bundled model weights or ML runtime).
	ChunkSizeTokens int
	// TokenizeURL, when non-empty, is a separate endpoint (e.g. vLLM's own
	// "http://host:port/tokenize") this package POSTs {"model":...,
	// "prompt": text} to for an exact token count instead of an estimate.
	// Deliberately never inferred/guessed from BaseURL -- not every
	// OpenAI-compatible embeddings server exposes an equivalent route, so
	// this is opt-in, explicit admin config, not a try-then-fallback
	// probe. Meaningless (and unused) when ChunkSizeTokens is 0.
	TokenizeURL string
	CreatedAt   time.Time
}

// EmbeddingEndpointIDPattern is every valid EmbeddingHTTPEndpoint.ID --
// lowercase alphanumeric/underscore, 1-20 characters. Short enough that
// "idx_documents_embedding_vector_<id>_hnsw" (37 fixed characters of
// prefix+suffix, see sqlrepo.vectorIndexNameFor) always stays within
// Postgres's 63-byte identifier limit, and safe to concatenate directly
// into a SQL identifier there precisely because nothing outside this
// pattern is ever accepted as an ID -- enforced once here, at creation,
// rather than re-validated at every call site that builds an identifier
// from it.
var EmbeddingEndpointIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

var embeddingEndpointSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// NewEmbeddingEndpointID derives an ID from a display name -- lowercased,
// non-alphanumeric runs collapsed to a single underscore, trimmed to fit
// EmbeddingEndpointIDPattern -- and, if that collides with a key already
// present in existing (which callers populate with every other configured
// endpoint's ID, plus the reserved "hash", so a new endpoint can never
// collide with the built-in provider either), appends the shortest numeric
// suffix that doesn't. Falls back to a timestamp-derived ID if name has no
// alphanumeric characters at all.
func NewEmbeddingEndpointID(name string, existing map[string]bool) string {
	slug := strings.Trim(embeddingEndpointSlugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_"), "_")
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "_")
	}
	if slug == "" {
		slug = fmt.Sprintf("ep%d", time.Now().UnixNano()%1_000_000_000)
	}
	if !existing[slug] {
		return slug
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("_%d", n)
		base := slug
		if len(base)+len(suffix) > 20 {
			base = base[:20-len(suffix)]
		}
		if candidate := base + suffix; !existing[candidate] {
			return candidate
		}
	}
}

// ReconcileSearchWeights returns a valid choice for
// OperationalSettingsValues.EmbeddingSearchWeights given the currently
// enabled provider set: every entry in weights that still names an
// enabled provider (EmbeddingProviderHash with hashEnabled true, or an
// enabled entry in endpoints) survives with its original weight; every
// other entry (a disabled or deleted provider) is dropped. If nothing
// survives -- weights was empty/nil, or every entry it named has since
// been disabled -- falls back to {EmbeddingProviderHash: 1} if hashEnabled,
// else the first enabled endpoint at weight 1, else {EmbeddingProviderHash:
// 1} regardless (search always needs something to try, even in the
// degenerate case where nothing is actually enabled -- callers reading
// with nothing enabled get no results either way, but at least don't
// silently score against a dangling provider).
//
// OperationalSettings.Set() cannot do this self-healing itself, unlike
// every other field it clamps, because provider validity now depends on
// the dynamically configured embedding_http_endpoints table rather than a
// fixed enum. Callers apply this wherever both the settings and the live
// endpoint list are already read together -- see bootstrap.applySettingsOnce
// and admin.go's settings handlers.
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
