# nginx + Let's Encrypt setup

The `searchengine` package runs the app on `127.0.0.1:8080` (an
unprivileged systemd service, so it can't bind :80/:443 directly).
nginx sits in front, handling TLS and proxying to that local port.

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

## Notes

- Only port 80/443 need to be open on this host; the app itself stays
  on `127.0.0.1:8080` and is never exposed directly.
- If the domain's DNS changes to point at a different host, certbot
  won't be able to renew until the domain resolves back to this
  machine (HTTP-01 validation fetches the challenge from the domain
  itself, not the IP).
