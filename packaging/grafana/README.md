# Grafana dashboards

Four dashboards, all on the same Grafana instance
(`https://grafana.logging.de-fra.ionos.com`, org `2348`) that the IONOS
Monitoring Service pipelines in `../prometheus/` and `../prometheus-gpu/`
feed, one per Prometheus `external_labels.site` value those two directories'
`prometheus.yml` set:

| File | Dashboard | Data source |
|---|---|---|
| `dashboards/se-mo-sys-de.json` | `se.mo-sys.de` | `../prometheus/`'s `node`/`nginx`/`postgres` jobs (`site="se-mo-sys-de"`) |
| `dashboards/postgres.json` | `postgres` | `../prometheus/`'s `postgres` job (query time, connections, cache hit ratio, replication lag) |
| `dashboards/searxng.json` | `searxng` | `../prometheus/`'s `searxng` job -- per-engine request rate, result count, response time, reliability, selectable via an `engine_name` template variable |
| `dashboards/gpu-mo-sys-de.json` | `gpu.mo-sys.de` | `../prometheus-gpu/`'s `node`/`gpu`/`vllm`/`vllm-chat` jobs (`site="gpu-h200"`) -- GPU utilization/memory/temperature/power plus both vLLM instances' request/latency/queue metrics |

Every dashboard queries the same single Prometheus datasource (Grafana
datasource UID `efxuqky56wikgf` in this org) -- these JSON files aren't
portable to a different Grafana org/datasource without either changing that
UID throughout or re-pointing the panels after import.

Each file is a dashboard's `dashboard` object exactly as Grafana's own
`GET /api/dashboards/uid/<uid>` returns it (`id` nulled out, `version`
stripped -- both are meaningless outside this specific org's dashboard
store) -- not a hand-authored file. `uid` is kept, since importing with it
present updates that same dashboard in place rather than creating a
duplicate.

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
on this org (Administration -> Service accounts) -- never committed
anywhere, generated per-use the same way every other credential in this
repo's packaging is handled.

`overwrite: true` is required -- without it, re-importing a `uid` that
already exists is rejected as a conflict rather than updating it.

## Keeping these in sync

These dashboards get edited live, in the Grafana UI or via its API, the same
way the `searxng` dashboard's `engine_name` template variable was added --
there's no mechanism that pushes a live edit back into this repo
automatically. **After editing a dashboard live, re-export it here in the
same change** (or as an immediate follow-up), the same way
`docs/architecture/README.md`/`docs/configuration.md` must be kept in sync with the
code changes that motivate them -- see the root `CLAUDE.md`'s "Keep the
Grafana dashboards in sync" section.

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
