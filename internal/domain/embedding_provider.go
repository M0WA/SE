package domain

// EmbeddingProviderHash names the one built-in embedding provider:
// hashembed.Embedder, the dependency-free feature-hashing pseudo-embedding
// every process has always used. Every other provider choice for
// OperationalSettingsValues.EmbeddingProvider is an EmbeddingHTTPEndpoint.ID
// -- there is no longer a second fixed constant for "the" HTTP provider,
// since any number of HTTP endpoints may be configured. See
// ReconcileActiveProvider for how a provider choice is validated against
// the currently enabled set.
const EmbeddingProviderHash = "hash"
