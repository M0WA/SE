package domain

import "math"

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
