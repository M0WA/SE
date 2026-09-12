package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestCosineSimilarity_IdenticalVectorsGiveOne(t *testing.T) {
	v := []float32{1, 2, 3}
	if got := domain.CosineSimilarity(v, v); got < 0.999 || got > 1.001 {
		t.Errorf("expected ~1.0 for identical vectors, got %f", got)
	}
}

func TestCosineSimilarity_OrthogonalVectorsGiveZero(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := domain.CosineSimilarity(a, b); got != 0 {
		t.Errorf("expected 0 for orthogonal vectors, got %f", got)
	}
}

func TestCosineSimilarity_MismatchedLengthReturnsZero(t *testing.T) {
	if got := domain.CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("expected 0 for dimension mismatch, got %f", got)
	}
}

func TestCosineSimilarity_ZeroVectorReturnsZero(t *testing.T) {
	if got := domain.CosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("expected 0 for zero vector, got %f", got)
	}
}

func TestCosineSimilarity_EmptyVectorsReturnZero(t *testing.T) {
	if got := domain.CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("expected 0 for empty vectors, got %f", got)
	}
}

func TestVectorNorm_ComputesEuclideanNorm(t *testing.T) {
	if got := domain.VectorNorm([]float32{3, 4}); got < 4.999 || got > 5.001 {
		t.Errorf("expected norm 5 for {3,4}, got %f", got)
	}
}

func TestVectorNorm_EmptyVectorIsZero(t *testing.T) {
	if got := domain.VectorNorm(nil); got != 0 {
		t.Errorf("expected 0 norm for an empty vector, got %f", got)
	}
}

func TestVectorNorm_ZeroVectorIsZero(t *testing.T) {
	if got := domain.VectorNorm([]float32{0, 0, 0}); got != 0 {
		t.Errorf("expected 0 norm for an all-zero vector, got %f", got)
	}
}

// TestCosineSimilarityWithNorms_MatchesCosineSimilarity pins down the
// precomputed-norm path against a table of vector pairs (including the
// degenerate cases CosineSimilarity itself special-cases) so it can never
// silently drift from the from-scratch implementation it's meant to be an
// optimized equivalent of.
func TestCosineSimilarityWithNorms_MatchesCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}},
		{"opposite", []float32{1, 0}, []float32{-1, 0}},
		{"mismatched length", []float32{1, 2}, []float32{1, 2, 3}},
		{"zero vector", []float32{0, 0}, []float32{1, 1}},
		{"both zero", []float32{0, 0}, []float32{0, 0}},
		{"empty", nil, nil},
		{"non-unit vectors", []float32{2, 0, 0}, []float32{1, 1, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := domain.CosineSimilarity(tc.a, tc.b)
			got := domain.CosineSimilarityWithNorms(tc.a, tc.b, domain.VectorNorm(tc.a), domain.VectorNorm(tc.b))
			if got != want {
				t.Errorf("CosineSimilarityWithNorms(%v, %v) = %f, want %f (from CosineSimilarity)", tc.a, tc.b, got, want)
			}
		})
	}
}

// TestCosineSimilarityWithNorms_StaleNormProducesDifferentResult documents
// the precomputed-norm form's contract: callers are responsible for keeping
// a vector's norm in sync with the vector itself (e.g. recomputing it
// whenever a document is re-embedded) -- passing a stale norm doesn't
// error, it just produces a different (wrong) similarity, unlike
// CosineSimilarity which always recomputes both norms fresh.
func TestCosineSimilarityWithNorms_StaleNormProducesDifferentResult(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0}
	correct := domain.CosineSimilarityWithNorms(a, b, domain.VectorNorm(a), domain.VectorNorm(b))
	staleNormB := domain.VectorNorm(b) * 2
	stale := domain.CosineSimilarityWithNorms(a, b, domain.VectorNorm(a), staleNormB)
	if stale == correct {
		t.Fatalf("expected a stale norm to change the result (correct=%f), got same value", correct)
	}
}
