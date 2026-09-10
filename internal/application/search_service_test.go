package application_test

import (
	"context"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
)

type fakeIndexer struct {
	addCalls    []domain.Document
	searchQuery string
	searchTopK  int
	results     []domain.SearchResult
	docCount    int
}

func (f *fakeIndexer) Add(doc domain.Document) { f.addCalls = append(f.addCalls, doc) }
func (f *fakeIndexer) Search(query string, topK int) []domain.SearchResult {
	f.searchQuery, f.searchTopK = query, topK
	return f.results
}
func (f *fakeIndexer) DocCount() int { return f.docCount }

func TestSearchService_Search_ReturnsIndexResults(t *testing.T) {
	idx := &fakeIndexer{results: []domain.SearchResult{{URL: "http://a", Score: 1.2}}}
	svc := application.NewSearchService(idx)

	results, err := svc.Search(context.Background(), "katzen", 5)
	if err != nil {
		t.Fatalf("unerwarteter Fehler: %v", err)
	}
	if len(results) != 1 || results[0].URL != "http://a" {
		t.Errorf("unerwartete Ergebnisse: %v", results)
	}
	if idx.searchQuery != "katzen" || idx.searchTopK != 5 {
		t.Errorf("Index mit falschen Argumenten aufgerufen: %q %d", idx.searchQuery, idx.searchTopK)
	}
}

func TestSearchService_Search_EmptyQueryRejected(t *testing.T) {
	svc := application.NewSearchService(&fakeIndexer{})
	_, err := svc.Search(context.Background(), "   ", 5)
	if err != application.ErrEmptyQuery {
		t.Errorf("erwartet ErrEmptyQuery, bekam %v", err)
	}
}

func TestSearchService_Search_DefaultsTopK(t *testing.T) {
	idx := &fakeIndexer{}
	svc := application.NewSearchService(idx)
	_, _ = svc.Search(context.Background(), "katzen", 0)
	if idx.searchTopK != 10 {
		t.Errorf("erwartet Standard topK=10, bekam %d", idx.searchTopK)
	}
}
