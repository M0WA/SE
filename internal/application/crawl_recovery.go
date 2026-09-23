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

// RecoverInterruptedCrawls runs once at crawl-server startup: a job left
// queued/running when the process stopped has no goroutine working it,
// so without this it'd show "running" forever.
//
// A job needing a cookie/Basic auth can't resume (credentials aren't
// persisted) -- marked failed with ErrCrawlInterruptedByRestart. Every
// other job resumes in place under its existing ID, restarting from the
// same seeds (safe: document IDs are idempotent), with PrioritizeUnindexed
// forced on so the remaining budget reaches missing pages first.
//
// MaxPages is shrunk by PagesCrawled already spent, so repeated restarts
// don't reset the budget; one already past budget is marked done instead.
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
		remainingMaxPages := j.Request.MaxPages
		if remainingMaxPages > 0 {
			remainingMaxPages -= j.PagesCrawled
			if remainingMaxPages <= 0 {
				if doneErr := jobs.MarkDone(ctx, j.ID); doneErr != nil {
					return recovered, abandoned, doneErr
				}
				recovered++
				continue
			}
		}
		opts := ports.CrawlOptions{
			SeedURLs:            j.Request.SeedURLs,
			MaxPages:            remainingMaxPages,
			RespectRobots:       j.Request.RespectRobots,
			UserAgent:           j.Request.UserAgent,
			LinkScope:           j.Request.LinkScope,
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
