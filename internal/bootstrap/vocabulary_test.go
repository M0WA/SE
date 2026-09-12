package bootstrap_test

import (
	"context"
	"testing"

	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

func TestSyncVocabulary_SeedsCacheImmediatelyFromEmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	cache := domain.NewVocabularyCache(nil)

	bootstrap.SyncVocabulary(syncContext(t), repo, cache)

	if terms := cache.Get(); len(terms) != 0 {
		t.Errorf("expected an empty corpus to seed an empty vocabulary, got %+v", terms)
	}
}

func TestSyncVocabulary_SeedsCacheFromExistingDocuments(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "cats and dogs"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving doc: %v", err)
	}

	cache := domain.NewVocabularyCache(nil)
	bootstrap.SyncVocabulary(syncContext(t), repo, cache)

	terms := cache.Get()
	byTerm := make(map[string]bool, len(terms))
	for _, s := range terms {
		byTerm[s.Term] = true
	}
	if !byTerm["cats"] || !byTerm["dogs"] {
		t.Errorf("expected 'cats' and 'dogs' in the seeded vocabulary, got %+v", terms)
	}
}

func TestSyncVocabulary_ReReadsOnEachCall(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	cache := domain.NewVocabularyCache(nil)

	bootstrap.SyncVocabulary(syncContext(t), repo, cache)
	if terms := cache.Get(); len(terms) != 0 {
		t.Fatalf("expected an empty vocabulary before any document exists, got %+v", terms)
	}

	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "widgets"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulates the next poll tick picking up the newly saved document.
	bootstrap.SyncVocabulary(syncContext(t), repo, cache)
	terms := cache.Get()
	if len(terms) != 1 || terms[0].Term != "widgets" {
		t.Errorf("expected the vocabulary to pick up 'widgets' after a later sync (simulating the next poll tick), got %+v", terms)
	}
}
