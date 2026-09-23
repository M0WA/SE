# SearXNG engine: this instance's own search index

`searchengine_index.py` is a [SearXNG](https://github.com/searxng/searxng)
engine module that queries this deployment's own public `GET /search` API
(`cmd/search`) and folds its hits into SearXNG's ordinary blended results --
cited the same way as any other engine SearXNG queries (Bing, Brave,
DuckDuckGo -- see `../searxng/settings.yml`'s `keep_only` list).

This replaces the old, separate "RAG" mechanism (the chat backend querying
its own index directly, out of band from web search): the instance's own
index is now just one more engine SearXNG blends, so a chat answer can cite
"this site's own indexed pages" alongside a Bing or Brave hit, with no
separate toggle or code path on the `searchengine` side.

Unlike `cmd/mcp-web` (shipped in the `searchengine` `.deb`, spawned by
`mcpclient`), this file installs *into a SearXNG installation*, not into
anything the `.deb` ships. `searchengine` has no idea this engine exists --
requests through it look like ordinary calls to the public `/search` API.

## Install

This repo's own SearXNG deployment lives in `../searxng/` (Docker, on
`se.mo-sys.de` -- see `../searxng/README.md`). Because that container only
bind-mounts `settings.yml` (see its `docker-compose.yml`), a new engine
module needs one more bind mount, not just a file copy into the image:

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
instance runs `use_default_settings.engines.keep_only` as an allowlist -- a
custom engine needs both an addition there *and* its own `engines:` entry,
the same two-part pattern already used for `bing`'s `disabled: false`
override:

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

`base_url`/`top_k`/`internal_api_key` above aren't fields SearXNG itself
understands -- they're `searchengine_index.py`'s own module-level config
variables (see the module docstring). SearXNG's documented per-engine-config
mechanism (https://docs.searxng.org/admin/settings/settings_engines.html)
sets every key beyond its own reserved handful (`name`, `engine`,
`shortcut`, `disabled`, `categories`, ...) as an attribute of the same name
directly onto that engine's loaded Python module -- no separate schema or
plugin config file. Same mechanism `../searxng/settings.yml` already uses
for `bing`'s `disabled: false` override, just against a module this repo
owns instead of a built-in one.

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

Worth calling out in case either setting gets "cleaned up" without knowing why it's there:

- **`enable_http = True` on this module.** SearXNG hardcodes every engine's
  outbound network to HTTPS-only by default, independent of `settings.yml` --
  a plain `http://` URL (this engine's `base_url` is always loopback `http://`)
  gets rejected with `curl_cffi.requests.exceptions.InvalidSchema` before
  connecting. SearXNG's loader honors a matching `enable_http` attribute on
  the module if present; removing it reintroduces the crash.
- **`network_mode: host` on the SearXNG container.** A bridge-network
  container has its own loopback, separate from the host's -- so even with
  `enable_http` fixed, `base_url = "http://127.0.0.1:8080"` would hit *the
  SearXNG container itself* (which also answers on `/search`, with its own
  HTML instead of `cmd/search`'s JSON) or time out. `network_mode: host`
  makes the container's loopback the host's, with `GRANIAN_HOST`/
  `GRANIAN_PORT` keeping SearXNG itself loopback-only -- see its README's Notes.

## Authentication: SEARCH_INTERNAL_API_KEY

`/search` is normally session-cookie-gated. SearXNG's engine calls are plain
server-to-server HTTP with no browser, cookie jar, or login flow -- without
something else granting access, every request here gets a `401`.

That something else is `SEARCH_INTERNAL_API_KEY`: set on the `cmd/search`
process (see `../searchengine.env` and `Config.InternalSearchAPIKey`), it
lets any request carrying a matching `X-Internal-API-Key` header through
`/search` without a session, via `requireAuthAPIOrInternalKey`
(`internal/adapters/restapi/auth.go`). This engine sends that header with
`internal_api_key` from `settings.yml` above.

Unset by default -- **the bypass doesn't exist** until an admin opts in;
`/search` stays session-cookie-only otherwise. Zero results here almost
always means `SEARCH_INTERNAL_API_KEY` isn't set, or doesn't match this
engine's `internal_api_key` -- a deliberate security default, not a bug;
don't "fix" it by loosening `/search`'s auth instead.
