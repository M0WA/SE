package application_test

import (
	"context"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
)

type fakeSQLRepo struct {
	postings   map[string][]domain.PostingStats
	embeddings map[string][]float32
	docs       map[string]domain.Document
}

func (r *fakeSQLRepo) SaveDocument(context.Context, domain.Document, []float32) error { return nil }
func (r *fakeSQLRepo) PostingsForTerm(_ context.Context, term string) ([]domain.PostingStats, error) {
	return r.postings[term], nil
}
func (r *fakeSQLRepo) CorpusStats(context.Context) (int, float64, error) { return 2, 10, nil }
func (r *fakeSQLRepo) AllEmbeddings(context.Context) (map[string][]float32, error) {
	return r.embeddings, nil
}
func (r *fakeSQLRepo) DocumentByID(_ context.Context, id string) (domain.Document, error) {
	return r.docs[id], nil
}

type fakeEmbedder struct{ vec []float32 }

func (e *fakeEmbedder) Embed(context.Context, string) ([]float32, error) { return e.vec, nil }
func (e *fakeEmbedder) Dimensions() int                                  { return len(e.vec) }

func TestHybridSearch_CombinesBM25AndSemantic(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 1, TotalDocs: 2, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{
			"1": {1, 0}, "2": {0, 1},
		},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"},
			"2": {ID: "2", URL: "http://b", Title: "Hunde", Text: "Hunde sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	svc := application.NewHybridSearchService(repo, embedder, 0.5)
	results, err := svc.Search(context.Background(), "katzen", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 || results[0].DocID != "1" {
		t.Errorf("expected doc 1 first (BM25+semantic), got %+v", results)
	}
}

func TestHybridSearch_FindsSemanticOnlyMatch(t *testing.T) {
	repo := &fakeSQLRepo{
		postings:   map[string][]domain.PostingStats{"quantenphysik": {}},
		embeddings: map[string][]float32{"2": {1, 0}},
		docs:       map[string]domain.Document{"2": {ID: "2", URL: "http://b", Title: "Physik", Text: "Physik-Artikel"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	svc := application.NewHybridSearchService(repo, embedder, 0.3)
	results, err := svc.Search(context.Background(), "quantenphysik", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected purely semantic match found, got %+v", results)
	}
}

func TestHybridSearch_EmptyQueryRejected(t *testing.T) {
	svc := application.NewHybridSearchService(&fakeSQLRepo{}, &fakeEmbedder{}, 0.5)
	_, err := svc.Search(context.Background(), "   ", 10)
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestHybridSearch_TopKLimitsResults(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{},
		embeddings: map[string][]float32{
			"1": {1, 0}, "2": {1, 0}, "3": {1, 0},
		},
		docs: map[string]domain.Document{
			"1": {ID: "1"}, "2": {ID: "2"}, "3": {ID: "3"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, 0.5)
	results, _ := svc.Search(context.Background(), "test", 2)
	if len(results) != 2 {
		t.Errorf("expected topK=2, got %d", len(results))
	}
}
