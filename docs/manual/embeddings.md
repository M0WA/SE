# Embeddings

[← Manual home](README.md)

*`/admin/embeddings`*

The overview of the search engine's semantic vector space: which embedding providers are on, which one (or blend) actually scores search results, and a button to re-embed the whole corpus after changing a provider's model or dimensions. Use it as the landing page before diving into individual HTTP endpoint configuration.

![Embeddings](images/embeddings.png)

## Current configuration

A read-only summary from two places: operational settings (whether the hash provider is enabled, and which providers are weighted into search) and the configured HTTP endpoints. "Enabled providers" lists everything kept warm on crawl -- the hash provider if on, plus every enabled HTTP endpoint, regardless of whether it's used for search yet. "Active for search" is narrower: only providers with a positive weight in the blended semantic score (EmbeddingSearchWeights), each with its weight in parentheses -- a provider enabled but at weight 0 doesn't contribute anything a searcher sees. Change enabled/active state from the Settings page; this page only reports current state.

## HTTP endpoints configured

A count of HTTP embedding endpoints, enabled or not. Click "Manage HTTP endpoints" to add, edit, or delete them.

## Title weight

Shows EmbeddingTitleWeight as a fraction 0-1: at 0, semantic similarity comes from body text only; at 1, title only; in between blends the two. Set on the Settings page, not here -- surfaced here since it affects every provider's scoring equally.

## Recompute embeddings

Re-embeds every stored document's text against every enabled provider. It doesn't re-crawl -- the indexed text is unchanged -- just recomputes vectors. Needed after enabling a new provider, or changing an enabled provider's model or dimensions, since existing vectors were computed against the old configuration. NOT needed just to switch which enabled provider is active for search: both stay current on every crawl regardless, so flipping the weight on Settings takes effect instantly.

## Recompute status and trigger

"Recompute embeddings" starts the job and returns immediately -- a full-corpus recompute is a real per-document network round trip against every enabled provider, so it runs as a background job rather than blocking your tab. This page then polls status every couple seconds, showing "Recomputing...", corpus size, and, once done, documents recomputed/failed and duration. Since status is server-side, another admin's trigger shows up here too on the next poll. A second recompute can't start while one runs -- the button is disabled and the server rejects a duplicate.

> **Worth knowing:**
> - "Active for search" can differ from "Enabled providers" -- a provider needs both enabled AND a positive weight to influence results.
> - Recompute is corpus-wide and can take real time on a large corpus; there's no per-provider or per-document recompute here.

---
← [PageRank](pagerank.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [HTTP embedding endpoints](embedding-endpoints.md) →
