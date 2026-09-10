package domain_test

import (
	"strings"
	"testing"

	"searchengine/internal/domain"
)

func TestInvertedIndex_SearchRanksByRelevance(t *testing.T) {
	idx := domain.NewInvertedIndex()
	idx.Add(domain.Document{ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind Haustiere. Katzen schnurren gerne."})
	idx.Add(domain.Document{ID: "2", URL: "http://b", Title: "Hunde", Text: "Hunde sind auch Haustiere und bellen gerne."})

	results := idx.Search("Katzen", 10)
	if len(results) != 1 {
		t.Fatalf("erwartet 1 Ergebnis, bekam %d", len(results))
	}
	if results[0].URL != "http://a" {
		t.Errorf("erwartet Dokument a zuerst, bekam %s", results[0].URL)
	}
}

func TestInvertedIndex_SearchNoMatches(t *testing.T) {
	idx := domain.NewInvertedIndex()
	idx.Add(domain.Document{ID: "1", URL: "http://a", Title: "Katzen", Text: "Katzen sind Haustiere."})

	if results := idx.Search("Dinosaurier", 10); len(results) != 0 {
		t.Errorf("erwartet keine Ergebnisse, bekam %d", len(results))
	}
}

func TestInvertedIndex_SearchEmptyIndex(t *testing.T) {
	idx := domain.NewInvertedIndex()
	if results := idx.Search("irrelevant", 10); len(results) != 0 {
		t.Errorf("erwartet keine Ergebnisse bei leerem Index, bekam %v", results)
	}
}

func TestInvertedIndex_TopKLimitsResults(t *testing.T) {
	idx := domain.NewInvertedIndex()
	for i := 0; i < 5; i++ {
		idx.Add(domain.Document{
			ID: string(rune('a' + i)), URL: "http://x", Title: "Test",
			Text: strings.Repeat("gemeinsam ", 3),
		})
	}
	results := idx.Search("gemeinsam", 2)
	if len(results) != 2 {
		t.Errorf("erwartet topK=2 Ergebnisse, bekam %d", len(results))
	}
}

func TestInvertedIndex_TieBreakIsDeterministic(t *testing.T) {
	idx := domain.NewInvertedIndex()
	idx.Add(domain.Document{ID: "2", URL: "http://b", Title: "X", Text: "gemeinsam wort"})
	idx.Add(domain.Document{ID: "1", URL: "http://a", Title: "X", Text: "gemeinsam wort"})

	results := idx.Search("gemeinsam", 10)
	if len(results) != 2 || results[0].URL != "http://a" {
		t.Errorf("erwartet deterministischen Tie-Break mit a zuerst, bekam %v", results)
	}
}

func TestInvertedIndex_DocCount(t *testing.T) {
	idx := domain.NewInvertedIndex()
	if idx.DocCount() != 0 {
		t.Errorf("erwartet 0 Dokumente initial")
	}
	idx.Add(domain.Document{ID: "1", URL: "http://a", Title: "x", Text: "gemeinsam wörter hier"})
	if idx.DocCount() != 1 {
		t.Errorf("erwartet 1 Dokument nach Add")
	}
}

func TestSnippet_HighlightsAndTruncates(t *testing.T) {
	text := strings.Repeat("Lorem ipsum dolor sitzt amet. ", 20) + "Katzen sind toll." + strings.Repeat(" filler", 20)
	snippet := domain.Snippet(text, []string{"katzen"}, 100)
	if !strings.Contains(snippet, "<mark>") {
		t.Errorf("erwartet hervorgehobenen Begriff im Snippet, bekam: %s", snippet)
	}
}

func TestSnippet_NoMatchLongTextTruncates(t *testing.T) {
	text := strings.Repeat("x", 500)
	snippet := domain.Snippet(text, []string{"nichtvorhanden"}, 50)
	if !strings.HasSuffix(snippet, "…") {
		t.Errorf("erwartet abgeschnittenes Snippet mit Ellipse, bekam: %s", snippet)
	}
}

func TestSnippet_ShortTextNoMatchReturnsAsIs(t *testing.T) {
	text := "kurz"
	if got := domain.Snippet(text, []string{"nichtvorhanden"}, 50); got != text {
		t.Errorf("erwartet %q, bekam %q", text, got)
	}
}

func TestSnippet_MultipleTermsHighlighted(t *testing.T) {
	snippet := domain.Snippet("Katzen und Hunde spielen zusammen im Garten heute.", []string{"katzen", "hunde"}, 200)
	if strings.Count(snippet, "<mark>") != 2 {
		t.Errorf("erwartet 2 Highlights, bekam: %s", snippet)
	}
}
