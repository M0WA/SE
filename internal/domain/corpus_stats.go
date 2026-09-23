package domain

import "sync"

// CorpusStatsCache holds the corpus-wide stats BM25 scoring needs (doc
// count, average doc length), refreshed on a timer rather than recomputed
// per search. Safe for concurrent use.
type CorpusStatsCache struct {
	mu        sync.RWMutex
	totalDocs int
	avgDocLen float64
}

// NewCorpusStatsCache seeds the cache with an initial snapshot so a request
// handled before the first background refresh sees a real value.
func NewCorpusStatsCache(totalDocs int, avgDocLen float64) *CorpusStatsCache {
	c := &CorpusStatsCache{}
	c.Set(totalDocs, avgDocLen)
	return c
}

// Get returns the most recently cached (totalDocs, avgDocLen). A nil
// *CorpusStatsCache (e.g. a test that doesn't care about corpus size)
// reports an empty corpus rather than panicking.
func (c *CorpusStatsCache) Get() (totalDocs int, avgDocLen float64) {
	if c == nil {
		return 0, 1
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalDocs, c.avgDocLen
}

// Set replaces the cached snapshot. avgDocLen is floored at 1 so BM25Score's
// length-normalization term never divides by zero or a negative value.
func (c *CorpusStatsCache) Set(totalDocs int, avgDocLen float64) {
	if c == nil {
		return
	}
	if avgDocLen <= 0 {
		avgDocLen = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalDocs, c.avgDocLen = totalDocs, avgDocLen
}
