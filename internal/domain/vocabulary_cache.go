package domain

import "sync"

// VocabularyCache holds a snapshot of every distinct corpus term (with
// doc/total frequency), refreshed on a timer rather than scanned per
// fuzzy-correction search. Same RWMutex pattern as CorpusStatsCache.
type VocabularyCache struct {
	mu    sync.RWMutex
	terms []TermStat
}

// NewVocabularyCache seeds the cache with an initial snapshot so a request
// handled before the first refresh still sees real vocabulary, not empty.
func NewVocabularyCache(terms []TermStat) *VocabularyCache {
	c := &VocabularyCache{}
	c.Set(terms)
	return c
}

// Get returns the most recently cached vocabulary snapshot. A nil
// *VocabularyCache reports an empty vocabulary rather than panicking.
func (c *VocabularyCache) Get() []TermStat {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.terms
}

// Set replaces the cached vocabulary snapshot wholesale. The caller's slice
// becomes owned by the cache (never mutated in place), so concurrent Get()
// readers always see a consistent snapshot.
func (c *VocabularyCache) Set(terms []TermStat) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terms = terms
}
