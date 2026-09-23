# Architecture

searchengine is a self-hosted AI chatbot with search-engine features, written in Go as a hexagonal architecture: a chat model that can cite live web results and this instance's own crawled index side by side, backed by a hybrid (BM25 + semantic) search engine also exposed as a plain public search UI. `internal/domain` holds pure business logic (no I/O); `internal/ports` defines the interfaces to the outside world; `internal/application` orchestrates use cases (search, crawling, background jobs) purely in terms of those ports; `internal/adapters/*` implement the ports against real infrastructure (SQL, HTTP fetchers, embedding/chat APIs, headless browsers). Three independent binaries share this core -- `cmd/search` (public), `cmd/admin` (internal), `cmd/crawl` (internal-only) -- coordinating mainly through the shared SQL database, with one exception: `cmd/admin` talks to `cmd/crawl` over an internal HTTP API to run/poll crawl jobs. Four more binaries (`cmd/mcp-web`, `cmd/mcp-datetime`, `cmd/mcp-sandbox`, `cmd/mcp-files`) are first-party MCP tool servers spawned on demand as `stdio` subprocesses, not systemd services -- see Binaries below.

![Architecture diagram](architecture.svg)

Source: [`architecture.mmd`](architecture.mmd) (Mermaid) -- edit that file, then regenerate the picture:

```sh
echo '{"args": ["--no-sandbox"]}' > /tmp/puppeteer-config.json
npx --yes @mermaid-js/mermaid-cli@latest \
  -i docs/architecture/architecture.mmd -o docs/architecture/architecture.svg -b white \
  -p /tmp/puppeteer-config.json
```

(The `-p`/`--no-sandbox` step works around Puppeteer's headless Chromium
refusing to start under most CI/container/dev-VM setups without it --
`mmdc` fails with `No usable sandbox!` otherwise.)

## Layers

### Domain (`internal/domain`)

Pure logic — every file imports only the Go standard library, with no SQL, HTTP, or framework dependency.

