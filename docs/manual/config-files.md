# Config files

[← Manual home](README.md)

Every file this project's packaging touches or ships, and how it gets installed.

## The full list

| Repo path | Deployed path | Install | Configures |
|---|---|---|---|
| `packaging/searchengine.env` | `/etc/searchengine/searchengine.env` | Auto (`.deb` conffile, edits preserved across upgrades) | Runtime config for all three binaries -- see [Environment variables](environment-variables.md). |
| `packaging/create-admin.sh` | n/a -- run directly from a repo checkout, never installed anywhere | Manual, not shipped in the `.deb` | Seeds the first `is_admin=true` account directly into the database. See [Installation](installation.md) step 7. |
| `packaging/searchengine-{search,admin,crawl}.service` | `/lib/systemd/system/searchengine-*.service` | Auto | systemd units for the three binaries. |
| `packaging/debian/{control,postinst,prerm}` | n/a -- become the `.deb`'s own metadata/maintainer scripts | Auto (build-time) | Package metadata, dependencies, install/removal behavior. |
| `packaging/nginx/searchengine.conf` | `/etc/nginx/sites-available/searchengine` | Manual | nginx reverse-proxy routing split between search-server and admin-server. See [packaging/nginx/README.md](https://github.com/M0WA/SE/blob/main/packaging/nginx/README.md). |
| `packaging/nginx/stub_status.conf` | `/etc/nginx/conf.d/stub_status.conf` | Manual | Loopback-only nginx `stub_status` page for `prometheus-nginx-exporter`. |
| `packaging/prometheus/prometheus.yml` | `/etc/prometheus/prometheus.yml` | Manual | Prometheus agent-mode scrape/`remote_write` config. See [packaging/prometheus/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus/README.md). |
| `packaging/prometheus/prometheus.default` | `/etc/default/prometheus` | Manual | `prometheus.service` ARGS (agent mode). |
| `packaging/prometheus/prometheus-nginx-exporter.default` | `/etc/default/prometheus-nginx-exporter` | Manual | Exporter ARGS. |
| `packaging/prometheus/prometheus-node-exporter.default` | `/etc/default/prometheus-node-exporter` | Manual | Exporter ARGS. |
| `packaging/prometheus/prometheus-postgres-exporter.override.conf` | `/etc/systemd/system/prometheus-postgres-exporter.service.d/override.conf` | Manual | systemd drop-in wiring `DB_DSN` into the postgres exporter. |
| `packaging/prometheus/postgres-exporter-datasource.sh` | `/usr/local/bin/postgres-exporter-datasource.sh` | Manual | Wrapper re-exporting `DB_DSN` as `DATA_SOURCE_NAME`. |
| `packaging/searxng/docker-compose.yml` | `/opt/searxng/docker-compose.yml` | Manual | Docker Compose service definition for self-hosted SearXNG. See [packaging/searxng/README.md](https://github.com/M0WA/SE/blob/main/packaging/searxng/README.md). |
| `packaging/searxng/settings.yml` | `/opt/searxng/settings.yml` | Manual | SearXNG application config. |
| `packaging/searxng/searxng.service` | `/etc/systemd/system/searxng.service` | Manual | systemd unit wrapping `docker compose up`/`down`. |
| `packaging/prometheus-gpu/prometheus.yml` | `/etc/prometheus/prometheus.yml` (on the GPU host) | Manual | Prometheus agent-mode scrape/`remote_write` config for the GPU host. See [packaging/prometheus-gpu/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md). |
| `packaging/grafana/dashboards/*.json` | Imported via Grafana's API | Manual | Tracked source of truth for every Grafana dashboard. See [packaging/grafana/README.md](https://github.com/M0WA/SE/blob/main/packaging/grafana/README.md). |

---
← [Environment variables](environment-variables.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Signing In](login.md) →
