package bootstrap_test

import (
	"testing"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
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
