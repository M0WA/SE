package application_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
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
func (r *fakeSQLRepo) VocabularyStats(context.Context, int) (int, []domain.TermStat, error) {
	return 0, nil, nil
}
func (r *fakeSQLRepo) AllEmbeddings(context.Context) (map[string][]float32, error) {
	return r.embeddings, nil
}
func (r *fakeSQLRepo) DocumentByID(_ context.Context, id string) (domain.Document, error) {
	if r.docErrIDs[id] {
		return domain.Document{}, errors.New("boom")
	}
	return r.docs[id], nil
}
func (r *fakeSQLRepo) ListDocuments(context.Context, int, string) ([]domain.IndexedDocument, error) {
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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)
	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), nil)
	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected purely semantic match found, got %+v", results)
	}
}

func TestHybridSearch_EmptyQueryRejected(t *testing.T) {
	svc := application.NewHybridSearchService(&fakeSQLRepo{}, &fakeEmbedder{}, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)
	_, err := svc.Search(context.Background(), "   ", ports.SearchQuery{TopK: 10})
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen +haustiere", ports.SearchQuery{TopK: 10})
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen -zoo", ports.SearchQuery{TopK: 10})
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), `katzen "sehr verspielt"`, ports.SearchQuery{TopK: 10})
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen +erforderlich", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected the candidate to be dropped when its document can't be fetched, got %+v", results)
	}
}

func TestHybridSearch_BlockedDomainExcludesDocument(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "https://spammy.example/a", Title: "Katzen", Text: "Katzen sind toll"},
			"2": {ID: "2", URL: "https://ok.example/b", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	overrides := domain.NewRankingOverrides(domain.RankingOverridesValues{BlockedDomains: []string{"spammy.example"}})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), overrides)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected only doc 2 (doc 1 on a blocked domain), got %+v", results)
	}
}

func TestHybridSearch_BlockedTermExcludesDocument(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen und Casino"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen", Text: "Katzen im Zoo"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	overrides := domain.NewRankingOverrides(domain.RankingOverridesValues{BlockedTerms: []string{"casino"}})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), overrides)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected only doc 2 (doc 1 contains a blocked term), got %+v", results)
	}
}

func TestHybridSearch_BoostedDomainReordersResults(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 3, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "https://ok.example/a", Title: "Katzen", Text: "Katzen Katzen Katzen"},
			"2": {ID: "2", URL: "https://trusted.example/b", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	// Without any boost, doc 1's higher term frequency ranks it first.
	plainSvc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), nil)
	plainResults, err := plainSvc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plainResults) != 2 || plainResults[0].DocID != "1" {
		t.Fatalf("expected doc 1 first without boosting, got %+v", plainResults)
	}

	overrides := domain.NewRankingOverrides(domain.RankingOverridesValues{BoostedDomains: map[string]float64{"trusted.example": 10.0}})
	boostedSvc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), overrides)
	boostedResults, err := boostedSvc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(boostedResults) != 2 || boostedResults[0].DocID != "2" {
		t.Errorf("expected doc 2 boosted to first place, got %+v", boostedResults)
	}
}

func TestHybridSearch_NilOverridesBehavesUnrestricted(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected doc 1 unaffected by a nil overrides pointer, got %+v", results)
	}
}

func TestHybridSearch_SiteFilterExcludesNonMatchingHost(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "https://example.com/a", Title: "Katzen", Text: "Katzen sind toll"},
			"2": {ID: "2", URL: "https://other.example/b", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen site:example.com", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected only doc 1 (matches site:example.com), got %+v", results)
	}
}

func TestHybridSearch_SiteFilterAllowsSubdomain(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "https://www.example.com/a", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen site:example.com", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected doc 1 (www.example.com is a subdomain of example.com), got %+v", results)
	}
}

func TestHybridSearch_RecencySortOrdersByCrawledAtDescending(t *testing.T) {
	oldest := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 3, TotalDocs: 3, AvgDocLen: 10},
				{DocID: "2", TermFreq: 1, DocLength: 10, DocFreq: 3, TotalDocs: 3, AvgDocLen: 10},
				{DocID: "3", TermFreq: 1, DocLength: 10, DocFreq: 3, TotalDocs: 3, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}, "3": {1, 0}},
		docs: map[string]domain.Document{
			// Doc 1 has by far the strongest BM25 score (highest term
			// frequency) but is the oldest -- relevance order would put it
			// first; recency order must put it last.
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen Katzen Katzen Katzen Katzen", CrawledAt: oldest},
			"2": {ID: "2", URL: "http://b", Title: "Katzen", Text: "Katzen sind toll", CrawledAt: newest},
			"3": {ID: "3", URL: "http://c", Title: "Katzen", Text: "Katzen sind toll", CrawledAt: middle},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	relevance, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10, Sort: ports.SortRelevance})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(relevance) != 3 || relevance[0].DocID != "1" {
		t.Fatalf("expected doc 1 first by relevance (highest BM25), got %+v", relevance)
	}

	recency, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10, Sort: ports.SortRecency})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recency) != 3 {
		t.Fatalf("expected 3 results, got %d", len(recency))
	}
	gotOrder := []string{recency[0].DocID, recency[1].DocID, recency[2].DocID}
	wantOrder := []string{"2", "3", "1"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("expected recency order (newest first) %v, got %v", wantOrder, gotOrder)
	}
}

func TestHybridSearch_UnrecognizedSortFallsBackToRelevance(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10, Sort: "bogus"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected an unrecognized sort value to behave like relevance, got %+v", results)
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil)
	results, _ := svc.Search(context.Background(), "test", ports.SearchQuery{TopK: 2})
	if len(results) != 2 {
		t.Errorf("expected topK=2, got %d", len(results))
	}
}
