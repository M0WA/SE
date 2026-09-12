package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ErrCrawlInterruptedByRestart is the failure reason recorded for a job
// that was still queued or running when crawl-server stopped and can't be
// safely resumed (it needed credentials that were never persisted).
var ErrCrawlInterruptedByRestart = errors.New("crawl-server restarted before this job finished")

// RecoverInterruptedCrawls runs once at crawl-server startup. A job left in
// CrawlJobQueued or CrawlJobRunning status when the process last stopped
// (a crash, a deploy, ...) has no goroutine actually working on it anymore
// -- its in-memory queue/frontier died with the old process -- so without
// this, it would sit showing "running" (or "queued") forever, looking
// active when it's actually dead, and never get picked back up.
//
// A job whose request needed a cookie or Basic auth can't be safely
// resumed: those credentials are deliberately never persisted (the same
// storage-at-rest security rationale as domain.ScheduledCrawl's), so it's
// marked failed with ErrCrawlInterruptedByRestart, and it's on the admin to
// re-trigger it (e.g. via the Crawl page's "Recrawl" panel) with
// credentials supplied again.
//
// Every other interrupted job is resumed **in place**, under its own
// existing ID (via resume, not a fresh trigger) -- its pages_crawled count
// and page history keep accumulating rather than the job being marked
// failed and silently replaced by an unrelated-looking new job that starts
// over from zero. This is not a true resume from wherever the crawl's
// frontier actually was (no per-URL frontier is persisted) -- it restarts
// from the same seed URLs -- but that's safe: the crawler's idempotent
// per-URL document IDs mean already-indexed pages are just harmlessly
// re-verified, and PrioritizeUnindexed is forced on for this pass
// regardless of the original request's own setting, so the budget reaches
// still-missing pages fastest rather than being spent re-confirming ones
// already indexed before the restart.
func RecoverInterruptedCrawls(
	ctx context.Context,
	jobs ports.CrawlJobStore,
	resume func(jobID string, opts ports.CrawlOptions),
) (recovered, abandoned int, err error) {
	all, err := jobs.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, j := range all {
		if j.Status != domain.CrawlJobQueued && j.Status != domain.CrawlJobRunning {
			continue
		}
		if j.Request.HasCookie || j.Request.HasBasicAuth {
			if failErr := jobs.MarkFailed(ctx, j.ID, ErrCrawlInterruptedByRestart); failErr != nil {
				return recovered, abandoned, failErr
			}
			abandoned++
			continue
		}
		opts := ports.CrawlOptions{
			SeedURLs:            j.Request.SeedURLs,
			MaxPages:            j.Request.MaxPages,
			RespectRobots:       j.Request.RespectRobots,
			UserAgent:           j.Request.UserAgent,
			AllowOffDomainLinks: j.Request.AllowOffDomainLinks,
			UseSitemap:          j.Request.UseSitemap,
			FetchTimeoutSeconds: j.Request.FetchTimeoutSeconds,
			MinTextLength:       j.Request.MinTextLength,
			CrawlDelayMs:        j.Request.CrawlDelayMs,
			MaxResponseKB:       j.Request.MaxResponseKB,
			PrioritizeUnindexed: true,
		}
		resume(j.ID, opts)
		recovered++
	}
	return recovered, abandoned, nil
}
