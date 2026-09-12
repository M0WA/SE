package application_test

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeSQLRepo struct {
	postings   map[string][]domain.PostingStats
	embeddings map[string][]float32
	// embeddingNorms overrides the norm reported alongside an embedding
	// (which otherwise defaults to domain.VectorNorm of the vector itself,
	// as a real repository's precomputed norm_embedding column would), so
	// a test can inject a norm that doesn't match its vector to prove the
	// service actually consumes the repo-supplied norm rather than quietly
	// recomputing it.
	embeddingNorms map[string]float64
	// pageranks backs EmbeddedVector.PageRank on every embedding fetch (an
	// ID absent here simply reports 0, matching a document that predates
	// any pagerank column value beyond the schema's own bare-0 default).
	pageranks map[string]float64
	docs      map[string]domain.Document
	// vocabulary backs AllTerms, standing in for the full corpus vocabulary
	// a real repository's postings table would report -- used by fuzzy
	// query-term correction tests.
	vocabulary []domain.TermStat

	// documentsByIDsCalls lets tests assert the N+1 fix actually took:
	// DocumentsByIDs must be called at most once per Search() phase
	// (constraint filtering, then hydration) rather than once per
	// candidate/result.
	documentsByIDsCalls int
	// documentsByIDsSortedCalls counts DocumentsByIDsSortedByCrawledAt calls
	// separately from documentsByIDsCalls, so a recency-sort test can assert
	// it went through the ordered fetch (and never plain DocumentsByIDs, nor
	// a second fetch during hydration) rather than sorting in Go.
	documentsByIDsSortedCalls int
	// postingsForTermsCalls lets tests assert PostingsForTerms is called at
	// most once per Search() (one batched query across every unique query
	// term) rather than once per term.
	postingsForTermsCalls int
	// postingsForTermsArgs records the terms slice passed on each call, so
	// a multi-term query test can assert every term was actually requested
	// in that single batched call.
	postingsForTermsArgs [][]string
	// documentIDsByHostCalls lets a test assert a site: query actually
	// consulted the host lookup (rather than relying solely on the bounded
	// BM25/semantic candidate set, which -- pre-fix -- could silently miss a
	// site: match that wasn't a strong BM25/semantic hit).
	documentIDsByHostCalls int

	// annOK/annMatches/annErr let a test drive TopSemanticMatches' three
	// possible outcomes -- ANN unavailable (annOK false, the zero value),
	// ANN available with a given result set (annOK true, annMatches), or
	// the ANN query itself failing (annErr) -- without a real Postgres
	// connection. topSemanticMatchesCalls/sampleEmbeddingsCalls let a test
	// assert which path the service actually took.
	annOK                   bool
	annMatches              map[string]domain.EmbeddedVector
	annErr                  error
	topSemanticMatchesCalls int
	sampleEmbeddingsCalls   int
}

// TopSemanticMatches mimics ports.SQLRepository's ANN entry point: reports
// unavailable (ok=false, nil error) unless the test explicitly configured
// annOK, exactly like a repository whose EnableANN never succeeded.
func (r *fakeSQLRepo) TopSemanticMatches(_ context.Context, _ []float32, _ int) (map[string]domain.EmbeddedVector, bool, error) {
	r.topSemanticMatchesCalls++
	if r.annErr != nil {
		return nil, false, r.annErr
	}
	if !r.annOK {
		return nil, false, nil
	}
	return r.annMatches, true, nil
}

func (r *fakeSQLRepo) SaveDocument(context.Context, domain.Document, []float32) error { return nil }

// PostingsForTerms mimics a batched "WHERE term IN (...)" fetch: only
// requested terms come back, and only those actually present.
func (r *fakeSQLRepo) PostingsForTerms(_ context.Context, terms []string) (map[string][]domain.PostingStats, error) {
	r.postingsForTermsCalls++
	r.postingsForTermsArgs = append(r.postingsForTermsArgs, terms)
	out := make(map[string][]domain.PostingStats, len(terms))
	for _, term := range terms {
		if p, ok := r.postings[term]; ok {
			out[term] = p
		}
	}
	return out, nil
}
func (r *fakeSQLRepo) CorpusStats(context.Context) (int, float64, error) { return 2, 10, nil }
func (r *fakeSQLRepo) VocabularyStats(context.Context, int, string) (int, []domain.TermStat, error) {
	return 0, nil, nil
}
func (r *fakeSQLRepo) AllTerms(context.Context) ([]domain.TermStat, error) {
	return r.vocabulary, nil
}

// EmbeddingsForDocs mimics a batched "WHERE id IN (...)" fetch: only the
// requested IDs come back, and only those actually present. Each vector's
// norm is computed on the way out, standing in for a real repository's
// precomputed norm_embedding column.
func (r *fakeSQLRepo) EmbeddingsForDocs(_ context.Context, ids []string) (map[string]domain.EmbeddedVector, error) {
	out := make(map[string]domain.EmbeddedVector)
	for _, id := range ids {
		if v, ok := r.embeddings[id]; ok {
			out[id] = domain.EmbeddedVector{Vector: v, Norm: r.normFor(id, v), PageRank: r.pageranks[id]}
		}
	}
	return out, nil
}

