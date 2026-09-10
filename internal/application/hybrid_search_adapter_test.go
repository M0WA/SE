package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
)

func TestHybridAsSearchService_MapsFinalScoreToSearchResult(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	svc := application.NewHybridAsSearchService(repo, embedder, 0.5)
	results, err := svc.Search(context.Background(), "katzen", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := results[0]
	if got.URL != "http://a" || got.Title != "Katzen" {
		t.Errorf("expected doc a's URL/Title, got %+v", got)
	}
	if got.Score <= 0 {
		t.Errorf("expected positive score derived from FinalScore, got %v", got.Score)
	}
}

func TestHybridAsSearchService_PropagatesError(t *testing.T) {
	svc := application.NewHybridAsSearchService(&fakeSQLRepo{}, &fakeEmbedder{}, 0.5)
	_, err := svc.Search(context.Background(), "   ", 10)
	if err == nil {
		t.Error("expected empty-query error to propagate")
	}
}

type erroringSQLRepo struct{ fakeSQLRepo }

func (r *erroringSQLRepo) PostingsForTerm(context.Context, string) ([]domain.PostingStats, error) {
	return nil, errors.New("boom")
}

func TestHybridAsSearchService_PropagatesRepoError(t *testing.T) {
	svc := application.NewHybridAsSearchService(&erroringSQLRepo{}, &fakeEmbedder{}, 0.5)
	_, err := svc.Search(context.Background(), "katzen", 10)
	if err == nil {
		t.Error("expected repo error to propagate")
	}
}
