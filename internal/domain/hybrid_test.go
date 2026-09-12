package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestCombineScores_PureBM25WhenAlphaOne(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "a", BM25Score: 10, SemanticSim: 0.1},
		{DocID: "b", BM25Score: 5, SemanticSim: 0.9},
	}
	out := domain.CombineScores(in, 1.0)
	if out[0].DocID != "a" {
		t.Errorf("expected a first at alpha=1, got %s", out[0].DocID)
	}
}

func TestCombineScores_PureSemanticWhenAlphaZero(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "a", BM25Score: 10, SemanticSim: 0.1},
		{DocID: "b", BM25Score: 5, SemanticSim: 0.9},
	}
	out := domain.CombineScores(in, 0.0)
	if out[0].DocID != "b" {
		t.Errorf("expected b first at alpha=0, got %s", out[0].DocID)
	}
}

func TestCombineScores_ClampsAlphaOutOfRange(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 1, SemanticSim: 1}}
	out := domain.CombineScores(in, 5.0)
	if out[0].FinalScore != 1.0 {
		t.Errorf("expected clamped alpha=1, got FinalScore=%f", out[0].FinalScore)
	}
}

func TestCombineScores_ClampsNegativeAlpha(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 1, SemanticSim: 1}}
	out := domain.CombineScores(in, -1.0)
	if out[0].FinalScore != 1.0 {
		t.Errorf("expected clamped alpha=0 (pure semantic), got FinalScore=%f", out[0].FinalScore)
	}
}

func TestCombineScores_NegativeCosineClampedToZero(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 10, SemanticSim: -0.5}}
	out := domain.CombineScores(in, 0.0)
	if out[0].FinalScore != 0 {
		t.Errorf("expected 0 for negative cosine and alpha=0, got %f", out[0].FinalScore)
	}
}

func TestCombineScores_EmptyMaxBM25NoDivisionByZero(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 0, SemanticSim: 0.5}}
	out := domain.CombineScores(in, 0.5)
	if out[0].FinalScore < 0 {
		t.Error("expected no NaN/negative value when maxBM25=0")
	}
}

// TestCombineScores_SetsNormBM25 verifies each result's NormBM25 is its
// BM25Score as a fraction of the batch's own max -- the value the admin
// score-composition view relies on, not just an internal that only
// FinalScore reflects.
func TestCombineScores_SetsNormBM25(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "a", BM25Score: 10, SemanticSim: 0},
		{DocID: "b", BM25Score: 5, SemanticSim: 0},
	}
	out := domain.CombineScores(in, 1.0)
	byID := make(map[string]domain.HybridResult, len(out))
	for _, r := range out {
		byID[r.DocID] = r
	}
	if byID["a"].NormBM25 != 1.0 {
		t.Errorf("expected the batch max to normalize to 1.0, got %v", byID["a"].NormBM25)
	}
	if byID["b"].NormBM25 != 0.5 {
		t.Errorf("expected half the batch max to normalize to 0.5, got %v", byID["b"].NormBM25)
	}
}

func TestCombineScores_NormBM25ZeroWhenMaxBM25Zero(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 0, SemanticSim: 0.5}}
	out := domain.CombineScores(in, 0.5)
	if out[0].NormBM25 != 0 {
		t.Errorf("expected NormBM25=0 when every candidate's BM25Score is 0, got %v", out[0].NormBM25)
	}
}

func TestCombineScores_DeterministicTieBreak(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "z", BM25Score: 5, SemanticSim: 0.5},
		{DocID: "a", BM25Score: 5, SemanticSim: 0.5},
	}
	out := domain.CombineScores(in, 0.5)
	if out[0].DocID != "a" {
		t.Errorf("expected deterministic tie-break with 'a' first, got %s", out[0].DocID)
	}
}
