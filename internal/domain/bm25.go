package domain

import (
	"math"
	"sort"
)

const (
	DefaultBM25K1 = 1.2
	DefaultBM25B  = 0.75
)

// PostingStats is the raw data returned by the SQL repository.
type PostingStats struct {
	DocID     string
	TermFreq  int
	DocLength int
	DocFreq   int
	TotalDocs int
	AvgDocLen float64
}

// BM25Score computes the relevance score for a single term/document match.
// k1 controls term-frequency saturation, b controls length normalization
// strength -- both tunable at runtime via TuningSettings.
func BM25Score(s PostingStats, k1, b float64) float64 {
	idf := math.Log(
		(float64(s.TotalDocs)-float64(s.DocFreq)+0.5)/(float64(s.DocFreq)+0.5) + 1,
	)
	numerator := float64(s.TermFreq) * (k1 + 1)
	denominator := float64(s.TermFreq) + k1*(1-b+b*float64(s.DocLength)/s.AvgDocLen)
	return idf * (numerator / denominator)
}

// BM25ScoreDocument sums BM25 over all query terms for a document.
func BM25ScoreDocument(postingsPerTerm []PostingStats, k1, b float64) float64 {
	total := 0.0
	for _, p := range postingsPerTerm {
		total += BM25Score(p, k1, b)
	}
	return total
}

// TermStat is a single term's aggregate stats across the whole corpus, used
// by the admin vocabulary diagnostics.
type TermStat struct {
	Term      string
	DocFreq   int
	TotalFreq int
}

// TermScore is one query term's BM25 contribution to a specific document --
// the postings stats that produced it plus the resulting score -- for the
// admin debug view, which wants to show which terms actually drove a
// result's BM25Score rather than only its summed total.
type TermScore struct {
	Term      string
	TermFreq  int
	DocFreq   int
	DocLength int
	Score     float64
}

// BM25TermScores is BM25ScoreDocument's per-term counterpart, for admin
// diagnostics: the same total score, split by which term contributed how
// much. terms and postingsPerTerm must be parallel slices (same length,
// same index order) -- the shape hybridSearchService.Search already builds
// them in. Sorted by descending score so the strongest contributor leads.
func BM25TermScores(terms []string, postingsPerTerm []PostingStats, k1, b float64) []TermScore {
	if len(terms) == 0 {
		return nil
	}
	out := make([]TermScore, len(terms))
	for i, p := range postingsPerTerm {
		out[i] = TermScore{
			Term: terms[i], TermFreq: p.TermFreq, DocFreq: p.DocFreq, DocLength: p.DocLength,
			Score: BM25Score(p, k1, b),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
