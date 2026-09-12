package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNearestTerm_FindsSingleTypoWithinDistanceOne(t *testing.T) {
	vocabulary := []domain.TermStat{
		{Term: "cats", DocFreq: 5, TotalFreq: 10},
		{Term: "dogs", DocFreq: 3, TotalFreq: 6},
	}
	term, dist, found := domain.NearestTerm("catz", vocabulary, 2)
	if !found {
		t.Fatal("expected a match for 'catz'")
	}
	if term != "cats" || dist != 1 {
		t.Errorf("expected 'cats' at distance 1, got %q at distance %d", term, dist)
	}
}

func TestNearestTerm_PrefersSmallerDistance(t *testing.T) {
	// "catss" -> "cats": delete the extra trailing 's' = distance 1.
	// "catss" -> "dogss": 3 substitutions ("cat"->"dog") = distance 3, far
	// worse despite dogss's much higher frequency -- distance must win over
	// frequency, not the other way around.
	vocabulary := []domain.TermStat{
		{Term: "cats", DocFreq: 1, TotalFreq: 1},
		{Term: "dogss", DocFreq: 100, TotalFreq: 100},
	}
	term, dist, found := domain.NearestTerm("catss", vocabulary, 2)
	if !found || term != "cats" || dist != 1 {
		t.Errorf("expected 'cats' at distance 1, got %q at distance %d (found=%v)", term, dist, found)
	}
}

func TestNearestTerm_TieBreaksByHigherFrequency(t *testing.T) {
	// Both "bat" and "bad" are distance 1 from "bax".
	vocabulary := []domain.TermStat{
		{Term: "bat", DocFreq: 2, TotalFreq: 2},
		{Term: "bad", DocFreq: 9, TotalFreq: 20},
	}
	term, _, found := domain.NearestTerm("bax", vocabulary, 2)
	if !found || term != "bad" {
		t.Errorf("expected the more frequent tied term 'bad', got %q (found=%v)", term, found)
	}
}

func TestNearestTerm_NoCloseMatchReportsNotFound(t *testing.T) {
	vocabulary := []domain.TermStat{
		{Term: "elephant", DocFreq: 1, TotalFreq: 1},
		{Term: "giraffe", DocFreq: 1, TotalFreq: 1},
	}
	_, _, found := domain.NearestTerm("quixotic", vocabulary, 2)
	if found {
		t.Error("expected no match for a term with nothing close in vocabulary")
	}
}

func TestNearestTerm_NeverReturnsExactMatchToItself(t *testing.T) {
	vocabulary := []domain.TermStat{
		{Term: "cats", DocFreq: 5, TotalFreq: 5},
	}
	_, _, found := domain.NearestTerm("cats", vocabulary, 2)
	if found {
		t.Error("expected NearestTerm to never 'correct' a term to itself")
	}
}

func TestNearestTerm_RespectsMaxDistance(t *testing.T) {
	vocabulary := []domain.TermStat{
		{Term: "cats", DocFreq: 5, TotalFreq: 5},
	}
	// "cbtx" is distance 2 from "cats" (c-a-t-s -> c-b-t-x: substitute a->b, s->x).
	if _, _, found := domain.NearestTerm("cbtx", vocabulary, 1); found {
		t.Error("expected no match within maxDistance=1 for a 2-edit difference")
	}
	if _, dist, found := domain.NearestTerm("cbtx", vocabulary, 2); !found || dist != 2 {
		t.Errorf("expected a distance-2 match within maxDistance=2, got found=%v dist=%d", found, dist)
	}
}

func TestNearestTerm_EmptyVocabularyReportsNotFound(t *testing.T) {
	_, _, found := domain.NearestTerm("cats", nil, 2)
	if found {
		t.Error("expected no match against an empty vocabulary")
	}
}

func TestNearestTerm_NonPositiveMaxDistanceReportsNotFound(t *testing.T) {
	vocabulary := []domain.TermStat{{Term: "cats", DocFreq: 5, TotalFreq: 5}}
	if _, _, found := domain.NearestTerm("catz", vocabulary, 0); found {
		t.Error("expected maxDistance=0 to never match anything")
	}
}

func TestVocabularyCache_SetAndGetRoundTrip(t *testing.T) {
	c := domain.NewVocabularyCache(nil)
	terms := []domain.TermStat{{Term: "cats", DocFreq: 1, TotalFreq: 1}}
	c.Set(terms)
	got := c.Get()
	if len(got) != 1 || got[0].Term != "cats" {
		t.Errorf("expected round-tripped vocabulary, got %+v", got)
	}
}

func TestVocabularyCache_NilCacheIsNilSafe(t *testing.T) {
	var c *domain.VocabularyCache
	if got := c.Get(); got != nil {
		t.Errorf("expected nil *VocabularyCache to report an empty vocabulary, got %+v", got)
	}
	c.Set([]domain.TermStat{{Term: "cats"}}) // must not panic
}
