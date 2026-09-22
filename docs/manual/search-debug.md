# Search debug

[← Manual home](README.md)

*`/admin/search`*

This page runs a query the same way the public search box does, but shows you the machinery underneath: the raw BM25 and semantic-similarity scores for every result, unblended, plus a second tool for looking up exactly which documents contain a given term. Use it whenever ranking looks wrong and you need to see why a result placed where it did, rather than guessing from the public results page.

![Search debug](images/search-debug.png)

## Debug a query

Type a query into the field the same way an end user would and hit Run. The query box understands the same syntax the public search does: plain words match anywhere, +word requires a term to be present, -word excludes documents containing it, "exact phrase" matches that phrase literally rather than as separate words, and site:example.com restricts results to one host. The Sort dropdown lets you compare Best match ranking against Most recent, which is useful for checking whether a document's low position is a ranking problem or just genuinely old content.

## Reading the results table

Each row is one matching document with five numbers: bm25 (the lexical match score, unnormalized), semantic (cosine similarity between the query's and the document's embeddings, 0 to 1), pagerank (the document's raw link-authority score), and final (what actually determined its rank, after blending and after any admin override). A result with a strong bm25 but weak semantic score matched mostly on exact wording; the reverse means the document is conceptually related but doesn't use the query's words. This table fetches up to 5000 matches in one request and paginates them locally 50 at a time — the Next/Previous buttons don't refetch from the server, so paging through a large result set is instant.

## Drilling into one result

Click a result's title to open its full score breakdown on a separate page. That's where you see the per-term BM25 contributions, the semantic similarity as a gauge, the document's PageRank in both raw and normalized form, and a visual bar showing exactly how much each of BM25, semantic, and PageRank contributed to the final score. Use this when a single result's position is surprising and the summary table's four numbers aren't enough to explain it.

## Look up a term

The second panel is a plain inverted-index lookup: type a single token (not a phrase, not a query with operators) and it lists every document containing that term, with the term's frequency in each document and that document's total length. The status line also reports the term's document frequency — how many documents in the whole corpus contain it at all. This is the tool to reach for when you suspect a query isn't matching because a word was never indexed, was stemmed differently than expected, or is simply rarer in the corpus than you assumed.

> **Worth knowing:**
> - A result the debug tool finds through semantic similarity alone (no exact term match) shows an explicit note in its BM25 breakdown instead of an empty table — that's expected for synonym/paraphrase matches, not a bug.
> - If a result's composed score (bm25 + semantic + pagerank contributions) doesn't add up to its final score, the detail page tells you why: an admin ranking boost or block from Overrides was applied on top.

---
← [Jobs](jobs.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [PageRank](pagerank.md) →
