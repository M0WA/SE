# Infrastructure options (IONOS)

[← Manual home](README.md)

This project is infrastructure-agnostic -- any host that can run a `.deb`, any Postgres/MySQL/SQLite database, any OpenAI-compatible inference endpoint. The reference deployment behind this manual's screenshots runs on IONOS; each piece maps to one IONOS product if you'd rather not source your own:

## Webserver (nginx + the three searchengine binaries)

A plain virtual machine -- [IONOS Cloud Compute Engine](https://cloud.ionos.de/compute). The host [Installation](installation.md) installs the `.deb`, nginx, and TLS certificate onto; any Debian/Ubuntu VM works, IONOS or otherwise.

## GPU inference (self-hosted vLLM)

An [IONOS GPU Server](https://www.ionos.de/server/gpu-server) -- a dedicated GPU host running the two vLLM processes (the reference deployment uses one NVIDIA H200 NVL for both embedding and chat inference). See [Installation](installation.md) step 17 and [packaging/prometheus-gpu/README.md](https://github.com/M0WA/SE/blob/main/packaging/prometheus-gpu/README.md) for monitoring it.

## Hosted AI instead of self-hosting

If managing GPU capacity isn't for you, [IONOS AI Model Hub](https://cloud.ionos.de/managed/ai-model-hub) is a managed, hosted-model alternative to running vLLM yourself. [Embedding endpoints](embedding-endpoint-detail.md) and [Chat settings](chat-settings.md) both just need a plain OpenAI-compatible HTTP endpoint, so pointing at Model Hub instead needs no code changes, only a different Base URL/API key.

Model Hub's own inference endpoint is `https://inference.<location>.ionos.com/v1/...` (location written with a dash, e.g. `de-txl`/`de-fra`, unlike the `de/txl` form used elsewhere on this page) -- an ordinary OpenAI-compatible API, so it drops straight into a Base URL field:

```
curl https://inference.de-txl.ionos.com/v1/models \
  -H "Authorization: Bearer $IONOS_TOKEN"
```

Calling that endpoint (listing/using models) works with any account token, including the contract's own main/owner user. **Managing** Model Hub access itself is a separate thing: the `accessAndManageAiModelHub` group privilege can only be held by a sub-user's group, never granted to (or by) the contract's main/owner user -- attempting it from the owner account fails even though the owner can call the inference endpoint above just fine. `ionosctl` has no command group for Model Hub as of this writing, and neither `ionosctl compute group create` nor `update` exposes a flag for this specific privilege, so setting it needs a raw Cloud API call:

```
curl -X PUT "https://api.ionos.com/cloudapi/v6/um/groups/<GROUP_ID>" \
  -H "Authorization: Bearer $IONOS_TOKEN" -H "Content-Type: application/json" \
  -d '{"properties": {"accessAndManageAiModelHub": true}}'
```

(IONOS Cloud API v6 groups only accept `PUT` for updates -- `OPTIONS` on the endpoint reports no `PATCH` -- but merge rather than replace, so sending just this one property leaves every other privilege on the group untouched.)

See [Provisioning with ionosctl](#provisioning-with-ionosctl) below for creating that sub-user and group.

## Database

A managed alternative to self-hosting: [IONOS DBaaS for PostgreSQL](https://cloud.ionos.de/managed/dbaas/postgresql) (`DB_DRIVER=pgx`) or [IONOS DBaaS for MariaDB](https://cloud.ionos.de/managed/dbaas/mariadb) (MySQL-compatible, `DB_DRIVER=mysql`) -- see [Environment variables](environment-variables.md). SQLite (the default, no external database) remains simplest for a small single-host deployment.

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
ionosctl datacenter create --name searchengine --location de/fra --wait
```

Note the returned `DatacenterId` (or `ionosctl datacenter list`) -- every command below needs it.

**Webserver VM**: a server, a public LAN, a boot volume attached to the server, and a NIC, same as any plain Compute Engine host. The boot volume is created standalone, then explicitly attached -- `volume create` has no `--server-id` of its own:

```
ionosctl server create --datacenter-id <DATACENTER_ID> --name se-web \
  --cores 4 --ram 8192 --cpu-family INTEL_SKYLAKE --wait

ionosctl lan create --datacenter-id <DATACENTER_ID> --name public --public --wait

ionosctl volume create --datacenter-id <DATACENTER_ID> \
  --name se-web-boot --size 50GB --image-alias ubuntu:22.04 --licence-type LINUX \
  --ssh-key-paths ~/.ssh/id_ed25519.pub --wait

ionosctl server volume attach --datacenter-id <DATACENTER_ID> \
  --server-id <SERVER_ID> --volume-id <VOLUME_ID>

ionosctl nic create --datacenter-id <DATACENTER_ID> --server-id <SERVER_ID> \
  --lan-id <LAN_ID> --name eth0 --dhcp --wait

ionosctl nic list --datacenter-id <DATACENTER_ID> --server-id <SERVER_ID>   # get the assigned IP
```

SSH in once the IP is up, then follow [Installation](installation.md) from step 1.

**Database**: `ionosctl dbaas postgres cluster create` provisions a managed Postgres instance attached to the datacenter's LAN:

```
ionosctl dbaas postgres cluster create --name searchengine-db --location de/fra \
  --datacenter-id <DATACENTER_ID> --lan-id <LAN_ID> --cidr 10.7.221.0/24 \
  --version 16 --instances 1 --cores 2 --ram 4GB \
  --storage-size 20GB --storage-type SSD_PREMIUM \
  --db-username searchengine --db-password '<strong random password>' \
  --wait
```

Feed the resulting host/port into `DB_DSN` (see [Environment variables](environment-variables.md)). `ionosctl dbaas mariadb cluster create` follows a similar shape for the MariaDB alternative, but that product's CLI surface has changed across `ionosctl` releases -- run `ionosctl dbaas mariadb cluster create --help` to confirm current flags before scripting it.

**GPU inference**: a GPU Server is an ordinary `server create`, just with `--type GPU` and a template -- its Direct Attached Storage is sized from the template automatically (no separate `volume create`/`attach` needed, unlike the webserver above), and it's always AMD_TURIN (`--cpu-family` isn't accepted for this type):

```
ionosctl compute template list --filters Name=H200   # find a template id for the GPU count/size you want

ionosctl server create --datacenter-id <DATACENTER_ID> --name se-gpu \
  --type GPU --template-id <TEMPLATE_ID> --licence-type LINUX \
  --ssh-key-paths ~/.ssh/id_ed25519.pub --wait
```

**Monitoring pipeline**: also a plain `ionosctl` resource:

```
ionosctl monitoring pipeline create --name searchengine --location de/fra --wait
ionosctl monitoring pipeline list   # GrafanaEndpoint/HttpEndpoint -- the remote_write target for packaging/prometheus/
ionosctl monitoring key create --pipeline-id <PIPELINE_ID>   # the key packaging/prometheus/'s remote_write config authenticates with
```

**Sub-user for AI Model Hub**: `accessAndManageAiModelHub` (see [Hosted AI instead of self-hosting](#hosted-ai-instead-of-self-hosting) above) can only be held by a sub-user's group, not the contract's main/owner user. `ionosctl compute user create` always creates a sub-user -- there's no way to create a second "main" user:

```
ionosctl compute user create --first-name AI --last-name ModelHub \
  --email <SUBUSER_EMAIL> --password '<strong random password>'

ionosctl compute group create --name model-hub-managers
# ionosctl has no flag for accessAndManageAiModelHub -- set it via the curl PUT shown above, then:
ionosctl compute group user add --group-id <GROUP_ID> --user-id <SUBUSER_ID>
```

That sub-user's own login/token (not the owner's) is what can now manage Model Hub -- calling its inference endpoint, as shown above, never needed this in the first place.

> **Worth knowing:**
> - None of this is required -- every piece above has a self-hosted or alternate-provider equivalent documented elsewhere in this manual. This page just names the specific IONOS product the reference deployment actually uses.
> - Every `ionosctl <resource> create` command above accepts `--help` to list its current full flag set -- useful since exact flags/defaults (cores, RAM, image aliases, postgres version, template ids) drift across `ionosctl` and IONOS API versions faster than this page can track. `--wait`/`-w` (not `--wait-for-request`) blocks until a created resource reaches `AVAILABLE`.

---
[↑ Manual home](README.md) &nbsp;·&nbsp; [Installation](installation.md) →
