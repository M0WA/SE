package bootstrap

import (
	"log"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/adapters/settingscrypto"
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

// NewEmbedders constructs one ports.EmbeddingProvider per currently-
// enabled provider (see domain.OperationalSettingsValues.
// EmbeddingHashEnabled/EmbeddingHTTPEnabled), keyed by
// domain.EmbeddingProviderHash/EmbeddingProviderHTTP -- every process
// (cmd/search, cmd/admin, cmd/crawl) uses this map for the rest of its
// lifetime: crawling and recomputing embed every enabled provider, search
// embeds its query only against whichever one is currently active (see
// domain.OperationalSettingsValues.EmbeddingProvider). Callers must pass
// settings already synced from the settings store (i.e. call this after
// bootstrap.SyncSettings has completed its first load, not before) --
// like NewEmbedder, this is read exactly once at startup rather than
// hot-reloaded, since each embedder's Dimensions() is baked into
// sqlrepo.Repository.EnableANN's pgvector column sizing.
func NewEmbedders(v domain.OperationalSettingsValues) map[string]ports.EmbeddingProvider {
	embedders := make(map[string]ports.EmbeddingProvider, 2)
	if v.EmbeddingHashEnabled {
		embedders[domain.EmbeddingProviderHash] = hashembed.New(128)
	}
	if v.EmbeddingHTTPEnabled {
		log.Printf("embeddings: using HTTP provider at %s (model %q, %d dimensions)", v.EmbeddingHTTPBaseURL, v.EmbeddingHTTPModel, v.EmbeddingHTTPDimensions)
		embedders[domain.EmbeddingProviderHTTP] = httpembed.New(httpembed.Config{
			BaseURL:    v.EmbeddingHTTPBaseURL,
			APIKey:     v.EmbeddingHTTPAPIKey,
			Model:      v.EmbeddingHTTPModel,
			Dimensions: v.EmbeddingHTTPDimensions,
		})
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

// DecryptEmbeddingKey returns v with EmbeddingHTTPAPIKey decrypted via
// settingsEncryptionKey (see settingscrypto's package doc comment) -- call
// this on the value passed to NewEmbedder when the admin settings API may
// have persisted an encrypted key (handleAdminSettings does, when
// SettingsEncryptionKey is configured). A nil settingsEncryptionKey, or a
// value that was never encrypted in the first place (an unconfigured key
// at save time, or data older than this feature), passes v through
// unchanged -- see settingscrypto.Decrypt. A genuine decryption failure
// (the value IS encrypted but this process's key is nil, wrong, or the
// data is corrupt) is logged and returns v with EmbeddingHTTPAPIKey
// cleared rather than the raw ciphertext, so a misconfigured embedder
// fails fast and visibly (every request to a bad URL/key rejected) rather
// than silently sending garbage as a Bearer token.
func DecryptEmbeddingKey(v domain.OperationalSettingsValues, settingsEncryptionKey []byte) domain.OperationalSettingsValues {
	dec, err := settingscrypto.Decrypt(settingsEncryptionKey, v.EmbeddingHTTPAPIKey)
	if err != nil {
		log.Printf("decrypting embedding API key: %v", err)
		v.EmbeddingHTTPAPIKey = ""
		return v
	}
	v.EmbeddingHTTPAPIKey = dec
	return v
}
