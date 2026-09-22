# Documents

[← Manual home](README.md)

*`/admin/documents`*

The Documents page is where you find indexed pages and the domains they belong to, and where the crawl vocabulary lives. Use it to check whether a page or domain made it into the index, spot-check what got crawled, and jump into a domain's full page list or a vocabulary term's detail.

![Documents](images/documents.png)

## Domain/URL search

Type into the search box and results update as you type, after a short pause (200ms) so you're not re-searching on every keystroke. The text is a regular expression, not a plain substring — so a literal dot or parenthesis needs escaping, but you also get real pattern power: `^shop\.` matches only hosts starting with "shop.", `\.de$` matches every .de domain. Matching is case-insensitive. Leave the box empty and nothing is shown — the page doesn't dump every domain and document by default, since a large corpus would make that useless as a first screen.

## How the search works under the hood

The first time you search, the page fetches up to 1000 domains and up to 2000 documents in one shot and caches them in memory; every keystroke after that just re-filters those two arrays client-side with your regex, so results feel instant and don't hammer the server. This means very large corpora are capped at that sample for search purposes — if your site has more than 2000 documents or 1000 distinct domains, an obscure one might not show up here even though it's genuinely indexed. Use the per-domain page (below) for a complete, uncapped view of one domain once you've found it.

## Domain results

Each match shows the host and how many pages are indexed under it. Click a row to open that domain's dedicated page (`/admin/documents/{host}`), which lists every one of its pages, not just the up-to-2000-document sample this search page draws from.

## Document results

Below the domain list, matching individual pages show as a table of URL, title, and host — matched against the URL, the title, or the derived host, so a search for a company name finds pages whose title mentions it even if the URL doesn't. Click through to a URL to open the live page in a new tab; there's no delete action here — deleting happens on the domain detail page, where you're looking at the full, current list rather than this search sample.

## Vocabulary

This panel lists every distinct term the index has tokenized out of crawled content, each with a document frequency (how many pages contain it) and a total frequency (how many times it occurs across the whole corpus). Unlike domain/URL search, this list is server-paged and server-sorted, so it stays accurate and fast no matter how large the vocabulary gets — nothing here is capped to an in-memory sample.

## Filtering, sorting, and paging vocabulary

The filter box is a plain substring match, not a regex, applied server-side against the lowercased term (indexed terms are always lowercased, so searching in mixed case still works). Click a column header to sort by it — clicking the already-active column flips ascending/descending; term starts ascending (A→Z) when first clicked, the two frequency columns start descending (most frequent first), since that's usually what you want to see first. "Per page" controls how many rows load at once; Previous/Next page through the filtered, sorted result set. The summary line always shows the true corpus-wide vocabulary size, and — only while a filter is active — how many terms that filter matched, which is also what the pager's page count is computed from.

## Why vocabulary matters: fuzzy correction

This isn't just a diagnostic curiosity — it's the same term list the public search's typo correction draws from. When a searcher's query term gets zero hits, the engine fuzzy-matches it against this vocabulary (within a bounded edit distance) and quietly substitutes the closest real term for scoring, showing the correction transparently in results rather than silently rewriting what the user typed. If a term you'd expect users to find isn't showing up here, that's exactly why a related misspelled query wouldn't get corrected to it either.

## Opening a term's detail

Click any term in the table to open its detail page, which shows exactly which documents contain it and how strongly. Use this to sanity-check that a term genuinely came from real content (not, say, boilerplate or a crawl artifact) before trusting how it's influencing ranking or fuzzy correction.

> **Worth knowing:**
> - Domain/URL search only sees a capped in-memory sample (1000 domains, 2000 documents) — for a complete picture of one domain's pages, open its detail page rather than relying on this search alone.
> - The domain/URL box takes a regex; the vocabulary box takes a plain substring. Typing regex syntax into the vocabulary box searches for it literally.

---
← [Your files](account-files.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Domain detail](domain-detail.md) →
