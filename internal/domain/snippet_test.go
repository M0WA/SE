package domain_test

import (
	"strings"
	"testing"

	"searchengine/internal/domain"
)

func TestSnippet_HighlightsAndTruncates(t *testing.T) {
	text := strings.Repeat("Lorem ipsum dolor sitzt amet. ", 20) + "Katzen sind toll." + strings.Repeat(" filler", 20)
	snippet := domain.Snippet(text, []string{"katzen"}, 100)
	if !strings.Contains(snippet, "<mark>") {
		t.Errorf("expected highlighted term in snippet, got: %s", snippet)
	}
}

func TestSnippet_NoMatchLongTextTruncates(t *testing.T) {
	text := strings.Repeat("x", 500)
	snippet := domain.Snippet(text, []string{"nichtvorhanden"}, 50)
	if !strings.HasSuffix(snippet, "…") {
		t.Errorf("expected truncated snippet with ellipsis, got: %s", snippet)
	}
}

func TestSnippet_ShortTextNoMatchReturnsAsIs(t *testing.T) {
	text := "kurz"
	if got := domain.Snippet(text, []string{"nichtvorhanden"}, 50); got != text {
		t.Errorf("expected %q, got %q", text, got)
	}
}

func TestSnippet_MultipleTermsHighlighted(t *testing.T) {
	snippet := domain.Snippet("Katzen und Hunde spielen zusammen im Garten heute.", []string{"katzen", "hunde"}, 200)
	if strings.Count(snippet, "<mark>") != 2 {
		t.Errorf("expected 2 highlights, got: %s", snippet)
	}
}
