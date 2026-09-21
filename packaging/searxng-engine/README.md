# SearXNG engine: this instance's own search index

`searchengine_index.py` is a [SearXNG](https://github.com/searxng/searxng)
engine module that queries this deployment's own public `GET /search` API
(`cmd/search`) and folds its hits into SearXNG's ordinary blended results --
cited the same way as any other engine SearXNG queries (Bing, Brave,
DuckDuckGo -- see `../searxng/settings.yml`'s `keep_only` list).

This replaces the old, separate "RAG" mechanism, where the chat backend
queried its own index directly and out of band from web search, with a
result the chat's web-search feature already knows how to consume: the
instance's own index becomes just one more engine among the ones SearXNG
already blends, so a chat answer can cite "this site's own indexed pages"
right alongside a Bing or Brave hit, with no separate toggle or code path
for it on the `searchengine` side at all.

Unlike `cmd/mcp-web` (installed onto `searchengine`'s own host as part of
the `searchengine` `.deb`, spawned by `internal/adapters/mcpclient`), this
file is installed *into a SearXNG installation*, not into anything shipped
by the `searchengine` `.deb`. `searchengine` itself has no idea this engine
exists; from its point of view, requests through it look like ordinary
calls to the public `/search` API.

## Install

This repo's own SearXNG deployment lives in `../searxng/` (Docker, on
`se.mo-sys.de` -- see `../searxng/README.md`). Because that container only
bind-mounts `settings.yml` (see its `docker-compose.yml`), getting a new
engine module into it needs one more bind mount added, not just a file copy
into the image:

```sh
cp searchengine_index.py /opt/searxng/
```

then add a line to `/opt/searxng/docker-compose.yml`'s existing `volumes:`
list (alongside the `settings.yml` mount already there):

```yaml
    volumes:
      - ./settings.yml:/etc/searxng/settings.yml:ro
      - ./searchengine_index.py:/usr/local/searxng/searx/engines/searchengine_index.py:ro
```

(`/usr/local/searxng/searx/engines/` is where the upstream `searxng/searxng`
image's own engine modules live -- a plain non-Docker SearXNG install
instead copies this file straight into that install's `searx/engines/`
directory, no volume mount needed.)

Then register the engine in `settings.yml`. This repo's `../searxng/`
instance runs `use_default_settings.engines.keep_only` as an allowlist (see
its own README/comments for why) -- a custom engine isn't one of the
defaults `keep_only` filters, so it needs both an addition to that allowlist
*and* its own `engines:` entry, the same two-part pattern already used there
for `bing`'s `disabled: false` override:

```yaml
use_default_settings:
  engines:
    keep_only:
      - bing
      - brave
      - duckduckgo
      - searchengine

engines:
  - name: bing
    disabled: false
  - name: searchengine
    engine: searchengine_index
    shortcut: se
    disabled: false
    base_url: "http://127.0.0.1:8080"
    top_k: 10
    internal_api_key: "REPLACE_WITH_SEARCH_INTERNAL_API_KEY"
```

`base_url`/`top_k`/`internal_api_key` above are not fields SearXNG itself
understands -- they're `searchengine_index.py`'s own module-level
configuration variables (see the module docstring). SearXNG's documented
mechanism for per-engine config
(https://docs.searxng.org/admin/settings/settings_engines.html) is exactly
this: every key in an engine's `settings.yml` entry beyond the handful it
reserves for itself (`name`, `engine`, `shortcut`, `disabled`, `categories`,
...) is set as an attribute of the same name directly onto that engine's
loaded Python module before `request()`/`response()` are ever called --
there's no separate schema or plugin config file to write. This is the same
mechanism `../searxng/settings.yml` already relies on for `bing`'s
`disabled: false` override; a `base_url`/`top_k`/`internal_api_key` entry
here works the same way, just against a module this repo owns instead of a
built-in one.

**Never commit a real `internal_api_key`** into a tracked `settings.yml` --
same convention `../searxng/settings.yml` already uses for `server.secret_key`
and `../prometheus/README.md`'s `prometheus.yml` API key: keep a placeholder
in anything checked in, fill in the real value only in the deployed copy.

Restart SearXNG so it picks up both the new module file and the
`settings.yml` change (per `../searxng/README.md`, it reads its config once
at container startup, not on file change):

```sh
cd /opt/searxng
docker compose restart searxng
```

## Two connectivity gotchas, both already handled by `../searxng/docker-compose.yml`

These bit this engine hard enough during initial deployment that they're
worth calling out explicitly, in case either setting is ever "cleaned up"
without realizing why it's there:

- **`enable_http = True` on this module.** SearXNG's network layer
  (`searx/network/network.py`'s `initialize()`) hardcodes every engine's
  outbound network to HTTPS-only by default (`enable_http: False`),
  independent of anything in `settings.yml` -- a plain `http://` URL (this
  engine's `base_url` is always `http://...`, since it's a loopback call)
  gets rejected with `curl_cffi.requests.exceptions.InvalidSchema` before a
  connection is even attempted. SearXNG's engine loader reads a matching
  `enable_http` attribute directly off this module if present and builds a
  per-engine network from it -- that's what the `enable_http = True` in
  `searchengine_index.py` is for; removing it reintroduces the crash.
- **`network_mode: host` on the SearXNG container.** A container on its own
  bridge network has its own loopback, separate from the host's -- so even
  with `enable_http` fixed, `base_url = "http://127.0.0.1:8080"` would
  connect to *the SearXNG container itself* (which also happens to answer
  on `/search`, returning its own HTML instead of `cmd/search`'s JSON) or
  time out, not to `cmd/search`, which binds `127.0.0.1:8080` on the host
  only. `../searxng/docker-compose.yml` uses `network_mode: host` so the
  container's loopback *is* the host's loopback, with
  `GRANIAN_HOST`/`GRANIAN_PORT` keeping SearXNG itself loopback-only under
  that setup -- see its README's Notes.

## Authentication: SEARCH_INTERNAL_API_KEY

`/search` is a normal, session-cookie-gated endpoint on `cmd/search` --
this whole site can require a signed-in browser session, and by default
`/search` is no exception. SearXNG's engine calls are plain server-to-server
HTTP requests with no browser, no cookie jar, and no login flow to run --
without something else granting access, every request this engine makes
would get a `401`.

That something else is `SEARCH_INTERNAL_API_KEY`: an admin sets this
environment variable on the `cmd/search` process (see `../searchengine.env`
and `internal/adapters/restapi/handler.go`'s `Config.InternalSearchAPIKey`),
and any request carrying a matching `X-Internal-API-Key` header is let
through `/search` without a session, via `requireAuthAPIOrInternalKey` (see
its doc comment in `internal/adapters/restapi/auth.go`). This engine sends
that exact header, with the exact value of `internal_api_key` set in
`settings.yml` above.

`SEARCH_INTERNAL_API_KEY` is unset by default, meaning **the bypass does not
exist at all** until an admin opts in -- `/search` stays session-cookie-only
exactly as before this mechanism existed. If this engine is returning no
results, check for `401`s first: it almost always means
`SEARCH_INTERNAL_API_KEY` isn't set on the `cmd/search` process, or is set
to a value that doesn't match this engine's `internal_api_key`. This is a
deliberate security default, not a bug -- don't "fix" it by loosening
`/search`'s normal auth requirement instead of setting the key on both
sides.
