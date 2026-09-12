package domain

import (
	"sort"
	"time"
)

// CorrectedTerm records that a query term had zero postings hits and was
// fuzzy-matched to a near-miss vocabulary term (within a bounded edit
// distance) for BM25 scoring purposes -- see hybridSearchService.Search and
// domain.NearestTerm. Surfaced to callers/UI so a correction is always shown
// transparently rather than silently rewriting the displayed query.
type CorrectedTerm struct {
	Original  string `json:"original"`
	Corrected string `json:"corrected"`
}

// HybridResult is a ranked match with a broken-down score. CrawledAt is
// populated only when the caller needs it for recency sorting -- it's the
// zero time otherwise. CorrectedTerms is the same for every result of a
// given Search call (it describes the query, not this particular
// document) -- empty when fuzzy correction is disabled, or when every query
// term either matched something or had no close-enough vocabulary term to
// substitute.
type HybridResult struct {
	DocID       string
	URL         string
	Title       string
	Snippet     string
	BM25Score   float64
	SemanticSim float64
	// PageRank is this document's raw (unnormalized) link-authority score
	// (see domain.PageRank and sqlrepo's documents.pagerank column) --
	// carried alongside the other components so hybridSearchService.Search
	// can normalize it against the candidate batch's own max and blend it
	// into FinalScore. Not itself part of the wire-format admin debug view;
	// see FinalScore for the blended result.
	PageRank       float64
	FinalScore     float64
	CrawledAt      time.Time
	CorrectedTerms []CorrectedTerm
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

// SortByCrawledAt orders results by descending CrawledAt (most recently
// crawled first), ignoring BM25Score/SemanticSim/FinalScore entirely --
// ties (including two zero CrawledAt values, from documents the caller
// never populated it for) are broken by DocID for a deterministic order.
func SortByCrawledAt(results []HybridResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].CrawledAt.Equal(results[j].CrawledAt) {
			return results[i].DocID < results[j].DocID
		}
		return results[i].CrawledAt.After(results[j].CrawledAt)
	})
}
