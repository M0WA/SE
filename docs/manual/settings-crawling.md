# Settings: Crawling

[← Manual home](README.md)

*`/admin/settings`*

The "Crawling" group on the [Settings](settings.md) page.

## Crawler: Fetch timeout

The number of seconds the crawler waits for a single page to respond before giving up on it — the default is 8. Raise it if you're crawling slow or heavily-loaded sites and seeing pages time out that would otherwise succeed; lower it if a crawl is getting stuck for a long time on unresponsive hosts and you'd rather move on quickly.

## Crawler: User-Agent

The User-Agent header sent with every crawl request — it defaults to a standard desktop Firefox string, chosen so crawled sites treat requests like an ordinary browser visit rather than flagging or blocking an identifiable bot. Change this if a specific site you need to crawl blocks or serves different content to the default string, or if you want your crawler to identify itself honestly (e.g. with a contact URL) for sites where that matters. A single crawl job can also override this per-job, independent of this global default.

## Crawler: Default max pages

The page limit applied to a crawl job that doesn't specify its own — the default is 20000. This is a safety ceiling as much as a target: it exists so a misconfigured or unexpectedly link-heavy seed doesn't crawl indefinitely. Any individual crawl can set its own limit that overrides this default.

## Crawler: Minimum text length

Pages whose extracted text is shorter than this many characters are skipped as thin content rather than indexed — the default is 50. Raise it if your corpus is picking up near-empty pages (redirect stubs, cookie-notice-only pages, broken renders) that add noise without real content; lower it toward 0 if you're crawling a source that legitimately has short-but-meaningful pages (e.g. short-form listings) that are being wrongly excluded.

## Crawler: Crawl delay

Milliseconds to wait before each fetch after the first, per crawl — the default is 250ms, a quarter-second of politeness between requests to the same crawl's target. Raise it for sites that rate-limit or block aggressive crawlers; lower it (down to 0) only for crawls against infrastructure you control or that you know can handle a faster pace, since this delay is the main thing standing between a crawl and looking like abuse to the site being crawled.

## Crawler: Max response size

The number of kilobytes read from a single page's response before the crawler truncates it — the default is 5120 KB (5MB). This protects against a single enormous response (a giant JSON blob served with an HTML content-type, a misconfigured endpoint) consuming disproportionate memory or time during a crawl. Raise it only if you have a specific, legitimate source with unusually large real pages that are getting cut off.

## Crawler: Crawl history retention

How many past crawl jobs — each with its full per-page detail — the crawl server keeps before pruning the oldest; the default is 200. This is DB-backed now, not memory-bounded, so raising it is mainly a matter of how much history you want to browse on the Jobs page versus how much storage you're comfortable spending; the oldest jobs beyond the limit are deleted periodically, not instantly, so a lowered value takes a little while to actually shrink the stored history.

## Crawler: Concurrent crawls

How many crawl jobs are allowed to actually fetch pages at the same time — the default is 3. A burst of triggered jobs beyond this limit queues rather than opening unbounded connections. Changing this takes effect for the next job that starts, without a restart, but an already-running job keeps its slot until it finishes, so lowering this doesn't immediately free up capacity — it just stops new jobs from starting until the count drops.

## Crawler: Rendering

This chooses how a crawl fetches each page by default, unless the crawl job itself overrides it: None (plain HTTP fetch, the default — fastest, no JavaScript execution), or a real headless browser (Chromium or Firefox) that runs the page's JavaScript before extracting content. Real browser rendering is much slower — each page opens its own browser tab — and downloads the browser engine the first time it's ever used on that machine. Turn it on globally only if most of the sites you crawl need JavaScript to render their real content; otherwise, leave this on None and enable rendering per-crawl for the specific sites that need it.

## Crawler: Link scope

This controls how far a crawl follows discovered links by default, unless a crawl job sets its own scope. The default is "Same domain, any subdomain, any top-level domain" (broader than domain-only, since most real sites span multiple subdomains and sometimes multiple TLDs for the same brand). The four options, narrowest to broadest: exact seed host only; the seed's domain including subdomains (e.g. a seed of www.example.com also follows blog.example.com but not example.org); same domain name under any subdomain and any TLD (from example.com, also follows example.org and www.example.de, but not other.com); and any domain at all, which follows every link the crawler finds regardless of host. Pick the narrowest scope that still covers the site you actually want indexed — a scope broader than necessary can pull in unrelated content fast.

## Crawler: Treat www. and the bare domain as the same page

When checked (the default), the crawler folds a leading "www." off a page's host before deciding its identity, so www.example.com/x and example.com/x are always treated and stored as the same document, never crawled or indexed twice. This only affects future crawls; duplicates that already exist from before this was enabled are found and merged separately by content dedup, not by this setting. Turn it off only for a site you know genuinely serves different content at www. than at its bare domain — that's rare, but it does happen.

## Documents: Version history

This bounds how many versions of a document — the current one plus archived predecessors — are kept when a re-crawl changes its content; the default is 3. A document that's never changed always has exactly one version regardless of this setting; it only limits how much history a frequently-changing page accumulates. Lowering the value prunes existing extra versions the next time that specific document is re-crawled, not immediately across the whole corpus — so don't expect an instant storage drop from lowering it.

## Content dedup: Merge duplicate/near-duplicate documents

When checked (the default), a periodic background pass finds documents that are byte-identical or near-identical to another already-indexed document — a mirror, a copy served under a different host — and merges each group into one canonical document, so a search never lists the same content twice. Unlike most toggles on this page, turning this on matters immediately in a destructive way: a merge deletes the losing documents' own rows, including their individual link/version/embedding history, which is not migrated to the surviving document. Review results and trigger an on-demand recompute from the Content dedup page rather than only waiting for the scheduled pass.

## Content dedup: Matching method

Exact (the default) only merges documents whose normalized text is byte-identical. Near-duplicate additionally merges documents within the SimHash distance threshold below, catching near-misses like the same article with a different timestamp or ad slot. Switch to near-duplicate if you're seeing obvious duplicates survive exact matching because of small incidental differences in the page text; be aware it's a fuzzier match and can occasionally merge documents that only happen to share a lot of vocabulary.

## Content dedup: Near-duplicate threshold

The maximum Hamming distance (out of 64 bits) two documents' SimHash fingerprints may differ by and still count as a match — only used when the matching method above is set to near-duplicate. The default is 3, clamped to a 1–10 range; the default is a moderately conservative starting point meant to catch real near-duplicates without merging documents that only share some vocabulary. Raise it cautiously — each step up merges a meaningfully wider range of "close enough" content.

## Content dedup: Recompute interval

How often, in minutes, the background content-dedup pass runs automatically — the default is 120, with a 15-minute floor (higher than PageRank's 5-minute floor, since a dedup pass is a destructive write that deletes rows, not just a re-score). A recompute also always runs immediately after a crawl finishes, once dedup is enabled, so this interval mainly matters for catching duplicates that appear between crawls.

---
← [Settings: Search & ranking](settings-search-ranking.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: Content rules](settings-content-rules.md) →
