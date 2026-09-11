package domain_test

import (
	"testing"
	"time"

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

func TestSortByCrawledAt_OrdersMostRecentFirstIgnoringScore(t *testing.T) {
	now := time.Now()
	in := []domain.HybridResult{
		{DocID: "old", FinalScore: 0.9, CrawledAt: now.Add(-48 * time.Hour)},
		{DocID: "new", FinalScore: 0.1, CrawledAt: now},
		{DocID: "mid", FinalScore: 0.5, CrawledAt: now.Add(-24 * time.Hour)},
	}
	domain.SortByCrawledAt(in)
	got := []string{in[0].DocID, in[1].DocID, in[2].DocID}
	want := []string{"new", "mid", "old"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
}

func TestSortByCrawledAt_DeterministicTieBreak(t *testing.T) {
	same := time.Now()
	in := []domain.HybridResult{
		{DocID: "z", CrawledAt: same},
		{DocID: "a", CrawledAt: same},
	}
	domain.SortByCrawledAt(in)
	if in[0].DocID != "a" {
		t.Errorf("expected deterministic tie-break with 'a' first, got %s", in[0].DocID)
	}
}
