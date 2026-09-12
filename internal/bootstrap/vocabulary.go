package bootstrap

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
)

// vocabularyPollInterval mirrors corpusStatsPollInterval/settingsPollInterval:
// the corpus's distinct-term vocabulary only changes when documents are
// crawled or deleted -- nowhere near once per search request -- so a ~10s
// propagation delay is more than fresh enough while sparing every search
// request needing fuzzy correction its own full vocabulary scan.
const vocabularyPollInterval = 10 * time.Second

// VocabularySource is the read side of ports.SQLRepository that
// SyncVocabulary needs, narrowed so it isn't tied to the full port.
type VocabularySource interface {
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
}

// SyncVocabulary seeds cache with source's current vocabulary immediately,
// then keeps refreshing it every ~10s for as long as ctx stays alive -- the
// same immediate-then-poll pattern SyncCorpusStats/SyncSettings use. This
// lets hybrid search's fuzzy (typo-tolerant) query-term correction check a
// term with zero postings hits against an in-memory vocabulary snapshot
// instead of running a full vocabulary scan per request; a term crawled or
// deleted by another process still reaches this process's fuzzy matching
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