// normFor returns the injected override norm for id if embeddingNorms sets
// one, otherwise domain.VectorNorm(v) -- standing in for a real
// repository's precomputed norm_embedding column.
func (r *fakeSQLRepo) normFor(id string, v []float32) float64 {
	if r.embeddingNorms != nil {
		if n, ok := r.embeddingNorms[id]; ok {
			return n
		}
	}
	return domain.VectorNorm(v)
}

// SampleEmbeddings mimics a bounded "ORDER BY id LIMIT limit" query, so
// tests exercising a small pool size can rely on a deterministic subset.
func (r *fakeSQLRepo) SampleEmbeddings(_ context.Context, limit int) (map[string]domain.EmbeddedVector, error) {
	r.sampleEmbeddingsCalls++
	if limit <= 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	ids := make([]string, 0, len(r.embeddings))
	for id := range r.embeddings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if limit < len(ids) {
		ids = ids[:limit]
	}
	out := make(map[string]domain.EmbeddedVector, len(ids))
	for _, id := range ids {
		out[id] = domain.EmbeddedVector{Vector: r.embeddings[id], Norm: r.normFor(id, r.embeddings[id]), PageRank: r.pageranks[id]}
	}
	return out, nil
}

// DocumentsByIDs mimics a batched "WHERE id IN (...)" fetch: an ID with no
// matching document is simply absent from the result, never an error.
func (r *fakeSQLRepo) DocumentsByIDs(_ context.Context, ids []string) (map[string]domain.Document, error) {
	r.documentsByIDsCalls++
	out := make(map[string]domain.Document)
	for _, id := range ids {
		if doc, ok := r.docs[id]; ok {
			out[id] = doc
		}
	}
	return out, nil
}

// DocumentsByIDsSortedByCrawledAt mimics the real repository's
// idx_documents_crawled_at-backed "ORDER BY crawled_at DESC, id ASC" query:
// same missing-ID-is-omitted contract as DocumentsByIDs, but returned as a
// slice already in that order, so the service under test never needs to
// sort it itself.
func (r *fakeSQLRepo) DocumentsByIDsSortedByCrawledAt(_ context.Context, ids []string) ([]domain.Document, error) {
	r.documentsByIDsSortedCalls++
	docs := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if doc, ok := r.docs[id]; ok {
			docs = append(docs, doc)
		}
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].CrawledAt.Equal(docs[j].CrawledAt) {
			return docs[i].ID < docs[j].ID
		}
		return docs[i].CrawledAt.After(docs[j].CrawledAt)
	})
	return docs, nil
}

// DocumentIDsByHost mimics the real repository's idx_documents_host-backed
// lookup: an exact host match or a subdomain of one of hosts, mirroring
// domain.ParsedQuery.SiteAllowed's own matching rule.
func (r *fakeSQLRepo) DocumentIDsByHost(_ context.Context, hosts []string) ([]string, error) {
	r.documentIDsByHostCalls++
	var ids []string
	for id, doc := range r.docs {
		u, err := url.Parse(doc.URL)
		if err != nil {
			continue
		}
		host := u.Hostname()
		for _, h := range hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, nil
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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)
	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 || results[0].DocID != "1" {
		t.Errorf("expected doc 1 first (BM25+semantic), got %+v", results)
	}
}

// TestHybridSearch_NonPositiveTopKDefaultsToTen proves Search's own
// internal fallback (independent of the HTTP layer's intQueryParam
// default): a caller passing TopK<=0 directly still gets a sane result
// count rather than zero results or an error.
func TestHybridSearch_NonPositiveTopKDefaultsToTen(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected the single matching doc back with a non-positive TopK, got %+v", results)
	}
}

// TestHybridSearch_DuplicateQueryTermsCountedOnce proves the query-term
// dedup loop: a query repeating the same term must not fetch or score
// its postings more than once.
func TestHybridSearch_DuplicateQueryTermsCountedOnce(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

	if _, err := svc.Search(context.Background(), "katzen katzen katzen", ports.SearchQuery{TopK: 10}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.postingsForTermsArgs) != 1 || len(repo.postingsForTermsArgs[0]) != 1 {
		t.Errorf("expected the repeated term deduped to a single-term batched call, got %+v", repo.postingsForTermsArgs)
	}
}

