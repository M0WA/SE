package domain

import "sort"

// HybridResult ist ein gerankter Treffer mit aufgeschlüsseltem Score.
type HybridResult struct {
	DocID       string
	URL         string
	Title       string
	Snippet     string
	BM25Score   float64
	SemanticSim float64
	FinalScore  float64
}

// CombineScores mischt BM25 und Cosine-Similarity zu einem finalen Score.
// alpha=1 -> reines BM25, alpha=0 -> rein semantisch.
func CombineScores(candidates []HybridResult, alpha float64) []HybridResult {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}

	maxBM25 := 0.0
	for _, c := range candidates {
		if c.BM25Score > maxBM25 {
			maxBM25 = c.BM25Score
		}
	}

	result := make([]HybridResult, len(candidates))
	copy(result, candidates)
	for i := range result {
		normBM25 := 0.0
		if maxBM25 > 0 {
			normBM25 = result[i].BM25Score / maxBM25
		}
		semantic := result[i].SemanticSim
		if semantic < 0 {
			semantic = 0
		}
		result[i].FinalScore = alpha*normBM25 + (1-alpha)*semantic
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].FinalScore == result[j].FinalScore {
			return result[i].DocID < result[j].DocID
		}
		return result[i].FinalScore > result[j].FinalScore
	})
	return result
}
