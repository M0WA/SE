package domain

import "math"

// CosineSimilarity measures the semantic closeness of two embedding vectors,
// range [-1, 1]. Recomputes both norms every call -- fine for a one-off
// comparison, but a caller comparing one vector against many (hybrid
// search) should use VectorNorm once plus CosineSimilarityWithNorms instead.
func CosineSimilarity(a, b []float32) float64 {
	return CosineSimilarityWithNorms(a, b, VectorNorm(a), VectorNorm(b))
}

// VectorNorm computes a vector's Euclidean (L2) norm: sqrt of the sum of
// its elements' squares. Depends only on the vector itself, so it can be
// computed once (per query, or once per document at embedding time) and
// reused across every comparison that vector takes part in, rather than
// recomputed inside every call to a cosine-similarity function.
func VectorNorm(v []float32) float64 {
	var sumSquares float64
	for _, x := range v {
		sumSquares += float64(x) * float64(x)
	}
	return math.Sqrt(sumSquares)
}

// CosineSimilarityWithNorms is CosineSimilarity with precomputed norms
// (see VectorNorm) to skip redundant sum-of-squares passes. normA/normB
// must be VectorNorm(a)/VectorNorm(b) -- a stale norm silently produces a
// wrong result, not an error.
func CosineSimilarityWithNorms(a, b []float32, normA, normB float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (normA * normB)
}

// CombineWeighted linearly combines title/body embeddings as
// titleWeight*title + (1-titleWeight)*body (see EmbeddingTitleWeight),
// giving the title tunable influence a provider's own pooling over a
// concatenated string wouldn't reliably give it. Never renormalized --
// cosine similarity is scale-invariant. title/body must be equal length;
// titleWeight assumed already clamped to [0,1] by the caller.
func CombineWeighted(title, body []float32, titleWeight float64) []float32 {
	combined := make([]float32, len(title))
	for i := range title {
		combined[i] = float32(titleWeight*float64(title[i]) + (1-titleWeight)*float64(body[i]))
	}
	return combined
}

// EmbeddedVector is a document's embedding paired with its precomputed
// Norm (see VectorNorm), computed once at persist time rather than on
// every scoring request. PageRank is fetched alongside since both are
// read together for every hybrid search candidate.
type EmbeddedVector struct {
	Vector   []float32
	Norm     float64
	PageRank float64
}