func TestHybridSearch_FindsSemanticOnlyMatch(t *testing.T) {
	repo := &fakeSQLRepo{
		postings:   map[string][]domain.PostingStats{"quantenphysik": {}},
		embeddings: map[string][]float32{"2": {1, 0}},
		docs:       map[string]domain.Document{"2": {ID: "2", URL: "http://b", Title: "Physik", Text: "Physik-Artikel"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)
	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Errorf("expected purely semantic match found, got %+v", results)
	}
}

func TestHybridSearch_EmptyQueryRejected(t *testing.T) {
	svc := application.NewHybridSearchService(&fakeSQLRepo{}, &fakeEmbedder{}, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)

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
		// No docs["1"] entry: the batched DocumentsByIDs fetch below
		// simply won't return it, exactly as if the document had been
		// deleted concurrently -- the candidate should be dropped rather
		// than failing the whole search.
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, overrides, domain.NewCorpusStatsCache(2, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, overrides, domain.NewCorpusStatsCache(2, 10), nil)

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
	plainSvc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)
	plainResults, err := plainSvc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plainResults) != 2 || plainResults[0].DocID != "1" {
		t.Fatalf("expected doc 1 first without boosting, got %+v", plainResults)
	}

	overrides := domain.NewRankingOverrides(domain.RankingOverridesValues{BoostedDomains: map[string]float64{"trusted.example": 10.0}})
	boostedSvc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), nil, overrides, domain.NewCorpusStatsCache(2, 10), nil)
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

	results, err := svc.Search(context.Background(), "katzen site:example.com", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Errorf("expected doc 1 (www.example.com is a subdomain of example.com), got %+v", results)
	}
}

// TestHybridSearch_SiteFilterFindsMatchOutsideBoundedCandidatePool guards
// against a real regression: bounding the semantic candidate pool (see
// TestHybridSearch_SemanticCandidatePoolSizeBoundsSampledCandidates) means a
// site: match that is neither a BM25 hit for the query's other terms nor
// within the small sampled pool would otherwise vanish from the results
// entirely -- exactly what happened in production for "site:example.com
// test" once the pool bound shipped. Doc "site-match" matches neither
// query term ("widgets") nor is in the size-1 sample pool (SampleEmbeddings
// sorts by ID and "other-a"/"other-b" both sort before "site-match"), so it
// must be found via the DocumentIDsByHost lookup instead.
func TestHybridSearch_SiteFilterFindsMatchOutsideBoundedCandidatePool(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"widgets": {
				{DocID: "other-a", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 3, AvgDocLen: 10},
				{DocID: "other-b", TermFreq: 1, DocLength: 10, DocFreq: 2, TotalDocs: 3, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{
			"other-a":    {1, 0},
			"other-b":    {1, 0},
			"site-match": {0, 1},
		},
		docs: map[string]domain.Document{
			"other-a":    {ID: "other-a", URL: "https://elsewhere.example/a", Title: "Widgets", Text: "Widgets galore"},
			"other-b":    {ID: "other-b", URL: "https://elsewhere.example/b", Title: "Widgets", Text: "Widgets galore"},
			"site-match": {ID: "site-match", URL: "https://example.com/about", Title: "About", Text: "No relevant terms here"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{SemanticCandidatePoolSize: 1})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), opSettings, nil, domain.NewCorpusStatsCache(3, 10), nil)

	results, err := svc.Search(context.Background(), "widgets site:example.com", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.documentIDsByHostCalls != 1 {
		t.Errorf("expected exactly one DocumentIDsByHost call, got %d", repo.documentIDsByHostCalls)
	}
	if len(results) != 1 || results[0].DocID != "site-match" {
		t.Errorf("expected only doc site-match (site:example.com), got %+v", results)
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

	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(3, 10), nil)

	relevance, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10, Sort: ports.SortRelevance})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(relevance) != 3 || relevance[0].DocID != "1" {
		t.Fatalf("expected doc 1 first by relevance (highest BM25), got %+v", relevance)
	}
	// Reset call counters: the relevance-sort Search above already exercised
	// the unconstrained fast path's own DocumentsByIDs hydration call, which
	// would otherwise be mistaken for one made by the recency-sort Search
	// below.
	repo.documentsByIDsCalls = 0
	repo.documentsByIDsSortedCalls = 0

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

	// The recency-sort path must fetch through the ordered
	// DocumentsByIDsSortedByCrawledAt query (one call, covering both the
	// constraint-filter/CrawledAt fetch and result hydration -- docCache
	// already holds every candidate, so hydration needs no second fetch)
	// rather than the plain, unordered DocumentsByIDs -- ordering by
	// crawled_at must come from SQL, not an in-app sort.Slice.
	if repo.documentsByIDsSortedCalls != 1 {
		t.Errorf("expected exactly one DocumentsByIDsSortedByCrawledAt call, got %d", repo.documentsByIDsSortedCalls)
	}
	if repo.documentsByIDsCalls != 0 {
		t.Errorf("expected zero plain DocumentsByIDs calls on the recency-sort path, got %d", repo.documentsByIDsCalls)
	}
}

