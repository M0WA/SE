package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TriggerDueCrawls finds every scheduled crawl that's due (enabled, and
// next_run_at at or before now), triggers each one via trigger -- the same
// job-creation path a manually-triggered crawl goes through -- and advances
// its schedule to last_run_at=now, next_run_at=now+interval. It reports how
// many schedules were triggered.
//
// A trigger failure for one schedule is logged and skipped, not fatal: its
// next_run_at is left untouched, so the next tick retries it rather than
// silently losing that schedule's recurrence.
func TriggerDueCrawls(ctx context.Context, store ports.ScheduledCrawlStore, trigger func(ports.CrawlOptions) (string, error), now time.Time) (int, error) {
	due, err := store.DueScheduledCrawls(ctx, now)
	if err != nil {
		return 0, err
	}

	triggered := 0
	for _, s := range due {
		jobID, err := trigger(scheduledCrawlOptions(s))
		if err != nil {
			log.Printf("triggering scheduled crawl %s: %v", s.ID, err)
			continue
		}

		nextRun := now.Add(time.Duration(s.IntervalMinutes) * time.Minute)
		if err := store.MarkScheduledCrawlRun(ctx, s.ID, now, nextRun); err != nil {
			log.Printf("recording run for scheduled crawl %s (job %s): %v", s.ID, jobID, err)
			continue
		}
		triggered++
	}
	return triggered, nil
}

func scheduledCrawlOptions(s domain.ScheduledCrawl) ports.CrawlOptions {
	return ports.CrawlOptions{
		SeedURLs:            s.SeedURLs,
		MaxPages:            s.MaxPages,
		RespectRobots:       s.RespectRobots,
		UserAgent:           s.UserAgent,
		AllowOffDomainLinks: s.AllowOffDomainLinks,
		UseSitemap:          s.UseSitemap,
	}
}
