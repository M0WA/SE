# Vocabulary term detail

[← Manual home](README.md)

*`/admin/vocabulary/term`*

Opened by clicking a term on the Documents page's Vocabulary table, this page shows exactly which indexed pages contain that term, how strongly, and with what context — the postings list underlying both BM25 ranking and typo/fuzzy correction for it.

![Vocabulary term detail](images/vocabulary-term.png)

## How you get here

The term comes from the URL query string (`?term=...`), the same one-static-page-per-value pattern domain detail uses for hosts. So this page only makes sense opened from the Vocabulary list (or a link carrying a term) — with no `term` param, it shows a message asking you to open it from there instead.

## The summary line

Next to the term: how many pages are shown, and its document frequency overall. When the list is capped (the server returns at most 500 postings), the summary says how many are "shown of" the true total rather than claiming completeness, and suggests narrowing the term for the rest. Under 500 documents, the wording switches to a plain "appears in N documents total".

## Sort order

Results are sorted by term frequency, highest first — the same convention as the search debug page's BM25 term breakdown. This puts the pages most representative of the term at the top, usually what you want when judging whether it's meaningful or noise.

## Reading a row

Each row is one page containing the term: title (linking to the live page) with its URL underneath, a snippet showing the term in context, term frequency on that page, and total indexed length. The excerpt is generated the same way as public search snippets, so what you see matches what a searcher would see.

## Why this page matters

It's the ground truth behind two things: BM25's use of this term, and the fuzzy/typo correction public search silently applies when a query term gets zero hits — a near-miss matched to a real vocabulary term within a bounded edit distance. Wondering why a search surfaces (or misses) certain pages, or why a misspelling did or didn't get corrected here, is exactly what this postings list answers — precisely which documents this term is grounded in, not just an aggregate count.

> **Worth knowing:**
> - An empty postings list for a term you expected usually means it was never tokenized out of any crawled page — check domain detail's content rather than assuming this page is broken.
> - Don't judge a term's usefulness by postings count alone — check a few excerpts too, since a high-frequency term that's really boilerplate (a nav label, a cookie notice) skews ranking without being meaningful content.

---
← [Domain detail](domain-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Content Dedup](content-dedup.md) →
