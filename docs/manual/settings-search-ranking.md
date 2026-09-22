# Settings: Search & ranking

[← Manual home](README.md)

*`/admin/settings`*

The "Search & ranking" group on the [Settings](settings.md) page.

## Ranking: Alpha

Alpha controls the blend between keyword matching (BM25) and semantic similarity in a search's final score: 0 is pure semantic, 1 is pure BM25, and the default is 0.5, an even split. Push it toward 1 if searches are returning semantically-related-but-off-topic results for exact-phrase queries; push it toward 0 if users type natural-language questions and expect concept matches even when the exact words aren't on the page. Any value you enter outside 0–1 is clamped rather than rejected, so there's no way to break search by fat-fingering this field.

## Ranking: k1

k1 controls term-frequency saturation in the BM25 formula — how much additional benefit a document gets from a search term appearing many times rather than once. The default is 1.2, a standard middle-of-the-road value. Raise it if you want documents that repeat a query term heavily to keep climbing in score; lower it toward 0 if you want a single mention to count almost as much as ten (useful against keyword-stuffed pages). Negative values are clamped to 0.

## Ranking: b

b controls document-length normalization: 0 means length has no effect on scoring, 1 means full normalization (a term match in a short document counts for much more than the same match in a long one). The default is 0.75. Raise it toward 1 if long pages are dominating results just by containing more words overall; lower it toward 0 if you're seeing thin pages outrank substantive ones purely because they're short. Values outside 0–1 are clamped.

## Ranking: Title weight

Title weight is how many times a match in a document's title counts toward its term frequency, compared to one occurrence of the same word in the body — the default of 2 means a title hit is worth roughly a double body mention (tempered by k1's saturation, so it's not a flat multiplier). Raise it if titles in your corpus are reliably descriptive and you want them to dominate ranking more; values below 1 fall back to the default. This only affects documents crawled or re-crawled after you change it — it is not retroactively applied to already-indexed content, so a change here needs a re-crawl to show up across the existing corpus.

## Search: Default result count

This is the number of results returned when a search request doesn't specify its own count — the default is 5000. It's a ceiling more than a typical page size (the UI paginates well below this), so you'd normally only raise it if something downstream is consuming the full result set programmatically and hitting the cap.

## Search: Semantic candidate pool size

This bounds how many documents get scored for semantic similarity on any single search, regardless of how large the corpus is — the default is 200. Every BM25 match is always scored in full; this setting only limits how many additional documents are sampled (or, with ANN search on, retrieved via nearest-neighbor) to catch a match that's semantically relevant but shares no keywords with the query. Raising it improves semantic recall at the cost of more work per search; on a corpus of millions of documents, this is what keeps semantic scoring fast rather than scanning everything.

## Search: Use ANN search when available

When checked (the default), the semantic candidate pool is filled from a real approximate-nearest-neighbor index rather than a bounded brute-force sample — but only on Postgres with the pgvector extension installed; on SQLite, MySQL, or a Postgres server without the extension, this setting has no effect and the brute-force path is always used. Uncheck it to force brute-force even where ANN is available, which is mainly useful for troubleshooting a suspicious ranking difference by ruling out the ANN index as the cause.

## Embeddings: Compute hash embeddings

This turns on the built-in, dependency-free hash embedding — feature hashing, not a trained model — as a semantic provider for every document. It's on by default and is the only semantic provider that requires no external service. Unlike every other field on this page, this one is read once at process startup (it sizes a pgvector column via EnableANN), so flipping it only takes effect after the process restarts, not within the usual ~10-second live-reload window.

## Embeddings: Active for search

This section lists one weight field per enabled semantic provider — the built-in hash provider plus any HTTP embedding endpoint you've enabled on the HTTP endpoints page — and controls how much each contributes to the blended semantic score. Weights are relative to each other, not absolute: 1 and 1 weighs two providers equally, the same as 10 and 10 would, and a weight of 0 removes a provider from the blend entirely. Switching which provider is active takes effect immediately, without a restart, but enabling a provider that wasn't already active changes the vector space for it from scratch — existing embeddings from a different provider aren't comparable until every document is recomputed for the new one, which you trigger from the Embeddings page. A weight entry naming a provider that's no longer enabled (its endpoint was deleted or disabled since this was last saved) shows up locked and labeled as stale; saving drops it automatically. Any individual search request can override these weights for itself via a `?semantic=` query parameter without touching this global default.

## Embeddings: Title weight in embeddings

This blends a document's title into its embedding vector as titleWeight × title-vector + (1 − titleWeight) × body-vector, rather than embedding the concatenated text in one call (where most pooling models dilute the title's signal away). The default is 0.3, a modest edge toward the body. Setting it to 0 embeds the body only; setting it to 1 embeds the title alone, which also doubles the number of embedding calls made against any enabled HTTP endpoint. Like title weight for BM25 above, this only takes effect for documents crawled, re-crawled, or explicitly recomputed afterward.

## Link authority: PageRank weight

This controls how much link authority (PageRank, computed from the crawled link graph) influences final ranking: 0, the default, means it has no effect at all; 1 means ranking is driven entirely by link authority, ignoring BM25/semantic score. Most deployments leave this at or near 0 unless you specifically want well-linked pages to consistently outrank sparse ones regardless of keyword relevance — it's a blunt instrument, so raise it gradually and check real search results after each change.

## Link authority: Recompute interval

This is how often, in minutes, the crawler recomputes PageRank from the current link graph in the background — the default is 60, with a 5-minute floor that can't be set lower even if you try, to prevent an aggressive value from spinning the recompute in a tight loop. A recompute always also runs immediately after any crawl job finishes, since that's when the link graph actually changes, so this interval mainly matters for catching graph changes between crawls (e.g. if you're deleting documents or editing links some other way).

## Fuzzy matching: Correct misspelled query terms

When checked (the default), a query term that matches nothing at all in the index is looked up against the vocabulary for a near-miss within the configured edit distance, and that correction is substituted for scoring purposes only — the query as displayed to the user is never rewritten. A term that already matches something is never touched, so this only kicks in for genuine zero-result terms, not to "improve" an already-working query. Turn it off if you'd rather searches fail cleanly on a typo than silently substitute a guessed correction.

## Fuzzy matching: Max edit distance

This bounds how many single-character edits (insertions, deletions, substitutions) a fuzzy correction may be from the original query term — the field only accepts 1 or 2, and the default is 2. 1 is conservative and mainly catches a single typo or transposition; 2 is more forgiving of multi-character mistakes but has a higher chance of matching an unrelated short word by coincidence. If fuzzy matching feels like it's guessing wrong too often, dropping this to 1 is the first thing to try before disabling the feature outright.

---
← [Settings](settings.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: Crawling](settings-crawling.md) →
