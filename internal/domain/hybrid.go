package domain

import (
	"sort"
	"time"
)

// CorrectedTerm records that a query term had zero postings hits and was
// fuzzy-matched (NearestTerm) to a near-miss vocabulary term for BM25
// scoring. Surfaced to the UI so a correction is shown, not silent.
type CorrectedTerm struct {
	Original  string `json:"original"`
	Corrected string `json:"corrected"`
}

// HybridResult is a ranked match with a broken-down score. CrawledAt is
// the zero time unless recency sorting needs it. CorrectedTerms describes
// the query, not this document -- identical across one Search call's results.
type HybridResult struct {
	DocID     string
	URL       string
	Title     string
	Snippet   string
	BM25Score float64
	// NormBM25 is BM25Score normalized against this batch's max (set by
	// CombineScores) -- the actual fraction blended into FinalScore.
	NormBM25    float64
	SemanticSim float64
	// PageRank is this document's raw link-authority score, carried so
	// Search can normalize and blend it into FinalScore.
	PageRank float64
	// NormalizedPageRank is PageRank normalized against this batch's max --
	// the value actually blended into FinalScore. Zero when PageRankWeight
	// is 0 or every candidate has zero PageRank.
	NormalizedPageRank float64
	FinalScore         float64
	CrawledAt          time.Time
	CorrectedTerms     []CorrectedTerm
	// BM25Terms breaks BM25Score down by query term -- empty for a purely
	// semantic match. Populated only for the admin debug view, not scoring.
	BM25Terms []TermScore
	// Alpha, K1, B and PageRankWeight are the tuning parameters that
	// produced this result -- identical across one Search call's results,
	// carried so the admin debug view can show them without a round trip.
	Alpha          float64
	K1             float64
	B              float64
	PageRankWeight float64
}

// CombineScores blends BM25 and cosine similarity into a final score.
// alpha=1 -> pure BM25, alpha=0 -> pure semantic.
func CombineScores(candidates []HybridResult, alpha float64) []HybridResult {
	alpha = clamp(alpha, 0, 1)

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
		result[i].NormBM25 = normBM25
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
// by DocID. Exported so callers that adjust FinalScore after CombineScores
// can restore this invariant.
func SortByFinalScore(results []HybridResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].FinalScore == results[j].FinalScore {
			return results[i].DocID < results[j].DocID
		}
		return results[i].FinalScore > results[j].FinalScore
	})
}
