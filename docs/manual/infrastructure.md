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

## Provisioning with ionosctl

The pieces above that are ordinary IONOS Cloud API resources (the webserver VM, the Postgres database) can be scripted end to end with [`ionosctl`](https://github.com/ionos-cloud/ionosctl), IONOS's official CLI, instead of clicking through the DCD web console. Install it and authenticate first:

```
curl -sL https://github.com/ionos-cloud/ionosctl/releases/latest/download/install.sh | bash
ionosctl login   # or set IONOS_USERNAME/IONOS_PASSWORD, or IONOS_TOKEN for a token
```

Every resource below lives in a datacenter -- create one first:

```
ionosctl datacenter create --name searchengine --location de/fra --wait-for-request
```

Note the returned `DatacenterId` (or `ionosctl datacenter list`) -- every command below needs it.

**Webserver VM**: a server, a boot volume, and a public network, same as any plain Compute Engine host:

```
ionosctl server create --datacenter-id <DATACENTER_ID> --name se-web \
  --cores 4 --ram 8192 --cpu-family INTEL_SKYLAKE --wait-for-request

ionosctl lan create --datacenter-id <DATACENTER_ID> --name public --public --wait-for-request

ionosctl volume create --datacenter-id <DATACENTER_ID> --server-id <SERVER_ID> \
  --name se-web-boot --size 50 --image-alias ubuntu:22.04 --licence-type LINUX \
  --ssh-key-paths ~/.ssh/id_ed25519.pub --wait-for-request

ionosctl nic create --datacenter-id <DATACENTER_ID> --server-id <SERVER_ID> \
  --lan-id <LAN_ID> --name eth0 --dhcp --wait-for-request

ionosctl nic list --datacenter-id <DATACENTER_ID> --server-id <SERVER_ID>   # get the assigned IP
```

SSH in once the IP is up, then follow [Installation](installation.md) from step 1.

**Database**: `ionosctl dbaas postgres cluster create` provisions a managed Postgres instance attached to the datacenter's LAN:

```
ionosctl dbaas postgres cluster create --name searchengine-db --location de/fra \
  --datacenter-id <DATACENTER_ID> --lan-id <LAN_ID> --cidr 10.7.221.0/24 \
  --postgres-version 16 --instances 1 --cores 2 --ram 4096 \
  --storage-size 20 --storage-type SSD \
  --db-user searchengine --db-password '<strong random password>' \
  --wait-for-request
```

Feed the resulting host/port into `DB_DSN` (see [Environment variables](environment-variables.md)). `ionosctl dbaas mariadb cluster create` follows a similar shape for the MariaDB alternative, but that product's CLI surface has changed across `ionosctl` releases -- run `ionosctl dbaas mariadb cluster create --help` to confirm current flags before scripting it.

> **Worth knowing:**
> - None of this is required -- every piece above has a self-hosted or alternate-provider equivalent documented elsewhere in this manual. This page just names the specific IONOS product the reference deployment actually uses.
> - GPU Servers and the Monitoring Service aren't self-service Cloud API resources the way a plain VM or a DBaaS cluster is -- both are ordered/enabled through the DCD web console (GPU quota needs enabling on the contract first; Monitoring Service needs its dashboard to hand you the `remote_write` endpoint and token). Once a GPU server is provisioned, though, it's just another Linux host reachable over SSH -- the rest of [Installation](installation.md) applies the same way.
> - Every `ionosctl <resource> create` command above accepts `--help` to list its current full flag set -- useful since exact flags/defaults (cores, RAM, image aliases, postgres-version) drift across `ionosctl` and IONOS API versions faster than this page can track.

---
[↑ Manual home](README.md) &nbsp;·&nbsp; [Installation](installation.md) →
