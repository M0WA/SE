package application

import (
	"context"
	"encoding/json"
	"log"
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
// Called by cmd/crawl's periodic ticker, and once more right after each
// crawl job completes successfully, since that's when the graph actually
// changes -- and, on demand, by the admin PageRank debug page's "force
// recalculation" button.
//
// A document with neither an incoming nor an outgoing link never appears
// in the link graph at all, so it's simply not part of scores and
// UpdatePageRanks leaves it untouched -- it keeps whatever neutral default
// (or prior score) it already had rather than being reset to 0.
func RunPageRankJob(ctx context.Context, repo ports.PageRankRepository) (PageRankRunResult, error) {
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
	return PageRankRunResult{Documents: len(scores), PageRankRunInfo: info}, nil
}

// RunPageRankJobWithStatus wraps RunPageRankJob, additionally persisting a
// domain.PageRankStatus to settings (under ports.SettingsKeyPageRankStatus)
// so any process's admin PageRank debug page can show whether a recompute
// triggered by anything -- another admin's click, the periodic ticker in
// cmd/crawl, or a post-crawl trigger -- is currently running, and what the
// last completed run found, without needing to have triggered it itself.
// settings may be nil (e.g. in a test, or a process that never configured
// one), in which case this behaves exactly like RunPageRankJob with the
// status bookkeeping skipped.
//
// On error, InProgress is cleared but LastRunAt/Documents/Iterations/
// FinalDelta are left as whatever the last successful run recorded --
// a failed recompute didn't produce a new result to show.
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

// LoadPageRankStatus reads the persisted PageRank status back (see
// RunPageRankJobWithStatus) -- used both internally, to update it without
// clobbering fields a concurrent recompute isn't touching, and by the
// admin PageRank debug page's GET handler, to show it. A nil settings, a
// store error, a missing key (nothing has ever recomputed through this
// mechanism), or an undecodable value all just return the zero value --
// "nothing to show yet" is never treated as an error.
func LoadPageRankStatus(ctx context.Context, settings ports.SettingsStore) domain.PageRankStatus {
	if settings == nil {
		return domain.PageRankStatus{}
	}
	value, found, err := settings.GetSetting(ctx, ports.SettingsKeyPageRankStatus)
	if err != nil || !found {
		return domain.PageRankStatus{}
	}
	var status domain.PageRankStatus
	if err := json.Unmarshal([]byte(value), &status); err != nil {
		log.Printf("decoding pagerank status: %v", err)
		return domain.PageRankStatus{}
	}
	return status
}

func savePageRankStatus(ctx context.Context, settings ports.SettingsStore, status domain.PageRankStatus) {
	if settings == nil {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		log.Printf("encoding pagerank status: %v", err)
		return
	}
	if err := settings.SaveSetting(ctx, ports.SettingsKeyPageRankStatus, string(data)); err != nil {
		log.Printf("saving pagerank status: %v", err)
	}
}
