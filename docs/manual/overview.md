# Overview

[← Manual home](README.md)

*`/admin`*

This is the page you land on right after signing in, and the top entry in the admin navigation rail. It's a dashboard, not a place you configure anything directly -- a fast read on whether crawling is healthy, how big the index is, and whether anything looks stuck, with links into the pages where you'd actually go fix something.

![Overview](images/overview.png)

## The navigation rail

Every admin page shares the same sidebar, grouped by what you're trying to do rather than listed as one long flat menu: Content (Documents, Content dedup) for browsing and de-duplicating what's actually indexed; Crawling (Schedule, Jobs) for starting and tracking crawls; Relevance (Search, PageRank, Embeddings) for tuning how results are ranked; Chat (Settings, MCP servers, Agents) for the chat assistant's configuration; and System (Settings, Database, Users) for server-wide settings and accounts. Overview itself sits above all the groups as a single top-level link, since it doesn't belong to any one category. Whichever page you're currently on is visually marked in the rail, so you always know where you are without checking the URL.

## Signing out

The icon button at the top right of every admin page (not just this one) ends your session and sends you back to the public search page. It clears your session cookie on the server as well as in your browser, so the link you were on stops working immediately for anyone who might have it -- there's no "undo" short of signing in again.

## Index stats

The "Index stats" panel is the three plainest numbers about your corpus: how many documents are indexed, their average length in tokens, and which database driver is actually running underneath (SQLite or Postgres). Average length matters more than it looks -- a sudden drop usually means a crawl started pulling in mostly boilerplate or near-empty pages rather than real content, worth checking on the Documents page if you see it.

## Pages per domain and content age

The donut chart breaks your indexed pages down by domain, largest first; clicking any domain in its legend takes you straight to that domain's document list. Next to it, the age-of-content bar chart buckets every indexed page by how long ago it was last fetched, so you can see at a glance whether your content is fresh or has gone stale because crawling stopped running. Two more bar charts appear once there's data for them: documents by version number (how many times a page's content has actually changed since it was first indexed) and documents by number of stored versions (how many past copies are currently retained, which is capped by the version-history limit under Settings and will always be lower than the version-number chart once pruning kicks in).

## Operational tiles

The small stat tiles above the charts are a live snapshot of what's happening right now: how many crawl jobs are running versus queued, how many schedules are enabled, disabled, currently in progress, or overdue (meaning their next scheduled run time has already passed without starting), and the database connection pool's current usage (in-use, idle, and total open connections). An "orphan pages" tile appears once PageRank has run at least once, showing how many pages scored at or below the orphan threshold -- pages PageRank effectively couldn't find a path to, which usually means broken or missing internal links.

## Running crawl jobs

When one or more crawl jobs are actively running, they're listed here with their seed URLs and a live count of pages crawled so far -- the same information the Jobs page shows in full, just surfaced here so you don't have to navigate away to see whether a crawl you just started is actually making progress. This block disappears entirely when nothing is running, so its absence itself is informative: no crawl currently active.

## Trend charts

Four more charts round out the page once there's enough history: a 30-day crawl job outcomes donut (how many jobs finished, failed, or were cancelled); a 14-day fetch throughput chart stacking each day's fetch outcomes (success, error, etc.) on top of each other; a 30-day line chart of documents indexed per day; and a 14-day line chart of average fetch duration, useful for spotting a target site that's started responding slowly or a crawler config that's gotten less efficient. A PageRank distribution histogram also appears once PageRank has been computed, showing how scores are spread across the whole corpus rather than just the single orphan-count tile above.

> **Worth knowing:**
> - Every chart here is read-only -- there's nothing to click through to change behavior directly; use it to notice a problem, then go to the relevant page (Jobs, Schedule, PageRank, Documents) to act on it.
> - Blocks with no data yet (no running jobs, no PageRank computed, a brand-new install with no crawl history) simply don't render rather than showing an empty chart, so a sparse-looking Overview page on a fresh install is expected, not a bug.

---
← [Signing In](login.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Search & Chat](search-chat.md) →
