package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TriggerDueCrawls finds every due crawl, triggers each, and marks it
// in-progress immediately so a tick never double-triggers a running job.
// onDone fires on finish: sets last/next run, clears in-progress, and
// disables a one-off or MaxRuns-exhausted entry. Returns how many were
// triggered; a failed trigger is logged and skipped, retried next tick.
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
			if err := store.MarkScheduledCrawlRun(context.Background(), s.ID, finishedAt, finishedAt.Add(interval), stillEnabled, false, runCount, ""); err != nil {
				log.Printf("recording completion for scheduled crawl %s: %v", s.ID, err)
			}
		}

		jobID, err := trigger(ctx, scheduledCrawlOptions(s), onDone)
		if err != nil {
			log.Printf("triggering scheduled crawl %s: %v", s.ID, err)
			continue
		}

		// enabled stays s.Enabled here -- this call only marks in-progress
		// and records jobID, so startup crash-recovery can tell this run
		// apart from a stale one (see ResetStaleInProgress). next_run_at is
		// a UI placeholder; onDone overwrites it with the real value.
		nextRun := now.Add(interval)
		if err := store.MarkScheduledCrawlRun(ctx, s.ID, now, nextRun, s.Enabled, true, runCount, jobID); err != nil {
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
