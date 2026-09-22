# Embeddings

[← Manual home](README.md)

*`/admin/embeddings`*

This page is the overview of the search engine's semantic vector space: which embedding providers are turned on, which one (or blend of several) actually scores search results, and a button to re-embed the whole corpus after you change a provider's model or dimensions. Use it as the landing page before diving into individual HTTP endpoint configuration.

![Embeddings](images/embeddings.png)

## Current configuration

This panel is a read-only summary pulled from two places: the operational settings (whether the built-in hash provider is enabled, and which providers are actively weighted into search) and the list of configured HTTP endpoints. "Enabled providers" lists every provider that is currently being kept warm on crawl -- the hash provider if it's on, plus the name of every HTTP endpoint whose own "Enabled" checkbox is checked, regardless of whether it's actually used for search yet. "Active for search" is a different, narrower list: it shows only the providers with a positive weight in the blended semantic score (domain.OperationalSettingsValues.EmbeddingSearchWeights), each with its weight in parentheses -- a provider that's enabled but sitting at weight 0 (or missing from the weights map) doesn't show up here because it isn't contributing anything to what a searcher sees. You change which providers are enabled and which are active for search from the Settings page, not from here; this page only reports the current state.

## HTTP endpoints configured

A simple count of how many HTTP embedding endpoints exist, enabled or not. Click "Manage HTTP endpoints" to go add, edit, or delete them.

## Title weight

Shows domain.OperationalSettingsValues.EmbeddingTitleWeight as a fraction from 0 to 1: at 0, semantic similarity is computed from the document body only; at 1, from the title only; anything in between blends the two. This value is also set on the Settings page, not here -- this page just surfaces it next to the rest of the embedding configuration since it affects every provider's scoring equally.

## Recompute embeddings

Re-embeds every already-stored document's text against every currently-enabled provider. It does not re-crawl anything -- the indexed text itself hasn't changed -- it just recomputes the vectors. You need this after enabling a new provider for the first time, or after changing an already-enabled provider's model or dimensions on Settings, because existing stored vectors were computed against the old configuration and won't match the new one. You do NOT need to run this just to switch which already-enabled provider is active for search: both providers' vectors are kept current on every crawl regardless of which one search is currently reading from, so flipping the weight on Settings takes effect instantly with no recompute.

## Recompute status and trigger

The "Recompute embeddings" button starts the job and immediately returns -- a full-corpus recompute is a real per-document network round trip against every enabled provider, so it always runs as a background job on the server rather than blocking your browser tab. Once started, this page polls the job's status every couple of seconds and shows a live "Recomputing..." state, corpus size, and once finished, how many documents were recomputed, how many failed, and how long it took. Because the status is stored server-side (not just in this browser tab), if another admin -- or another admin-server process -- triggers a recompute, this page picks that up too on its next poll. You cannot start a second recompute while one is already running; the button is disabled and the server rejects a duplicate trigger.

> **Worth knowing:**
> - "Active for search" can differ from "Enabled providers" -- a provider must be both enabled AND carry a positive weight to actually influence search results.
> - Recompute is corpus-wide and can take real time (minutes) on a large corpus; there's no per-provider or per-document recompute from this page.

---
← [PageRank](pagerank.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [HTTP embedding endpoints](embedding-endpoints.md) →
