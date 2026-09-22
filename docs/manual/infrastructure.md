# Infrastructure options (IONOS)

[← Manual home](README.md)

This project is infrastructure-agnostic -- any host that can run a `.deb`, any Postgres/MySQL/SQLite database, any OpenAI-compatible inference endpoint. The reference deployment this manual's screenshots and examples are drawn from runs on IONOS, and each piece maps directly onto one IONOS product if you'd rather not source your own:

## Webserver (nginx + the three searchengine binaries)

A plain virtual machine -- [IONOS Cloud Compute Engine](https://cloud.ionos.de/compute). This is the host [Installation](installation.md) installs the `.deb`, nginx, and TLS certificate onto; any Debian/Ubuntu VM works, IONOS or otherwise.

## GPU inference (self-hosted vLLM)

An [IONOS GPU Server](https://www.ionos.de/server/gpu-server) -- a dedicated GPU host running the two vLLM processes ([Config files](config-files.md)'s reference deployment uses one NVIDIA H200 NVL for both embedding and chat inference). See [Installation](installation.md) step 17 and [packaging/prometheus-gpu/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md) for monitoring it.

## Hosted AI instead of self-hosting

If managing your own GPU capacity isn't something you want to take on, [IONOS AI Model Hub](https://cloud.ionos.de/managed/ai-model-hub) is a managed, hosted-model alternative to running vLLM yourself -- since [Embedding endpoints](embedding-endpoint-detail.md) and [Chat settings](chat-settings.md) both just need a plain OpenAI-compatible HTTP endpoint, pointing them at a hosted Model Hub endpoint instead of a self-hosted vLLM process needs no code changes, only a different Base URL/API key.

## Database

A managed database instead of a self-hosted one: [IONOS DBaaS for PostgreSQL](https://cloud.ionos.de/managed/dbaas/postgresql) (`DB_DRIVER=postgres`) or [IONOS DBaaS for MariaDB](https://cloud.ionos.de/managed/dbaas/mariadb) (MySQL-compatible, `DB_DRIVER=mysql`) -- see [Environment variables](environment-variables.md). SQLite (the default, no external database at all) remains the simplest option for a small single-host deployment.

## Monitoring pipeline

The [IONOS Monitoring Service](https://cloud.ionos.de/managed/monitoring-service) is the `remote_write` target [packaging/prometheus/](https://github.com/M0WA/SE/blob/main/packaging/prometheus/README.md) and [packaging/prometheus-gpu/](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md) push host, nginx, Postgres, and GPU/vLLM metrics into -- see [Installation](installation.md) steps 11 and 14, and [packaging/grafana/](https://github.com/M0WA/SE/blob/main/packaging/grafana/README.md) for the dashboards built on top of it.

> **Worth knowing:**
> - None of this is required -- every piece above has a self-hosted or alternate-provider equivalent already documented elsewhere in this manual (SQLite instead of a managed database, a self-hosted vLLM instead of AI Model Hub, no monitoring pipeline at all). This page just names the specific IONOS product each piece of the reference deployment actually uses.

---
[↑ Manual home](README.md) &nbsp;·&nbsp; [Installation](installation.md) →
