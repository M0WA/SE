package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

func TestSyncCorpusStats_SeedsCacheImmediatelyFromEmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	cache := domain.NewCorpusStatsCache(0, 1)

	bootstrap.SyncCorpusStats(syncContext(t), repo, cache)

	totalDocs, avgDocLen := cache.Get()
	if totalDocs != 0 || avgDocLen != 1 {
		t.Errorf("expected an empty corpus to seed (0, 1), got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestSyncCorpusStats_SeedsCacheFromExistingDocuments(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "one two three four five"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "one two three four five six seven"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	cache := domain.NewCorpusStatsCache(0, 1)
	bootstrap.SyncCorpusStats(syncContext(t), repo, cache)

	totalDocs, avgDocLen := cache.Get()
	if totalDocs != 2 {
		t.Errorf("expected totalDocs=2, got %d", totalDocs)
	}
	if avgDocLen <= 0 {
		t.Errorf("expected a positive avgDocLen, got %v", avgDocLen)
	}
}

func TestSyncCorpusStats_ReReadsOnEachCall(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	cache := domain.NewCorpusStatsCache(0, 1)

	bootstrap.SyncCorpusStats(syncContext(t), repo, cache)
	if totalDocs, _ := cache.Get(); totalDocs != 0 {
		t.Fatalf("expected totalDocs=0 before any document exists, got %d", totalDocs)
	}

	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text here"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulates the next poll tick picking up the newly saved document.
	bootstrap.SyncCorpusStats(syncContext(t), repo, cache)
	if totalDocs, _ := cache.Get(); totalDocs != 1 {
		t.Errorf("expected totalDocs=1 after a later sync (simulating the next poll tick), got %d", totalDocs)
	}
}

type erroringCorpusStatsSource struct{}

func (erroringCorpusStatsSource) CorpusStats(context.Context) (int, float64, error) {
	return 0, 0, errors.New("db unavailable")
}

// TestSyncCorpusStats_SourceErrorLeavesCacheUntouched proves a source
// failure (the DB briefly unreachable, say) is logged and skipped rather
// than zeroing out -- or crashing -- an otherwise-healthy cache.
func TestSyncCorpusStats_SourceErrorLeavesCacheUntouched(t *testing.T) {
	cache := domain.NewCorpusStatsCache(7, 42)
	bootstrap.SyncCorpusStats(syncContext(t), erroringCorpusStatsSource{}, cache)

	totalDocs, avgDocLen := cache.Get()
	if totalDocs != 7 || avgDocLen != 42 {
		t.Errorf("expected the cache's prior values preserved on a source error, got (%d, %v)", totalDocs, avgDocLen)
	}
}
