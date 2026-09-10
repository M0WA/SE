package domain_test

import (
	"math"
	"testing"

	"searchengine/internal/domain"
)

func TestBM25Score_HigherTermFreqScoresHigher(t *testing.T) {
	base := domain.PostingStats{DocLength: 100, DocFreq: 5, TotalDocs: 1000, AvgDocLen: 100}
	low := base
	low.TermFreq = 1
	high := base
	high.TermFreq = 10

	if domain.BM25Score(high, domain.DefaultBM25K1, domain.DefaultBM25B) <= domain.BM25Score(low, domain.DefaultBM25K1, domain.DefaultBM25B) {
		t.Error("expected higher score for higher term frequency")
	}
}

func TestBM25Score_RareTermScoresHigherThanCommon(t *testing.T) {
	rare := domain.PostingStats{TermFreq: 2, DocLength: 100, DocFreq: 2, TotalDocs: 1000, AvgDocLen: 100}
	common := domain.PostingStats{TermFreq: 2, DocLength: 100, DocFreq: 800, TotalDocs: 1000, AvgDocLen: 100}

	if domain.BM25Score(rare, domain.DefaultBM25K1, domain.DefaultBM25B) <= domain.BM25Score(common, domain.DefaultBM25K1, domain.DefaultBM25B) {
		t.Error("expected higher score for rare term (higher IDF)")
	}
}

func TestBM25Score_LongerDocPenalizedRelativeToAvg(t *testing.T) {
	short := domain.PostingStats{TermFreq: 3, DocLength: 50, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100}
	long := domain.PostingStats{TermFreq: 3, DocLength: 500, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100}

	if domain.BM25Score(long, domain.DefaultBM25K1, domain.DefaultBM25B) >= domain.BM25Score(short, domain.DefaultBM25K1, domain.DefaultBM25B) {
		t.Error("expected length normalization: very long document should score lower")
	}
}

func TestBM25Score_HigherBIncreasesLengthPenalty(t *testing.T) {
	long := domain.PostingStats{TermFreq: 3, DocLength: 500, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100}

	lowB := domain.BM25Score(long, domain.DefaultBM25K1, 0.1)
	highB := domain.BM25Score(long, domain.DefaultBM25K1, 1.0)
	if highB >= lowB {
		t.Error("expected a higher b to penalize long documents more")
	}
}

func TestBM25ScoreDocument_SumsAcrossTerms(t *testing.T) {
	stats := []domain.PostingStats{
		{TermFreq: 2, DocLength: 100, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100},
		{TermFreq: 1, DocLength: 100, DocFreq: 50, TotalDocs: 1000, AvgDocLen: 100},
	}
	sum := domain.BM25ScoreDocument(stats, domain.DefaultBM25K1, domain.DefaultBM25B)
	single := domain.BM25Score(stats[0], domain.DefaultBM25K1, domain.DefaultBM25B)
	if sum <= single {
		t.Error("expected sum over multiple terms to be greater than a single score")
	}
}

func TestBM25Score_ZeroDocFreqNoNaN(t *testing.T) {
	s := domain.PostingStats{TermFreq: 1, DocLength: 10, DocFreq: 0, TotalDocs: 100, AvgDocLen: 10}
	score := domain.BM25Score(s, domain.DefaultBM25K1, domain.DefaultBM25B)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		t.Error("expected finite score even with df=0")
	}
}
