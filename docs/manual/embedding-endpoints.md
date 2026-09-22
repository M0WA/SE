# HTTP embedding endpoints

[← Manual home](README.md)

*`/admin/embeddings/endpoints`*

This page lists every configured HTTP embedding endpoint -- external OpenAI-compatible embeddings APIs, whether a local inference server (Ollama, llama.cpp, LM Studio) or a hosted provider -- and is where you add, edit, or remove them. It's the management view; the actual field-by-field configuration for one endpoint happens on its own detail page.

![HTTP embedding endpoints](images/embedding-endpoints.png)

## The endpoint table

Each row is one independently configured HTTP endpoint, showing its name, base URL, model, dimensions, rate limit per second, and whether it's enabled. Every endpoint's embeddings are computed and stored completely independently of every other endpoint and of the built-in hash provider -- there's no shared state between rows beyond the document text itself. You can enable any number of endpoints at once; the built-in hash provider and every HTTP endpoint can all be kept warm simultaneously, and switching which one search actually reads from (on the Settings page) then takes effect instantly with no recompute needed.

## Adding an endpoint

Click "Add endpoint" to open a blank configuration form on the endpoint detail page. If no endpoints are configured yet, the table area instead shows a plain prompt telling you to add one.

## Editing and deleting

Each row's "Edit" link opens that endpoint's detail page pre-filled with its current configuration. "Delete" asks for confirmation first, then removes the endpoint entirely -- the confirmation dialog explicitly warns that search and any future recompute will no longer be able to use it. Deleting an endpoint does not retroactively touch documents already indexed against it; it simply stops that provider from being usable going forward. If the deleted endpoint was contributing to the active search weights, the system falls back automatically (see ReconcileSearchWeights) to the hash provider or another still-enabled endpoint rather than leaving search silently broken.

> **Worth knowing:**
> - This table has no client-side search filter, unlike most other admin list pages -- fine for the realistic small number of embedding endpoints most installs configure.

---
← [Embeddings](embeddings.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Embedding endpoint detail](embedding-endpoint-detail.md) →
