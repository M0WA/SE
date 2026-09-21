# SearXNG (internal web search for the chat feature)

`se.mo-sys.de` runs a self-hosted [SearXNG](https://github.com/searxng/searxng)
metasearch instance, in Docker, so the chat feature's `web_search` MCP tool
(`cmd/mcp-web`, spawned by `internal/adapters/mcpclient`) can search the live
web instead of only the local index -- the model itself decides whether to
invoke it, this instance never queries SearXNG on its own. Configured with
three upstream engines -- Bing, Brave, and DuckDuckGo -- plus one of this
repo's own, `searchengine` (`../searxng-engine/`), which folds this
instance's own indexed corpus into the same blended results instead of
querying it out of band the way the old, separate "RAG" mechanism did --
everything else SearXNG ships with is disabled (see `settings.yml`'s
`keep_only`).

This is independent of the `searchengine` binaries/package -- it's a separate
Docker service on the VM, not shipped in the `searchengine` .deb, the same
way `../prometheus/` is host-level infrastructure rather than part of the
package.

## Install

Docker isn't otherwise used on this box (every other service here is a native
systemd unit) -- installed from Debian's own repo, not a third-party one:

```sh
apt-get install docker.io docker-compose
systemctl is-active docker
```

## Configure

```sh
mkdir -p /opt/searxng
cp docker-compose.yml settings.yml /opt/searxng/
cp ../searxng-engine/searchengine_index.py /opt/searxng/
cd /opt/searxng
sed -i "s/REPLACE_WITH_OPENSSL_RAND_HEX_32/$(openssl rand -hex 32)/" settings.yml
sed -i "s/REPLACE_WITH_SEARCH_INTERNAL_API_KEY/$(openssl rand -hex 32)/" settings.yml
```

Set the exact same value from that second `sed` as `SEARCH_INTERNAL_API_KEY`
in `/etc/searchengine/searchengine.env` on the box running `cmd/search`, then
restart `searchengine-search` -- see `../searxng-engine/README.md`'s
Authentication section for why both sides need to agree on this value.

**Never commit the filled-in `settings.yml`** -- `secret_key` signs SearXNG's
own session cookies, and `internal_api_key` grants unauthenticated access to
`/search`. The tracked copy in this directory keeps both as placeholders;
only the deployed `/opt/searxng/settings.yml` has the real values (same
convention `../prometheus/README.md` uses for its `prometheus.yml` API key).

Install the systemd unit (manages `docker compose up`/`down` the same way
every other service on this box is `systemctl`-controlled, rather than
relying on Docker's own restart policy alone):

```sh
cp searxng.service /etc/systemd/system/searxng.service
systemctl daemon-reload
systemctl enable --now searxng
```

## Verify

```sh
systemctl is-active searxng
docker ps --filter name=searxng
curl -s 'http://127.0.0.1:8888/search?q=test&format=json' | head -c 300
```

The `curl` should return a JSON body with a non-empty `results` array. A
`403`/empty `results` most often means `search.formats` in `settings.yml`
doesn't list `json`, or the container is still using a stale mounted config
from before a settings change (needs `docker compose restart searxng` from
`/opt/searxng`, not just a file edit -- the container only reads
`settings.yml` at startup).

## Notes

- Bound `127.0.0.1` only, same reasoning as every other service in
  `../prometheus/README.md`'s Notes -- nothing outside this host (or even
  outside `searchengine`'s own processes on it) needs to reach this
  directly. `docker-compose.yml` uses `network_mode: host` (not a bridge +
  published port) specifically so the `searchengine` engine can reach
  `cmd/search` on the host's own `127.0.0.1:8080` -- see
  `../searxng-engine/README.md`'s Install section for why a bridge network
  can't do this -- with `GRANIAN_HOST=127.0.0.1`/`GRANIAN_PORT=8888` (its
  environment) keeping SearXNG itself loopback-only under host networking,
  same as the bridge setup did.
- `search.formats` includes `json` deliberately -- SearXNG disables it by
  default as an anti-scraping measure for public instances, but this
  instance has exactly one caller (the `web_search` chat hook script,
  over loopback), so there's no scraping surface being protected by
  leaving it off.
- `server.limiter: false` -- SearXNG's rate limiter needs a Redis backend;
  skipped rather than standing up Redis solely to protect an instance
  nothing but localhost can even reach.
- A `settings.yml` edit needs `docker compose restart searxng` (from
  `/opt/searxng`) to take effect -- it's read once at container startup, not
  watched for changes. Deploying a change to `searchengine_index.py` itself
  needs the same restart (and, if the file is new rather than an edit, `cp`
  it into `/opt/searxng/` first) -- it's bind-mounted read-only, so the
  container only ever sees whatever the host file said at its last start.
- `searchengine` (the engine above) silently returns zero results if
  `SEARCH_INTERNAL_API_KEY` isn't set on `cmd/search`, or doesn't match
  `internal_api_key` here -- see `../searxng-engine/README.md`'s
  Authentication section before assuming the engine itself is broken.
- If SearXNG's upstream image changes its default engine list or config
  schema, `use_default_settings.engines.keep_only` re-resolves against
  whatever engines still exist under those exact names (`bing`, `brave`,
  `duckduckgo`) -- an upstream rename would silently drop that engine
  rather than error, so a `curl` Verify check after any image update is
  worth doing, not just assuming the pin still holds.
- Startpage was the original fourth choice but had to be dropped: SearXNG
  marks that engine `inactive` upstream (not just `disabled`) because
  Startpage added a proof-of-work CAPTCHA that broke the scraper (see
  https://github.com/searxng/searxng/pull/6669) -- `keep_only` can't
  override `inactive`, and it doesn't show up in `/config` at all when it
  applies. If upstream ever restores support, re-adding it is just adding
  `startpage` back to `keep_only`.
- `keep_only` only controls which engines survive the default list -- it
  does *not* flip an individual engine's own `disabled`/`inactive` flag.
  `bing` ships `disabled: true` by default (unlike `brave`/`duckduckgo`,
  which ship enabled), so it needs the explicit `engines: [{name: bing,
  disabled: false}]` override in `settings.yml` on top of `keep_only`, or
  it stays in the list but is never actually queried -- confirmed live via
  `GET /config` showing `"enabled": false` for it before that override was
  added.
