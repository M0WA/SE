package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ErrCrawlInterruptedByRestart is the failure reason recorded for a job
// that was still queued or running when crawl-server stopped.
var ErrCrawlInterruptedByRestart = errors.New("crawl-server restarted before this job finished")

// RecoverInterruptedCrawls runs once at crawl-server startup. A job left in
// CrawlJobQueued or CrawlJobRunning status when the process last stopped
// (a crash, a deploy, ...) has no goroutine actually working on it anymore
// -- its in-memory queue/frontier died with the old process -- so without
// this, it would sit showing "running" (or "queued") forever, looking
// active when it's actually dead, and never get picked back up.
//
// A job whose request needed a cookie or Basic auth can't be safely
// restarted: those credentials are deliberately never persisted (the same
// storage-at-rest security rationale as domain.ScheduledCrawl's), so it's
// simply marked failed with ErrCrawlInterruptedByRestart, and it's on the
// admin to re-trigger it (e.g. via the Crawl page's "Recrawl" panel) with
// credentials supplied again.
//
// Every other interrupted job is restarted from its own seed URLs via
// trigger, reusing every setting its original request carried (including
// the per-crawl overrides) -- not a true resume from wherever it left off
// (no per-URL frontier is persisted), but safe and complete: the crawler's
// idempotent per-URL document IDs mean already-indexed pages are just
// harmlessly re-verified, and PrioritizeUnindexed is forced on for this
// recovery pass regardless of the original request's setting, since
// otherwise much of the budget could be spent re-confirming pages already
// indexed before the restart rather than reaching ones that never got
// there.
func RecoverInterruptedCrawls(
	ctx context.Context,
	jobs ports.CrawlJobStore,
	trigger func(ctx context.Context, opts ports.CrawlOptions) (string, error),
) (recovered, abandoned int, err error) {
	all, err := jobs.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, j := range all {
		if j.Status != domain.CrawlJobQueued && j.Status != domain.CrawlJobRunning {
			continue
		}
		if failErr := jobs.MarkFailed(ctx, j.ID, ErrCrawlInterruptedByRestart); failErr != nil {
			return recovered, abandoned, failErr
		}
		if j.Request.HasCookie || j.Request.HasBasicAuth {
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
		if _, triggerErr := trigger(ctx, opts); triggerErr != nil {
			return recovered, abandoned, triggerErr
		}
		recovered++
	}
	return recovered, abandoned, nil
}
