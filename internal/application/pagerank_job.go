package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// PageRankRunResult reports what a RunPageRankJob call did -- surfaced by
// the admin PageRank debug page after a forced recompute.
type PageRankRunResult struct {
	// Documents is how many documents got a freshly computed score (0 for
	// an empty link graph, where the recompute is skipped entirely).
	Documents int
	domain.PageRankRunInfo
}

// RunPageRankJob recomputes every document's PageRank from the current
// link graph and writes it back in one batched update. A document with no
// incoming/outgoing link never appears in the graph, so UpdatePageRanks
// leaves it untouched rather than resetting it to 0.
func RunPageRankJob(ctx context.Context, repo ports.PageRankRepository) (PageRankRunResult, error) {
	start := time.Now()
	// Best-effort: a link resolved here just means a more complete graph
	// this run; a failure here is never a reason to skip the recompute
	// itself -- it proceeds against whatever's already resolved.
	if _, err := repo.ResolvePendingLinks(ctx); err != nil {
		log.Printf("resolving pending links before pagerank recompute: %v", err)
	}
	graph, err := repo.LinkGraph(ctx)
	if err != nil {
		return PageRankRunResult{}, err
	}
	scores, info := domain.PageRank(graph)
	if len(scores) == 0 {
		return PageRankRunResult{}, nil
	}
	if err := repo.UpdatePageRanks(ctx, scores); err != nil {
		return PageRankRunResult{}, err
	}
	info.DurationMs = time.Since(start).Milliseconds()
	return PageRankRunResult{Documents: len(scores), PageRankRunInfo: info}, nil
}

// RunPageRankJobWithStatus wraps RunPageRankJob, persisting a
// domain.PageRankStatus so any process's admin page can show whether a
// recompute is running and what it last found. settings may be nil
// (bookkeeping skipped). On error, InProgress clears but other fields
// are left as-is.
func RunPageRankJobWithStatus(ctx context.Context, repo ports.PageRankRepository, settings ports.SettingsStore) (PageRankRunResult, error) {
	status := LoadPageRankStatus(ctx, settings)
	status.InProgress = true
	savePageRankStatus(ctx, settings, status)

	result, err := RunPageRankJob(ctx, repo)

	status.InProgress = false
	if err == nil {
		status.LastRunAt = time.Now().UTC()
		status.Documents = result.Documents
		status.PageRankRunInfo = result.PageRankRunInfo
	}
	savePageRankStatus(ctx, settings, status)
	return result, err
}

// LoadPageRankStatus reads the persisted status back -- a nil settings,
// store error, missing key, or bad value all return the zero value;
// "nothing to show yet" is never an error.
func LoadPageRankStatus(ctx context.Context, settings ports.SettingsStore) domain.PageRankStatus {
	return loadJSONStatus[domain.PageRankStatus](ctx, settings, ports.SettingsKeyPageRankStatus, "pagerank status")
}

func savePageRankStatus(ctx context.Context, settings ports.SettingsStore, status domain.PageRankStatus) {
	saveJSONStatus(ctx, settings, ports.SettingsKeyPageRankStatus, "pagerank status", status)
}
