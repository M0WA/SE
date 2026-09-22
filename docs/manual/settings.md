# Settings

[← Manual home](README.md)

*`/admin/settings`*

This is the single page for every global tuning knob in searchengine: how ranking blends BM25 and semantic scores, how the crawler behaves by default, which words or domains get blocked or boosted, and system-level limits like the database pool and session length. Every field here applies to all three processes (search, admin, crawl) within about 10 seconds of saving — there's no restart step. Groups are collapsed by default; open the one you need with the summary bar, and check the eight tiles at the top of the page for a quick read of the current configuration before you dig into any group.

![Settings](images/settings.png)

This single page holds four collapsible groups of global tuning knobs, each documented on its own page:

- [Search & ranking](settings-search-ranking.md) -- BM25 weights, semantic pool, link authority, fuzzy matching
- [Crawling](settings-crawling.md) -- fetch limits, rendering, link scope, version history
- [Content rules](settings-content-rules.md) -- blocked and boosted terms and domains
- [System](settings-system.md) -- database connection pool, session length

## At a glance (summary tiles)

The eight tiles above the form (alpha·k1·b, title weight, ANN search, fuzzy matching, blocked, boosted, session length, crawl default) are a read-only snapshot of the current values, refreshed the instant you save. Use them to sanity-check a change without reopening every group, or to confirm at a glance that nothing drifted from what you expect before you start editing. They read from the same two API calls (GET /admin/api/settings and GET /admin/api/overrides) that populate the form fields, so they're always in sync with what's actually stored.

---
← [Agent detail](agent-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: Search & ranking](settings-search-ranking.md) →
