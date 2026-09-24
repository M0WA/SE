# SE - AI chatbot

A self-hosted AI chatbot with search-engine features, written in Go: chat
with a model that cites live web results and this instance's own crawled
index side by side, backed by a hybrid BM25 + semantic search engine also
exposed as a plain public search UI.

Docs: **[User manual](docs/manual/README.md)** (install, configure, and use
it) · **[Architecture](docs/architecture/README.md)** (how it's built) --
also served as a site at https://m0wa.github.io/SE/.

## Binaries

| Binary | Role |
|---|---|
| `cmd/search` | Public. Search UI, chat UI, `/search` API. |
| `cmd/admin` | Internal. Every admin settings page and `/admin/api/*`. |
| `cmd/crawl` | Internal only. Runs crawls, tracks job state. |
| `cmd/mcp-{web,datetime,sandbox,files,vision}` | First-party MCP tool servers, spawned on demand as `stdio` subprocesses. |

## Packaging

| Directory | What it's for |
|---|---|
| [packaging/docker/](packaging/docker/README.md) | `docker compose up` -- an alternative to the `.deb` install below, same three binaries + Postgres in containers. Image published to `ghcr.io/m0wa/se` on every release. |
| [packaging/nginx/](packaging/nginx/README.md) | Routes the binaries behind one public domain + TLS. |
| [packaging/prometheus/](packaging/prometheus/README.md) | Host/nginx/Postgres metrics → IONOS monitoring. |
| [packaging/prometheus-gpu/](packaging/prometheus-gpu/README.md) | Same, for the GPU inference host. |
| [packaging/grafana/](packaging/grafana/README.md) | Dashboards for both Prometheus pipelines. |
| [packaging/searxng/](packaging/searxng/README.md) | Self-hosted SearXNG for the chat feature's live web search. |
| [packaging/searxng-engine/](packaging/searxng-engine/README.md) | Custom SearXNG engine folding this instance's own index in. |

## Development

```sh
go build ./... && go vet ./... && go test ./... -race -count=1
npm install && npm test   # internal/adapters/restapi/*.js
```

See `CLAUDE.md` for the branch/PR/release workflow and doc-sync rules.

`cmd/e2e-check` is a manual, dev-host-only smoke test against a real deployed
instance (default `se.mo-sys.de`) -- never wired into CI, never packaged. Run
by hand after a release: `go run ./cmd/e2e-check -host <host> -creds <path>`
(see `cmd/e2e-check/credentials.example.json` for the credentials file shape;
never commit a real one).
