package domain

import "sort"

// HybridResult is a ranked match with a broken-down score.
type HybridResult struct {
	DocID       string
	URL         string
	Title       string
	Snippet     string
	BM25Score   float64
	SemanticSim float64
	FinalScore  float64
}

// CombineScores blends BM25 and cosine similarity into a final score.
// alpha=1 -> pure BM25, alpha=0 -> pure semantic.
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

	SortByFinalScore(result)
	return result
}

// SortByFinalScore orders results by descending FinalScore, breaking ties
// by DocID for a deterministic order. Exported so callers that adjust
// FinalScore after CombineScores (a boost multiplier, say) can restore
// this invariant without duplicating the tie-break rule.
func SortByFinalScore(results []HybridResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].FinalScore == results[j].FinalScore {
			return results[i].DocID < results[j].DocID
		}
		return results[i].FinalScore > results[j].FinalScore
	})
}
