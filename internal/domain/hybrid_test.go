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
		t.Errorf("erwartet a zuerst bei alpha=1, bekam %s", out[0].DocID)
	}
}

func TestCombineScores_PureSemanticWhenAlphaZero(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "a", BM25Score: 10, SemanticSim: 0.1},
		{DocID: "b", BM25Score: 5, SemanticSim: 0.9},
	}
	out := domain.CombineScores(in, 0.0)
	if out[0].DocID != "b" {
		t.Errorf("erwartet b zuerst bei alpha=0, bekam %s", out[0].DocID)
	}
}

func TestCombineScores_ClampsAlphaOutOfRange(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 1, SemanticSim: 1}}
	out := domain.CombineScores(in, 5.0)
	if out[0].FinalScore != 1.0 {
		t.Errorf("erwartet geclamptes alpha=1, bekam FinalScore=%f", out[0].FinalScore)
	}
}

func TestCombineScores_NegativeCosineClampedToZero(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 10, SemanticSim: -0.5}}
	out := domain.CombineScores(in, 0.0)
	if out[0].FinalScore != 0 {
		t.Errorf("erwartet 0 bei negativer Cosine und alpha=0, bekam %f", out[0].FinalScore)
	}
}

func TestCombineScores_EmptyMaxBM25NoDivisionByZero(t *testing.T) {
	in := []domain.HybridResult{{DocID: "a", BM25Score: 0, SemanticSim: 0.5}}
	out := domain.CombineScores(in, 0.5)
	if out[0].FinalScore < 0 {
		t.Error("erwartet keinen NaN/negativen Wert bei maxBM25=0")
	}
}

func TestCombineScores_DeterministicTieBreak(t *testing.T) {
	in := []domain.HybridResult{
		{DocID: "z", BM25Score: 5, SemanticSim: 0.5},
		{DocID: "a", BM25Score: 5, SemanticSim: 0.5},
	}
	out := domain.CombineScores(in, 0.5)
	if out[0].DocID != "a" {
		t.Errorf("erwartet deterministischen Tie-Break mit 'a' zuerst, bekam %s", out[0].DocID)
	}
}
