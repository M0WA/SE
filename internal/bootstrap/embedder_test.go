package bootstrap_test

import (
	"testing"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
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
