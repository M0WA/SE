# Installation

[← Manual home](README.md)

The ordered install flow for a fresh Debian/Ubuntu host, for standing up a new deployment. Steps 1-2 are handled automatically by the `.deb`; everything from nginx onward is a manual step the package deliberately skips.

**Prefer containers?** [Docker Compose installation](docker-installation.md) is a full alternative to this whole flow -- the same three binaries plus Postgres, in one command, image published to `ghcr.io/m0wa/se` on every release. It skips nginx/TLS/systemd entirely (bring your own reverse proxy if you want those), so it's a faster way to get a real, working instance up, at the cost of the rendering-capable-crawl and sandboxed-code-execution capabilities noted there.

## 1. Install prerequisite OS packages

The `.deb`'s `Depends` is just `libc6`; `Recommends` pulls in the GTK/Cairo/NSS libraries Playwright's Chromium/Firefox rendering needs.

```
apt-get update && apt-get install ./searchengine_<version>_amd64.deb
# or, to skip the Playwright/GTK stack entirely:
apt-get install --no-install-recommends ./searchengine_<version>_amd64.deb
```

Skip them with `--no-install-recommends` if crawls will only ever use the default plain-HTTP fetch (no JS rendering).

## 2. Install the .deb package

Handled by the package. `dpkg` installs the three binaries (`/usr/bin/searchengine-{search,admin,crawl}`), their systemd units, and a template `/etc/searchengine/searchengine.env`.

```
dpkg -i searchengine_<version>_amd64.deb
# postinst prints: "searchengine installed. Services: searchengine-search, searchengine-admin, searchengine-crawl"
```

