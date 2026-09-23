# PageRank

[← Manual home](README.md)

*`/admin/pagerank`*

PageRank scores every document's link authority — how much weight other pages lend it by linking to it. This page is where you watch that score evolve, read the algorithm's fixed constants, and force a fresh recompute on demand. It recomputes automatically on the Settings interval and right after every crawl, so most of the time this page is read-only observation; Force recalculation exists for when you don't want to wait.

![PageRank](images/pagerank.png)

## Current distribution

Shows the corpus-wide spread right now: total scored documents, min, max, and average score. A healthy, well-linked corpus has a wide spread with a long tail of low-authority pages and a few clear high-authority ones; a suspiciously flat distribution (min ≈ max ≈ average) usually means PageRank hasn't run yet, or the link graph is too sparse or uniform to differentiate documents.

## Last recompute

Reports when the most recent recompute finished, documents scored, iterations to converge, the final convergence delta (how far scores still moved on the last iteration), and run duration. If a recompute is running anywhere — this browser, another admin, or cmd/crawl's scheduled/post-crawl trigger — this section shows "Recomputing…" instead, since status is shared across every process on the database. If nothing has ever run, it says so plainly rather than showing zeros.

## Algorithm

Damping factor, max iterations, and convergence epsilon are the classic PageRank constants — compiled into the binary, not editable here, shown for transparency. Damping factor (0.85) is the probability mass a page passes along outbound links, versus the remainder spread evenly regardless of link structure; higher makes link structure matter more. Max iterations caps how long a recompute runs before giving up (50, well above real-world needs), and convergence epsilon is the threshold below which one iteration's score movement counts as settled. Blend weight and recompute interval, by contrast, are read from Tuning in Settings, not fixed — blend weight controls how much PageRank influences final ranking (0 = no influence), and recompute interval controls how often the scheduled run fires; change either on Settings, not here.

## Force recalculation

Recomputes every document's score from the current link graph immediately, instead of waiting for the next scheduled or post-crawl run. Runs synchronously and blocks until done — the button shows a loading state, then reports the same documents/iterations/delta/duration numbers as any other run. Use this after a bulk link-graph change (a large crawl, a bulk delete, a domain removal) when you want scores to reflect reality right away.

> **Worth knowing:**
> - A document with no incoming or outgoing links is left untouched by a recompute rather than reset to 0 — it isn't part of the link graph, so it keeps its last score (1/N if never scored).
> - An empty link graph is reported as 0 documents scored in 0 iterations — a legitimate result, not an error, in both the stats panel and a forced recompute.

---
← [Search debug](search-debug.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Embeddings](embeddings.md) →
