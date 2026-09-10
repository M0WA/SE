package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
)

type fakeSQLRepo struct {
	postings   map[string][]domain.PostingStats
	embeddings map[string][]float32
	docs       map[string]domain.Document
	docErrIDs  map[string]bool
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
	if r.docErrIDs[id] {
		return domain.Document{}, errors.New("boom")
	}
	return r.docs[id], nil
}
func (r *fakeSQLRepo) ListDocuments(context.Context, int) ([]domain.IndexedDocument, error) {
	return nil, nil
}
func (r *fakeSQLRepo) DeleteDocument(context.Context, string) error { return nil }

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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))
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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B))
	results, err := svc.Search(context.Background(), "quantenphysik", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected purely semantic match found, got %+v", results)
	}
}

func TestHybridSearch_EmptyQueryRejected(t *testing.T) {
	svc := application.NewHybridSearchService(&fakeSQLRepo{}, &fakeEmbedder{}, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))
	_, err := svc.Search(context.Background(), "   ", 10)
	if err == nil {
		t.Error("expected error for empty query")
	}
}

func TestHybridSearch_RequiredWordFiltersOutNonMatching(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind haustiere"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen", Text: "Katzen im Zoo"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))

	results, err := svc.Search(context.Background(), "katzen +haustiere", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected only doc 1 (has 'haustiere'), got %+v", results)
	}
}

func TestHybridSearch_ExcludedWordFiltersOutMatching(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind haustiere"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen", Text: "Katzen im Zoo"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))

	results, err := svc.Search(context.Background(), "katzen -zoo", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected only doc 1 (excludes 'zoo'), got %+v", results)
	}
}

func TestHybridSearch_PhraseFiltersOutNonMatching(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind sehr verspielt"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen", Text: "Katzen sind manchmal verspielt"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))

	results, err := svc.Search(context.Background(), `katzen "sehr verspielt"`, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected only doc 1 (exact phrase), got %+v", results)
	}
}

func TestHybridSearch_ConstraintDocumentFetchErrorSkipsCandidate(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docErrIDs:  map[string]bool{"1": true},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))

	results, err := svc.Search(context.Background(), "katzen +erforderlich", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected the candidate to be dropped when its document can't be fetched, got %+v", results)
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B))
	results, _ := svc.Search(context.Background(), "test", 2)
	if len(results) != 2 {
		t.Errorf("expected topK=2, got %d", len(results))
	}
}
