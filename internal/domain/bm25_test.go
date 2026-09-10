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

	if domain.BM25Score(high) <= domain.BM25Score(low) {
		t.Error("expected higher score for higher term frequency")
	}
}

func TestBM25Score_RareTermScoresHigherThanCommon(t *testing.T) {
	rare := domain.PostingStats{TermFreq: 2, DocLength: 100, DocFreq: 2, TotalDocs: 1000, AvgDocLen: 100}
	common := domain.PostingStats{TermFreq: 2, DocLength: 100, DocFreq: 800, TotalDocs: 1000, AvgDocLen: 100}

	if domain.BM25Score(rare) <= domain.BM25Score(common) {
		t.Error("expected higher score for rare term (higher IDF)")
	}
}

func TestBM25Score_LongerDocPenalizedRelativeToAvg(t *testing.T) {
	short := domain.PostingStats{TermFreq: 3, DocLength: 50, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100}
	long := domain.PostingStats{TermFreq: 3, DocLength: 500, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100}

	if domain.BM25Score(long) >= domain.BM25Score(short) {
		t.Error("expected length normalization: very long document should score lower")
	}
}

func TestBM25ScoreDocument_SumsAcrossTerms(t *testing.T) {
	stats := []domain.PostingStats{
		{TermFreq: 2, DocLength: 100, DocFreq: 10, TotalDocs: 1000, AvgDocLen: 100},
		{TermFreq: 1, DocLength: 100, DocFreq: 50, TotalDocs: 1000, AvgDocLen: 100},
	}
	sum := domain.BM25ScoreDocument(stats)
	single := domain.BM25Score(stats[0])
	if sum <= single {
		t.Error("expected sum over multiple terms to be greater than a single score")
	}
}

func TestBM25Score_ZeroDocFreqNoNaN(t *testing.T) {
	s := domain.PostingStats{TermFreq: 1, DocLength: 10, DocFreq: 0, TotalDocs: 100, AvgDocLen: 10}
	if math.IsNaN(domain.BM25Score(s)) || math.IsInf(domain.BM25Score(s), 0) {
		t.Error("expected finite score even with df=0")
	}
}
