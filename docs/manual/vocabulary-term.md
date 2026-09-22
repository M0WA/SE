# Vocabulary term detail

[← Manual home](README.md)

*`/admin/vocabulary/term`*

Opened by clicking a term on the Documents page's Vocabulary table, this page shows exactly which indexed pages contain one specific term, how strongly, and with what surrounding context — the postings list that underlies both BM25 ranking and typo/fuzzy correction for that term.

![Vocabulary term detail](images/vocabulary-term.png)

## How you get here

The term itself comes from the page's own URL query string (`?term=...`), the same one-static-page-per-any-value pattern the domain detail page uses for hosts. That means this page only makes sense opened from the Vocabulary list (or a link carrying a term) — visiting it directly with no `term` param shows a message asking you to open it from there instead, since there's nothing to look up without one.

## The summary line

Next to the term, you'll see how many pages are shown and how many documents the term appears in overall (its document frequency). When the list is capped — the server returns at most 500 postings — the summary says how many are "shown of" the true total rather than claiming to be complete, and a status message suggests narrowing the term if you need the rest. For a term that appears in fewer than 500 documents, the two numbers match and the wording switches to a plain "appears in N documents total".

## Sort order

Results are sorted by term frequency, highest first — the pages where this term occurs most often lead the list, the same convention used in the public search debug page's BM25 term breakdown. That puts the pages most representative of the term at the top, which is usually what you want when judging whether a term is meaningful or noise.

## Reading a row

Each row is one page containing the term: title (linking to the live page) with its URL underneath, a snippet excerpt showing the term in context, term frequency (how many times it occurs on that specific page), and that page's total indexed document length. The excerpt is generated the same way as public search result snippets, so what you see here matches what a searcher would see for a query matching this term.

## Why this page matters

It's the ground truth behind two things: BM25 ranking's use of this term, and the fuzzy/typo correction the public search silently applies when a query term gets zero direct hits — a near-miss is matched to a real vocabulary term within a bounded edit distance. If you're wondering why a search for a particular word is surfacing (or failing to surface) certain pages, or why a misspelling did or didn't get corrected to this term, this postings list is where to check — it shows precisely which documents this term is grounded in, not just an aggregate count.

> **Worth knowing:**
> - An empty postings list ("No pages contain…") for a term you expected to find usually means it was never actually tokenized out of any crawled page — check the domain detail page's content for that page rather than assuming this page is broken.
> - Don't rely on the postings count alone to judge a term's usefulness for correction — check a few excerpts too, since a high-frequency term that's really boilerplate (a nav label, a cookie notice) skews ranking without being meaningful content.

---
← [Domain detail](domain-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Content Dedup](content-dedup.md) →
