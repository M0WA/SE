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

func TestSnippet_EscapesHTMLInMatchedWindow(t *testing.T) {
	text := `Great widgets <img src=x onerror=alert(1)> for sale, buy katzen today.`
	snippet := domain.Snippet(text, nil, []string{"katzen"}, 200)
	if strings.Contains(snippet, "<img") {
		t.Errorf("expected crawled markup to be escaped, got: %s", snippet)
	}
	if !strings.Contains(snippet, "&lt;img") {
		t.Errorf("expected escaped &lt;img in snippet, got: %s", snippet)
	}
	if !strings.Contains(snippet, "<mark>katzen</mark>") {
		t.Errorf("expected the real term still highlighted via a literal <mark> tag, got: %s", snippet)
	}
}

func TestSnippet_EscapesHTMLNoMatchShortText(t *testing.T) {
	text := `<script>alert(1)</script>`
	got := domain.Snippet(text, nil, []string{"nichtvorhanden"}, 200)
	if strings.Contains(got, "<script>") {
		t.Errorf("expected script tag escaped, got: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected &lt;script&gt; in output, got: %s", got)
	}
}

func TestSnippet_EscapesHTMLNoMatchLongTextTruncated(t *testing.T) {
	text := "<script>" + strings.Repeat("x", 500)
	got := domain.Snippet(text, nil, []string{"nichtvorhanden"}, 50)
	if strings.Contains(got, "<script>") {
		t.Errorf("expected script tag escaped in truncated output, got: %s", got)
	}
	if !strings.HasPrefix(got, "&lt;script&gt;") {
		t.Errorf("expected escaped prefix, got: %s", got)
	}
}
