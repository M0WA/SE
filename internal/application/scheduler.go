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
// job-creation path a manually-triggered crawl goes through -- and
// disables it immediately, regardless of whether it's recurring: this
// takes it out of contention the instant it starts, so a scheduler tick
// while it's still running (even one that runs past its own interval)
// never sees it as due again and double-triggers it. trigger's onDone
// callback fires exactly once the triggered job actually finishes
// (success or failure), at which point last_run_at is set to that real
// finish time, next_run_at to finish+interval, and -- for an entry that's
// still supposed to recur -- enabled is flipped back on. Only once onDone
// re-enables it can the next tick ever pick it up again, so "wait until
// the last run is done, then schedule the next one that far out" holds no
// matter how long a run takes. It reports how many crawls were triggered.
//
// A non-recurring entry (s.Recurring false -- a plain one-off crawl, not a
// repeating schedule) and a recurring entry that just reached its MaxRuns
// cap both stay disabled through onDone too, rather than being re-enabled
// -- stillEnabled (computed once per entry, from state as of trigger time)
// governs both what onDone eventually restores enabled to and what
// runCount it records, so the two calls always agree.
//
// A trigger failure for one entry is logged and skipped, not fatal: its
// next_run_at/enabled are left untouched, so the next tick retries it
// rather than silently losing it.
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

		// Always disabled here, even for a still-recurring entry: enabled
		// only becomes true again via onDone above, once the run this
		// triggered has actually finished. next_run_at is a placeholder
		// (never actually reachable while enabled is false, but still
		// worth recording for the UI's "next run" display) -- onDone
		// overwrites it with the real finish+interval regardless.
		nextRun := now.Add(interval)
		if err := store.MarkScheduledCrawlRun(ctx, s.ID, now, nextRun, false, runCount); err != nil {
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
