package domain

import "sync"

// CorpusStatsCache holds the corpus-wide statistics BM25 scoring needs for
// every query term -- total document count and average document length --
// kept in memory and refreshed on a timer (see bootstrap.SyncCorpusStats)
// rather than recomputed via a full-table COUNT/AVG scan on every search
// request. Safe for concurrent use, following the same RWMutex-guarded
// live-value pattern as TuningSettings/OperationalSettings: read on every
// search, written only by the background refresh.
type CorpusStatsCache struct {
	mu        sync.RWMutex
	totalDocs int
	avgDocLen float64
}

// NewCorpusStatsCache seeds the cache with an initial snapshot -- typically
// whatever CorpusStats(ctx) reports at startup -- so a request handled
// before the first background refresh still sees a real value rather than
// an empty-corpus default.
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

// Set replaces the cached snapshot -- called once at startup and again on
// every subsequent refresh tick by bootstrap.SyncCorpusStats. avgDocLen is
// floored at 1 (mirroring sqlrepo.Repository.CorpusStats' own handling of an
// empty corpus) so BM25Score's length-normalization term never divides by
// zero or a negative value.
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
