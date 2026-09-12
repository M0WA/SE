package domain_test

import (
	"math/rand"
	"testing"

	"searchengine/internal/domain"
)

// benchVectors builds n random 384-dim vectors (a typical small
// embedding-model dimensionality), deterministically seeded so repeated
// runs are comparable.
func benchVectors(n, dims int) [][]float32 {
	rng := rand.New(rand.NewSource(1))
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dims)
		for j := range v {
			v[j] = rng.Float32()
		}
		vecs[i] = v
	}
	return vecs
}

// BenchmarkCosineSimilarity_Fresh is the "before" path: CosineSimilarity
// recomputes both vectors' norms (two full sum-of-squares passes) on every
// single comparison, exactly as every call in a scoring loop over 5,000
// candidates would have before recomputed-vector-norms was fixed.
func BenchmarkCosineSimilarity_Fresh(b *testing.B) {
	const dims = 384
	const numCandidates = 5000
	query := benchVectors(1, dims)[0]
	candidates := benchVectors(numCandidates, dims)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sum float64
		for _, c := range candidates {
			sum += domain.CosineSimilarity(query, c)
		}
		if sum == 0 {
			b.Fatal("unexpected zero sum")
		}
	}
}

// BenchmarkCosineSimilarityWithNorms_Precomputed is the "after" path: the
// query's norm is computed exactly once (outside the candidate loop,
// mirroring hybrid_search_service.go's queryNorm), and each candidate's norm
// is supplied precomputed (mirroring the repo-supplied norm_embedding
// column) rather than recomputed from the vector on every comparison.
func BenchmarkCosineSimilarityWithNorms_Precomputed(b *testing.B) {
	const dims = 384
	const numCandidates = 5000
	query := benchVectors(1, dims)[0]
	candidates := benchVectors(numCandidates, dims)
	queryNorm := domain.VectorNorm(query)
	candidateNorms := make([]float64, numCandidates)
	for i, c := range candidates {
		candidateNorms[i] = domain.VectorNorm(c)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sum float64
		for j, c := range candidates {
			sum += domain.CosineSimilarityWithNorms(query, c, queryNorm, candidateNorms[j])
		}
		if sum == 0 {
			b.Fatal("unexpected zero sum")
		}
	}
}
