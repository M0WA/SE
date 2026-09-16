package bootstrap_test

import (
	"testing"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestNewEmbedder_DefaultsToHash(t *testing.T) {
	e := bootstrap.NewEmbedder(domain.OperationalSettingsValues{EmbeddingProvider: domain.EmbeddingProviderHash})
	if _, ok := e.(*hashembed.Embedder); !ok {
		t.Fatalf("expected *hashembed.Embedder, got %T", e)
	}
	if e.Dimensions() != 128 {
		t.Errorf("expected 128 dimensions, got %d", e.Dimensions())
	}
}

func TestNewEmbedder_BlankProviderDefaultsToHash(t *testing.T) {
	e := bootstrap.NewEmbedder(domain.OperationalSettingsValues{})
	if _, ok := e.(*hashembed.Embedder); !ok {
		t.Fatalf("expected *hashembed.Embedder for a zero-value settings snapshot, got %T", e)
	}
}

func TestNewEmbedder_HTTPProviderBuildsHTTPEmbedder(t *testing.T) {
	e := bootstrap.NewEmbedder(domain.OperationalSettingsValues{
		EmbeddingProvider:       domain.EmbeddingProviderHTTP,
		EmbeddingHTTPBaseURL:    "http://localhost:11434/v1",
		EmbeddingHTTPAPIKey:     "sk-test",
		EmbeddingHTTPModel:      "nomic-embed-text",
		EmbeddingHTTPDimensions: 768,
	})
	he, ok := e.(*httpembed.Embedder)
	if !ok {
		t.Fatalf("expected *httpembed.Embedder, got %T", e)
	}
	if he.Dimensions() != 768 {
		t.Errorf("expected 768 dimensions, got %d", he.Dimensions())
	}
}

func TestNewEmbedders_HashOnlyByDefault(t *testing.T) {
	embedders := bootstrap.NewEmbedders(domain.OperationalSettingsValues{EmbeddingHashEnabled: true})
	if len(embedders) != 1 {
		t.Fatalf("expected exactly one embedder, got %d: %+v", len(embedders), embedders)
	}
	if _, ok := embedders[domain.EmbeddingProviderHash].(*hashembed.Embedder); !ok {
		t.Errorf("expected a *hashembed.Embedder under %q, got %+v", domain.EmbeddingProviderHash, embedders)
	}
}

func TestNewEmbedders_NeitherEnabledReturnsEmptyMap(t *testing.T) {
	embedders := bootstrap.NewEmbedders(domain.OperationalSettingsValues{})
	if len(embedders) != 0 {
		t.Errorf("expected no embedders when neither is enabled, got %+v", embedders)
	}
}

func TestNewEmbedders_HTTPOnlyBuildsJustHTTPEmbedder(t *testing.T) {
	embedders := bootstrap.NewEmbedders(domain.OperationalSettingsValues{
		EmbeddingHTTPEnabled:    true,
		EmbeddingHTTPBaseURL:    "http://localhost:11434/v1",
		EmbeddingHTTPModel:      "nomic-embed-text",
		EmbeddingHTTPDimensions: 768,
	})
	if len(embedders) != 1 {
		t.Fatalf("expected exactly one embedder, got %d: %+v", len(embedders), embedders)
	}
	he, ok := embedders[domain.EmbeddingProviderHTTP].(*httpembed.Embedder)
	if !ok {
		t.Fatalf("expected a *httpembed.Embedder under %q, got %+v", domain.EmbeddingProviderHTTP, embedders)
	}
	if he.Dimensions() != 768 {
		t.Errorf("expected 768 dimensions, got %d", he.Dimensions())
	}
}

func TestNewEmbedders_BothEnabledBuildsBoth(t *testing.T) {
	embedders := bootstrap.NewEmbedders(domain.OperationalSettingsValues{
		EmbeddingHashEnabled:    true,
		EmbeddingHTTPEnabled:    true,
		EmbeddingHTTPBaseURL:    "http://localhost:11434/v1",
		EmbeddingHTTPDimensions: 768,
	})
	if len(embedders) != 2 {
		t.Fatalf("expected both embedders, got %d: %+v", len(embedders), embedders)
	}
	if _, ok := embedders[domain.EmbeddingProviderHash]; !ok {
		t.Error("expected the hash provider present")
	}
	if _, ok := embedders[domain.EmbeddingProviderHTTP]; !ok {
		t.Error("expected the http provider present")
	}
}

func TestEmbedderDimensions_ReflectsEachEmbeddersOwnDimensions(t *testing.T) {
	embedders := bootstrap.NewEmbedders(domain.OperationalSettingsValues{
		EmbeddingHashEnabled:    true,
		EmbeddingHTTPEnabled:    true,
		EmbeddingHTTPBaseURL:    "http://localhost:11434/v1",
		EmbeddingHTTPDimensions: 768,
	})
	dims := bootstrap.EmbedderDimensions(embedders)
	if dims[domain.EmbeddingProviderHash] != 128 {
		t.Errorf("expected hash dims=128, got %+v", dims)
	}
	if dims[domain.EmbeddingProviderHTTP] != 768 {
		t.Errorf("expected http dims=768, got %+v", dims)
	}
}

func TestEmbedderDimensions_EmptyMapForEmptyEmbedders(t *testing.T) {
	dims := bootstrap.EmbedderDimensions(map[string]ports.EmbeddingProvider{})
	if len(dims) != 0 {
		t.Errorf("expected an empty map, got %+v", dims)
	}
}

func TestDecryptEmbeddingKey_NilKeyPassesPlaintextThrough(t *testing.T) {
	v := bootstrap.DecryptEmbeddingKey(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: "sk-plain"}, nil)
	if v.EmbeddingHTTPAPIKey != "sk-plain" {
		t.Errorf("expected the plaintext key unchanged, got %q", v.EmbeddingHTTPAPIKey)
	}
}

func TestDecryptEmbeddingKey_DecryptsAnEncryptedValue(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := bootstrap.DecryptEmbeddingKey(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: enc}, key)
	if v.EmbeddingHTTPAPIKey != "sk-real-secret" {
		t.Errorf("expected the decrypted key, got %q", v.EmbeddingHTTPAPIKey)
	}
}

func TestDecryptEmbeddingKey_FailureClearsTheKeyRatherThanLeakingCiphertext(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Decrypting with no key configured, even though the stored value IS
	// encrypted, must not hand the raw ciphertext to NewEmbedder as if it
	// were a usable API key.
	v := bootstrap.DecryptEmbeddingKey(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: enc}, nil)
	if v.EmbeddingHTTPAPIKey != "" {
		t.Errorf("expected the key cleared on a decryption failure, got %q", v.EmbeddingHTTPAPIKey)
	}
}

// hex64 returns a syntactically valid 64-character hex string for
// settingscrypto.ParseKey, without hardcoding the same literal in every
// test above.
func hex64() string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = digits[i%16]
	}
	return string(b)
}
