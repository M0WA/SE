# searchengine

A self-hosted hybrid (BM25 + semantic) search engine, written in Go. Crawls
the web (or an intranet), indexes it with a combination of classic lexical
ranking and vector embeddings, and serves both a public search UI and a chat
interface that can cite live web results and this instance's own index side
by side.

This file is a map of the documentation, not the documentation itself --
each linked document is the single source of truth for its own area; nothing
below is duplicated in more than one place. All of it also renders as a real
site via GitHub Pages: https://m0wa.github.io/SE/ (`docs/_config.yml`).

## Start here

- **Using the admin UI?** [docs/manual/](docs/manual/README.md) -- the
  end-user manual: every admin settings page, screenshotted and explained
  field by field, plus the public search/chat page.
- **Installing or configuring a deployment?**
  [docs/configuration.md](docs/configuration.md) -- every environment
  variable, every config file under `packaging/`, every database-backed
  setting, and the full install checklist from a bare `.deb` to a working
  site behind nginx/TLS.
- **Understanding how it's built?**
  [docs/architecture/README.md](docs/architecture/README.md) -- the
  hexagonal-architecture layout (`internal/domain`/`ports`/`application`/
  `adapters`), all seven binaries, and how they connect to external systems,
  with a diagram.

## Binaries

Three always-running services share one SQL database (SQLite by default,
Postgres in the reference dev deployment):

| Binary | Role |
|---|---|
| `cmd/search` | Public, internet-facing. Search UI, chat UI, `/search` API. |
| `cmd/admin` | Internal. Every admin settings page and `/admin/api/*` endpoint. |
| `cmd/crawl` | Internal only, never exposed by nginx. Runs crawls, tracks job state. |

Four more, `cmd/mcp-web`, `cmd/mcp-datetime`, `cmd/mcp-sandbox`, and
`cmd/mcp-files`, are first-party MCP tool servers the chat backend spawns on
demand as `stdio` subprocesses rather than running continuously -- see
[docs/architecture/README.md](docs/architecture/README.md)'s Binaries
section for what each one does.

## Deployment packaging

Everything under `packaging/` is either shipped in the `.deb` (the three
core binaries and their systemd units) or a tracked reference config for
infrastructure installed alongside it. Each has its own README with install
steps -- [docs/configuration.md](docs/configuration.md) sequences all of
them into one ordered checklist; these are the per-component details:

| Directory | What it's for |
|---|---|
| [packaging/nginx/](packaging/nginx/README.md) | Routes the three binaries behind one public domain + TLS. |
| [packaging/prometheus/](packaging/prometheus/README.md) | Host/nginx/Postgres metrics → IONOS monitoring. |
| [packaging/prometheus-gpu/](packaging/prometheus-gpu/README.md) | Same pipeline, for the GPU inference host (node + DCGM + vLLM metrics). |
| [packaging/grafana/](packaging/grafana/README.md) | The Grafana dashboards backing both Prometheus pipelines above. |
| [packaging/searxng/](packaging/searxng/README.md) | Self-hosted SearXNG instance backing the chat feature's live web search. |
| [packaging/searxng-engine/](packaging/searxng-engine/README.md) | A custom SearXNG engine that folds this instance's own index into SearXNG's blended results. |

## Development

```sh
go build ./...
go vet ./...
go test ./... -race -count=1
npm install && npm test   # internal/adapters/restapi/*.js
```

See `CLAUDE.md` for the full branch/PR/release workflow, test-coverage
expectations, and the doc-sync rules this repo enforces on every change.
