# Jobs

[← Manual home](README.md)

*`/admin/jobs`*

The operational view of crawling on this instance: every schedule set up, and every job (a triggered crawl run) ever executed, most recent first. Use it to check whether a crawl is running, see how it went, cancel something stuck, or manage schedules.

![Jobs](images/jobs.png)

## Schedules table

Lists every crawl set up on this instance, one-off or repeating, with options saved for reuse. The first column is an Enabled checkbox to pause/resume directly — the same dedicated toggle as schedule-detail's Enabled field, so pausing/resuming never reschedules the next run — followed by seed, next run time, and last run time (repeat interval and link scope stay on schedule-detail, not repeated here). Per-row icons: Run now (▶), Edit (✎, opens schedule-detail), Delete (trash).

## Jobs table

Lists every crawl actually triggered — one-off runs and every fire of a repeating schedule — most recent first. Columns show seed, status (Queued, Running, Done, Failed, Cancelled), pages crawled so far, duration, and a live pages/second reading from consecutive polls (shows "—" until observed twice). Only a limited number of jobs run at once — see the concurrency setting on Settings — so a job can sit Queued while others finish.

## Viewing and cancelling a job

Click the magnifying-glass icon to load a job's detail: the exact options it ran with (seed URLs, max pages, robots handling, link scope, allowed/blocked domains, renderer, sitemap/prioritization flags, fetch overrides, and whether a cookie or Basic auth was set — never the credential value), plus a per-page table of every attempted URL, outcome, title, content length, links found, fetch duration, timestamp. A Cancel action (the same trash icon, labeled "Cancel" on hover) appears next to View only while Queued or Running; cancelling stops the job and leaves already-indexed pages in place.

## Filtering and clearing ended jobs

Both tables get a regex filter box once they have a row — Schedules filters by seed, Jobs by seed or status text (e.g. "failed"). The job-detail page has its own filter (URL, status, title, or error detail), pagination past 50 attempted pages, and click-to-sort columns. "Clear ended jobs" removes every non-Queued/Running job (Done, Failed, Cancelled) in one action, reporting how many it removed.

## One-off Crawl vs. recurring schedule

The Crawl page's form always creates the same kind of record regardless of interval — the difference shows up only here. A one-off crawl (Interval 0) creates an already-due schedule, runs once, and shows up only as a Jobs row, never in Schedules. A repeating crawl shows up in both: as a Schedules row you manage, and, each time it fires, a fresh Jobs row you inspect after. This page keeps polling briefly right after you're redirected from creating a crawl, since the job appears only once crawl-server's scheduler next ticks, typically within seconds.

> **Worth knowing:**
> - The Jobs table auto-refreshes every 1.5s whenever a job is Queued or Running, and stops once nothing is active — leave this page open during a large crawl to watch it live.
> - A job's page-list defaults to sorting by fetched time, most recent first — most useful while a crawl is in progress or just finished.

---
← [Schedule detail](schedule-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Search debug](search-debug.md) →
