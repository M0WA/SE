# Settings: Crawling

[← Manual home](README.md)

*`/admin/settings`*

The "Crawling" group on the [Settings](settings.md) page.

## Crawler: Fetch timeout

Seconds the crawler waits for a page to respond before giving up — default 8. Raise it for slow or heavily-loaded sites timing out unnecessarily; lower it to move on quickly from unresponsive hosts.

## Crawler: User-Agent

The User-Agent header sent with every request — defaults to a standard desktop Firefox string, so sites treat requests like an ordinary browser visit rather than flagging a bot. Change it if a site blocks the default, or to identify your crawler honestly (e.g. a contact URL). A crawl job can override this per-job, independent of this default.

## Crawler: Default max pages

The page limit for a crawl job that doesn't specify its own — default 20000. A safety ceiling as much as a target, so a misconfigured or unexpectedly link-heavy seed doesn't crawl indefinitely. Any crawl can override it.

## Crawler: Minimum text length

Pages whose extracted text is shorter than this many characters are skipped as thin content — default 50. Raise it if near-empty pages (redirect stubs, cookie notices) are adding noise; lower toward 0 if legitimately short pages (e.g. short-form listings) are being wrongly excluded.

## Crawler: Crawl delay

Milliseconds to wait before each fetch after the first, per crawl — default 250ms of politeness between requests to the same target. Raise it for sites that rate-limit aggressive crawlers; lower it (down to 0) only against infrastructure you control, since this delay is the main thing standing between a crawl and looking like abuse.

## Crawler: Max response size

Kilobytes read from a single page's response before the crawler truncates it — default 5120 KB (5MB). Protects against an enormous response (a giant JSON blob, a misconfigured endpoint) consuming disproportionate memory or time. Raise it only for a legitimate source with unusually large pages getting cut off.

## Crawler: Crawl history retention

How many past crawl jobs, each with full per-page detail, the crawl server keeps before pruning the oldest — default 200. DB-backed, not memory-bounded, so raising it is a tradeoff between Jobs-page history and storage. Jobs beyond the limit are pruned periodically, not instantly, so lowering it takes a while to shrink stored history.

## Crawler: Concurrent crawls

How many crawl jobs fetch pages at the same time — default 3. A burst beyond this limit queues rather than opening unbounded connections. Takes effect for the next job to start, without a restart, but an already-running job keeps its slot until finished, so lowering it doesn't free capacity immediately.

## Crawler: Rendering

How a crawl fetches each page by default, unless the job overrides it: None (plain HTTP, default — fastest, no JS execution), or a real headless browser (Chromium/Firefox) that runs the page's JavaScript first. Real browser rendering is much slower — each page opens its own tab — and downloads the engine on first use. Turn it on globally only if most crawled sites need JavaScript; otherwise leave it None and enable rendering per-crawl.

## Crawler: Link scope

How far a crawl follows discovered links by default, unless a job sets its own scope. Default is "Same domain, any subdomain, any TLD" (broader than domain-only, since real sites often span multiple subdomains/TLDs). The four options, narrowest to broadest: exact seed host only; the seed's domain including subdomains (e.g. www.example.com also follows blog.example.com but not example.org); same domain name under any subdomain/TLD (from example.com, also follows example.org and www.example.de, but not other.com); any domain at all. Pick the narrowest scope that still covers what you want indexed — broader than necessary pulls in unrelated content fast.

## Crawler: Treat www. and the bare domain as the same page

When checked (default), the crawler folds a leading "www." off a page's host, so www.example.com/x and example.com/x are always treated as the same document, never indexed twice. This only affects future crawls; existing duplicates are found and merged separately by content dedup. Turn it off only for a site that genuinely serves different content at www. than at its bare domain — rare, but it happens.

## Documents: Version history

Bounds how many versions of a document — current plus archived predecessors — are kept when a re-crawl changes its content; default 3. A never-changed document always has exactly one version regardless. Lowering the value prunes extra versions the next time that document is re-crawled, not immediately corpus-wide, so don't expect an instant storage drop.

## Content dedup: Merge duplicate/near-duplicate documents

When checked (default), a periodic background pass finds documents byte-identical or near-identical to another already-indexed one and merges each group into one canonical document, so a search never lists the same content twice. Unlike most toggles here, this matters destructively right away: a merge deletes the losing documents' rows, including their link/version/embedding history, not migrated to the survivor. Review results and trigger an on-demand recompute from the Content dedup page rather than only waiting for the scheduled pass.

## Content dedup: Matching method

Exact (default) only merges documents whose normalized text is byte-identical. Near-duplicate additionally merges within the SimHash distance threshold below, catching near-misses like the same article with a different timestamp or ad slot. Switch to it if obvious duplicates survive exact matching due to small incidental text differences; it's fuzzier and can occasionally merge documents that just share a lot of vocabulary.

## Content dedup: Near-duplicate threshold

The maximum Hamming distance (out of 64 bits) two SimHash fingerprints may differ by and still count as a match — only used when matching method is near-duplicate. Default 3, clamped to 1–10, a moderately conservative start meant to catch real near-duplicates without over-merging. Raise it cautiously — each step merges a meaningfully wider range of "close enough" content.

## Content dedup: Recompute interval

How often, in minutes, the background content-dedup pass runs automatically — default 120, with a 15-minute floor (higher than PageRank's 5-minute floor, since dedup destructively deletes rows rather than just re-scoring). It also always runs immediately after a crawl finishes once enabled, so this interval mainly matters for duplicates appearing between crawls.

---
← [Settings: Search & ranking](settings-search-ranking.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: Content rules](settings-content-rules.md) →