// TestHybridSearch_ResultHydrationUsesBatchedFetchNotPerResult verifies the
// fix for the result-hydration N+1: on the common, unconstrained-query fast
// path (docCache never gets populated by the constraint-filter step, since
// there are no constraints/overrides/recency sort here), the final
// URL/Title/Snippet hydration for topK results must go through a single
// DocumentsByIDs batch call rather than one DocumentByID round trip per
// result.
func TestHybridSearch_ResultHydrationUsesBatchedFetchNotPerResult(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 3, DocLength: 10, DocFreq: 4, TotalDocs: 4, AvgDocLen: 10},
				{DocID: "2", TermFreq: 2, DocLength: 10, DocFreq: 4, TotalDocs: 4, AvgDocLen: 10},
				{DocID: "3", TermFreq: 1, DocLength: 10, DocFreq: 4, TotalDocs: 4, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}, "3": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen A", Text: "Katzen sind toll"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen B", Text: "Katzen sind toll"},
			"3": {ID: "3", URL: "http://c", Title: "Katzen C", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(4, 10), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected all 3 candidates, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.URL == "" || r.Title == "" {
			t.Errorf("expected doc %s to be hydrated with URL/Title, got %+v", r.DocID, r)
		}
	}
	if repo.documentsByIDsCalls != 1 {
		t.Errorf("expected exactly one batched DocumentsByIDs call to hydrate all topK results, got %d", repo.documentsByIDsCalls)
	}
}

// TestHybridSearch_ConstrainedQueryReusesDocCacheForHydration verifies that
// when the constraint-filter step already ran (populating docCache), the
// result-hydration step makes no further document fetch at all -- it must
// not issue a second DocumentsByIDs call for documents it already has
// cached.
func TestHybridSearch_ConstrainedQueryReusesDocCacheForHydration(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {
				{DocID: "1", TermFreq: 3, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
				{DocID: "2", TermFreq: 2, DocLength: 10, DocFreq: 2, TotalDocs: 2, AvgDocLen: 10},
			},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {1, 0}},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen A", Text: "Katzen sind haustiere"},
			"2": {ID: "2", URL: "http://b", Title: "Katzen B", Text: "Katzen im Zoo"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), nil)

	results, err := svc.Search(context.Background(), "katzen +haustiere", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" || results[0].URL == "" {
		t.Fatalf("expected hydrated doc 1, got %+v", results)
	}
	if repo.documentsByIDsCalls != 1 {
		t.Errorf("expected exactly one DocumentsByIDs call (constraint filtering), reused for hydration with no second call, got %d", repo.documentsByIDsCalls)
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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

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
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)
	results, _ := svc.Search(context.Background(), "test", ports.SearchQuery{TopK: 2})
	if len(results) != 2 {
		t.Errorf("expected topK=2, got %d", len(results))
	}
}

// TestHybridSearch_SemanticCandidatePoolSizeBoundsSampledCandidates verifies
// the fix for the "every search scores the entire corpus" problem: with no
// BM25 hits at all, the number of documents that ever get a semantic score
// is capped at the configured pool size rather than growing with corpus
// size -- a query that could semantically match everything only surfaces
// results from within that bounded sample.
func TestHybridSearch_SemanticCandidatePoolSizeBoundsSampledCandidates(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{"quantenphysik": {}},
		embeddings: map[string][]float32{
			"1": {1, 0}, "2": {1, 0}, "3": {1, 0}, "4": {1, 0}, "5": {1, 0},
		},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a"}, "2": {ID: "2", URL: "http://b"},
			"3": {ID: "3", URL: "http://c"}, "4": {ID: "4", URL: "http://d"},
			"5": {ID: "5", URL: "http://e"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{SemanticCandidatePoolSize: 2})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), opSettings, nil, nil, nil)

	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The fake's SampleEmbeddings returns the lowest 2 IDs in sorted order
	// ("1", "2") when asked for a pool of 2 -- never all 5.
	if len(results) != 2 {
		t.Fatalf("expected the semantic candidate pool to bound results to 2, got %d: %+v", len(results), results)
	}
	for _, r := range results {
		if r.DocID != "1" && r.DocID != "2" {
			t.Errorf("expected only the sampled pool (docs 1, 2), got doc %s", r.DocID)
		}
	}
}

