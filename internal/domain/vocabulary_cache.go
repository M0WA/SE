package domain

import "sync"

// VocabularyCache holds a snapshot of every distinct corpus term (with
// doc/total frequency), refreshed on a timer (bootstrap.SyncVocabulary)
// rather than scanned on every fuzzy-correction search. Same RWMutex
// live-value pattern as CorpusStatsCache.
type VocabularyCache struct {
	mu    sync.RWMutex
	terms []TermStat
}

// NewVocabularyCache seeds the cache with an initial snapshot -- typically
// whatever AllTerms(ctx) reports at startup -- so a request handled before
// the first background refresh still sees real vocabulary rather than an
// empty one (which just means fuzzy correction finds nothing until the
// first refresh completes, never an error).
func NewVocabularyCache(terms []TermStat) *VocabularyCache {
	c := &VocabularyCache{}
	c.Set(terms)
	return c
}

// Get returns the most recently cached vocabulary snapshot. A nil
// *VocabularyCache (e.g. a test that doesn't exercise fuzzy correction, or
// a process that never wired one up) reports an empty vocabulary rather
// than panicking, so fuzzy correction is simply unavailable rather than a
// crash.
func (c *VocabularyCache) Get() []TermStat {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.terms
}

// Set replaces the cached vocabulary snapshot wholesale -- called once at
// startup and again on every subsequent refresh tick by
// bootstrap.SyncVocabulary. The caller's slice becomes owned by the cache
// (never mutated in place), so concurrent Get() readers always see a
// complete, consistent snapshot.
func (c *VocabularyCache) Set(terms []TermStat) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terms = terms
}
