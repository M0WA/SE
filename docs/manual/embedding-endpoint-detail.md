# Embedding endpoint detail

[← Manual home](README.md)

*`/admin/embeddings/endpoint/{id}`*

The add/edit form for one HTTP embedding endpoint's full configuration: connection details, chunking behavior, and two live probes (List available models, Test connection) that call the endpoint for real before you save. The same page handles both creating a new endpoint (route ends in /new) and editing an existing one.

![Embedding endpoint detail](images/embedding-endpoint-detail.png)

## Name and Base URL

Name is the display label you'll see everywhere else (the endpoints table, the Settings weight picker, the embeddings overview). It's also used to mint the endpoint's internal ID the first time you save a new endpoint -- lowercased, non-alphanumeric characters collapsed, deduped against every other endpoint's ID and the reserved word "hash". That ID never changes afterward even if you rename the endpoint later, because it's the actual database column/index suffix storing this provider's vectors. Base URL is the OpenAI-compatible embeddings API's root, e.g. http://localhost:11434/v1 for a local Ollama instance -- the server appends /embeddings itself when calling out.

## API key

Sent as an "Authorization: Bearer <key>" header on every call to this endpoint; leave it blank for a local server that doesn't require auth. For security, a previously saved key is never echoed back into this field -- it always starts blank when you open an existing endpoint, with the placeholder text telling you a key is already set. Leaving it blank and saving preserves whatever key is already stored; you only need to type a new value here if you're actually changing the key. To remove a stored key entirely (going back to no auth), check "Remove the stored API key" -- that checkbox is only enabled when a key is currently set.

## Model

The model name sent in the embeddings request body's "model" field -- whatever the endpoint expects, e.g. bge-m3 or text-embedding-3-small. Use "List available models" next to this field to query the endpoint directly (using whatever base URL/API key/model you've currently typed, even before saving) and see what it actually offers; not every provider supports this, in which case you'll see a message saying model listing isn't supported rather than an error.

## Dimensions

The expected length of the embedding vector this model returns. This must match reality: the server errors out if a real response's vector length doesn't match, and this number is also used to size the database column/index that stores this provider's vectors. Check the model's documentation if you're not sure -- common values are 768, 1024, or 1536 depending on the model.

## Rate limit

Caps real HTTP requests per second against this specific endpoint, enforced per actual network call (not per document -- one document can fire multiple calls once chunking splits it). Leave at 0 for unlimited, which is fine for a local server you control. Set it lower for a hosted provider with its own rate limits to avoid getting throttled or blocked during a corpus-wide recompute or a busy crawl.

## Chunk size

The maximum tokens sent in one embed call before longer text gets split into multiple chunks, each embedded separately and then mean-pooled into a single vector -- this exists because embedding models have a hard context limit, and a long document's full text would otherwise fail outright rather than degrade gracefully. Set to 0 to never split (send text whole) -- reasonable if your documents are always short or the model's context window is generous. When set, the chunk boundary is measured either by an exact tokenizer (if you set Tokenize URL below) or, absent that, by an estimate derived from character count, which is close enough for most models but not exact.

## Tokenize URL

An optional separate endpoint -- for example vLLM's own /tokenize -- that this server calls to get an exact token count for a piece of text, used only to size chunks precisely when Chunk size is non-zero. It's opt-in: leaving it blank falls back to the character-count estimate described above, which works fine in practice but can occasionally over- or under-split near the boundary. Only worth setting if you're seeing chunking-related embedding failures with the estimate, or if you want maximally accurate chunk boundaries against a model with an unusually small context window.

## Enabled

Controls whether this endpoint's embedding gets kept current for every document going forward, on every crawl. Unchecking this does not delete any already-computed vectors for this provider -- it just stops updating them, so search results from it (if it's still weighted as active) will drift stale over time. A newly added endpoint starts with this checked by default.

## Test connection

Makes one real embed call against whatever base URL/API key/model/dimensions you've currently typed into the form -- even before you've saved -- so you find out immediately if the credentials or URL are wrong rather than discovering it on the next real search or recompute. Shows "Connection succeeded" or the actual error message (bad credentials, network failure, dimension mismatch, etc.) returned by the endpoint.

## Save and Delete

Save creates a new endpoint (redirecting you to its own detail page once minted) or updates the existing one in place. Delete is only available when editing an existing endpoint (not on the "Add endpoint" form) and asks for confirmation, warning that search and recompute will no longer be able to use it -- the same warning and behavior as deleting from the endpoints list page.

> **Worth knowing:**
> - Test connection and List available models both work against the form's current unsaved values, so you can validate a brand-new endpoint before ever clicking Save.
> - If you're editing an endpoint and want to test it without retyping the API key, that's fine -- the test probe automatically falls back to the real stored key when you leave the field blank.

---
← [HTTP embedding endpoints](embedding-endpoints.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Chat Settings](chat-settings.md) →