// TestHybridSearch_BM25HitsAlwaysScoredRegardlessOfPoolSize verifies that
// even a tiny semantic candidate pool never drops an actual BM25 match --
// EmbeddingsForDocs fetches the query's own postings hits directly, so
// they're scored regardless of whether they'd have landed in the bounded
// sample.
func TestHybridSearch_BM25HitsAlwaysScoredRegardlessOfPoolSize(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			// "9" sorts after "1".."8", so a pool size of 1 (which samples
			// only the lowest sorted ID) would never include it by chance.
			"katzen": {{DocID: "9", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{
			"1": {1, 0}, "9": {1, 0},
		},
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a"},
			"9": {ID: "9", URL: "http://z", Title: "Katzen", Text: "Katzen sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{SemanticCandidatePoolSize: 1})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), opSettings, nil, domain.NewCorpusStatsCache(1, 10), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.DocID == "9" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the BM25 hit (doc 9) to be scored despite a pool size of 1, got %+v", results)
	}
}

// TestHybridSearch_PostingsForTermsCalledOnceForMultiTermQuery verifies the
// fix for the "one PostingsForTerm call per query term, per request"
// problem: a multi-term query must issue exactly one batched
// PostingsForTerms call carrying every unique term, not one call per term.
func TestHybridSearch_PostingsForTermsCalledOnceForMultiTermQuery(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1}},
			"hunde":  {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1}},
			"vogel":  {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Tiere", Text: "Katzen Hunde Vogel"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

	_, err := svc.Search(context.Background(), "katzen hunde vogel", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.postingsForTermsCalls != 1 {
		t.Fatalf("expected exactly one batched PostingsForTerms call for a 3-term query, got %d", repo.postingsForTermsCalls)
	}
	gotTerms := append([]string{}, repo.postingsForTermsArgs[0]...)
	sort.Strings(gotTerms)
	wantTerms := []string{"hunde", "katzen", "vogel"}
	if !reflect.DeepEqual(gotTerms, wantTerms) {
		t.Errorf("expected all 3 terms requested in that single call, got %v", gotTerms)
	}
}

// TestHybridSearch_BM25ScoringUsesCorpusStatsCacheValues verifies BM25
// scoring reads totalDocs/avgDocLen from the injected *domain.CorpusStatsCache
// rather than from anywhere else: the fake's CorpusStats method always
// returns a fixed (2, 10) regardless of what's injected here, so a BM25
// score that changes between two different cache values proves the cache
// -- not some other, unbounded per-request corpus query -- drove idf.
func TestHybridSearch_BM25ScoringUsesCorpusStatsCacheValues(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 3, DocLength: 10, DocFreq: 1}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	smallCorpus := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(10, 10), nil)
	smallResults, err := smallCorpus.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	largeCorpus := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(100000, 10), nil)
	largeResults, err := largeCorpus.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(smallResults) != 1 || len(largeResults) != 1 {
		t.Fatalf("expected one result from each search, got %d and %d", len(smallResults), len(largeResults))
	}
	if smallResults[0].BM25Score == largeResults[0].BM25Score {
		t.Errorf("expected BM25 score to depend on the injected corpus stats cache (idf grows with totalDocs), got equal scores %v", smallResults[0].BM25Score)
	}
}

// newFuzzyTestRepo builds a fakeSQLRepo with a single document indexed under
// "katzen" (never the misspelled "katzn"), for the fuzzy-correction tests
// below -- a fresh instance per svc, since fakeSQLRepo's call counters must
// start at zero for each Search() under test.
func newFuzzyTestRepo() *fakeSQLRepo {
	return &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 5, DocLength: 10, DocFreq: 1, TotalDocs: 1, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
}

// TestHybridSearch_FuzzyMatch_CorrectsTypoAndFindsMatch verifies the core
// fuzzy-correction path: a query term ("katzn") with zero postings hits is
// substituted, for BM25 scoring only, with the one vocabulary term within
// edit distance ("katzen"), finding the same document a correctly-spelled
// query would -- and the substitution is reported back via CorrectedTerms
// rather than silently rewriting the query.
func TestHybridSearch_FuzzyMatch_CorrectsTypoAndFindsMatch(t *testing.T) {
	vocabulary := domain.NewVocabularyCache([]domain.TermStat{{Term: "katzen", DocFreq: 1, TotalFreq: 5}})
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(newFuzzyTestRepo(), embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), vocabulary)

	results, err := svc.Search(context.Background(), "katzn", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Fatalf("expected the fuzzy-corrected query to find doc 1, got %+v", results)
	}
	if results[0].BM25Score == 0 {
		t.Errorf("expected a nonzero BM25 score once 'katzn' is corrected to 'katzen', got %v", results[0].BM25Score)
	}
	want := []domain.CorrectedTerm{{Original: "katzn", Corrected: "katzen"}}
	if !reflect.DeepEqual(results[0].CorrectedTerms, want) {
		t.Errorf("expected CorrectedTerms %+v, got %+v", want, results[0].CorrectedTerms)
	}
}

// TestHybridSearch_FuzzyMatch_ScoreMatchesCorrectlySpelledQuery proves the
// corrected term's BM25 contribution is exactly what searching for the
// correctly-spelled term directly would have produced, not an
// approximation.
func TestHybridSearch_FuzzyMatch_ScoreMatchesCorrectlySpelledQuery(t *testing.T) {
	vocabulary := domain.NewVocabularyCache([]domain.TermStat{{Term: "katzen", DocFreq: 1, TotalFreq: 5}})
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	typoSvc := application.NewHybridSearchService(newFuzzyTestRepo(), embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), vocabulary)
	typoResults, err := typoSvc.Search(context.Background(), "katzn", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	correctSvc := application.NewHybridSearchService(newFuzzyTestRepo(), embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), vocabulary)
	correctResults, err := correctSvc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(typoResults) != 1 || len(correctResults) != 1 {
		t.Fatalf("expected 1 result from each search, got %d and %d", len(typoResults), len(correctResults))
	}
	if typoResults[0].BM25Score != correctResults[0].BM25Score {
		t.Errorf("expected the corrected typo query's BM25 score (%v) to exactly match the correctly-spelled query's score (%v)",
			typoResults[0].BM25Score, correctResults[0].BM25Score)
	}
}

