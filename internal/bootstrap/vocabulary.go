package bootstrap

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
)

// vocabularyPollInterval used to mirror corpusStatsPollInterval/
// settingsPollInterval's 10s, but AllTerms is a full GROUP BY scan over
// postings (unlike corpus stats' cheap COUNT/AVG on documents) --
// confirmed via EXPLAIN ANALYZE on production taking 13+ seconds against
// a 13M-row postings table, run independently by all three processes on
// this timer. At that size a 10s interval means the database is running
// this scan almost continuously, starving concurrent crawl writes of
// I/O and buffer cache -- confirmed as the dominant cause of a real
// crawl-throughput incident. Fuzzy-correction freshness tolerates being
// several minutes stale just fine, so this is deliberately much coarser
// than corpus stats' still-cheap 10s.
const vocabularyPollInterval = 10 * time.Minute

// VocabularySource is the read side of ports.SQLRepository that
// SyncVocabulary needs, narrowed so it isn't tied to the full port.
type VocabularySource interface {
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
}

// SyncVocabulary seeds cache with source's current vocabulary immediately,
// then keeps refreshing it every ~10s for as long as ctx stays alive -- lets
// fuzzy query-term correction check an in-memory snapshot instead of a full
// vocabulary scan per request, picking up terms crawled/deleted elsewhere
// within a poll interval.
func SyncVocabulary(ctx context.Context, source VocabularySource, cache *domain.VocabularyCache) {
	pollRefresh(ctx, vocabularyPollInterval, func() { refreshVocabulary(ctx, source, cache) })
}

func refreshVocabulary(ctx context.Context, source VocabularySource, cache *domain.VocabularyCache) {
	terms, err := source.AllTerms(ctx)
	if err != nil {
		log.Printf("refreshing vocabulary: %v", err)
		return
	}
	cache.Set(terms)
}
