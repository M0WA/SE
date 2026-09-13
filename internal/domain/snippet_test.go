package domain_test

import (
	"strings"
	"testing"

	"searchengine/internal/domain"
)

func TestSnippet_HighlightsAndTruncates(t *testing.T) {
	text := strings.Repeat("Lorem ipsum dolor sitzt amet. ", 20) + "Katzen sind toll." + strings.Repeat(" filler", 20)
	snippet := domain.Snippet(text, nil, []string{"katzen"}, 100)
	if !strings.Contains(snippet, "<mark>") {
		t.Errorf("expected highlighted term in snippet, got: %s", snippet)
	}
}

func TestSnippet_NoMatchLongTextTruncates(t *testing.T) {
	text := strings.Repeat("x", 500)
	snippet := domain.Snippet(text, nil, []string{"nichtvorhanden"}, 50)
	if !strings.HasSuffix(snippet, "…") {
		t.Errorf("expected truncated snippet with ellipsis, got: %s", snippet)
	}
}

func TestSnippet_ShortTextNoMatchReturnsAsIs(t *testing.T) {
	text := "kurz"
	if got := domain.Snippet(text, nil, []string{"nichtvorhanden"}, 50); got != text {
		t.Errorf("expected %q, got %q", text, got)
	}
}

func TestSnippet_MultipleTermsHighlighted(t *testing.T) {
	snippet := domain.Snippet("Katzen und Hunde spielen zusammen im Garten heute.", nil, []string{"katzen", "hunde"}, 200)
	if strings.Count(snippet, "<mark>") != 2 {
		t.Errorf("expected 2 highlights, got: %s", snippet)
	}
}

func TestSnippet_PhraseHighlightedAsOneSpan(t *testing.T) {
	text := "This document explains the raft consensus algorithm in detail for distributed systems."
	snippet := domain.Snippet(text, []string{"raft consensus algorithm"}, []string{"raft", "consensus", "algorithm"}, 200)
	if !strings.Contains(snippet, "<mark>raft consensus algorithm</mark>") {
		t.Errorf("expected phrase highlighted as one contiguous span, got: %s", snippet)
	}
	if strings.Contains(snippet, "<mark><mark>") {
		t.Errorf("expected no nested <mark> tags from the per-word pass, got: %s", snippet)
	}
}

func TestSnippet_PhraseTakesPriorityForWindowPlacement(t *testing.T) {
	text := "raft " + strings.Repeat("filler word here. ", 20) + "the raft consensus algorithm is described here."
	snippet := domain.Snippet(text, []string{"raft consensus algorithm"}, []string{"raft"}, 200)
	if !strings.Contains(snippet, "raft consensus algorithm") {
		t.Errorf("expected excerpt window centered on the phrase match, got: %s", snippet)
	}
}

func TestSnippet_TermOutsideAnyPhraseStillHighlighted(t *testing.T) {
	text := "The raft consensus algorithm is popular; so is the paxos algorithm."
	snippet := domain.Snippet(text, []string{"raft consensus algorithm"}, []string{"paxos"}, 200)
	if !strings.Contains(snippet, "<mark>paxos</mark>") {
		t.Errorf("expected term outside the phrase span to still be highlighted, got: %s", snippet)
	}
}
