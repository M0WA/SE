# Settings: Search & ranking

[← Manual home](README.md)

*`/admin/settings`*

The "Search & ranking" group on the [Settings](settings.md) page.

## Ranking: Alpha

Alpha blends keyword matching (BM25) and semantic similarity: 0 is pure semantic, 1 is pure BM25, default 0.5. Push toward 1 if exact-phrase queries return semantically-related-but-off-topic results; toward 0 if users expect concept matches for natural-language questions. Values outside 0–1 are clamped, not rejected, so fat-fingering this field can't break search.

## Ranking: k1

k1 controls BM25 term-frequency saturation — how much extra benefit a document gets from a term appearing many times rather than once. Default 1.2, a standard middle value. Raise it so heavy repetition keeps climbing in score; lower toward 0 so one mention counts almost as much as ten (useful against keyword-stuffed pages). Negative values are clamped to 0.

## Ranking: b

b controls document-length normalization: 0 means length has no effect, 1 means full normalization (a match in a short document counts far more than the same match in a long one). Default 0.75. Raise toward 1 if long pages dominate just by having more words; lower toward 0 if thin pages outrank substantive ones purely for being short. Values outside 0–1 are clamped.

## Ranking: Title weight

How many times a title match counts toward term frequency, versus one body occurrence — default 2 means a title hit is worth roughly double (tempered by k1's saturation, not a flat multiplier). Raise it if titles are reliably descriptive; values below 1 fall back to the default. Only affects documents crawled or re-crawled after the change — a re-crawl is needed to apply it to the existing corpus.

## Search: Default result count

The number of results returned when a search request doesn't specify its own count — default 5000. A ceiling more than a typical page size (the UI paginates well below this); raise it only if something downstream consumes the full result set programmatically and hits the cap.

## Search: Semantic candidate pool size

Bounds how many documents get scored for semantic similarity per search, regardless of corpus size — default 200. Every BM25 match is always scored in full; this only limits how many additional documents are sampled (or, with ANN on, retrieved via nearest-neighbor) to catch keyword-free semantic matches. Raising it improves recall at the cost of more work per search — on a corpus of millions, this is what keeps semantic scoring fast.

## Search: Use ANN search when available

When checked (default), the semantic candidate pool is filled from a real approximate-nearest-neighbor index instead of a bounded brute-force sample — but only on Postgres with pgvector; on SQLite, MySQL, or Postgres without the extension, this has no effect and brute-force is always used. Uncheck it to force brute-force where ANN is available, mainly useful for ruling out the ANN index while troubleshooting a ranking difference.

## Embeddings: Compute hash embeddings

Turns on the built-in, dependency-free hash embedding — feature hashing, not a trained model — as a semantic provider for every document. On by default, the only provider needing no external service. Unlike every other field here, this is read once at process startup (it sizes a pgvector column via EnableANN), so it only takes effect after a restart, not the usual ~10-second live reload.

## Embeddings: Active for search

Lists one weight field per enabled semantic provider — the hash provider plus any enabled HTTP endpoint — controlling how much each contributes to the blended score. Weights are relative, not absolute: 1 and 1 weighs two providers equally, same as 10 and 10; a weight of 0 removes a provider entirely. Switching which is active takes effect immediately, but activating a provider that wasn't already changes its vector space from scratch — existing embeddings aren't comparable until every document is recomputed via the Embeddings page. A weight naming a no-longer-enabled provider shows locked and labeled stale; saving drops it. Any search request can override these weights via a `?semantic=` query parameter.

## Embeddings: Title weight in embeddings

Blends a document's title into its embedding vector as titleWeight × title-vector + (1 − titleWeight) × body-vector, rather than embedding concatenated text in one call (where pooling models dilute the title's signal). Default 0.3, a modest edge toward the body. 0 embeds body only; 1 embeds title alone, doubling embedding calls against any HTTP endpoint. Like BM25's title weight, only affects documents crawled, re-crawled, or explicitly recomputed afterward.

## Link authority: PageRank weight

Controls how much link authority (PageRank) influences final ranking: 0 (default) means no effect; 1 means ranking is driven entirely by it, ignoring BM25/semantic score. Most deployments leave this at or near 0 unless well-linked pages should consistently outrank sparse ones regardless of keyword relevance — a blunt instrument, so raise it gradually and check real results after each change.

## Link authority: Recompute interval

How often, in minutes, the crawler recomputes PageRank from the current link graph in the background — default 60, with a 5-minute floor that can't be set lower, preventing an aggressive value from spinning in a tight loop. A recompute also always runs immediately after any crawl finishes, so this interval mainly matters for graph changes between crawls (e.g. deleting documents).

## Fuzzy matching: Correct misspelled query terms

When checked (default), a query term matching nothing in the index is looked up against the vocabulary for a near-miss within the configured edit distance, substituted for scoring only — the displayed query is never rewritten. A term that already matches is never touched, so this only kicks in for genuine zero-result terms. Turn it off to let searches fail cleanly on a typo instead of silently substituting a guess.

## Fuzzy matching: Max edit distance

Bounds how many single-character edits a fuzzy correction may be from the original term — accepts only 1 or 2, default 2. 1 is conservative, mainly catching a single typo or transposition; 2 is more forgiving but more likely to coincidentally match an unrelated short word. If fuzzy matching guesses wrong too often, try dropping to 1 before disabling it outright.

---
← [Settings](settings.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings: Crawling](settings-crawling.md) →
