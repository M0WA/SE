# SearXNG (internal web search for the chat feature)

`se.mo-sys.de` runs a self-hosted [SearXNG](https://github.com/searxng/searxng)
metasearch instance, in Docker, so the chat feature's `web_search` MCP tool
(`cmd/mcp-web`) can search the live web instead of only the local index --
the model decides whether to invoke it; this instance never queries SearXNG
on its own. Configured with three upstream engines (Bing, Brave, DuckDuckGo)
plus this repo's own `searchengine` (`../searxng-engine/`), which folds the
local index into the same blended results instead of querying it out of
band the way the old "RAG" mechanism did -- everything else is disabled
(see `settings.yml`'s `keep_only`).

Independent of the `searchengine` binaries/package -- a separate Docker
service on the VM, not shipped in the `.deb`, same as `../prometheus/`.

## Install

Docker isn't otherwise used on this box (every other service is a native
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

Set that same value as `SEARCH_INTERNAL_API_KEY` in
`/etc/searchengine/searchengine.env` on the box running `cmd/search`, then
restart `searchengine-search` -- see `../searxng-engine/README.md`'s
Authentication section for why both sides must agree.

**Never commit the filled-in `settings.yml`** -- `secret_key` signs session
cookies, `internal_api_key` grants unauthenticated `/search` access. Keep
placeholders in the tracked copy; real values only in the deployed
`/opt/searxng/settings.yml` (same convention as `../prometheus/`'s API key).

Install the systemd unit (manages `docker compose up`/`down`, same as every
other service here, rather than relying on Docker's restart policy alone):

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
`403`/empty `results` usually means `search.formats` doesn't list `json`,
or the container's mounted config is stale (needs `docker compose restart
searxng` from `/opt/searxng` -- it only reads `settings.yml` at startup).

## Notes

- Bound `127.0.0.1` only, same reasoning as `../prometheus/README.md`'s
  Notes. `docker-compose.yml` uses `network_mode: host` (not a bridge +
  published port) so the `searchengine` engine can reach `cmd/search` on
  the host's own `127.0.0.1:8080` -- see `../searxng-engine/README.md`'s
  Install section -- with `GRANIAN_HOST=127.0.0.1`/`GRANIAN_PORT=8888`
  keeping SearXNG itself loopback-only.
- `search.formats` includes `json` deliberately -- SearXNG disables it by
  default as an anti-scraping measure, but this instance has exactly one
  caller (the `web_search` tool, over loopback), so there's no scraping
  surface to protect.
- `server.limiter: false` -- SearXNG's rate limiter needs Redis; skipped
  rather than standing up Redis for an instance only localhost can reach.
- A `settings.yml` or `searchengine_index.py` edit needs `docker compose
  restart searxng` (from `/opt/searxng`) to take effect -- both are read
  once at container startup, not watched for changes; a new (not edited)
  `searchengine_index.py` also needs `cp`-ing into `/opt/searxng/` first.
- `searchengine` (the engine above) silently returns zero results if
  `SEARCH_INTERNAL_API_KEY` isn't set on `cmd/search`, or doesn't match
  `internal_api_key` here -- see `../searxng-engine/README.md`'s
  Authentication section.
- If SearXNG's upstream image changes its default engine list, `keep_only`
  re-resolves against whatever engines still exist under those exact names
  -- an upstream rename would silently drop the engine rather than error,
  so re-run the `curl` Verify check after any image update.
- Startpage was the original fourth choice but had to be dropped: SearXNG
  marks it `inactive` upstream (not just `disabled`), since Startpage added
  a proof-of-work CAPTCHA that broke the scraper (searxng/searxng#6669) --
  `keep_only` can't override `inactive`. Re-adding it later is just adding
  `startpage` back to `keep_only`.
- `keep_only` only controls which engines survive the default list -- it
  does *not* flip an engine's own `disabled`/`inactive` flag. `bing` ships
  `disabled: true` by default (unlike `brave`/`duckduckgo`), so it needs
  the explicit `engines: [{name: bing, disabled: false}]` override too, or
  it stays listed but never queried -- confirmed via `GET /config` showing
  `"enabled": false` before that override was added.
