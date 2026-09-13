package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TriggerDueCrawls finds every crawl that's due (enabled, and next_run_at
// at or before now), triggers each one via trigger -- the same
// job-creation path a manually-triggered crawl goes through -- and records
// last_run_at=now, next_run_at=now+interval as a provisional placeholder,
// so a scheduler tick before the crawl finishes never sees it as due
// again. trigger's onDone callback fires exactly once the triggered job
// actually finishes (success or failure), at which point last_run_at/
// next_run_at are corrected to reflect THAT finish time instead -- always
// the same as or later than the placeholder above, since the job can't
// finish before it's triggered -- so a crawl that runs longer than its own
// interval still can't overlap with its own next run. It reports how many
// crawls were triggered.
//
// A non-recurring entry (s.Recurring false -- a plain one-off crawl, not a
// repeating schedule) is disabled immediately at trigger time rather than
// waiting for onDone: since its "interval" is meaningless, leaving it
// enabled with next_run_at=now would just have the very next tick trigger
// it again. onDone still fires for it (recording the real finish time as
// last_run_at), it just re-affirms disabled rather than re-enabling it. A
// recurring entry with a positive MaxRuns is disabled the same way once
// this run reaches that cap -- runCount (this run included) is computed
// once per entry and passed to both MarkScheduledCrawlRun calls below, so
// the placeholder and the onDone correction always agree on it.
//
// A trigger failure for one entry is logged and skipped, not fatal: its
// next_run_at is left untouched, so the next tick retries it rather than
// silently losing it.
func TriggerDueCrawls(ctx context.Context, store ports.ScheduledCrawlStore, trigger func(context.Context, ports.CrawlOptions, func()) (string, error), now time.Time) (int, error) {
	due, err := store.DueScheduledCrawls(ctx, now)
	if err != nil {
		return 0, err
	}

	triggered := 0
	for _, s := range due {
		interval := time.Duration(s.IntervalMinutes) * time.Minute
		runCount := s.RunCount + 1
		stillEnabled := s.Recurring && (s.MaxRuns <= 0 || runCount < s.MaxRuns)
		onDone := func() {
			finishedAt := time.Now()
			if err := store.MarkScheduledCrawlRun(context.Background(), s.ID, finishedAt, finishedAt.Add(interval), stillEnabled, runCount); err != nil {
				log.Printf("recording completion for scheduled crawl %s: %v", s.ID, err)
			}
		}

		jobID, err := trigger(ctx, scheduledCrawlOptions(s), onDone)
		if err != nil {
			log.Printf("triggering scheduled crawl %s: %v", s.ID, err)
			continue
		}

		nextRun := now.Add(interval)
		if err := store.MarkScheduledCrawlRun(ctx, s.ID, now, nextRun, stillEnabled, runCount); err != nil {
			log.Printf("recording run for scheduled crawl %s (job %s): %v", s.ID, jobID, err)
			continue
		}
		triggered++
	}
	return triggered, nil
}

func scheduledCrawlOptions(s domain.ScheduledCrawl) ports.CrawlOptions {
	return ports.CrawlOptions{
		SeedURLs:             s.SeedURLs,
		MaxPages:             s.MaxPages,
		RespectRobots:        s.RespectRobots,
		UserAgent:            s.UserAgent,
		Cookie:               s.Cookie,
		BasicAuthUser:        s.BasicAuthUser,
		BasicAuthPass:        s.BasicAuthPass,
		LinkScope:            s.LinkScope,
		AllowedDomains:       s.AllowedDomains,
		BlockedDomains:       s.BlockedDomains,
		FollowIndexedDomains: s.FollowIndexedDomains,
		UseSitemap:           s.UseSitemap,
		FetchTimeoutSeconds:  s.FetchTimeoutSeconds,
		MinTextLength:        s.MinTextLength,
		CrawlDelayMs:         s.CrawlDelayMs,
		MaxResponseKB:        s.MaxResponseKB,
		PrioritizeUnindexed:  s.PrioritizeUnindexed,
		Renderer:             s.Renderer,
	}
}
