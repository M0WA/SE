package application

import (
	"context"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TriggerDueCrawls finds every crawl that's due (enabled, not already
// in-progress, and next_run_at at or before now), triggers each one via
// trigger -- the same job-creation path a manually-triggered crawl goes
// through -- and marks it in-progress immediately: this takes it out of
// contention the instant it starts, so a scheduler tick while it's still
// running (even one that runs past its own interval) never sees it as due
// again and double-triggers it. Critically, this never touches the
// entry's own Enabled value (the admin's own on/off toggle) -- only
// InProgress, a separate column that exists purely to prevent double-
// triggering. trigger's onDone callback fires exactly once the triggered
// job actually finishes (success or failure), at which point last_run_at
// is set to that real finish time, next_run_at to finish+interval,
// in_progress is cleared back to false, and -- only for a one-off entry,
// or a recurring one that just reached its MaxRuns cap -- enabled is
// turned off too (the one case where a trigger legitimately does change
// it, because the schedule has genuinely reached an end state). Only once
// onDone clears in_progress can the next tick ever pick this entry up
// again, so "wait until the last run is done, then schedule the next one
// that far out" holds no matter how long a run takes. It reports how many
// crawls were triggered.
//
// stillEnabled (computed once per entry, from state as of trigger time)
// governs both what onDone eventually sets enabled to and what runCount it
// records, so the two calls always agree.
//
// A trigger failure for one entry is logged and skipped, not fatal: its
// next_run_at/enabled/in_progress are left untouched, so the next tick
// retries it rather than silently losing it.
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
			if err := store.MarkScheduledCrawlRun(context.Background(), s.ID, finishedAt, finishedAt.Add(interval), stillEnabled, false, runCount); err != nil {
				log.Printf("recording completion for scheduled crawl %s: %v", s.ID, err)
			}
		}

		jobID, err := trigger(ctx, scheduledCrawlOptions(s), onDone)
		if err != nil {
			log.Printf("triggering scheduled crawl %s: %v", s.ID, err)
			continue
		}

		// enabled stays exactly s.Enabled here -- this call's only job is
		// to mark the entry in-progress so DueScheduledCrawls skips it
		// until onDone above clears that back to false. next_run_at is a
		// placeholder (never actually reachable while in_progress is true,
		// but still worth recording for the UI's "next run" display) --
		// onDone overwrites it with the real finish+interval regardless.
		nextRun := now.Add(interval)
		if err := store.MarkScheduledCrawlRun(ctx, s.ID, now, nextRun, s.Enabled, true, runCount); err != nil {
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
