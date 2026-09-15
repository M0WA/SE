package domain

// EmbeddingProvider* names which ports.EmbeddingProvider implementation
// OperationalSettingsValues.EmbeddingProvider selects: EmbeddingProviderHash
// (hashembed.Embedder, the dependency-free feature-hashing pseudo-embedding
// every process has always used) or EmbeddingProviderHTTP (httpembed.
// Embedder, calling an OpenAI-compatible embeddings HTTP endpoint for a real
// trained model). See OperationalSettingsValues.EmbeddingProvider's doc
// comment for the full picture, including why switching providers requires
// a process restart and invalidates every previously stored embedding.
const (
	EmbeddingProviderHash = "hash"
	EmbeddingProviderHTTP = "http"
)

// ValidEmbeddingProvider reports whether name is a recognized embedding
// provider choice.
func ValidEmbeddingProvider(name string) bool {
	switch name {
	case EmbeddingProviderHash, EmbeddingProviderHTTP:
		return true
	default:
		return false
	}
}
