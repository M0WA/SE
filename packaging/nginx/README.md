# nginx + Let's Encrypt setup

The `searchengine` package runs three independent systemd services, each
its own binary/process:

- `searchengine-search` (search-server) -- listens on `127.0.0.1:8080`, serves public search.
- `searchengine-admin` (admin-server) -- listens on `127.0.0.1:8081`, login/admin UI.
- `searchengine-crawl` (crawl-server) -- listens on `127.0.0.1:8082`, internal only.

All three bind loopback-only. nginx is the only thing meant to reach any
of them -- including search-server, whose *results* are public but whose
plain-HTTP port isn't meant to be reachable directly (no TLS, no access
control, no rate limiting outside of nginx).

## Routing

nginx must split traffic between search-server and admin-server by path:

- `/`, `/style.css`, `/search`, `/index.js` -> search-server, `http://127.0.0.1:8080`
- `/login`, `/logout`, `/admin` and its subpaths (`/admin.js`,
  `/admin/documents`, `/admin/crawl`, `/admin/jobs`, `/admin/settings`,
  `/admin/search`, `/admin/api/...`, and every per-page script the admin UI
  serves -- `/admin_*.js`, `/admin_crawl.js`, `/admin_jobs.js`, `/login.js`)
  -> admin-server, `http://127.0.0.1:8081`

The `location /admin`/`/login`/`/logout` blocks below are plain **string
prefix** matches, not path-segment-aware -- `/admin` matches anything
starting with those five characters, not just `/admin/...`. A new
admin-only static asset route only reaches admin-server through the
existing rules if its path literally starts with `/admin`, `/login`, or
`/logout`; anything else falls through to the catch-all `location /`
below (search-server) instead and needs its own `location` block added
here. See CLAUDE.md's "Keep nginx's routing config in sync" for the
real incident this caused.

crawl-server (`127.0.0.1:8082`) is an internal API that only
admin-server talks to (via `CRAWL_SERVER_URL`). It must never be added
to the nginx config or otherwise exposed on a public listener.

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

This rewrites `/etc/nginx/sites-enabled/searchengine` in place: adds the
`443 ssl` server block pointing at `/etc/letsencrypt/live/<domain>/`,
and turns the port-80 server block into a 301 redirect to HTTPS.
`--register-unsafely-without-email` skips expiry-notice emails; drop it
and pass `--email you@example.com` instead if you want those.

Certbot also installs a `certbot.timer` systemd timer that renews the
certificate automatically before it expires (Let's Encrypt certs are
valid 90 days) -- nothing else to schedule. Check it with:

```sh
systemctl status certbot.timer
certbot certificates
```

## nginx stats for the Prometheus agent

`stub_status.conf` adds a separate `127.0.0.1:8090`-only server block
exposing nginx's `stub_status` page, scraped by `prometheus-nginx-exporter`
-- see `../prometheus/README.md`. It's outside the public `<DOMAIN>` server
blocks above entirely, so it's never reachable through the public listener
regardless of path -- unaffected by the `location /admin` string-prefix
gotcha described below.

```sh
cp stub_status.conf /etc/nginx/conf.d/stub_status.conf
nginx -t && systemctl reload nginx
```

## Notes

- Only port 80/443 need to be open on this host; nginx proxies to
  search-server (`127.0.0.1:8080`) and admin-server (`127.0.0.1:8081`)
  over localhost, neither of which is exposed directly. This matters in
  practice, not just in principle: there's no firewall on a typical bare
  VM blocking other ports, so an app that binds `0.0.0.0` is directly
  reachable from the internet the moment it starts.
- crawl-server (`127.0.0.1:8082`) must never be proxied or opened
  publicly -- it's only meant to be called by admin-server on localhost,
  and relies on that (this proxy config, plus `CRAWL_LISTEN_ADDR`'s
  default loopback-only bind) as its main protection. Set
  `CRAWL_INTERNAL_TOKEN` in `searchengine.env` for a second, independent
  layer that still holds if either of those is ever misconfigured (e.g.
  `CRAWL_LISTEN_ADDR` changed to `0.0.0.0` for container networking) --
  see that file's own comment.
- If the domain's DNS changes to point at a different host, certbot
  won't be able to renew until the domain resolves back to this
  machine (HTTP-01 validation fetches the challenge from the domain
  itself, not the IP).