`postinst` creates a system user `searchengine` (no shell, no home dir), chmods `searchengine.env` to `640` for that user, creates `/var/lib/searchengine` and its `tmp` subdirectory (Playwright's `TMPDIR`), and enables/starts all three services.

## 3. Expect the services to come up unconfigured

Expected, not a bug. The shipped `searchengine.env` points at a local SQLite file with no admin user yet, so admin sign-in fails closed until one is created (step 7).

```
systemctl status searchengine-search searchengine-admin searchengine-crawl
journalctl -u searchengine-admin -n 50
```

## 4. Set the database driver and connection string

Edit `/etc/searchengine/searchengine.env` (mode `640`, already owned by `searchengine:searchengine`). All three binaries share one database, pinged as part of `/healthz`.

```
# SQLite (default, already works out of the box):
DB_DRIVER=sqlite
DB_DSN=file:/var/lib/searchengine/search.db?cache=shared

# Postgres (dev-deployment style) -- "pgx", the actual database/sql
# driver name github.com/jackc/pgx/v5/stdlib registers, NOT "postgres"
# (a bare `sql.Open("postgres", ...)` fails with "unknown driver"; the
# dialect layer accepts either name, but only "pgx" actually opens a
# connection):
DB_DRIVER=pgx
DB_DSN=postgres://user:pass@dbhost:5432/searchengine?sslmode=require
```

## 5. Set the optional hardening secrets

Recommended. Both default to blank/disabled if skipped.

```
CRAWL_INTERNAL_TOKEN=$(openssl rand -hex 32)
SETTINGS_ENCRYPTION_KEY=$(openssl rand -hex 32)
```

## 6. Restart and verify

`EnvironmentFile` changes need a restart -- systemd doesn't hot-reload it.

```
systemctl restart searchengine-search searchengine-admin searchengine-crawl
systemctl is-active searchengine-search searchengine-admin searchengine-crawl
curl -s http://127.0.0.1:8080/healthz
curl -s http://127.0.0.1:8081/healthz
curl -s http://127.0.0.1:8082/healthz
```

## 7. Create the initial admin user

There is no hardcoded admin account -- an admin is just a regular account
(a `users` row) with its `is_admin` flag set, same as any other account
`/admin/users` manages, and any number of accounts can hold it. Until at
least one exists, `/admin` sign-in always refuses -- the most common reason
a fresh install "looks broken." `packaging/create-admin.sh` seeds the
first one directly against the database (there's no admin session yet to
create it through the API); needs `apache2-utils` for `htpasswd` (bcrypt
hashing) and, depending on `DB_DRIVER`, either `sqlite3` (the default) or
the `postgresql-client` package for `psql`:

```
apt-get install apache2-utils sqlite3   # sqlite3 for the default DB_DRIVER=sqlite, + postgresql-client instead if DB_DRIVER=pgx
./packaging/create-admin.sh admin
# prompts for a password (bcrypt-hashed locally, never logged), then
# inserts a users row with is_admin=true using the DB_DRIVER/DB_DSN
# already restarted-into in step 6
```

Run it again with a different username any time to add another admin --
nothing about it is a one-time-only operation. Reset a forgotten admin
password, or demote/promote an existing account, from `/admin/users` once
at least one admin can sign in; `packaging/create-admin.sh` is only for
when none can yet.

## 8. Install and configure nginx

The `.deb` never touches nginx. All three services bind loopback-only by design; nginx is the only intended entry point. `packaging/nginx/searchengine.conf` is the tracked source of truth for the routing split -- see [packaging/nginx/README.md](https://github.com/M0WA/SE/blob/main/packaging/nginx/README.md) for the `/admin` string-prefix-match gotcha.

```
apt-get install nginx certbot python3-certbot-nginx
sed 's/<DOMAIN>/your.domain.example/' packaging/nginx/searchengine.conf > /etc/nginx/sites-available/searchengine
rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/searchengine /etc/nginx/sites-enabled/searchengine
nginx -t && systemctl reload nginx
systemctl enable --now nginx
```

## 9. Issue the TLS certificate

Requires DNS for the domain to already resolve here.

```
certbot --nginx -d your.domain.example --non-interactive --agree-tos --register-unsafely-without-email --redirect
systemctl status certbot.timer
certbot certificates
```

## 10. Verify the public site end-to-end

```
curl -sk https://your.domain.example/healthz
curl -sk https://your.domain.example/admin/    # should hit admin-server's login page
```

## 11. (Optional) Host/nginx/Postgres monitoring

Independent of the package -- forwards to an IONOS monitoring pipeline. See [packaging/prometheus/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus/README.md).

```
apt-get install prometheus prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter
cp packaging/prometheus/prometheus.default /etc/default/prometheus
cp packaging/prometheus/prometheus-node-exporter.default /etc/default/prometheus-node-exporter
cp packaging/prometheus/prometheus-nginx-exporter.default /etc/default/prometheus-nginx-exporter
cp packaging/nginx/stub_status.conf /etc/nginx/conf.d/stub_status.conf && nginx -t && systemctl reload nginx
cp packaging/prometheus/postgres-exporter-datasource.sh /usr/local/bin/ && chmod 755 /usr/local/bin/postgres-exporter-datasource.sh
mkdir -p /etc/systemd/system/prometheus-postgres-exporter.service.d
cp packaging/prometheus/prometheus-postgres-exporter.override.conf /etc/systemd/system/prometheus-postgres-exporter.service.d/override.conf
psql "$DB_DSN" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"   # Postgres only
systemctl daemon-reload
systemctl enable --now prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter prometheus
promtool check config /etc/prometheus/prometheus.yml
```

## 12. (Optional) Stand up SearXNG

Docker-based, for the chat feature's live web search. See [packaging/searxng/README.md](https://github.com/M0WA/SE/blob/main/packaging/searxng/README.md). Includes `searchengine`, a custom SearXNG engine ([packaging/searxng-engine/README.md](https://github.com/M0WA/SE/blob/main/packaging/searxng-engine/README.md)) folding this deployment's own indexed corpus into the blended results.

```
apt-get install docker.io docker-compose
mkdir -p /opt/searxng
cp packaging/searxng/docker-compose.yml packaging/searxng/settings.yml /opt/searxng/
cp packaging/searxng-engine/searchengine_index.py /opt/searxng/
cd /opt/searxng
sed -i "s/REPLACE_WITH_OPENSSL_RAND_HEX_32/$(openssl rand -hex 32)/" settings.yml
sed -i "s/REPLACE_WITH_SEARCH_INTERNAL_API_KEY/$(openssl rand -hex 32)/" settings.yml
cp packaging/searxng/searxng.service /etc/systemd/system/searxng.service
systemctl daemon-reload
systemctl enable --now searxng
# set SEARCH_INTERNAL_API_KEY in /etc/searchengine/searchengine.env to the same value as internal_api_key above, then:
systemctl restart searchengine-search
curl -s 'http://127.0.0.1:8888/search?q=test&format=json' | head -c 300
```

## 13. (Optional) Wire SearXNG metrics into Prometheus

Only relevant if both steps 11 and 12 were done.

```
# settings.yml: general.open_metrics: <password>
# prometheus.yml: sed 's/<SEARXNG_METRICS_PASSWORD>/<same password>/'
docker compose restart searxng
```

## 14. (Optional) GPU host monitoring

On the separate GPU host running vLLM (step 17). Same IONOS pipeline as step 11, a second independent Prometheus agent. See [packaging/prometheus-gpu/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md).

```
apt-get install prometheus prometheus-node-exporter
cp packaging/prometheus/prometheus.default /etc/default/prometheus
cp packaging/prometheus/prometheus-node-exporter.default /etc/default/prometheus-node-exporter
docker run -d --name dcgm-exporter --restart unless-stopped --gpus all --cap-add SYS_ADMIN -p 127.0.0.1:9400:9400 nvcr.io/nvidia/k8s/dcgm-exporter:latest
systemctl daemon-reload
systemctl enable --now prometheus-node-exporter prometheus
promtool check config /etc/prometheus/prometheus.yml
```

## 15. (Optional) Import the Grafana dashboards

Only relevant if some/all of steps 11, 13, and 14 were done. See [packaging/grafana/README.md](https://github.com/M0WA/SE/blob/main/packaging/grafana/README.md).

```
for f in packaging/grafana/dashboards/*.json; do
  curl -s -X POST -H "Authorization: Bearer <GRAFANA_API_TOKEN>" -H "Content-Type: application/json" \
    -d "{\"dashboard\": $(cat "$f"), \"overwrite\": true}" \
    "https://<your-grafana-instance>/api/dashboards/db"
done
```

## 16. (Optional) Configure the built-in MCP tool servers

The `.deb` already installs `/usr/bin/searchengine-mcp-{web,datetime,sandbox,files}` -- nothing to copy or chmod. None is a systemd service: search-server/admin-server spawn one on demand as a stdio subprocess whenever an MCP server row on [MCP servers](mcp-servers.md) points at it. `mcp-sandbox` additionally needs the `searchengine` user in the host's `docker` group (`usermod -aG docker searchengine`, then restart the services) -- a deliberate privilege elevation the package never grants automatically. See [MCP servers](mcp-servers.md) for what each built-in server does and how to add a row for it.

## 17. (Optional) Configure an embedding and chat inference backend

[Embedding endpoints](embedding-endpoint-detail.md) and [Chat settings](chat-settings.md) each point at a plain OpenAI-compatible HTTP endpoint -- any such API works, self-hosted or third-party. As a concrete reference, the project's own dev deployment points both at self-hosted [vLLM](https://github.com/vllm-project/vllm) processes on a separate GPU host:

```
# Embeddings: Qwen/Qwen3-VL-Embedding-8B -- --gpu-memory-utilization is
# required whenever a second vLLM process (the chat one below, or any
# other) already shares the same GPU: the flag's default (~0.9) sizes
# itself against the GPU's TOTAL memory, not what's actually free, so it
# fails to start ("Free memory ... is less than desired GPU memory
# utilization") the moment another process has already claimed most of
# the card. Size it to fit in whatever's actually free at the time.
vllm serve Qwen/Qwen3-VL-Embedding-8B --runner pooling --convert embed --gpu-memory-utilization 0.2

# Chat -- --enable-auto-tool-choice and --tool-call-parser are required for
# MCP servers to work at all: without them the model is never offered tool
# calls and every configured server sits unused.
vllm serve RedHatAI/Qwen2.5-72B-Instruct-FP8-dynamic --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser hermes
```

## 18. (Optional) Harden the host firewall

Every service above already binds `127.0.0.1` or a private-LAN IP except
nginx and sshd, so a host firewall is a second, independent layer rather
than the only thing preventing exposure -- see
[`packaging/firewall/README.md`](https://github.com/M0WA/SE/blob/main/packaging/firewall/README.md)
for the exact policy (SSH everywhere; nginx also public on the webserver
host; a GPU/inference host's own embedding/chat ports stay private-LAN-only
either way) and, critically, its verify-before-persist steps -- a
misapplied rule can permanently lock out the only way into a host with no
console/KVM fallback, so never skip confirming the change from a fresh
connection before making it survive a reboot.

## 19. Final smoke test of the whole stack

```
systemctl is-active searchengine-search searchengine-admin searchengine-crawl nginx
curl -sk https://your.domain.example/
curl -sk https://your.domain.example/admin/
curl -sk https://your.domain.example/healthz
```

---
← [Infrastructure options (IONOS)](infrastructure.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Docker Compose installation](docker-installation.md) →
