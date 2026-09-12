package application_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// benchHybridRepo is a benchmark-only ports.SQLRepository double, separate
// from this package's fakeSQLRepo (used by the correctness tests): its
// SampleEmbeddings is O(limit) via a sorted ID list built once at
// construction, mirroring a real SQL "ORDER BY id LIMIT limit" query served
// by the primary key index (cost independent of corpus size beyond the
// limit itself) rather than fakeSQLRepo's sort.Strings-every-call
// implementation, which is O(n log n) per call and would otherwise swamp
// this benchmark's whole point -- proving the *bounded* candidate pool's
// cost doesn't grow with corpus size -- with an artifact of the simpler
// test double instead of the real optimization being measured.
type benchHybridRepo struct {
	sortedIDs  []string
	postings   map[string][]domain.PostingStats
	embeddings map[string]domain.EmbeddedVector
	docs       map[string]domain.Document
}

func (r *benchHybridRepo) SaveDocument(context.Context, domain.Document, []float32) error { return nil }

func (r *benchHybridRepo) PostingsForTerms(_ context.Context, terms []string) (map[string][]domain.PostingStats, error) {
	out := make(map[string][]domain.PostingStats, len(terms))
	for _, t := range terms {
		if p, ok := r.postings[t]; ok {
			out[t] = p
		}
	}
	return out, nil
}

func (r *benchHybridRepo) CorpusStats(context.Context) (int, float64, error) {
	return len(r.sortedIDs), 120, nil
}

func (r *benchHybridRepo) VocabularyStats(context.Context, int) (int, []domain.TermStat, error) {
	return 0, nil, nil
}

// AllTerms is unused by this benchmark (fuzzy correction is never
// exercised here) -- a no-op stub only to satisfy ports.SQLRepository.
func (r *benchHybridRepo) AllTerms(context.Context) ([]domain.TermStat, error) {
	return nil, nil
}

