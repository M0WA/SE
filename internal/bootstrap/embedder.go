package bootstrap

import (
	"context"
	"log"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// NewHTTPEmbedder constructs a ports.EmbeddingProvider for one candidate
// HTTP endpoint config -- used both to build a real, long-lived embedder for
// an enabled domain.EmbeddingHTTPEndpoint (see NewEmbedders) and, by the
// admin API's testEmbeddingConnectivity/handleAdminEmbeddingsModels, to
// probe an endpoint's settings before they're actually saved. e.APIKey must
// already be plaintext (see DecryptEndpointAPIKey) -- this never decrypts
// anything itself.
func NewHTTPEmbedder(e domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
	return httpembed.New(httpembed.Config{
		BaseURL:            e.BaseURL,
		APIKey:             e.APIKey,
		Model:              e.Model,
		Dimensions:         e.Dimensions,
		ChunkSizeTokens:    e.ChunkSizeTokens,
		TokenizeURL:        e.TokenizeURL,
		RateLimitPerSecond: e.RateLimitPerSecond,
	})
}

// NewEmbedders constructs one ports.EmbeddingProvider per currently-enabled
// provider -- the built-in hashembed.Embedder (see
// domain.OperationalSettingsValues.EmbeddingHashEnabled) and one
// httpembed.Embedder per enabled endpoint in endpoints (see
// domain.EmbeddingHTTPEndpoint.Enabled) -- keyed by domain.
// EmbeddingProviderHash or the endpoint's own ID. Every process (cmd/search,
// cmd/admin, cmd/crawl) uses this map for the rest of its lifetime: crawling
// and recomputing embed every enabled provider, search embeds its query
// only against whichever one is currently active (see domain.
// OperationalSettingsValues.EmbeddingProvider). Callers must pass endpoints
// with APIKey already decrypted (see DecryptEndpointAPIKey) and settings
// already synced from the settings store (i.e. call this after bootstrap.
// SyncSettings has completed its first load, not before) -- this is read
// exactly once at startup rather than hot-reloaded, since each embedder's
// Dimensions() is baked into sqlrepo.Repository.EnableANN's pgvector column
// sizing.
func NewEmbedders(hashEnabled bool, endpoints []domain.EmbeddingHTTPEndpoint) map[string]ports.EmbeddingProvider {
	embedders := make(map[string]ports.EmbeddingProvider, 1+len(endpoints))
	if hashEnabled {
		embedders[domain.EmbeddingProviderHash] = hashembed.New(128)
	}
	for _, e := range endpoints {
		if !e.Enabled {
			continue
		}
		log.Printf("embeddings: using HTTP endpoint %q (%s) at %s (model %q, %d dimensions)", e.Name, e.ID, e.BaseURL, e.Model, e.Dimensions)
		embedders[e.ID] = NewHTTPEmbedder(e)
	}
	return embedders
}

// EmbedderDimensions reduces an embedders map (see NewEmbedders) to just
// each provider's Dimensions(), the shape sqlrepo.Repository.EnableANN
// actually needs for pgvector column sizing -- callers don't need to know
// EnableANN's exact parameter shape to go from "the embedders I built" to
// "what I pass it".
func EmbedderDimensions(embedders map[string]ports.EmbeddingProvider) map[string]int {
	dims := make(map[string]int, len(embedders))
	for provider, embedder := range embedders {
		dims[provider] = embedder.Dimensions()
	}
	return dims
}

// DecryptEndpointAPIKey returns e with APIKey decrypted via
// settingsEncryptionKey (see settingscrypto's package doc comment) -- call
// this on every domain.EmbeddingHTTPEndpoint loaded from the store before
// passing it to NewEmbedders/NewHTTPEmbedder, since admin.go persists
// APIKey encrypted whenever SettingsEncryptionKey is configured. A nil
// settingsEncryptionKey, or a value that was never encrypted in the first
// place (an unconfigured key at save time), passes e through unchanged --
// see settingscrypto.Decrypt. A genuine decryption failure (the value IS
// encrypted but this process's key is nil, wrong, or the data is corrupt)
// is logged and returns e with APIKey cleared rather than the raw
// ciphertext, so a misconfigured embedder fails fast and visibly (every
// request to a bad URL/key rejected) rather than silently sending garbage
// as a Bearer token.
func DecryptEndpointAPIKey(e domain.EmbeddingHTTPEndpoint, settingsEncryptionKey []byte) domain.EmbeddingHTTPEndpoint {
	dec, err := settingscrypto.Decrypt(settingsEncryptionKey, e.APIKey)
	if err != nil {
		log.Printf("decrypting embedding endpoint %q API key: %v", e.ID, err)
		e.APIKey = ""
		return e
	}
	e.APIKey = dec
	return e
}

// DecryptEndpointAPIKeys applies DecryptEndpointAPIKey to every endpoint in
// endpoints, returning a new slice -- the usual shape NewEmbedders' caller
// needs after loading the full endpoint list from the store.
func DecryptEndpointAPIKeys(endpoints []domain.EmbeddingHTTPEndpoint, settingsEncryptionKey []byte) []domain.EmbeddingHTTPEndpoint {
	out := make([]domain.EmbeddingHTTPEndpoint, len(endpoints))
	for i, e := range endpoints {
		out[i] = DecryptEndpointAPIKey(e, settingsEncryptionKey)
	}
	return out
}

// LoadEmbeddingEndpoints lists every configured HTTP embedding endpoint
// from store and decrypts each one's APIKey (see DecryptEndpointAPIKeys) --
// the one-time startup read every process (cmd/search, cmd/admin,
// cmd/crawl) does before calling NewEmbedders. A store error is logged and
// treated as "no endpoints configured" (an empty slice) rather than fatal,
// matching this codebase's convention of degrading gracefully rather than
// failing startup over an optional/best-effort read.
func LoadEmbeddingEndpoints(ctx context.Context, store ports.EmbeddingEndpointStore, settingsEncryptionKey []byte) []domain.EmbeddingHTTPEndpoint {
	endpoints, err := store.ListEmbeddingEndpoints(ctx)
	if err != nil {
		log.Printf("loading embedding endpoints: %v", err)
		return nil
	}
	return DecryptEndpointAPIKeys(endpoints, settingsEncryptionKey)
}