// TestHybridSearch_FuzzyMatch_NoCloseVocabularyTermLeftUncorrected verifies
// that a term with nothing within the bounded edit distance is left alone
// -- the query still runs (other terms keep working normally) and simply
// finds nothing extra for that term, with no correction reported.
func TestHybridSearch_FuzzyMatch_NoCloseVocabularyTermLeftUncorrected(t *testing.T) {
	vocabulary := domain.NewVocabularyCache([]domain.TermStat{{Term: "katzen", DocFreq: 1, TotalFreq: 5}})
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(newFuzzyTestRepo(), embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), vocabulary)

	results, err := svc.Search(context.Background(), "katzen quixoticnonsense", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "1" {
		t.Fatalf("expected the query to still run and find doc 1 via 'katzen', got %+v", results)
	}
	if len(results[0].CorrectedTerms) != 0 {
		t.Errorf("expected no correction for a term with nothing close in vocabulary, got %+v", results[0].CorrectedTerms)
	}
}

// TestHybridSearch_FuzzyMatch_NeverTouchesATermThatAlreadyMatched verifies a
// term that already has postings hits is never looked up or substituted,
// even when a closer vocabulary term happens to exist.
func TestHybridSearch_FuzzyMatch_NeverTouchesATermThatAlreadyMatched(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 2, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}},
		docs:       map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	// A near-miss vocabulary term (distance 1 from "katzen") that would
	// otherwise win any lookup by far outweighing it in frequency --
	// included to prove the vocabulary is never even consulted for a term
	// that already has postings hits.
	vocabulary := domain.NewVocabularyCache([]domain.TermStat{{Term: "katzem", DocFreq: 100, TotalFreq: 500}})
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(2, 10), vocabulary)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || len(results[0].CorrectedTerms) != 0 {
		t.Errorf("expected a term with an existing postings hit to never be corrected, got %+v", results)
	}
	if repo.postingsForTermsCalls != 1 {
		t.Errorf("expected exactly one PostingsForTerms call (no correction re-fetch needed) when every term already matched, got %d", repo.postingsForTermsCalls)
	}
}

// TestHybridSearch_FuzzyMatch_DisabledSkipsCorrectionEntirely verifies
// FuzzyMatchEnabled=false disables the whole feature: no vocabulary lookup,
// no second PostingsForTerms call, no CorrectedTerms reported, and BM25
// scoring for the misspelled term stays at zero -- exactly the behavior
// before this feature existed.
func TestHybridSearch_FuzzyMatch_DisabledSkipsCorrectionEntirely(t *testing.T) {
	vocabulary := domain.NewVocabularyCache([]domain.TermStat{{Term: "katzen", DocFreq: 1, TotalFreq: 5}})
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{FuzzyMatchEnabled: false})
	repo := newFuzzyTestRepo()
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), opSettings, nil, domain.NewCorpusStatsCache(1, 10), vocabulary)

	// Doc 1 is still found -- via the unbounded semantic candidate pool,
	// not BM25 -- since disabling fuzzy correction doesn't disable search
	// itself, only the typo-correction feature.
	results, err := svc.Search(context.Background(), "katzn", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 semantic-only result even with fuzzy matching disabled, got %+v", results)
	}
	if results[0].BM25Score != 0 {
		t.Errorf("expected zero BM25 score with fuzzy matching disabled (no correction applied), got %v", results[0].BM25Score)
	}
	if len(results[0].CorrectedTerms) != 0 {
		t.Errorf("expected no corrected terms reported when fuzzy matching is disabled, got %+v", results[0].CorrectedTerms)
	}
	if repo.postingsForTermsCalls != 1 {
		t.Errorf("expected exactly one PostingsForTerms call (no correction re-fetch) when fuzzy matching is disabled, got %d", repo.postingsForTermsCalls)
	}
}

