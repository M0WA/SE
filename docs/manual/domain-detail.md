# Domain detail

[← Manual home](README.md)

*`/admin/documents/{host}`*

This page is the complete, current view of every page indexed under a single domain — reached by clicking a domain from the Documents search. It's where you review a site's crawl results in detail, spot pages worth re-crawling or removing, and see a page's edit history across re-crawls.

![Domain detail](images/domain-detail.png)

## Header and summary

The host is read straight from the page's own URL (`/admin/documents/{host}`), so this one static page works for any domain you land on. The summary line next to the title gives page count, average and total indexed length (in tokens), and totals for internal links, external links, and backlinks across every page in the domain — a quick read on how substantial and how well-interlinked this domain's crawl is. A "Crawl" link next to it jumps straight to the Crawl page pre-filled with this domain's origin URL, so re-crawling a domain you're already looking at is one click away.

## The length chart

Above the table, a bar chart shows each page's indexed document length (in tokens) as one bar, tallest relative to the domain's longest page. Hover a bar to see that page's title and exact token count. It's a fast way to spot outliers — a page that's suspiciously short (a near-empty template, a paywall stub) or one page that dwarfs the rest (maybe a sitemap or archive dump that shouldn't be weighted like a normal article).

## Filtering the page table

The filter box matches a regex, case-insensitively, against each page's title or URL — the same convention used across the admin's other list filters. An invalid pattern is reported inline rather than throwing; fix it and the table updates automatically. This is purely a client-side filter over the domain's already-loaded pages, so it's instant with no extra requests.

## The page table's columns

Each row is one page: title (linking out to the live URL) with the URL shown underneath, indexed length, internal links (links from this page to the same domain), external links (links to other domains), backlinks (other indexed pages that link to this one), and PageRank — the link-authority score fed into ranking, tunable via the Tuning page's PageRank weight. Hovering internal/external/backlinks explains each in a tooltip if you forget which is which.

## Version history

A page that's been re-crawled with materially different content shows a version number greater than v1 in the last-but-one column; click it to open the History panel below, listing every prior version with its own title, indexed length, and crawl timestamp. A page still on v1 has only ever been crawled once, so there's nothing to show — the button only appears once there's real history.

## Deleting a single page

The Delete button on a row asks for confirmation, then removes that one page from the index immediately — the row disappears from the table as soon as the request succeeds. This is a straightforward, synchronous single-document delete, unrelated to the bulk delete-all flow below.

## Delete all in this domain

This button removes every page in the domain from the index in one action. After you confirm, the server looks up every matching document ID, queues their deletion in a background goroutine detached from your request, and responds immediately — so the removal survives you navigating away or closing the tab; it does not get silently half-finished the way one-request-per-page used to. While you stay on the page, it polls every 1.5 seconds and updates the status line and table live so you can watch the count shrink. If progress stalls (the remaining count holds steady for several polls in a row — some deletions failed and were logged server-side), it tells you how many were removed and how many are stuck rather than polling forever, and re-enables the button.

> **Worth knowing:**
> - Deleting all pages in a domain is irreversible and starts immediately after you confirm — there's no undo, only re-crawling the domain again from scratch.
> - If "Delete all" reports pages stuck rather than removed, check the server log for the specific document IDs that failed rather than assuming the whole domain is gone.

---
← [Documents](documents.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Vocabulary term detail](vocabulary-term.md) →
