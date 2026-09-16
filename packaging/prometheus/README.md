# Prometheus agent (host + nginx metrics -> IONOS monitoring)

`se.mo-sys.de` runs three Debian-packaged pieces to forward basic host and
nginx metrics to an IONOS Monitoring Service pipeline via Prometheus
`remote_write` -- no local TSDB, no local querying:

- `prometheus` in **agent mode** -- scrapes the two exporters below and
  forwards everything to the IONOS pipeline's push endpoint.
- `prometheus-node-exporter` -- cpu, memory, disk, disk I/O and network,
  via its default collectors. No extra flags needed.
- `prometheus-nginx-exporter` -- nginx connection/request counters, scraped
  from nginx's own `stub_status` page.

This is independent of the `searchengine` binaries/package -- it's host-level
observability, installed and configured directly on the VM, not shipped in
the `searchengine` .deb.

## Install

```sh
apt-get install prometheus prometheus-node-exporter prometheus-nginx-exporter
```

## Configure

```sh
cp prometheus.default /etc/default/prometheus
cp prometheus-node-exporter.default /etc/default/prometheus-node-exporter
cp prometheus-nginx-exporter.default /etc/default/prometheus-nginx-exporter
cp ../nginx/stub_status.conf /etc/nginx/conf.d/stub_status.conf
nginx -t && systemctl reload nginx
```

Copy `prometheus.yml`, filling in the real values from the IONOS DCD
(Observability > Monitoring > your pipeline) and a **host-unique**
`site` name:

```sh
sed -e 's/<IONOS_METRICS_ENDPOINT>/<pipeline id>-metrics.<pipeline uid>.monitoring.<region>.ionos.com/' \
    -e 's/<IONOS_APIKEY>/<real key here>/' \
    -e 's/<SITE_NAME>/<this-hosts-name>/' \
    prometheus.yml > /etc/prometheus/prometheus.yml
chown root:prometheus /etc/prometheus/prometheus.yml
chmod 640 /etc/prometheus/prometheus.yml
```

`<SITE_NAME>` must be different for every host pushing into the same
IONOS pipeline -- see `prometheus.yml`'s comment on `external_labels`.

**Never commit the filled-in `prometheus.yml`** -- the API key it carries is
a write credential for the monitoring pipeline. The tracked copy in this
directory keeps `<IONOS_METRICS_ENDPOINT>`/`<IONOS_APIKEY>` as placeholders;
only the deployed `/etc/prometheus/prometheus.yml` (root:prometheus, mode
640) has the real values.

```sh
systemctl daemon-reload
systemctl enable --now prometheus-node-exporter prometheus-nginx-exporter prometheus
```

## Verify

```sh
promtool check config /etc/prometheus/prometheus.yml
systemctl is-active prometheus prometheus-node-exporter prometheus-nginx-exporter
curl -s http://127.0.0.1:9090/metrics | grep prometheus_remote_storage_samples_failed_total
```

`prometheus_remote_storage_samples_failed_total` should stay at `0` (or stop
climbing) once the agent is up -- that's the counter that catches a bad
APIKEY/URL or a network problem reaching the IONOS endpoint.

## Notes

- All three services bind `127.0.0.1` only -- same reasoning as
  `../nginx/README.md`'s Notes: there's no firewall on a typical bare VM, so
  anything bound `0.0.0.0` is directly internet-reachable the moment it
  starts. Nothing outside this host needs to scrape these directly; only the
  local agent does, and it then pushes outward itself.
- `stub_status` (`../nginx/stub_status.conf`) is a separate server block on
  `127.0.0.1:8090`, entirely outside the public `<DOMAIN>` server blocks in
  `../nginx/searchengine.conf` -- it's not reachable through the public
  listener under any path, so it doesn't interact with the string-prefix
  routing gotcha described in the root `CLAUDE.md`.
- This Debian build of Prometheus (2.53.3) has no separate `agent`
  subcommand -- agent mode is entered via `--enable-feature=agent` instead
  (see `prometheus.default`'s comment).
- If the pipeline is ever recreated (new endpoint/key), only
  `/etc/prometheus/prometheus.yml` on the host needs updating -- nothing
  else here references the endpoint or key.
