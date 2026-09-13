package application

import (
	"context"

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
