# Prometheus agent (host + nginx + postgres metrics -> IONOS monitoring)

`se.mo-sys.de` runs four Debian-packaged pieces to forward host, nginx, and
Postgres metrics to an IONOS Monitoring Service pipeline via Prometheus
`remote_write` -- no local TSDB, no local querying:

- `prometheus` in **agent mode** -- scrapes the exporters below and
  forwards everything to the IONOS pipeline's push endpoint.
- `prometheus-node-exporter` -- cpu, memory, disk, disk I/O and network,
  via its default collectors.
- `prometheus-nginx-exporter` -- nginx connection/request counters, scraped
  from nginx's own `stub_status` page.
- `prometheus-postgres-exporter` -- connections, transactions, cache hit
  ratio, replication lag from the Postgres server the `searchengine`
  binaries talk to (a separate host, not this VM) -- reused via
  `searchengine`'s own `DB_DSN`, see below.

Independent of the `searchengine` binaries/package -- host-level
observability installed directly on the VM, not shipped in the `.deb`.

`prometheus.yml`'s `searxng` job is a fifth source, but not a fifth package:
SearXNG runs as a separate Docker deployment (`/opt/searxng`), entirely
outside this repo. It exposes an OpenMetrics endpoint (per-engine request
count, response time, result count, reliability) once
`general.open_metrics: <password>` is set in its own `settings.yml` -- that
password becomes this scrape job's `basic_auth.password`. Nothing to
install here; just point the scrape config at it.

## Install

```sh
apt-get install prometheus prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter
```

## Configure

```sh
cp prometheus.default /etc/default/prometheus
cp prometheus-node-exporter.default /etc/default/prometheus-node-exporter
cp prometheus-nginx-exporter.default /etc/default/prometheus-nginx-exporter
cp ../nginx/stub_status.conf /etc/nginx/conf.d/stub_status.conf
nginx -t && systemctl reload nginx
```

Postgres exporter: reuses `searchengine`'s own `DB_DSN` (already a working
connection) instead of a second credential on disk. Needs a systemd
drop-in, not just a `/etc/default` file, since it must load
`/etc/searchengine/searchengine.env` and rename `DB_DSN` to the
`DATA_SOURCE_NAME` the exporter expects:

```sh
cp postgres-exporter-datasource.sh /usr/local/bin/postgres-exporter-datasource.sh
chmod 755 /usr/local/bin/postgres-exporter-datasource.sh
mkdir -p /etc/systemd/system/prometheus-postgres-exporter.service.d
cp prometheus-postgres-exporter.override.conf /etc/systemd/system/prometheus-postgres-exporter.service.d/override.conf
```

See that override file's own comment for why `--disable-settings-metrics`
is required, not optional -- omitting it leaks the DSN's password into
`journalctl` on the first scrape error.

The `stat_statements` collector (query-time metrics) needs the extension
created once per database -- not part of `postinst`/this override, since it's
a Postgres-side change, not a host package:

```sh
psql "$DB_DSN" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"
```

On se.mo-sys.de's Postgres (a managed instance) this needed no restart --
it already preloads it via `shared_preload_libraries`. A self-hosted
Postgres without that preload needs `shared_preload_libraries =
'pg_stat_statements'` set and a restart *first* -- don't assume
`CREATE EXTENSION` alone is enough.

Copy `prometheus.yml`, filling in the real values from the IONOS DCD
(Observability > Monitoring > your pipeline) and a **host-unique**
`site` name:

```sh
sed -e 's/<IONOS_METRICS_ENDPOINT>/<pipeline id>-metrics.<pipeline uid>.monitoring.<region>.ionos.com/' \
    -e 's/<IONOS_APIKEY>/<real key here>/' \
    -e 's/<SITE_NAME>/<this-hosts-name>/' \
    -e 's/<SEARXNG_METRICS_PASSWORD>/<same value as SearXNG'"'"'s settings.yml open_metrics>/' \
    prometheus.yml > /etc/prometheus/prometheus.yml
chown root:prometheus /etc/prometheus/prometheus.yml
chmod 640 /etc/prometheus/prometheus.yml
```

Skip the `<SEARXNG_METRICS_PASSWORD>` substitution (leave the job as-is,
or delete it) on a host that doesn't run SearXNG.

`<SITE_NAME>` must be different for every host pushing into the same
IONOS pipeline -- see `prometheus.yml`'s comment on `external_labels`.

**Never commit the filled-in `prometheus.yml`** -- its API key is a write
credential for the monitoring pipeline. Keep `<IONOS_METRICS_ENDPOINT>`/
`<IONOS_APIKEY>` as placeholders in the tracked copy; only the deployed
`/etc/prometheus/prometheus.yml` (root:prometheus, mode 640) has real values.

```sh
systemctl daemon-reload
systemctl enable --now prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter prometheus
```

## Verify

```sh
promtool check config /etc/prometheus/prometheus.yml
systemctl is-active prometheus prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter
curl -s http://127.0.0.1:9187/metrics | grep -E '^pg_up|^pg_exporter_last_scrape_error'
curl -s http://127.0.0.1:9090/metrics | grep prometheus_remote_storage_samples_failed_total
```

`pg_up` should be `1` and `pg_exporter_last_scrape_error` should be `0`.

`prometheus_remote_storage_samples_failed_total` should stay at `0` (or stop
climbing) once the agent is up -- that's the counter that catches a bad
APIKEY/URL or a network problem reaching the IONOS endpoint.

## Notes

- All four services bind `127.0.0.1` only -- same reasoning as
  `../nginx/README.md`'s Notes: no firewall on a typical bare VM, so a
  `0.0.0.0` bind is directly internet-reachable. Only the local agent needs
  to scrape these; it then pushes outward itself.
- `stub_status` (`../nginx/stub_status.conf`) is a separate block on
  `127.0.0.1:8090`, outside the public `<DOMAIN>` blocks in
  `../nginx/searchengine.conf` -- not reachable through the public listener
  under any path, so it doesn't interact with the string-prefix routing
  gotcha in the root `CLAUDE.md`.
- This Debian build of Prometheus (2.53.3) has no separate `agent`
  subcommand -- agent mode is entered via `--enable-feature=agent` (see
  `prometheus.default`'s comment).
- If the pipeline is ever recreated (new endpoint/key), only
  `/etc/prometheus/prometheus.yml` needs updating.
- The postgres exporter's `apt` package ships with `User=prometheus` and
  its own `/etc/default/...`; the override here replaces both
  `EnvironmentFile=` and `ExecStart=` entirely (systemd drop-in syntax for
  "replace, don't append") rather than editing the package's unit file, so
  an `apt upgrade` can never silently drop this configuration.
- `EnvironmentFile=/etc/searchengine/searchengine.env` on the exporter's
  unit works regardless of that file's permissions relative to
  `User=prometheus` -- systemd (root, PID 1) reads `EnvironmentFile=`
  *before* dropping privileges, same as `searchengine-admin.service`
  already does. No group/ACL changes needed to pick up `DB_DSN`.
- If Postgres is recreated with a different password, only that one file
  changes -- the exporter reuses it live, same as the three `searchengine`
  services.