| Component | Responsibility |
|---|---|
| Document & search-result core (`document.go`) | `Document`, `SearchResult`, `IndexedDocument`, `DocumentVersion`, `DomainSummary`, `DocumentsOverview` and related admin-overview shapes. |
| Document aliasing & content dedup (`document_alias.go`, `content_fingerprint.go`, `content_dedup_status.go`) | Canonical-document alias tracking; `ContentHash`/`SimHash64` exact and near-duplicate fingerprinting; persisted dedup-run status. |
| BM25 scoring (`bm25.go`) | `PostingStats`, `TermStat`, `TermScore`; `BM25Score`/`BM25ScoreDocument`/`BM25TermScores` implement the BM25 relevance formula and its per-term breakdown. |
| Hybrid ranking, query parsing & snippets (`hybrid.go`, `query.go`, `snippet.go`, `overrides.go`) | `HybridResult` blending BM25+semantic+PageRank; `ParsedQuery`/`ParseQuery` for term/phrase/site: parsing; `Snippet` highlighting; admin-configured block/boost `RankingOverrides`. |
| Vector math & embedding endpoints (`vector.go`, `embedding_endpoint.go`, `embedding_provider.go`, `embedding_status.go`) | `CosineSimilarity`/`VectorNorm`/`CombineWeighted`; `EmbeddingHTTPEndpoint` config model; persisted recompute status. |
| Fuzzy matching & vocabulary (`fuzzy.go`, `vocabulary_cache.go`, `tokenizer.go`) | Levenshtein-based `NearestTerm` fallback for zero-hit query terms; `VocabularyCache`; shared `Tokenize` used by indexing, query parsing, and overrides. |
| Crawl jobs (`crawljob.go`) | `CrawlJob`/`CrawlJobSummary`/`CrawlPageEvent` models, plus a full in-memory, mutex-protected `CrawlJobStore` implementation used as the lightweight/test stand-in for the DB-backed one. |
| Scheduled crawls (`scheduled_crawl.go`) | `ScheduledCrawl` model for admin-created recurring or one-off crawl definitions. |
| PageRank (`pagerank.go`) | Iterative `PageRank` computation over the crawled link graph; persisted `PageRankStatus`. |
| Operational & tuning settings (`settings.go`, `tuning.go`, `link_scope.go`, `renderer.go`, `url_normalize.go`) | `OperationalSettings`, `TuningSettings`, link-scope/renderer enums, `CanonicalizeURL`. |
| Chat (`chat.go`) | `ChatMessage`, `ChatEndpoint` config, `ToolCallResult` -- web search/fetch is only via tools the model invokes on admin-configured `MCPServer` connections (`mcp_server.go`), never a direct search by this layer. |
| MCP servers (`mcp_server.go`) | `MCPServer` (admin-configured MCP connection -- stdio or Streamable HTTP), `MCPTool` (a tool discovered from an active server's `tools/list`, rediscovered every turn). |
| Agents (`agent.go`) | `Agent` -- an admin-defined specialization (static system prompt + optional `MCPServer` scope), addressed by `ChatEndpoint.DefaultAgentID` or a per-question override. |
| Corpus stats cache (`corpus_stats.go`) | Concurrency-safe cached snapshot of corpus-wide totals BM25 scoring needs per request. |
| Overview/admin metrics shapes (`overview_metrics.go`) | Pure data shapes feeding the admin Overview page's charts. |

### Ports (`internal/ports`)

30 interfaces defining pure contracts between the core and adapters; the package imports `database/sql` only for the `sql.DBStats` value type, performing no I/O itself.

| Port | Responsibility | Implemented by |
|---|---|---|
| `Fetcher` / `AuthFetcher` / `Renderer` | Plain HTTP fetch, authenticated fetch with per-request options, headless-browser rendering. | `httpfetcher`, `browserfetcher` |
| `RobotsChecker` | robots.txt compliance check before fetching. | `robots` |
| `EmbeddingProvider` | Text-to-vector conversion. | `hashembed`, `httpembed` |
| `SQLRepository` | Broad relational-DB port: document save/versioning/embeddings, postings/corpus-stats/vocabulary lookups, alias resolution, ANN semantic search. | `sqlrepo` |
| `PageRankRepository`, `ContentDedupRepository`, `EmbeddingRepository` | Narrow slices of `SQLRepository`, scoped to exactly what each background job needs. | `sqlrepo` |
| `SessionStore` | Shared-DB login session tokens, each carrying a role (admin vs. regular user) and, for a regular user, which `User` it belongs to. | `sqlrepo` |
| `UserStore` | CRUD for every DB-backed account -- there is no separate hardcoded admin account; `User.IsAdmin` is what additionally grants `/admin/*` access on top of the same self-service search/chat access every account gets. An admin manages create/delete/`IsAdmin`; each account self-serves its own password and personal chat prompt (`User.CustomPrompt`, injected into every turn) via search-server's `/account` page. | `sqlrepo` |
| `HealthChecker` | Cheap DB liveness check backing `/healthz`. | `sqlrepo` |
| `AdminRepository` | Read-mostly admin diagnostics port (stats, listings, time series, pool stats). | `sqlrepo` |
| `SearchService` | Primary driving port for public search. | `internal/application` (hybrid search use case) |
| `CrawlerService` | Executes an actual crawl with live per-page progress. | `internal/application` (crawl loop) |
| `CrawlJobService` | Network contract admin-server uses to poll/control crawl-server's jobs. | HTTP client adapter (`crawlclient`) |
| `CrawlJobStore` | crawl-server's own job/page-history persistence. | `domain.CrawlJobStore` (in-memory/test), `sqlrepo` (production) |
| `DebugSearchService` | Raw, unblended BM25/semantic/PageRank score breakdown for admin diagnostics. | `internal/application` |
| `SettingsStore` | Generic key/value settings persistence shared by every process. | `sqlrepo` |
| `ScheduledCrawlStore` | CRUD + scheduling operations on `ScheduledCrawl`, shared by admin CRUD and the crawl-server ticker. | `sqlrepo` |
| `EmbeddingEndpointStore`, `ChatEndpointStore` | CRUD/get-set for admin-configured embedding and chat endpoint config. | `sqlrepo` |
| `MCPServerStore` | CRUD for admin-configured MCP server connections. | `sqlrepo` |
| `UserMCPServerStore` | CRUD for per-user, self-service MCP server connections (`/account/mcp-servers`) -- same `MCPServer` shape, but each row is owned by and scoped to one userID; `http` transport only (no `stdio`, which grants local command execution -- admin rows only). | `sqlrepo` |
| `AgentStore` | CRUD for admin-defined agents -- a named specialization (static system prompt + optional MCP server scope), resolved by `ChatService.Chat` each turn. | `sqlrepo` |
| `FileStore` | CRUD for files a regular-user session uploaded (`/account/files`) or `cmd/mcp-files`'s `write_file` created for them -- scoped by owner userID. Always attached to one `PersistedChat`; `Repository.DeleteChat` explicitly cascade-deletes its files (SQLite, unlike Postgres, doesn't enforce the FK's `ON DELETE CASCADE`). `ChatService` never reads it directly -- `cmd/mcp-files` reaches it only via `restapi`'s `/account/api/files` endpoints. | `sqlrepo` |
| `ChatStore` | CRUD for a session's own pinned (persistent) chats (`/account/api/chats`) -- title, chosen agent, full turn history, resynced each turn and reloaded on page load. Only a pinned chat may have files attached. | `sqlrepo` |
| `ChatCompleter` | Calls an OpenAI-compatible chat-completions endpoint. | `httpchat` |
| `MCPToolProvider` / `MCPSession` | Opens one MCP session per chat turn across every active server (tool discovery + calls). | `mcpclient` |

### Application (`internal/application`)

Orchestration/use-case layer; verified to import only `internal/domain` and `internal/ports` (no adapter imports) in non-test code.

| Component | Responsibility | Key dependencies |
|---|---|---|
| `hybridSearchService` / `hybridSearchAdapter` | Core hybrid search: query parsing, concurrent BM25/embedding fetch, candidate pooling, constraint/override filtering, score blending, pagination, snippet hydration; adapted to the `SearchService` port. | `SQLRepository`, `EmbeddingProvider` |
| `crawlLoop` | Shared crawl control-flow engine (queueing, link-scope/allow-block filtering, sitemap discovery, robots checks, fetch, thin-content filtering, canonical aliasing), reused by every `CrawlerService` via an injected `save` callback. | `AuthFetcher`, `RobotsChecker` |
| `sqlCrawlerService` | Implements `CrawlerService`: drives `crawlLoop`, embeds and persists each fetched page. | `AuthFetcher`, `RobotsChecker`, `SQLRepository`, `EmbeddingProvider` |
| `embedTitleWeighted` | Shared helper computing a title+body weighted embedding, used by the crawler and the recompute job. | `EmbeddingProvider` |
| `RunPageRankJob` / `*WithStatus` | Recomputes PageRank from the link graph and writes results back. | `PageRankRepository`, `SettingsStore` |
| `RunContentDedupJob` / `*WithStatus` | Groups exact/near-duplicate documents (SimHash + LSH) and merges losers into a canonical doc. | `ContentDedupRepository`, `SettingsStore` |
| `RunEmbeddingRecomputeJob` / `*WithStatus` | Re-embeds every document's stored text against currently-enabled providers without recrawling. | `EmbeddingRepository`, `EmbeddingProvider`, `SettingsStore` |
| `TriggerDueCrawls` (scheduler) | Finds and triggers due scheduled crawls, records completion/next-run state. | `ScheduledCrawlStore` |
| `RecoverInterruptedCrawls` | Resumes or fails crawl jobs left queued/running when crawl-server last stopped. | `CrawlJobStore` |
| `RenderAwareFetcher` | Routes fetches through a headless-browser `Renderer` when requested. | `AuthFetcher`, `Renderer` |
| `ChatService` | Orchestrates one chat turn: loads config, resolves and injects the active agent (endpoint default or per-question override), merges the caller's own MCP servers into the global catalog (always, never narrowed by the agent's scope), opens an MCP session, trims history, delegates completion, and runs any tool calls the model makes. | `ChatEndpointStore`, `ChatCompleter`, `MCPServerStore`, `MCPToolProvider`, `AgentStore`, `UserMCPServerStore` |

