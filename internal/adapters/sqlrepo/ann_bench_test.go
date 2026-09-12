package sqlrepo_test

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
)

// benchmarkBruteForceANN computes the exact cosine-similarity top-K over
// every embedding in embeddings.
//
// What this file can and cannot measure without a live Postgres+pgvector
// server, stated up front the same way BenchmarkConnectionPoolTuning (in
// repository_bench_test.go) is honest about lacking a live Postgres/MySQL
// server for its own connection-pool benchmark:
//
//   - EnableANN is a no-op on any dialect but "postgres" (see ann.go), and
//     this test environment has no live Postgres+pgvector server (the same
//     constraint TEST_POSTGRES_DSN-gated tests in ann_test.go work around by
//     skipping when unset). So TopSemanticMatches can't actually be
//     benchmarked here: against SQLite it just returns ok=false immediately,
//     which would only benchmark a single boolean check, not a real ANN
//     query.
//   - What CAN be measured against the real, current SQLite path with no
//     external service: SampleEmbeddings, the exact fallback every process
//     uses whenever ANN is unavailable (the default everywhere except a
//     Postgres deployment with pgvector actually installed) -- this is real
//     production code, not a stand-in.
//   - Alongside it, benchmarkBruteForceANN is an explicit, clearly labeled
//     *stand-in* for "search by actual similarity instead of by ID sample":
//     a brute-force linear scan computing exact cosine distance against
//     every embedding in memory, then taking the top-K. This is NOT a
//     substitute for measuring real pgvector HNSW performance -- HNSW is a
//     sublinear approximate-nearest-neighbor index; this is an exact O(n)
//     scan. It exists only to characterize the *shape* of the trade-off (a
//     real-similarity lookup necessarily costs more than an ID-ordered
//     sample of the same size, and that gap grows with corpus size) using
//     data this repo already produces, not to predict real pgvector latency
//     or claim HNSW would perform anywhere near this linearly. Real
//     pgvector ANN timing can only come from benchmarking against an actual
//     Postgres+pgvector server, which ann_test.go already does
//     (TEST_POSTGRES_DSN-gated, skipped here).
func benchmarkBruteForceANN(queryVec []float32, embeddings map[string]domain.EmbeddedVector, k int) map[string]domain.EmbeddedVector {
	type scored struct {
		id  string
		sim float64
		vec domain.EmbeddedVector
	}
	queryNorm := domain.VectorNorm(queryVec)
	scoredAll := make([]scored, 0, len(embeddings))
	for id, ev := range embeddings {
		sim := domain.CosineSimilarityWithNorms(queryVec, ev.Vector, queryNorm, ev.Norm)
		scoredAll = append(scoredAll, scored{id: id, sim: sim, vec: ev})
	}
	sort.Slice(scoredAll, func(i, j int) bool { return scoredAll[i].sim > scoredAll[j].sim })
	if k > len(scoredAll) {
		k = len(scoredAll)
	}
	out := make(map[string]domain.EmbeddedVector, k)
	for _, s := range scoredAll[:k] {
		out[s.id] = s.vec
	}
	return out
}

// BenchmarkSemanticCandidateLookup compares, at a few corpus sizes, the
// real SampleEmbeddings fallback (an "ORDER BY id LIMIT limit" sample,
// what every process actually runs whenever ANN is unavailable -- the
// default outside a Postgres+pgvector deployment) against the brute-force
// exact-similarity stand-in described in this file's leading doc comment.
// See that comment for exactly what BruteForceANN does and doesn't
// represent -- in particular, it is NOT a measurement of real pgvector
// HNSW performance.
func BenchmarkSemanticCandidateLookup(b *testing.B) {
	const poolSize = 200
	const dims = 128

	for _, n := range []int{1000, 10000, 50000} {
		ctx := context.Background()
		repo, err := sqlrepo.New(ctx, "sqlite", benchDSN("benchann"))
		if err != nil {
			b.Fatalf("failed to create repo: %v", err)
		}

		rng := rand.New(rand.NewSource(3))
		embeddings := make(map[string]domain.EmbeddedVector, n)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("doc-%d", i)
			vec := make([]float32, dims)
			for j := range vec {
				vec[j] = rng.Float32()
			}
			doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: "T", Text: "benchmark content"}
			if err := repo.SaveDocument(ctx, doc, vec); err != nil {
				b.Fatalf("seeding document %s: %v", id, err)
			}
			embeddings[id] = domain.EmbeddedVector{Vector: vec, Norm: domain.VectorNorm(vec)}
		}
		queryVec := make([]float32, dims)
		for j := range queryVec {
			queryVec[j] = rng.Float32()
		}

		b.Run(fmt.Sprintf("Docs=%d/Fallback_SampleEmbeddings", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := repo.SampleEmbeddings(ctx, poolSize)
				if err != nil {
					b.Fatalf("SampleEmbeddings: %v", err)
				}
				if len(out) != poolSize {
					b.Fatalf("expected %d embeddings, got %d", poolSize, len(out))
				}
			}
		})

		b.Run(fmt.Sprintf("Docs=%d/StandIn_BruteForceExactNN", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out := benchmarkBruteForceANN(queryVec, embeddings, poolSize)
				if len(out) != poolSize {
					b.Fatalf("expected %d embeddings, got %d", poolSize, len(out))
				}
			}
		})

		repo.Close()
	}
}