func (r *benchHybridRepo) EmbeddingsForDocs(_ context.Context, ids []string) (map[string]domain.EmbeddedVector, error) {
	out := make(map[string]domain.EmbeddedVector, len(ids))
	for _, id := range ids {
		if v, ok := r.embeddings[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// SampleEmbeddings takes the first limit of the pre-sorted ID list -- O(limit),
// not O(corpus size), exactly like the real repository's indexed
// "ORDER BY id LIMIT limit" query.
func (r *benchHybridRepo) SampleEmbeddings(_ context.Context, limit int) (map[string]domain.EmbeddedVector, error) {
	if limit <= 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	if limit > len(r.sortedIDs) {
		limit = len(r.sortedIDs)
	}
	out := make(map[string]domain.EmbeddedVector, limit)
	for _, id := range r.sortedIDs[:limit] {
		out[id] = r.embeddings[id]
	}
	return out, nil
}

func (r *benchHybridRepo) DocumentsByIDs(_ context.Context, ids []string) (map[string]domain.Document, error) {
	out := make(map[string]domain.Document, len(ids))
	for _, id := range ids {
		if d, ok := r.docs[id]; ok {
			out[id] = d
		}
	}
	return out, nil
}

func (r *benchHybridRepo) DocumentsByIDsSortedByCrawledAt(_ context.Context, ids []string) ([]domain.Document, error) {
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := r.docs[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// DocumentIDsByHost is unused by this benchmark (no site: queries are
// benchmarked here) -- a no-op stub only to satisfy ports.SQLRepository.
func (r *benchHybridRepo) DocumentIDsByHost(context.Context, []string) ([]string, error) {
	return nil, nil
}

// TopSemanticMatches always reports ANN unavailable -- this benchmark
// exists to measure the bounded brute-force SampleEmbeddings path
// (benchHybridRepo's whole point, see its doc comment above), not the ANN
// path, so it must never divert to anything but SampleEmbeddings.
func (r *benchHybridRepo) TopSemanticMatches(context.Context, []float32, int) (map[string]domain.EmbeddedVector, bool, error) {
	return nil, false, nil
}

func (r *benchHybridRepo) ListDocuments(context.Context, int, string) ([]domain.IndexedDocument, error) {
	return nil, nil
}

func (r *benchHybridRepo) DeleteDocument(context.Context, string) error { return nil }

var _ ports.SQLRepository = (*benchHybridRepo)(nil)

// buildHybridBenchRepo builds a benchHybridRepo simulating an n-document
// corpus: every document gets a random 128-dim embedding (a typical small
// embedding-model dimensionality), and exactly matchDocsPerTerm documents --
// a fixed count, not a fraction of n, mirroring how a real query's BM25 hit
// count depends on how many pages actually contain that term, not on how
// large the whole corpus happens to be -- carry postings for each of two
// query terms.
func buildHybridBenchRepo(n, matchDocsPerTerm int) *benchHybridRepo {
	rng := rand.New(rand.NewSource(99))
	ids := make([]string, n)
	embeddings := make(map[string]domain.EmbeddedVector, n)
	docs := make(map[string]domain.Document, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc-%08d", i) // zero-padded so lexicographic == numeric order, like a real sorted ID column
		ids[i] = id
		vec := make([]float32, 128)
		for j := range vec {
			vec[j] = rng.Float32()
		}
		embeddings[id] = domain.EmbeddedVector{Vector: vec, Norm: domain.VectorNorm(vec)}
		docs[id] = domain.Document{ID: id, URL: "https://example.com/" + id, Title: "Doc", Text: "benchmark content alpha beta"}
	}

	postings := make(map[string][]domain.PostingStats)
	for _, term := range []string{"alpha", "beta"} {
		perm := rng.Perm(n)[:matchDocsPerTerm]
		stats := make([]domain.PostingStats, matchDocsPerTerm)
		for i, docIdx := range perm {
			stats[i] = domain.PostingStats{
				DocID:     ids[docIdx],
				TermFreq:  1 + rng.Intn(3),
				DocLength: 50 + rng.Intn(200),
				DocFreq:   matchDocsPerTerm,
			}
		}
		postings[term] = stats
	}

	return &benchHybridRepo{sortedIDs: ids, postings: postings, embeddings: embeddings, docs: docs}
}

// BenchmarkHybridSearch_Combine runs a realistic 2-term hybrid search over
// corpora of 5,000 and 50,000 documents, comparing the current (bounded)
// default semantic candidate pool size against a pool sized to the whole
// corpus. The unbounded-pool run stands in for the pre-fix behavior -- the
// old AllEmbeddings-plus-per-candidate-DocumentByID code path no longer
// exists to call directly, since it was deleted rather than left as dead
// code, so a SemanticCandidatePoolSize equal to corpus size is used here to
// reproduce the same "score every document" cost through the real,
// still-current Search() method. Expected result: the BoundedPool200 runs
// should show close to flat latency growth between 5k and 50k documents
// (the candidate set scored stays ~poolSize + BM25 hits, independent of
// corpus size), while the UnboundedPool runs should show latency growing
// roughly with corpus size.
func BenchmarkHybridSearch_Combine(b *testing.B) {
	const matchDocsPerTerm = 100

	type poolCase struct {
		label string
		size  func(corpusSize int) int
	}
	cases := []poolCase{
		{"BoundedPool200", func(int) int { return 200 }},
		{"UnboundedPoolFullCorpus", func(n int) int { return n }},
	}

	for _, n := range []int{5000, 50000} {
		repo := buildHybridBenchRepo(n, matchDocsPerTerm)
		embedder := &fakeEmbedder{vec: make([]float32, 128)}
		settings := domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B)
		corpusStats := domain.NewCorpusStatsCache(n, 120)

		for _, c := range cases {
			opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{SemanticCandidatePoolSize: c.size(n)})
			svc := application.NewHybridSearchService(repo, embedder, settings, opSettings, nil, corpusStats, nil)
			b.Run(fmt.Sprintf("Docs=%d/%s", n, c.label), func(b *testing.B) {
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					results, err := svc.Search(ctx, "alpha beta", ports.SearchQuery{TopK: 10})
					if err != nil {
						b.Fatalf("Search: %v", err)
					}
					if len(results) == 0 {
						b.Fatal("expected at least one result")
					}
				}
			})
		}
	}
}
