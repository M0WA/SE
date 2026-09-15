package bootstrap

import (
	"log"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// NewEmbedder constructs the ports.EmbeddingProvider every process (cmd/
// search, cmd/admin, cmd/crawl) uses for the rest of its lifetime, chosen by
// v.EmbeddingProvider. Callers must pass settings already synced from the
// settings store (i.e. call this after bootstrap.SyncSettings has completed
// its first load, not before) -- see domain.OperationalSettingsValues.
// EmbeddingProvider's doc comment for why this is read exactly once at
// startup rather than hot-reloaded like every other operational setting:
// its result's Dimensions() is baked into sqlrepo.Repository.EnableANN's
// pgvector column sizing, which every caller runs immediately after this.
func NewEmbedder(v domain.OperationalSettingsValues) ports.EmbeddingProvider {
	if v.EmbeddingProvider == domain.EmbeddingProviderHTTP {
		log.Printf("embeddings: using HTTP provider at %s (model %q, %d dimensions)", v.EmbeddingHTTPBaseURL, v.EmbeddingHTTPModel, v.EmbeddingHTTPDimensions)
		return httpembed.New(httpembed.Config{
			BaseURL:    v.EmbeddingHTTPBaseURL,
			APIKey:     v.EmbeddingHTTPAPIKey,
			Model:      v.EmbeddingHTTPModel,
			Dimensions: v.EmbeddingHTTPDimensions,
		})
	}
	return hashembed.New(128)
}
