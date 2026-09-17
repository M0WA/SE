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

// NewHTTPEmbedder constructs a ports.EmbeddingProvider for one candidate HTTP
// endpoint config -- used for a real long-lived embedder (see NewEmbedders)
// and to probe unsaved settings before persisting them. e.APIKey must
// already be plaintext (see DecryptEndpointAPIKey); this never decrypts.
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

// NewEmbedders builds one ports.EmbeddingProvider per enabled provider,
// keyed by domain.EmbeddingProviderHash or the endpoint's ID. Read once at
// startup, not hot-reloaded: Dimensions() is baked into EnableANN's pgvector
// sizing. Endpoints must already be decrypted (DecryptEndpointAPIKey).
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
// settingsEncryptionKey -- call before NewEmbedders/NewHTTPEmbedder. A nil
// or never-encrypted key passes through unchanged; a genuine decryption
// failure clears APIKey so a misconfigured embedder fails fast, visibly.
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

// LoadEmbeddingEndpoints lists every configured HTTP endpoint and decrypts
// each APIKey -- the one-time startup read before NewEmbedders. A store
// error is logged and treated as "no endpoints configured" rather than
// fatal, matching this codebase's degrade-gracefully convention.
func LoadEmbeddingEndpoints(ctx context.Context, store ports.EmbeddingEndpointStore, settingsEncryptionKey []byte) []domain.EmbeddingHTTPEndpoint {
	endpoints, err := store.ListEmbeddingEndpoints(ctx)
	if err != nil {
		log.Printf("loading embedding endpoints: %v", err)
		return nil
	}
	return DecryptEndpointAPIKeys(endpoints, settingsEncryptionKey)
}
