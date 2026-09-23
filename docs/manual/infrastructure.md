# Infrastructure options (IONOS)

[← Manual home](README.md)

This project is infrastructure-agnostic -- any host that can run a `.deb`, any Postgres/MySQL/SQLite database, any OpenAI-compatible inference endpoint. The reference deployment behind this manual's screenshots runs on IONOS; each piece maps to one IONOS product if you'd rather not source your own:

## Webserver (nginx + the three searchengine binaries)

A plain virtual machine -- [IONOS Cloud Compute Engine](https://cloud.ionos.de/compute). The host [Installation](installation.md) installs the `.deb`, nginx, and TLS certificate onto; any Debian/Ubuntu VM works, IONOS or otherwise.

## GPU inference (self-hosted vLLM)

An [IONOS GPU Server](https://www.ionos.de/server/gpu-server) -- a dedicated GPU host running the two vLLM processes (the reference deployment uses one NVIDIA H200 NVL for both embedding and chat inference). See [Installation](installation.md) step 17 and [packaging/prometheus-gpu/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md) for monitoring it.

## Hosted AI instead of self-hosting

If managing GPU capacity isn't for you, [IONOS AI Model Hub](https://cloud.ionos.de/managed/ai-model-hub) is a managed, hosted-model alternative to running vLLM yourself. [Embedding endpoints](embedding-endpoint-detail.md) and [Chat settings](chat-settings.md) both just need a plain OpenAI-compatible HTTP endpoint, so pointing at Model Hub instead needs no code changes, only a different Base URL/API key.

## Database

A managed alternative to self-hosting: [IONOS DBaaS for PostgreSQL](https://cloud.ionos.de/managed/dbaas/postgresql) (`DB_DRIVER=postgres`) or [IONOS DBaaS for MariaDB](https://cloud.ionos.de/managed/dbaas/mariadb) (MySQL-compatible, `DB_DRIVER=mysql`) -- see [Environment variables](environment-variables.md). SQLite (the default, no external database) remains simplest for a small single-host deployment.

## Monitoring pipeline

The [IONOS Monitoring Service](https://cloud.ionos.de/managed/monitoring-service) is the `remote_write` target [packaging/prometheus/](https://github.com/M0WA/SE/blob/main/packaging/prometheus/README.md) and [packaging/prometheus-gpu/](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md) push host, nginx, Postgres, and GPU/vLLM metrics into -- see [Installation](installation.md) steps 11 and 14, and [packaging/grafana/](https://github.com/M0WA/SE/blob/main/packaging/grafana/README.md) for its dashboards.

> **Worth knowing:**
> - None of this is required -- every piece above has a self-hosted or alternate-provider equivalent documented elsewhere in this manual. This page just names the specific IONOS product the reference deployment actually uses.

---
[↑ Manual home](README.md) &nbsp;·&nbsp; [Installation](installation.md) →
