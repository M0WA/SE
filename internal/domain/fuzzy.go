package domain

// levenshtein computes the classic edit distance between a and b. Pure,
// no external dependency -- brute-force Wagner-Fischer DP is fast enough
// at the vocabulary sizes this runs against (see NearestTerm).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	// Two-row rolling DP instead of the full O(len(a)*len(b)) matrix.
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
			best := del
			if ins < best {
				best = ins
			}
			if sub < best {
				best = sub
			}
			cur[j] = best
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// NearestTerm searches vocabulary for the closest term to target within
// maxDistance Levenshtein edits, to substitute for a zero-hit query term in
// BM25 scoring. Ties go to the more frequent term. target is never returned.
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
		// Length difference is a cheap lower bound -- skip the full DP
		// when it alone already rules the term out.
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

// moreFrequent prefers higher TotalFreq, then higher DocFreq, then
// lexicographic order, for a deterministic choice.
func moreFrequent(candidate, current TermStat) bool {
	if candidate.TotalFreq != current.TotalFreq {
		return candidate.TotalFreq > current.TotalFreq
	}
	if candidate.DocFreq != current.DocFreq {
		return candidate.DocFreq > current.DocFreq
	}
	return candidate.Term < current.Term
}
