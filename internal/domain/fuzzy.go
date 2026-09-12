package domain

// levenshtein computes the classic edit distance between a and b: the
// minimum number of single-character insertions, deletions, or
// substitutions needed to turn a into b. Pure, DB-agnostic, no external
// dependency -- brute-force Wagner-Fischer DP is more than fast enough at
// the vocabulary sizes this runs against (see NearestTerm).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	// Two-row rolling DP: prev/cur hold the edit distance between a
	// prefix of a and a prefix of b, one row of the classic matrix at a
	// time, rather than the full O(len(a)*len(b)) matrix.
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			min := del
			if ins < min {
				min = ins
			}
			if sub < min {
				min = sub
			}
			cur[j] = min
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// NearestTerm searches vocabulary for the closest term to target within
// maxDistance Levenshtein edits (see levenshtein), for substituting a query
// term that had zero postings hits into BM25 scoring instead (see
// hybridSearchService.Search's fuzzy-correction path). Ties -- same edit
// distance -- are broken by preferring the more frequent term (higher
// TotalFreq, then higher DocFreq, then lexicographically first) so the
// choice is deterministic. Reports found=false when nothing in vocabulary
// is within maxDistance, including when vocabulary is empty or maxDistance
// is non-positive. target itself is never returned even if present in
// vocabulary (distance 0): a term this is called for, by construction, had
// zero postings hits, so "correcting" it to itself would be a no-op.
func NearestTerm(target string, vocabulary []TermStat, maxDistance int) (term string, distance int, found bool) {
	if maxDistance <= 0 {
		return "", 0, false
	}
	bestDist := maxDistance + 1
	var best TermStat
	for _, v := range vocabulary {
		if v.Term == target {
			continue
		}
		// A cheap lower bound on Levenshtein distance is the difference in
		// length -- skip the full DP whenever that alone already rules the
		// term out (guaranteed distance > maxDistance), rather than running
		// Wagner-Fischer for every vocabulary entry regardless of how
		// obviously distant it is.
		if lenDiff := len(v.Term) - len(target); lenDiff > maxDistance || -lenDiff > maxDistance {
			continue
		}
		d := levenshtein(target, v.Term)
		if d > maxDistance {
			continue
		}
		if d < bestDist || (d == bestDist && moreFrequent(v, best)) {
			bestDist = d
			best = v
			found = true
		}
	}
	if !found {
		return "", 0, false
	}
	return best.Term, bestDist, true
}

// moreFrequent reports whether candidate should be preferred over current
// as the more frequent term: higher TotalFreq wins, ties broken by higher
// DocFreq, final tie broken lexicographically for a fully deterministic
// choice regardless of vocabulary iteration order.
func moreFrequent(candidate, current TermStat) bool {
	if candidate.TotalFreq != current.TotalFreq {
		return candidate.TotalFreq > current.TotalFreq
	}
	if candidate.DocFreq != current.DocFreq {
		return candidate.DocFreq > current.DocFreq
	}
	return candidate.Term < current.Term
}
