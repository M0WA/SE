package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNewScheduledCrawlID_ReturnsDistinctIDs(t *testing.T) {
	a := domain.NewScheduledCrawlID()
	b := domain.NewScheduledCrawlID()
	if a == b {
		t.Errorf("expected distinct scheduled crawl IDs, got %q twice", a)
	}
	if a == "" || b == "" {
		t.Error("expected non-empty scheduled crawl IDs")
	}
}