### Adapters (`internal/adapters`)

13 adapter packages, plus `restapi` (14 total) as the shared HTTP handler layer for `cmd/search` and `cmd/admin`.

| Adapter | Responsibility |
|---|---|
| `sqlrepo` | SQL persistence layer shared by all three binaries (SQLite locally/CI, Postgres in the dev deployment); implements `SQLRepository`, `PageRankRepository`, `ContentDedupRepository`, `EmbeddingRepository`, `SessionStore`, `AdminRepository`, `CrawlJobStore`, `SettingsStore`, `ScheduledCrawlStore`, `EmbeddingEndpointStore`, `ChatEndpointStore`, `UserStore`, `FileStore`, `ChatStore`, and more. |
| `restapi` | HTTP handler layer for both the public search UI/API and the admin UI/API — routing, JSON REST endpoints, embedded static assets, auth/session and crawl-internal-token checks. |
| `httpfetcher` | Default plain-HTTP page fetcher with timeout/UA/cookie/basic-auth support, routed through `netguard`. |
| `browserfetcher` | Renders JS-heavy pages via a headless Chromium or Firefox browser over Playwright. |
| `htmlparser` | Pure HTML title/text/link/canonical-URL extraction. |
| `robots` | Fetches, caches, and evaluates robots.txt rules. |
| `netguard` | Shared SSRF guard (custom `DialContext`), with two policies: a strict one (`AllowedIP`) blocking every private/reserved range, used by every outbound crawler fetch; and a permissive one (`AllowedConfiguredEndpointIP`) for admin-configured endpoints (`httpembed`/`httpchat`'s `BaseURL`/`TokenizeURL`) that only blocks link-local/multicast/unspecified addresses, since a self-hosted embeddings/chat backend legitimately lives on a private network or loopback. |
| `hashembed` | Dependency-free fallback embedding provider via feature hashing. |
| `httpembed` | Calls an OpenAI-compatible embeddings HTTP endpoint (e.g. IONOS AI Model Hub) with chunking and rate-limit-aware retry; outbound calls routed through `netguard`'s configured-endpoint policy. |
| `httpchat` | Calls an OpenAI-compatible chat-completions endpoint; outbound calls routed through `netguard`'s configured-endpoint policy. |
| `crawlclient` | HTTP client `cmd/admin` uses to delegate crawl-job operations to `cmd/crawl`. |
| `settingscrypto` | AES-256-GCM encryption of the admin-configured embedding/chat/MCP-server API keys at rest. |
| `mcpclient` | MCP client (`github.com/modelcontextprotocol/go-sdk`) -- connects to every active admin-configured `MCPServer` for a chat turn (spawning a `stdio` child process like `cmd/mcp-web`, or dialing an `http` server's Streamable HTTP endpoint), discovers its tools, and routes calls to the right connection. |
| `dockersandbox` | Runs untrusted Python/Go snippets in a locked-down Docker container via the `docker` CLI -- memory/CPU/process/wall-clock limits, dropped capabilities, read-only rootfs, no network unless configured. Used only by `cmd/mcp-sandbox`; not wired through any port, since that MCP binary is the entire integration surface. |

## Binaries

- **`cmd/search`** — public, internet-facing. Serves the index/search page, `/search` JSON API, chat endpoints (`POST /chat`, `GET /agents`), `/session` (nav role lookup), `/account`/`/account/api` (self-service password/chat-prompt), `/account/mcp-servers`/`/account/api/mcp-servers...` (self-service `http`-only MCP CRUD, merged into every chat turn), and `/account/files`/`/account/api/files...` (uploaded-file storage, also reachable via a short-lived bearer token for `cmd/mcp-files`). The only one of the three exposed to the public internet. Talks to the shared DB via `sqlrepo`, never to the other two binaries directly.
- **`cmd/admin`** — internal, reached only via nginx's `/admin`, `/login`, `/logout` prefixes. Hosts every `/admin/api/*` endpoint (settings, embedding endpoints, PageRank, content-dedup, sessions/auth, diagnostics, scheduled crawls, chat endpoints). Never fetches pages or touches robots.txt/documents itself -- only starts/polls crawl jobs on crawl-server via `crawlclient` (default `CRAWL_SERVER_URL=http://127.0.0.1:8082`, optional `X-Internal-Token`).
- **`cmd/crawl`** — internal only, never exposed by nginx. Runs actual crawls (fetch, robots check, HTML parse, embed, persist), tracks job state, and exposes an internal HTTP surface (`RoutesCrawlInternal`, default `127.0.0.1:8082`) only `cmd/admin`'s `crawlclient` calls. Also runs background schedulers (scheduled-crawl poller, PageRank/content-dedup recompute, job pruner) and recovers interrupted jobs on startup.
- **`cmd/mcp-web`** — first-party MCP server exposing `web_search` (proxies to self-hosted SearXNG) and `web_fetch` (SSRF-guarded via `httpfetcher`/`netguard`). Not a systemd service -- `mcpclient` spawns it as a `stdio` subprocess when an admin-configured `MCPServer` row (`Transport="stdio"`) points at `/usr/bin/searchengine-mcp-web`. Replaces the old `packaging/chat-hooks/web_search.sh`/`web_fetch.sh` scripts.
- **`cmd/mcp-datetime`** — first-party MCP server exposing `get_datetime` (current date/time, optional IANA timezone). Same spawn model as `cmd/mcp-web`. Replaced the old always-on `%c`/`strftime` prompt-placeholder mechanism: the model now asks for the time only when it needs it, instead of every system prompt carrying a timestamp.
- **`cmd/mcp-sandbox`** — first-party MCP server exposing `run_python`/`run_go`, running a model-supplied snippet in a locked-down Docker container via `dockersandbox`. Same spawn model. Resource limits and network access are admin-configured via the `MCPServer` row's `Args` (e.g. `-network`, `-memory=1g`), never per-call. Requires `docker` CLI access for the `searchengine` service user -- a real privilege elevation (Docker access is host-root-equivalent); see `docs/manual/installation.md` step 16 before enabling.
- **`cmd/mcp-files`** — first-party MCP server exposing `list_files`/`read_file`/`write_file` over a signed-in user's uploaded files (`/account/files`). Same spawn model, but holds no DB connection or admin credential: every call is a loopback HTTP request to `cmd/search`'s `/account/api/files` endpoints, authenticated with a short-lived per-user bearer token (`SE_FILES_API_TOKEN`, minted per turn by `handleChat`) -- keeping a compromised `mcp-files` process's blast radius to one user's own files.

At runtime, the three binaries coordinate almost entirely through the shared SQL database: each opens its own DB connection (a `*sql.DB` can't cross OS processes), and `internal/bootstrap`'s `SyncSettings` polls the settings table roughly every 10 seconds so an admin edit propagates to the others. The one real inter-binary call is `cmd/admin → cmd/crawl` over HTTP via `crawlclient`.

## Deployment

The dev/test deployment (`se.mo-sys.de`) runs all three Go binaries as independent, hardened systemd services from one Debian package: `searchengine-search.service`, `searchengine-admin.service`, `searchengine-crawl.service` (each `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=true`, `Restart=on-failure`), sharing one system user (`searchengine`) and one `EnvironmentFile` (`/etc/searchengine/searchengine.env`) holding `DB_DRIVER`/`DB_DSN`, admin credentials, per-service listen addresses (loopback-only by default), `CRAWL_SERVER_URL`/`CRAWL_INTERNAL_TOKEN`, and `SETTINGS_ENCRYPTION_KEY`. `postinst` creates the user and starts all three services, but deliberately does **not** install or reload nginx config -- the tracked `packaging/nginx/searchengine.conf` can drift from the live `/etc/nginx/...` unless manually re-synced.

nginx is the public entrypoint on 80/443, splitting traffic by path: `/login`, `/logout`, `/admin` (a plain string-prefix match, not path-segment-aware) route to admin-server (`127.0.0.1:8081`); everything else falls through to search-server (`127.0.0.1:8080`). crawl-server (`127.0.0.1:8082`) gets no location block and must never be exposed publicly. A separate, non-public block on `127.0.0.1:8090` exposes nginx's `stub_status` for scraping.

Observability is host-level, independent of the searchengine package: a Prometheus agent (`--enable-feature=agent`, no local TSDB) on `127.0.0.1:9090` scrapes `node-exporter` (9100), `nginx-exporter` (9113, via `stub_status`), `postgres-exporter` (9187, reusing `DB_DSN`), and optionally SearXNG's OpenMetrics endpoint, then `remote_write`-forwards to an external **IONOS Monitoring Service** pipeline. SearXNG -- the self-hosted metasearch instance `cmd/mcp-web`'s `web_search` tool queries -- runs as a separate Docker Compose deployment on `127.0.0.1:8888`, outside the searchengine `.deb`. Everything binds `127.0.0.1` only, since there is no host firewall.

The admin-configured embedding/chat endpoints (`httpembed`/`httpchat`, generic OpenAI-compatible HTTP clients) point at a dedicated inference host, `gpu.mo-sys.de` (one NVIDIA H200 NVL GPU), not a third-party API. Two `vLLM` processes share that GPU: `Qwen/Qwen3-VL-Embedding-8B` in embed mode on `:8000` (backing `httpembed`, `--gpu-memory-utilization 0.2` -- explicit, since the default tries to reserve a fraction of total VRAM regardless of what the chat process already holds), and `RedHatAI/Qwen2.5-72B-Instruct-FP8-dynamic` in generate mode on `:8001` (backing `httpchat`, `--max-model-len 32768`, no YaRN). Each is its own systemd unit, bound to the private network interface only, gated by a bearer API key. The host also runs `node-exporter` and NVIDIA's `DCGM` exporter, feeding the same IONOS pipeline under `external_labels.site: gpu-h200`.

## Keeping this document current

Update this document and its diagram whenever a domain type, port, use case, adapter, or binary is added, changed, or removed -- see `CLAUDE.md`'s architecture-documentation rule.