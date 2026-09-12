package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestCorpusStatsCache_NilGetReturnsEmptyCorpusDefaults(t *testing.T) {
	var c *domain.CorpusStatsCache
	totalDocs, avgDocLen := c.Get()
	if totalDocs != 0 || avgDocLen != 1 {
		t.Errorf("expected (0, 1) from a nil receiver, got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestCorpusStatsCache_NilSetIsNoop(t *testing.T) {
	var c *domain.CorpusStatsCache
	c.Set(100, 42) // must not panic
}

func TestCorpusStatsCache_SetAndGetRoundTrip(t *testing.T) {
	c := domain.NewCorpusStatsCache(0, 1)
	c.Set(50, 123.5)
	totalDocs, avgDocLen := c.Get()
	if totalDocs != 50 || avgDocLen != 123.5 {
		t.Errorf("expected (50, 123.5), got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestCorpusStatsCache_NewSeedsInitialValue(t *testing.T) {
	c := domain.NewCorpusStatsCache(7, 33.3)
	totalDocs, avgDocLen := c.Get()
	if totalDocs != 7 || avgDocLen != 33.3 {
		t.Errorf("expected the constructor's initial snapshot, got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestCorpusStatsCache_SetFloorsNonPositiveAvgDocLenAtOne(t *testing.T) {
	c := domain.NewCorpusStatsCache(0, 1)
	c.Set(0, 0)
	if _, avgDocLen := c.Get(); avgDocLen != 1 {
		t.Errorf("expected a zero avgDocLen to be floored at 1, got %v", avgDocLen)
	}
	c.Set(0, -5)
	if _, avgDocLen := c.Get(); avgDocLen != 1 {
		t.Errorf("expected a negative avgDocLen to be floored at 1, got %v", avgDocLen)
	}
}
