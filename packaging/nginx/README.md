# nginx + Let's Encrypt setup

The `searchengine` package runs three independent systemd services, each its
own binary/process:

- `searchengine-search` (search-server) -- `127.0.0.1:8080`, public search.
- `searchengine-admin` (admin-server) -- `127.0.0.1:8081`, login/admin UI.
- `searchengine-crawl` (crawl-server) -- `127.0.0.1:8082`, internal only.

All three bind loopback-only. nginx is the only thing meant to reach any of
them -- including search-server, whose plain-HTTP port has no TLS, access
control, or rate limiting outside of nginx.

## Routing

nginx must split traffic between search-server and admin-server by path:

- `/`, `/style.css`, `/search`, `/index.js`, `/session`, `/account`,
  `/account.js`, `/account/api`, `/account/mcp-servers` and its subpaths
  (`/account_mcp_servers.js`, `/account/mcp-servers/{id}`,
  `/account_mcp_server.js`, `/account/api/mcp-servers...`), `/vision/api/mode`,
  `/vision/api/heartbeat` -> search-server, `http://127.0.0.1:8080`
- `/login`, `/logout`, `/admin` and its subpaths (`/admin.js`,
  `/admin/documents`, `/admin/crawl`, `/admin/jobs`, `/admin/settings`,
  `/admin/search`, `/admin/api/...`, and every per-page script the admin UI
  serves -- `/admin_*.js`, `/admin_crawl.js`, `/admin_jobs.js`, `/login.js`)
  -> admin-server, `http://127.0.0.1:8081`

The `location /admin`/`/login`/`/logout` blocks below are plain **string
prefix** matches, not path-segment-aware -- `/admin` matches anything
starting with those five characters, not just `/admin/...`. A new
admin-only static asset only reaches admin-server if its path literally
starts with `/admin`, `/login`, or `/logout`; anything else falls through
to the catch-all `location /` (search-server) and needs its own `location`
block added here. See `CLAUDE.md`'s "Keep nginx's routing config in sync"
for the real incident this caused.

crawl-server (`127.0.0.1:8082`) is an internal API only admin-server talks
to (via `CRAWL_SERVER_URL`). Never add it to the nginx config or otherwise
expose it on a public listener.

## Timeouts

Both proxied blocks set `proxy_read_timeout 300s;`, not nginx's own 60s
default: a `/chat` turn can legitimately take longer than that (a single
slow MCP tool call, e.g. the "Image analyst" default agent's first,
uncached `easyocr` model download, plus completion time across however
many of `maxHookFollowUpRounds`' rounds actually need one). Left at the
default, a real chat turn hit this and surfaced as a raw 504 instead of
an answer or a graceful in-app tool error -- confirmed live. Keep this in
sync with `internal/adapters/mcpclient`'s own `callTimeout` (also 5
minutes) if either changes; nginx's own timeout should never be shorter,
or it cuts a legitimately-still-working request off first.

## Install

```sh
apt-get install nginx certbot python3-certbot-nginx
```

## Configure the reverse proxy

Copy `searchengine.conf` to `/etc/nginx/sites-available/searchengine`,
replacing `<DOMAIN>` with the real hostname (must already resolve to
this host -- certbot's HTTP-01 challenge needs that to issue a cert):

```sh
sed 's/<DOMAIN>/your.domain.example/' searchengine.conf \
  > /etc/nginx/sites-available/searchengine
rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/searchengine /etc/nginx/sites-enabled/searchengine
nginx -t && systemctl reload nginx
systemctl enable --now nginx
```

## Issue and install the certificate

```sh
certbot --nginx -d your.domain.example \
  --non-interactive --agree-tos --register-unsafely-without-email --redirect
```

This rewrites `/etc/nginx/sites-enabled/searchengine` in place: adds a
`443 ssl` block pointing at `/etc/letsencrypt/live/<domain>/`, and turns the
port-80 block into a 301 redirect to HTTPS. `--register-unsafely-without-email`
skips expiry-notice emails; drop it and pass `--email you@example.com` for those.

Certbot also installs a `certbot.timer` that renews the certificate
automatically before it expires (90-day validity) -- nothing else to
schedule. Check it with:

```sh
systemctl status certbot.timer
certbot certificates
```

## nginx stats for the Prometheus agent

`stub_status.conf` adds a separate `127.0.0.1:8090`-only server block
exposing nginx's `stub_status` page, scraped by `prometheus-nginx-exporter`
-- see `../prometheus/README.md`. It's outside the public `<DOMAIN>` blocks
entirely, so it's never reachable through the public listener, unaffected
by the `location /admin` string-prefix gotcha above.

```sh
cp stub_status.conf /etc/nginx/conf.d/stub_status.conf
nginx -t && systemctl reload nginx
```

## Notes

- Only port 80/443 need to be open; nginx proxies to search-server
  (`127.0.0.1:8080`) and admin-server (`127.0.0.1:8081`) over localhost,
  neither exposed directly. This matters in practice: there's no firewall
  on a typical bare VM, so an app bound `0.0.0.0` is directly reachable
  from the internet the moment it starts.
- crawl-server (`127.0.0.1:8082`) must never be proxied or opened
  publicly -- it relies on staying unproxied plus `CRAWL_LISTEN_ADDR`'s
  loopback-only default as its main protection. Set `CRAWL_INTERNAL_TOKEN`
  in `searchengine.env` for a second, independent layer that still holds
  if either is ever misconfigured (e.g. `CRAWL_LISTEN_ADDR` changed to
  `0.0.0.0` for container networking).
- If the domain's DNS changes to point at a different host, certbot can't
  renew until it resolves back here (HTTP-01 validation fetches the
  challenge from the domain, not the IP).
