package domain

import "math"

const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// PostingStats sind die Rohdaten, die die SQL-Repository liefert.
type PostingStats struct {
	DocID     string
	TermFreq  int
	DocLength int
	DocFreq   int
	TotalDocs int
	AvgDocLen float64
}

// BM25Score berechnet den Relevanz-Score für einen einzelnen Term/Dokument-Treffer.
func BM25Score(s PostingStats) float64 {
	idf := math.Log(
		(float64(s.TotalDocs)-float64(s.DocFreq)+0.5)/(float64(s.DocFreq)+0.5) + 1,
	)
	numerator := float64(s.TermFreq) * (bm25K1 + 1)
	denominator := float64(s.TermFreq) + bm25K1*(1-bm25B+bm25B*float64(s.DocLength)/s.AvgDocLen)
	return idf * (numerator / denominator)
}

// BM25ScoreDocument summiert BM25 über alle Query-Terme für ein Dokument.
func BM25ScoreDocument(postingsPerTerm []PostingStats) float64 {
	total := 0.0
	for _, p := range postingsPerTerm {
		total += BM25Score(p)
	}
	return total
}
