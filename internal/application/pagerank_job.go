package application

import (
	"context"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// PageRankRunResult reports what a RunPageRankJob call actually did --
// surfaced by the admin PageRank debug page after a forced recompute,
// where an admin explicitly wants to see the result of the run they just
// triggered, not just whether it errored.
type PageRankRunResult struct {
	// Documents is how many documents got a freshly computed score (0 for
	// an empty link graph, where the recompute is skipped entirely).
	Documents int
	domain.PageRankRunInfo
}

// RunPageRankJob recomputes every document's PageRank score from the
// current link graph and writes the results back in one batched update.
// Called by cmd/crawl's periodic ticker, right after each crawl completes,
// and on demand from the admin PageRank page. A document with no incoming
// or outgoing link never appears in the graph, so UpdatePageRanks leaves
// it untouched rather than resetting it to 0.
func RunPageRankJob(ctx context.Context, repo ports.PageRankRepository) (PageRankRunResult, error) {
	start := time.Now()
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
// domain.PageRankStatus so any process's admin PageRank page can show
// whether a recompute (triggered by anything, anywhere) is running and
// what the last one found. settings may be nil (status bookkeeping is
// then skipped). On error, InProgress clears but the last successful
// run's fields are left as-is -- a failure produced no new result.
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

// LoadPageRankStatus reads the persisted status back (see
// RunPageRankJobWithStatus) -- used internally and by the admin GET
// handler. A nil settings, store error, missing key, or bad value all
// just return the zero value; "nothing to show yet" is never an error.
func LoadPageRankStatus(ctx context.Context, settings ports.SettingsStore) domain.PageRankStatus {
	return loadJSONStatus[domain.PageRankStatus](ctx, settings, ports.SettingsKeyPageRankStatus, "pagerank status")
}

func savePageRankStatus(ctx context.Context, settings ports.SettingsStore, status domain.PageRankStatus) {
	saveJSONStatus(ctx, settings, ports.SettingsKeyPageRankStatus, "pagerank status", status)
}