// TestHybridSearch_SemanticScoringUsesRepoSuppliedNorm verifies the fix for
// the "document/query norms recomputed from scratch on every comparison"
// bottleneck: Search must score cosine similarity using the norm the repo
// hands back alongside each embedding (domain.EmbeddedVector.Norm --
// standing in for a real repository's precomputed norm_embedding column)
// rather than silently recomputing it from the vector. Doc 1's embedding is
// parallel to the query vector, so with its true norm the similarity would
// be 1.0; injecting a wrong norm must change the resulting semantic score.
func TestHybridSearch_SemanticScoringUsesRepoSuppliedNorm(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1}},
		},
		embeddings:     map[string][]float32{"1": {2, 0}},
		embeddingNorms: map[string]float64{"1": 10}, // true norm of {2,0} is 2, not 10
		docs:           map[string]domain.Document{"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, domain.NewCorpusStatsCache(1, 10), nil)

	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// queryVec={1,0} (norm 1), docVec={2,0}: dot=2. With the true norm (2),
	// similarity = 2/(1*2) = 1.0. With the injected norm (10), similarity =
	// 2/(1*10) = 0.2.
	const wantWithInjectedNorm = 0.2
	if got := results[0].SemanticSim; got < wantWithInjectedNorm-1e-9 || got > wantWithInjectedNorm+1e-9 {
		t.Errorf("expected semantic similarity computed from the repo-supplied norm (%v), got %v (true-norm value would be 1.0)", wantWithInjectedNorm, got)
	}
}

func TestHybridSearch_PageRankWeightZeroLeavesRankingUnchanged(t *testing.T) {
	newRepo := func() *fakeSQLRepo {
		return &fakeSQLRepo{
			postings: map[string][]domain.PostingStats{
				"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 2, AvgDocLen: 10}},
			},
			embeddings: map[string][]float32{"1": {1, 0}, "2": {0, 1}},
			// doc 2's pagerank vastly outweighs doc 1's -- with the default
			// PageRankWeight=0 this must have zero effect on ranking.
			pageranks: map[string]float64{"1": 0.01, "2": 0.9},
			docs: map[string]domain.Document{
				"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"},
				"2": {ID: "2", URL: "http://b", Title: "Hunde", Text: "Hunde sind toll"},
			},
		}
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	settings := domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B)
	// PageRankWeight defaults to 0 -- left untouched here on purpose.
	svc := application.NewHybridSearchService(newRepo(), embedder, settings, nil, nil, domain.NewCorpusStatsCache(2, 10), nil)
	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 || results[0].DocID != "1" {
		t.Fatalf("expected doc 1 first by BM25 alone -- PageRankWeight=0 must not influence ranking, got %+v", results)
	}
}

func TestHybridSearch_PageRankWeightBlendsIntoFinalScore(t *testing.T) {
	newRepo := func() *fakeSQLRepo {
		return &fakeSQLRepo{
			postings: map[string][]domain.PostingStats{
				"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 2, AvgDocLen: 10}},
			},
			embeddings: map[string][]float32{"1": {1, 0}, "2": {0, 1}},
			pageranks:  map[string]float64{"1": 0.01, "2": 0.9},
			docs: map[string]domain.Document{
				"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"},
				"2": {ID: "2", URL: "http://b", Title: "Hunde", Text: "Hunde sind toll"},
			},
		}
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	// doc 1 wins on BM25 alone (alpha=1.0, doc 2 has no postings hit at
	// all), but a heavy PageRankWeight should let doc 2's much larger
	// normalized pagerank overtake it.
	settings := domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B)
	settings.SetPageRankWeight(0.9)
	svc := application.NewHybridSearchService(newRepo(), embedder, settings, nil, nil, domain.NewCorpusStatsCache(2, 10), nil)
	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 || results[0].DocID != "2" {
		t.Fatalf("expected doc 2's overwhelming pagerank (weight 0.9) to overtake doc 1's BM25 lead, got %+v", results)
	}
}

