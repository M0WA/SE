# Prometheus agent (GPU host metrics -> IONOS monitoring)

`gpu.mo-sys.de` -- the H200 NVL box running both self-hosted vLLM instances
(see [docs/architecture/README.md](../../docs/architecture/README.md)'s Deployment
section) -- pushes into the **same** IONOS Monitoring Service pipeline as
`se.mo-sys.de` (`../prometheus/`), via its own independent Prometheus agent
scraping four local sources:

- `prometheus` in **agent mode** -- same role as `../prometheus/`'s: scrapes
  the exporters below and forwards everything to the IONOS pipeline's push
  endpoint. Config is byte-identical to `../prometheus/prometheus.default`
  (see Configure below), just deployed on a different host.
- `prometheus-node-exporter` -- cpu, memory, disk, disk I/O and network, via
  its default collectors. Also byte-identical to `../prometheus/`'s.
- **NVIDIA DCGM exporter** (`nvcr.io/nvidia/k8s/dcgm-exporter`, Docker) --
  per-GPU utilization, memory used/free, temperature, power draw, SM clock,
  ECC errors, etc. for the H200 NVL. Not a Debian package (NVIDIA ships it as
  a container image only) and not shipped in the `searchengine` .deb -- this
  is host-level observability infrastructure, same as `../prometheus/`.
- The two vLLM services themselves (`vllm-embed.service`/`vllm-chat.service`,
  outside this repo, see `docs/architecture/README.md`) each expose an
  OpenAI-compatible `/metrics` endpoint that `prometheus.yml`'s `vllm`/
  `vllm-chat` jobs scrape directly -- request counts, latency, running/
  waiting request queues, KV-cache usage.

This host has no nginx or Postgres, so it has no equivalent of
`../prometheus/`'s `nginx`/`postgres` jobs -- and no equivalent of `searxng`
either, since SearXNG runs on se.mo-sys.de, not here.

## Install

```sh
apt-get install prometheus prometheus-node-exporter
```

DCGM exporter is a container, not an apt package -- see its own section
below.

## Configure

`prometheus.default` and `prometheus-node-exporter.default` are identical to
`../prometheus/`'s (same agent-mode flags, same loopback-only bind) -- reuse
those files directly rather than a duplicate copy drifting out of sync here:

```sh
cp ../prometheus/prometheus.default /etc/default/prometheus
cp ../prometheus/prometheus-node-exporter.default /etc/default/prometheus-node-exporter
```

Copy this directory's own `prometheus.yml`, filling in the real values the
same way `../prometheus/README.md`'s Configure section describes (this is
the same IONOS pipeline, so the endpoint/key are the same too -- only
`<GPU_HOST_PRIVATE_IP>` is new here):

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

Installed as a plain `docker run`, not a systemd-wrapped Docker Compose
service like `../searxng/`'s -- there's no config file to manage (no
volumes, no settings beyond GPU access itself), so Docker's own
`--restart unless-stopped` is adequate on its own and a systemd unit would
just be an extra moving part with nothing to configure through it:

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
without it the exporter still runs but silently drops that metric family.

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
  `dcgm-exporter`, and both `vllm`/`vllm-chat` themselves) bind either
  `127.0.0.1` or this host's own private-network interface only -- never a
  public interface. Same reasoning as `../prometheus/README.md`'s Notes: no
  firewall in front, so a public bind is directly internet-reachable the
  instant it starts.
- `external_labels.site: gpu-h200` is baked into this directory's
  `prometheus.yml` rather than a `<SITE_NAME>` placeholder, unlike
  `../prometheus/prometheus.yml` -- see that file's own comment for why a
  unique `site` per host matters (a real collision was hit live before it
  was added), and this directory is only ever deployed to this one host, so
  there's no reuse case a placeholder would serve here.
- If the IONOS pipeline is ever recreated (new endpoint/key), both this
  host's and se.mo-sys.de's `/etc/prometheus/prometheus.yml` need the same
  update -- they push into the same pipeline, tracked as two separate
  packaging directories only because the two hosts scrape entirely
  different local sources.
