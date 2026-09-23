# Prometheus agent (GPU host metrics -> IONOS monitoring)

`gpu.mo-sys.de` -- the H200 NVL box running both self-hosted vLLM instances
(see [docs/architecture/README.md](../../docs/architecture/README.md)'s
Deployment section) -- pushes into the **same** IONOS Monitoring Service
pipeline as `se.mo-sys.de` (`../prometheus/`), via its own Prometheus agent
scraping four local sources:

- `prometheus` in **agent mode** -- same role as `../prometheus/`'s, and
  config byte-identical to its `prometheus.default` (see Configure below),
  just on a different host.
- `prometheus-node-exporter` -- cpu, memory, disk, disk I/O and network,
  also byte-identical to `../prometheus/`'s.
- **NVIDIA DCGM exporter** (`nvcr.io/nvidia/k8s/dcgm-exporter`, Docker) --
  per-GPU utilization, memory, temperature, power, SM clock, ECC errors for
  the H200 NVL. NVIDIA ships it only as a container image, not a Debian
  package, and it's not in the `searchengine` .deb -- host-level
  observability, same as `../prometheus/`.
- The two vLLM services (`vllm-embed.service`/`vllm-chat.service`, outside
  this repo) each expose an OpenAI-compatible `/metrics` endpoint that
  `prometheus.yml`'s `vllm`/`vllm-chat` jobs scrape directly -- request
  counts, latency, queue depth, KV-cache usage.

This host has no nginx or Postgres (no `../prometheus/` `nginx`/`postgres`
jobs) and no `searxng` job either, since SearXNG runs on se.mo-sys.de.

## Install

```sh
apt-get install prometheus prometheus-node-exporter
```

DCGM exporter is a container, not an apt package -- see its own section
below.

## Configure

`prometheus.default` and `prometheus-node-exporter.default` are identical to
`../prometheus/`'s -- reuse those files directly rather than a duplicate
copy drifting out of sync:

```sh
cp ../prometheus/prometheus.default /etc/default/prometheus
cp ../prometheus/prometheus-node-exporter.default /etc/default/prometheus-node-exporter
```

Copy this directory's own `prometheus.yml`, filling in real values the same
way `../prometheus/README.md`'s Configure section describes (same IONOS
pipeline, so endpoint/key are the same too -- only `<GPU_HOST_PRIVATE_IP>`
is new here):

```sh
sed -e 's/<IONOS_METRICS_ENDPOINT>/<pipeline id>-metrics.<pipeline uid>.monitoring.<region>.ionos.com/' \
    -e 's/<IONOS_APIKEY>/<real key here>/' \
    -e 's/<GPU_HOST_PRIVATE_IP>/<this host'"'"'s own private-network IP>/' \
    prometheus.yml > /etc/prometheus/prometheus.yml
chown root:prometheus /etc/prometheus/prometheus.yml
chmod 640 /etc/prometheus/prometheus.yml
```

**Never commit the filled-in `prometheus.yml`** -- same convention as
`../prometheus/README.md`: the API key is a write credential for the
monitoring pipeline.

```sh
systemctl daemon-reload
systemctl enable --now prometheus-node-exporter prometheus
```

### DCGM exporter

Installed as a plain `docker run`, not a systemd-wrapped Compose service
like `../searxng/`'s -- no config file to manage (no volumes, nothing
beyond GPU access itself), so Docker's own `--restart unless-stopped` is
adequate and a systemd unit would just be extra:

```sh
apt-get install docker.io
# nvidia-container-toolkit must already be installed and configured for
# Docker (`nvidia-ctk runtime configure --runtime=docker`) -- this is the
# same GPU-passthrough setup vLLM's own containers/services on this host
# already depend on, not something specific to this exporter.
docker run -d --name dcgm-exporter --restart unless-stopped \
  --gpus all --cap-add SYS_ADMIN \
  -p 127.0.0.1:9400:9400 \
  nvcr.io/nvidia/k8s/dcgm-exporter:latest
```

`--cap-add SYS_ADMIN` is required -- DCGM's profiling metrics (SM occupancy,
tensor core utilization) need it to read NVIDIA's performance counters;
without it the exporter runs but silently drops that metric family.

## Verify

```sh
promtool check config /etc/prometheus/prometheus.yml
systemctl is-active prometheus prometheus-node-exporter
docker ps --filter name=dcgm-exporter
curl -s http://127.0.0.1:9400/metrics | grep -c '^DCGM_FI_DEV_GPU_UTIL'
curl -s http://127.0.0.1:9090/metrics | grep prometheus_remote_storage_samples_failed_total
```

The `DCGM_FI_DEV_GPU_UTIL` count should equal the number of GPUs on this
host (1, for the single H200 NVL). `prometheus_remote_storage_samples_failed_total`
should stay at `0` (or stop climbing) once the agent is up, same as
`../prometheus/README.md`'s Verify section.

## Notes

- All four services (`prometheus`, `prometheus-node-exporter`,
  `dcgm-exporter`, both vLLM instances) bind `127.0.0.1` or this host's
  private-network interface only, never public -- same reasoning as
  `../prometheus/README.md`'s Notes.
- `external_labels.site: gpu-h200` is baked into this directory's
  `prometheus.yml` rather than a `<SITE_NAME>` placeholder, unlike
  `../prometheus/prometheus.yml` -- a real `site` collision was hit live
  before that was added, and this directory only ever deploys to this one
  host, so no placeholder reuse case exists here.
- If the IONOS pipeline is ever recreated, both this host's and
  se.mo-sys.de's `/etc/prometheus/prometheus.yml` need the same update --
  same pipeline, tracked as two directories only because the hosts scrape
  different local sources.
