package domain

import "testing"

func TestMoreFrequent_HigherTotalFreqWins(t *testing.T) {
	a := TermStat{Term: "a", TotalFreq: 10, DocFreq: 1}
	b := TermStat{Term: "b", TotalFreq: 5, DocFreq: 1}
	if !moreFrequent(a, b) {
		t.Error("expected higher TotalFreq to win")
	}
	if moreFrequent(b, a) {
		t.Error("expected lower TotalFreq to lose")
	}
}

func TestMoreFrequent_TiedTotalFreqFallsBackToDocFreq(t *testing.T) {
	a := TermStat{Term: "a", TotalFreq: 5, DocFreq: 3}
	b := TermStat{Term: "b", TotalFreq: 5, DocFreq: 1}
	if !moreFrequent(a, b) {
		t.Error("expected higher DocFreq to win when TotalFreq ties")
	}
	if moreFrequent(b, a) {
		t.Error("expected lower DocFreq to lose when TotalFreq ties")
	}
}

func TestMoreFrequent_TiedFrequenciesFallBackToLexicographicOrder(t *testing.T) {
	a := TermStat{Term: "aardvark", TotalFreq: 5, DocFreq: 3}
	b := TermStat{Term: "zebra", TotalFreq: 5, DocFreq: 3}
	if !moreFrequent(a, b) {
		t.Error("expected the lexicographically earlier term to win when frequencies tie")
	}
	if moreFrequent(b, a) {
		t.Error("expected the lexicographically later term to lose when frequencies tie")
	}
}

func TestLevenshtein_EmptyFirstArgReturnsSecondLength(t *testing.T) {
	if got := levenshtein("", "cats"); got != 4 {
		t.Errorf("expected 4, got %d", got)
	}
}

func TestLevenshtein_EmptySecondArgReturnsFirstLength(t *testing.T) {
	if got := levenshtein("cats", ""); got != 4 {
		t.Errorf("expected 4, got %d", got)
	}
}

func TestLevenshtein_BothEmptyIsZero(t *testing.T) {
	if got := levenshtein("", ""); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestLevenshtein_IdenticalStringsIsZero(t *testing.T) {
	if got := levenshtein("widgets", "widgets"); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestLevenshtein_SingleSubstitution(t *testing.T) {
	if got := levenshtein("cats", "cots"); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}
