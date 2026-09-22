# Content Dedup

[← Manual home](README.md)

*`/admin/content_dedup`*

The Content Dedup page finds documents that are byte-identical or near-identical to another already-indexed document — a mirror site, a copy of the same article under a different host — and merges each group into one canonical document, so a search result never lists the same content twice. It shows the status of the last recompute, lets you trigger a new one on demand, and lists every document that currently has aliases folded into it.

![Content Dedup](images/content-dedup.png)

## What content dedup actually does

A document fingerprint is taken from its normalized text: an exact hash for byte-identical matches, and (when the "simhash" method is enabled) a 64-bit SimHash fingerprint for near-duplicate matches, such as two copies of the same article with a different ad banner or a slightly reformatted byline. When a group of two or more documents share a fingerprint (exact hash match, or within the configured SimHash distance), the job picks one document in the group to keep — the one with the shortest hostname, breaking ties by whichever was crawled first — and merges every other document in the group into it. Merging is destructive: the losing documents' own rows are deleted from the index, and their URLs are recorded as aliases of the surviving canonical document. This is why content dedup is off by default and has to be turned on explicitly on the Settings page, along with picking a matching method and threshold, before this page has anything to do.

## Recompute now

This section shows whether a recompute is currently running, when the last one finished, and its result: how many groups were found and merged, how many documents were removed, and how long the run took. Click "Recompute now" to force a fresh full-corpus pass immediately, instead of waiting for the periodic run cmd/crawl performs on its own schedule (set by the recompute interval on Settings) or for the pass that runs automatically after each crawl finishes. This is most useful right after you first turn content dedup on, or right after you change its matching method or threshold on Settings — the existing corpus needs a pass under the new settings, and there's no reason to wait for the next scheduled tick to see the effect. The button is disabled while a recompute is already in progress, whether that recompute was started by your own click, another admin browser tab, or one of the automatic triggers — this page polls and reflects real state across every process talking to the same database, not just what this tab kicked off. If you click it while one is already running elsewhere, the request is rejected rather than starting a second overlapping pass, since two concurrent runs merging from their own separate snapshots of the corpus can leave inconsistent alias records behind.

## Reading the recompute result

"Groups merged" is the number of distinct duplicate/near-duplicate clusters the run found and folded into one document each. "Documents removed" is the total number of losing documents deleted across all of those groups — always at least equal to the group count, since a group of 2 removes 1 document and a group of 3 removes 2. "Duration" is how long the full pass took in milliseconds; for a large corpus using the near-duplicate method this can take a while, since it involves pairwise comparisons within banded buckets of documents in addition to the fingerprint scan itself. A zero for both groups and documents after a run isn't an error — it means the pass found nothing new to merge, which is expected once a corpus has already been cleaned up and no new duplicate content has been crawled since.

## Merged documents

This table lists every canonical document that currently has at least one alias URL attached to it, so a merge's effect is always visible rather than being a black-box count you have to trust blindly. Each row shows the canonical URL — the document that survived and is the one actually served in search results — and its aliases: every other URL now folded into it, with the reason each one was folded. "canonical tag" means ordinary crawl-time bookkeeping (a page declared a `rel=canonical` link, or the crawler folded a www/bare-domain pair) where no document was ever deleted or even separately created for that URL; "exact-content merge" and "near-duplicate merge" mean an actual content-dedup pass deleted that document's row and redirected it here. A single canonical document can accumulate aliases from more than one source over time, which is why aliases are listed individually rather than just as a count. The table is paginated 20 rows at a time — use Previous/Next to page through a large list — and shows "No documents have been merged yet" when the corpus is clean.

> **Worth knowing:**
> - Content dedup has to be turned on and configured (matching method, near-duplicate threshold, recompute interval) on the Settings page before this page shows any activity — it does nothing on its own while disabled.
> - A merge deletes the losing documents permanently. If you enable the near-duplicate (SimHash) method with too loose a threshold, review the Merged documents table after a recompute before trusting it — a threshold set too high can fold together documents that only coincidentally look similar.

---
← [Vocabulary term detail](vocabulary-term.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Crawl](crawl.md) →
