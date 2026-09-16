package domain

import "math"

// CosineSimilarity measures the semantic closeness of two embedding vectors,
// range [-1, 1]. It recomputes both vectors' norms from scratch on every
// call -- fine for a one-off comparison, but wasteful for a caller (like
// hybrid search) that compares one query vector against many candidate
// documents: use VectorNorm once per vector plus CosineSimilarityWithNorms
// instead, so neither the (per-request-constant) query norm nor a
// document's (never-changing-between-crawls) norm is recomputed on every
// pairwise comparison.
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

// CosineSimilarityWithNorms computes cosine similarity from two vectors
// plus their already-computed norms (see VectorNorm), skipping the
// sum-of-squares passes CosineSimilarity would otherwise redo on every
// call. normA and normB must be VectorNorm(a) and VectorNorm(b)
// respectively -- passing a stale norm for a vector that has actually
// changed silently produces a wrong result rather than an error.
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

// CombineWeighted linearly combines a title's and a body's embedding
// vectors as titleWeight*title + (1-titleWeight)*body, giving the title's
// semantics real, tunable influence over the resulting document vector
// (see OperationalSettingsValues.EmbeddingTitleWeight) rather than relying
// on however a provider's own pooling strategy happens to treat a
// concatenated title+body string -- for most pooling strategies (e.g. mean
// pooling over tokens), a short title diluted into a much longer body ends
// up with negligible influence on the resulting vector regardless of
// where in the string it appears.
//
// The result is never renormalized: cosine similarity (see
// CosineSimilarityWithNorms) is scale-invariant, so whatever magnitude the
// combination ends up with doesn't affect any downstream comparison.
// title and body must be the same length (guaranteed when both come from
// the same embedder) -- titleWeight is assumed already clamped to [0,1] by
// the caller (see domain.OperationalSettings.Set).
func CombineWeighted(title, body []float32, titleWeight float64) []float32 {
	combined := make([]float32, len(title))
	for i := range title {
		combined[i] = float32(titleWeight*float64(title[i]) + (1-titleWeight)*float64(body[i]))
	}
	return combined
}

// EmbeddedVector is a document's embedding paired with its precomputed
// norm (see VectorNorm), so repository callers that load embeddings for
// scoring never need to recompute a document's norm from scratch on every
// request -- it's computed once, at the point the embedding itself is
// produced or persisted, and carried alongside it from then on. PageRank
// is the document's current link-authority score (documents.pagerank),
// fetched alongside the embedding since both are read together for every
// hybrid search candidate -- see hybridSearchService.Search.
type EmbeddedVector struct {
	Vector   []float32
	Norm     float64
	PageRank float64
}
