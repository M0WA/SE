package bootstrap

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
)

// corpusStatsPollInterval mirrors settingsPollInterval: BM25's corpus-wide
// stats (total document count, average document length) only change when
// documents are crawled or deleted -- nowhere near once per search request
// -- so a ~10s propagation delay is more than fresh enough while sparing
// every search request its own full-table COUNT/AVG scan.
const corpusStatsPollInterval = 10 * time.Second

// CorpusStatsSource is the read side of ports.SQLRepository (and
// ports.AdminRepository) that SyncCorpusStats needs, narrowed so it isn't
// tied to either full port.
type CorpusStatsSource interface {
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
}

// SyncCorpusStats seeds cache with source's current corpus stats
// immediately, then keeps refreshing it every ~10s for as long as ctx stays
// alive -- the exact same immediate-then-poll pattern SyncSettings uses for
// the admin-configurable settings blobs. This lets hybrid search read
// totalDocs/avgDocLen from an in-memory cache on every query term (indeed,
// once per whole search request) instead of running a full-table scan
// per term, per request; a document crawled or deleted by another process
// still reaches this process's BM25 scoring within a poll interval.
func SyncCorpusStats(ctx context.Context, source CorpusStatsSource, cache *domain.CorpusStatsCache) {
	pollRefresh(ctx, corpusStatsPollInterval, func() { refreshCorpusStats(ctx, source, cache) })
}

func refreshCorpusStats(ctx context.Context, source CorpusStatsSource, cache *domain.CorpusStatsCache) {
	totalDocs, avgDocLen, err := source.CorpusStats(ctx)
	if err != nil {
		log.Printf("refreshing corpus stats: %v", err)
		return
	}
	cache.Set(totalDocs, avgDocLen)
}
