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

"Recompute embeddings" starts the job and returns immediately -- a full-corpus recompute is a real per-document network round trip against every enabled provider, so it runs as a background job rather than blocking your tab. This page then polls status every couple seconds, showing "Recomputing...", corpus size, a live progress count, and, once done, documents recomputed/failed and duration. Since status is server-side, another admin's trigger shows up here too on the next poll. A second recompute can't start while one runs -- the button is disabled and the server rejects a duplicate.

While a recompute is in progress, this page shows a running "N / total documents (X%)" count, updated as each batch of documents finishes -- not just a frozen 0 until the whole run completes. If any documents have failed so far, a "Failed so far" count appears alongside it. Progress is checkpointed server-side after every batch, so if this admin-server instance restarts mid-run (a redeploy, a crash), the next instance to start resumes automatically from that checkpoint rather than starting the whole corpus over -- the run's Documents/Failed counts stay cumulative across the interruption.

## Concurrency

How many documents a recompute processes at once, rather than one at a time. Each in-flight document still pays its own real Embed HTTP round trip per enabled provider, so raising this mainly overlaps network latency rather than adding real load beyond what an endpoint's own rate limit already allows through -- safe to raise freely for a provider with a configured rate limit; raise cautiously for one without, since concurrency alone is then the only throttle. Defaults to 4. Edit the number and click Save; it applies live, taking effect on a running recompute's very next batch, with no restart or new trigger needed.

> **Worth knowing:**
> - "Active for search" can differ from "Enabled providers" -- a provider needs both enabled AND a positive weight to influence results.
> - Recompute is corpus-wide and can take real time on a large corpus; there's no per-provider or per-document recompute here.
> - Concurrency is saved through the same settings store as the Settings page's other fields, just surfaced here since it's specific to this job.

---
← [PageRank](pagerank.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [HTTP embedding endpoints](embedding-endpoints.md) →
