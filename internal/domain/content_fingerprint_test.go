package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestContentHash_IdenticalTextsMatch(t *testing.T) {
	a := domain.ContentHash("Hello World")
	b := domain.ContentHash("Hello World")
	if a != b {
		t.Errorf("expected identical text to hash identically, got %q vs %q", a, b)
	}
}

func TestContentHash_CaseInsensitive(t *testing.T) {
	a := domain.ContentHash("Hello World")
	b := domain.ContentHash("hello world")
	if a != b {
		t.Errorf("expected case-insensitive match, got %q vs %q", a, b)
	}
}

func TestContentHash_DifferentTextsDiffer(t *testing.T) {
	a := domain.ContentHash("Hello World")
	b := domain.ContentHash("Goodbye World")
	if a == b {
		t.Error("expected different text to hash differently")
	}
}

func TestSimHash64_IdenticalTextsMatchExactly(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog many times today"
	a := domain.SimHash64(text)
	b := domain.SimHash64(text)
	if a != b {
		t.Errorf("expected identical text to produce identical simhash, got %x vs %x", a, b)
	}
	if domain.HammingDistance64(a, b) != 0 {
		t.Error("expected zero Hamming distance for identical text")
	}
}

// The core near-duplicate property: two texts differing by only a couple
// of words should stay within a small Hamming distance, unlike ContentHash
// (an exact hash) which would differ completely for the same inputs.
func TestSimHash64_NearDuplicateTextsStayClose(t *testing.T) {
	original := "the quick brown fox jumps over the lazy dog while the sun sets slowly over the hill today"
	edited := "the quick brown fox jumps over the lazy dog while the sun sets slowly over the hill yesterday"
	a := domain.SimHash64(original)
	b := domain.SimHash64(edited)
	dist := domain.HammingDistance64(a, b)
	if dist > 10 {
		t.Errorf("expected a near-duplicate (one word changed) to stay within a small Hamming distance, got %d", dist)
	}
	if domain.ContentHash(original) == domain.ContentHash(edited) {
		t.Error("expected ContentHash (exact match) to differ for texts that aren't byte-identical")
	}
}

func TestSimHash64_UnrelatedTextsDivergeSubstantially(t *testing.T) {
	a := domain.SimHash64("the quick brown fox jumps over the lazy dog repeatedly during the warm afternoon")
	b := domain.SimHash64("stock markets fell sharply today amid concerns about rising interest rates globally")
	dist := domain.HammingDistance64(a, b)
	if dist < 15 {
		t.Errorf("expected unrelated texts to diverge substantially, got Hamming distance %d", dist)
	}
}

func TestSimHash64_EmptyTextReturnsZero(t *testing.T) {
	if got := domain.SimHash64(""); got != 0 {
		t.Errorf("expected 0 for empty text, got %x", got)
	}
}

func TestSimHash64_ShorterThanShingleSizeStillHashes(t *testing.T) {
	// Fewer words than the shingle width (3) must not panic or always
	// return 0 -- a very short page is still a real document.
	got := domain.SimHash64("hello world")
	if got == 0 {
		t.Error("expected a non-zero fingerprint for short (sub-shingle-length) text")
	}
}

func TestEncodeDecodeSimHash64_RoundTrips(t *testing.T) {
	want := domain.SimHash64("a fairly ordinary sentence used only to produce a representative fingerprint")
	encoded := domain.EncodeSimHash64(want)
	if len(encoded) != 16 {
		t.Errorf("expected a 16-hex-character encoding, got %q (%d chars)", encoded, len(encoded))
	}
	if got := domain.DecodeSimHash64(encoded); got != want {
		t.Errorf("expected round-trip to recover the original value, got %x, want %x", got, want)
	}
}

func TestDecodeSimHash64_MalformedInputReturnsZero(t *testing.T) {
	if got := domain.DecodeSimHash64("not-hex"); got != 0 {
		t.Errorf("expected 0 for malformed input, got %x", got)
	}
	if got := domain.DecodeSimHash64("ab"); got != 0 {
		t.Errorf("expected 0 for too-short input, got %x", got)
	}
}

func TestHammingDistance64_SelfIsZero(t *testing.T) {
	h := domain.SimHash64("some representative text for this test")
	if domain.HammingDistance64(h, h) != 0 {
		t.Error("expected zero distance from a value to itself")
	}
}

func TestHammingDistance64_SingleBitFlip(t *testing.T) {
	var a uint64 = 0b1010
	b := a ^ 0b0001
	if domain.HammingDistance64(a, b) != 1 {
		t.Errorf("expected distance 1 for a single bit flip, got %d", domain.HammingDistance64(a, b))
	}
}