func TestHybridSearch_PageRankWeightIgnoredWhenNoCandidateHasAPositivePageRank(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{
			"katzen": {{DocID: "1", TermFreq: 1, DocLength: 10, DocFreq: 1, TotalDocs: 2, AvgDocLen: 10}},
		},
		embeddings: map[string][]float32{"1": {1, 0}, "2": {0, 1}},
		// No pageranks map at all -- every candidate reports PageRank 0
		// (as an un-migrated/never-scored document would), so the blend
		// must be a no-op rather than dividing by zero.
		docs: map[string]domain.Document{
			"1": {ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll"},
			"2": {ID: "2", URL: "http://b", Title: "Hunde", Text: "Hunde sind toll"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	settings := domain.NewTuningSettings(1.0, domain.DefaultBM25K1, domain.DefaultBM25B)
	settings.SetPageRankWeight(0.9)
	svc := application.NewHybridSearchService(repo, embedder, settings, nil, nil, domain.NewCorpusStatsCache(2, 10), nil)
	results, err := svc.Search(context.Background(), "katzen", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 || results[0].DocID != "1" {
		t.Fatalf("expected doc 1 still first (no pagerank data to blend), got %+v", results)
	}
}

// TestHybridSearch_ANNUsedWhenAvailableAndEnabled verifies the core wiring:
// when the repository reports ANN available (ok=true) and
// ANNSearchEnabled is at its default (true, since opSettings is nil here),
// TopSemanticMatches -- not SampleEmbeddings -- fills the semantic
// candidate pool.
func TestHybridSearch_ANNUsedWhenAvailableAndEnabled(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{"quantenphysik": {}},
		annOK:    true,
		annMatches: map[string]domain.EmbeddedVector{
			"2": {Vector: []float32{1, 0}, Norm: 1},
		},
		docs: map[string]domain.Document{"2": {ID: "2", URL: "http://b", Title: "Physik", Text: "Physik-Artikel"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)

	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].DocID != "2" {
		t.Fatalf("expected the ANN-supplied match found, got %+v", results)
	}
	if repo.topSemanticMatchesCalls != 1 {
		t.Errorf("expected exactly one TopSemanticMatches call, got %d", repo.topSemanticMatchesCalls)
	}
	if repo.sampleEmbeddingsCalls != 0 {
		t.Errorf("expected SampleEmbeddings never called when ANN is available and enabled, got %d calls", repo.sampleEmbeddingsCalls)
	}
}

// TestHybridSearch_ANNSearchDisabledForcesBruteForceFallback verifies the
// admin troubleshooting knob: even though the repository reports ANN
// available (annOK true), ANNSearchEnabled=false must force the existing
// brute-force SampleEmbeddings path instead -- TopSemanticMatches must not
// even be called, and the result must come from the brute-force sample
// (doc 9), not the ANN-only match (doc 2), proving the fallback was
// actually taken rather than merely uncounted.
func TestHybridSearch_ANNSearchDisabledForcesBruteForceFallback(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{"quantenphysik": {}},
		annOK:    true,
		annMatches: map[string]domain.EmbeddedVector{
			"2": {Vector: []float32{1, 0}, Norm: 1},
		},
		embeddings: map[string][]float32{"9": {1, 0}},
		docs: map[string]domain.Document{
			"2": {ID: "2", URL: "http://b", Title: "Physik", Text: "Physik-Artikel"},
			"9": {ID: "9", URL: "http://z", Title: "Brute", Text: "Brute-force sample doc"},
		},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{ANNSearchEnabled: false, SemanticCandidatePoolSize: 200})
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), opSettings, nil, nil, nil)

	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.topSemanticMatchesCalls != 0 {
		t.Errorf("expected TopSemanticMatches never called when ANNSearchEnabled=false, got %d calls", repo.topSemanticMatchesCalls)
	}
	if repo.sampleEmbeddingsCalls != 1 {
		t.Errorf("expected exactly one SampleEmbeddings call (the forced brute-force path), got %d", repo.sampleEmbeddingsCalls)
	}
	if len(results) != 1 || results[0].DocID != "9" {
		t.Errorf("expected doc 9 (from the forced brute-force sample), got %+v", results)
	}
}

// TestHybridSearch_ANNUnavailableFallsBackToSampleEmbeddings verifies the
// default-path contract on SQLite/dev/CI (or a Postgres server without
// pgvector): when the repository reports ANN unavailable (annOK false,
// the zero value -- exactly what sqlrepo.Repository.TopSemanticMatches
// reports whenever EnableANN never succeeded), the service must fall back
// to SampleEmbeddings and behave exactly as it did before ANN existed.
func TestHybridSearch_ANNUnavailableFallsBackToSampleEmbeddings(t *testing.T) {
	repo := &fakeSQLRepo{
		postings:   map[string][]domain.PostingStats{"quantenphysik": {}},
		embeddings: map[string][]float32{"9": {1, 0}},
		docs:       map[string]domain.Document{"9": {ID: "9", URL: "http://z", Title: "Brute", Text: "doc"}},
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)

	results, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.topSemanticMatchesCalls != 1 {
		t.Errorf("expected TopSemanticMatches to be tried once (ANNSearchEnabled defaults true), got %d", repo.topSemanticMatchesCalls)
	}
	if repo.sampleEmbeddingsCalls != 1 {
		t.Errorf("expected exactly one SampleEmbeddings fallback call, got %d", repo.sampleEmbeddingsCalls)
	}
	if len(results) != 1 || results[0].DocID != "9" {
		t.Errorf("expected doc 9 found via the brute-force fallback, got %+v", results)
	}
}

// TestHybridSearch_ANNQueryErrorPropagates verifies a genuine ANN query
// failure (as opposed to mere unavailability, which is ok=false with a nil
// error) is surfaced as a real search error rather than silently falling
// back -- an actual fault deserves the same treatment any other repository
// error already gets, not a quiet, possibly-stale substitution.
func TestHybridSearch_ANNQueryErrorPropagates(t *testing.T) {
	repo := &fakeSQLRepo{
		postings: map[string][]domain.PostingStats{"quantenphysik": {}},
		annErr:   errors.New("ann query failed"),
	}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	svc := application.NewHybridSearchService(repo, embedder, domain.NewTuningSettings(0.3, domain.DefaultBM25K1, domain.DefaultBM25B), nil, nil, nil, nil)

	if _, err := svc.Search(context.Background(), "quantenphysik", ports.SearchQuery{TopK: 10}); err == nil {
		t.Fatal("expected the ANN query failure to propagate as a search error")
	}
}
