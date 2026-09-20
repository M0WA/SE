package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TriggerDueCrawls finds every due crawl (enabled, not in-progress,
// next_run_at <= now), triggers each and marks it in-progress immediately
// so a scheduler tick never double-triggers a still-running job. onDone
// fires once the job finishes: sets last_run_at/next_run_at, clears
// in_progress, and (only for a one-off or a recurring entry past MaxRuns)
// disables it. Reports how many crawls were triggered.
//
// stillEnabled is computed once per entry so trigger and onDone agree on
// what enabled/runCount to record. A trigger failure is logged and
// skipped, not fatal -- the next tick retries it.
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

		// enabled stays s.Enabled -- this call's only job is marking the
		// entry in-progress (and recording jobID, so a crash-recovery pass
		// at next startup can tell this run apart from a genuinely stale
		// one -- see ports.ScheduledCrawlStore.ResetStaleInProgress).
		// next_run_at is a placeholder for the UI; onDone overwrites it
		// with the real finish+interval.
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
