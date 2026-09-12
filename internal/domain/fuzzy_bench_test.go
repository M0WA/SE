package domain_test

import (
	"fmt"
	"math/rand"
	"testing"

	"searchengine/internal/domain"
)

// buildSyntheticVocabulary generates n distinct pseudo-word terms (3-10
// lowercase letters), each with a plausible Zipf-ish frequency skew (a few
// very common terms, many rarer ones), mirroring what a real crawl's
// vocabulary looks like -- deterministically seeded so runs are comparable.
func buildSyntheticVocabulary(n int) []domain.TermStat {
	rng := rand.New(rand.NewSource(42))
	const letters = "abcdefghijklmnopqrstuvwxyz"
	seen := make(map[string]bool, n)
	terms := make([]domain.TermStat, 0, n)
	for len(terms) < n {
		length := 3 + rng.Intn(8) // 3..10 letters
		b := make([]byte, length)
		for i := range b {
			b[i] = letters[rng.Intn(len(letters))]
		}
		term := string(b)
		if seen[term] {
			continue
		}
		seen[term] = true
		// TotalFreq skewed via an inverse-rank-like draw: most terms rare,
		// a handful common -- close enough to a real corpus's long tail
		// for this benchmark's purpose (tie-breaking cost, not exact
		// distribution fidelity).
		totalFreq := 1 + rng.Intn(1+rng.Intn(500))
		docFreq := 1 + rng.Intn(1+totalFreq/2)
		terms = append(terms, domain.TermStat{Term: term, DocFreq: docFreq, TotalFreq: totalFreq})
	}
	return terms
}

// BenchmarkNearestTerm measures the fuzzy-lookup cost -- a full vocabulary
// scan with a cheap length-bound prefilter before any Levenshtein DP (see
// NearestTerm's doc comment) -- against realistically-sized vocabularies
// (a few thousand to tens of thousands of terms), the range a real crawled
// corpus's distinct-term count falls into. This runs once per query term
// that had zero postings hits (see hybridSearchService.Search's
// fuzzy-correction path), so its cost directly bounds how much latency
// fuzzy correction can add to a search request.
func BenchmarkNearestTerm(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		vocab := buildSyntheticVocabulary(n)
		b.Run(fmt.Sprintf("VocabSize=%d/CloseTypo", n), func(b *testing.B) {
			// A one-letter substitution away from a real vocabulary entry --
			// the common case this feature exists for.
			target := vocab[n/2].Term
			typo := []byte(target)
			typo[0] = 'z'
			if typo[0] == target[0] {
				typo[0] = 'q'
			}
			misspelled := string(typo)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, found := domain.NearestTerm(misspelled, vocab, 2)
				if !found {
					b.Fatal("expected a near match to be found")
				}
			}
		})

		b.Run(fmt.Sprintf("VocabSize=%d/NoMatch", n), func(b *testing.B) {
			// A target far (length alone rules most terms out immediately
			// via NearestTerm's cheap length-difference prefilter, but every
			// vocabulary entry still costs at least that comparison) from
			// anything in vocabulary, exercising the worst case where no
			// early return from a match ever short-circuits the scan.
			target := "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				domain.NearestTerm(target, vocab, 2)
			}
		})
	}
}
