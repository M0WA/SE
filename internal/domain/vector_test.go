package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestCosineSimilarity_IdenticalVectorsGiveOne(t *testing.T) {
	v := []float32{1, 2, 3}
	if got := domain.CosineSimilarity(v, v); got < 0.999 || got > 1.001 {
		t.Errorf("erwartet ~1.0 für identische Vektoren, bekam %f", got)
	}
}

func TestCosineSimilarity_OrthogonalVectorsGiveZero(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := domain.CosineSimilarity(a, b); got != 0 {
		t.Errorf("erwartet 0 für orthogonale Vektoren, bekam %f", got)
	}
}

func TestCosineSimilarity_MismatchedLengthReturnsZero(t *testing.T) {
	if got := domain.CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("erwartet 0 bei Dimensions-Mismatch, bekam %f", got)
	}
}

func TestCosineSimilarity_ZeroVectorReturnsZero(t *testing.T) {
	if got := domain.CosineSimilarity([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("erwartet 0 bei Nullvektor, bekam %f", got)
	}
}

func TestCosineSimilarity_EmptyVectorsReturnZero(t *testing.T) {
	if got := domain.CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("erwartet 0 bei leeren Vektoren, bekam %f", got)
	}
}
