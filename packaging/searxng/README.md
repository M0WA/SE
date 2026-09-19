# SearXNG (internal web search for the chat feature)

`se.mo-sys.de` runs a self-hosted [SearXNG](https://github.com/searxng/searxng)
metasearch instance, in Docker, so the chat feature (`internal/adapters/httpsearxng`)
can search the live web instead of only the local index. Configured with
exactly two upstream engines -- Bing, Brave, and DuckDuckGo -- everything else
SearXNG ships with is disabled (see `settings.yml`'s `keep_only`).

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
cd /opt/searxng
sed -i "s/REPLACE_WITH_OPENSSL_RAND_HEX_32/$(openssl rand -hex 32)/" settings.yml
```

**Never commit the filled-in `settings.yml`** -- `secret_key` signs SearXNG's
own session cookies. The tracked copy in this directory keeps the
placeholder; only the deployed `/opt/searxng/settings.yml` has the real value
(same convention `../prometheus/README.md` uses for its `prometheus.yml`
API key).

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
  directly.
- `search.formats` includes `json` deliberately -- SearXNG disables it by
  default as an anti-scraping measure for public instances, but this
  instance has exactly one caller (`internal/adapters/httpsearxng`, over
  loopback), so there's no scraping surface being protected by leaving it
  off.
- `server.limiter: false` -- SearXNG's rate limiter needs a Redis backend;
  skipped rather than standing up Redis solely to protect an instance
  nothing but localhost can even reach.
- A `settings.yml` edit needs `docker compose restart searxng` (from
  `/opt/searxng`) to take effect -- it's read once at container startup, not
  watched for changes.
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
