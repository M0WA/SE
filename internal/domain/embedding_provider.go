package domain

// EmbeddingProviderHash names the one built-in embedding provider:
// hashembed.Embedder, the dependency-free feature-hashing pseudo-embedding.
// Every other provider choice is an EmbeddingHTTPEndpoint.ID -- there's no
// fixed constant for "the" HTTP provider, since any number may be configured.
const EmbeddingProviderHash = "hash"
