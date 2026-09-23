# Grafana dashboards

Five dashboards on the same Grafana instance
(`https://grafana.logging.de-fra.ionos.com`, org `2348`), fed by the IONOS
Monitoring Service pipelines in `../prometheus/` and `../prometheus-gpu/`,
one per Prometheus `external_labels.site` value those directories set:

| File | Dashboard | Data source |
|---|---|---|
| `dashboards/se-mo-sys-de.json` | `se.mo-sys.de` | `../prometheus/`'s `node`/`nginx`/`postgres` jobs (`site="se-mo-sys-de"`) |
| `dashboards/postgres.json` | `postgres` | `../prometheus/`'s `postgres` job (query time, connections, cache hit ratio, replication lag) |
| `dashboards/searxng.json` | `searxng` | `../prometheus/`'s `searxng` job -- per-engine request rate, result count, response time, reliability, selectable via an `engine_name` template variable |
| `dashboards/gpu-mo-sys-de.json` | `gpu.mo-sys.de` | `../prometheus-gpu/`'s `node`/`gpu`/`vllm`/`vllm-chat` jobs (`site="gpu-h200"`) -- host/GPU utilization/memory/temperature/power, plus the chat instance's own request/latency/queue metrics |
| `dashboards/vllm.json` | `vLLM` | `../prometheus-gpu/`'s `vllm`/`vllm-chat` jobs (`site="gpu-h200"`) -- request/latency/queue/token-throughput metrics, one panel repeated per active vLLM model on the GPU host (Grafana's native panel-repeat, driven by a `model_name` template variable populated from live label values -- no per-model panel to hand-maintain when a model is swapped) |

Every dashboard queries the same Prometheus datasource (UID `efxuqky56wikgf`
in this org) -- these JSON files aren't portable to a different Grafana
org/datasource without changing that UID or re-pointing panels after import.

Each file is a dashboard's `dashboard` object exactly as Grafana's
`GET /api/dashboards/uid/<uid>` returns it (`id` nulled, `version`
stripped -- both meaningless outside this org's store), not hand-authored.
`uid` is kept, since importing with it present updates the same dashboard
rather than creating a duplicate.

## Import / update

```sh
for f in dashboards/*.json; do
  curl -s -X POST \
    -H "Authorization: Bearer <GRAFANA_API_TOKEN>" \
    -H "Content-Type: application/json" \
    -d "{\"dashboard\": $(cat "$f"), \"overwrite\": true}" \
    "https://grafana.logging.de-fra.ionos.com/api/dashboards/db"
done
```

`<GRAFANA_API_TOKEN>` is a service-account token with at least Editor access
on this org (Administration -> Service accounts) -- never committed,
generated per-use like every other credential in this repo's packaging.

`overwrite: true` is required -- without it, re-importing an existing `uid`
is rejected as a conflict instead of updating it.

## Keeping these in sync

These dashboards get edited live, in the Grafana UI or via its API (the same
way the `searxng` dashboard's `engine_name` template variable was added) --
nothing pushes a live edit back into this repo automatically. **After
editing a dashboard live, re-export it here in the same change** -- see the
root `CLAUDE.md`'s "Keep the Grafana dashboards in sync" section.

```sh
curl -s -H "Authorization: Bearer <GRAFANA_API_TOKEN>" \
  "https://grafana.logging.de-fra.ionos.com/api/dashboards/uid/<uid>" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)['dashboard']
d['id'] = None
d.pop('version', None)
json.dump(d, sys.stdout, indent=2, sort_keys=True)
print()
" > dashboards/<name>.json
```

Dashboard uids (also in the table above, and inside each file's own `uid`
field):

- `se.mo-sys.de`: `eef918c8-d272-4c6e-a659-f26db5f333ef`
- `postgres`: `f236b9ca-c452-4501-8659-f0e76368b5a2`
- `searxng`: `91073d22-9be1-41c1-8abd-85678bbdb8ad`
- `gpu.mo-sys.de`: `b42778e1-a229-4c01-8306-5ff970326fa6`
