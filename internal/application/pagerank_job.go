package application

import (
	"context"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// RunPageRankJob recomputes every document's PageRank score from the
// current link graph and writes the results back in one batched update.
// Called by cmd/crawl's periodic ticker, and once more right after each
// crawl job completes successfully, since that's when the graph actually
// changes.
//
// A document with neither an incoming nor an outgoing link never appears
// in the link graph at all, so it's simply not part of scores and
// UpdatePageRanks leaves it untouched -- it keeps whatever neutral default
// (or prior score) it already had rather than being reset to 0.
func RunPageRankJob(ctx context.Context, repo ports.PageRankRepository) error {
	graph, err := repo.LinkGraph(ctx)
	if err != nil {
		return err
	}
	scores := domain.PageRank(graph)
	if len(scores) == 0 {
		return nil
	}
	return repo.UpdatePageRanks(ctx, scores)
}
