# Configuration

searchengine is configured through three layers: environment variables (read at process startup by `cmd/search`, `cmd/admin`, and `cmd/crawl`), config files installed or templated by the Debian package under `packaging/`, and runtime settings stored in the shared SQL database and edited through the admin web UI. This document is the reference for all three, plus the steps to install the system in the first place. For how the three binaries and their surrounding infrastructure (nginx, the shared database, crawl-server's internal API) fit together, see [docs/architecture.md](architecture.md) — that content isn't repeated here.

## Installation

This is the ordered install flow for a fresh Debian/Ubuntu host. Steps 1–2 and the underlying package/service setup are handled automatically by the `.deb`; everything from nginx onward is a manual step the package deliberately does not perform.

1. **Install prerequisite OS packages.** The `.deb`'s `Depends` is just `libc6`; its `Recommends` pulls in the GTK/Cairo/NSS shared libraries Playwright's Chromium/Firefox rendering needs. Skip them with `--no-install-recommends` if crawls will only ever use the default plain-HTTP fetch (`Renderer=none`).
   ```
   apt-get update && apt-get install ./searchengine_<version>_amd64.deb
   # or, to skip the Playwright/GTK stack entirely:
   apt-get install --no-install-recommends ./searchengine_<version>_amd64.deb
   ```

2. **Install the `.deb` package.** *(Handled automatically by the package.)* `dpkg` installs the three binaries (`/usr/bin/searchengine-{search,admin,crawl}`), the three systemd units, and a template `/etc/searchengine/searchengine.env`. `postinst` then: creates a system user `searchengine` (no login shell, no home dir); chowns/chmods (`640`) `searchengine.env` to that user; creates `/var/lib/searchengine` and `/var/lib/searchengine/tmp` (Playwright's `TMPDIR`, needed because `ProtectSystem=strict` makes the real `/tmp` read-only for the services); runs `systemctl daemon-reload`, then `enable` and `start` on all three services.
   ```
   dpkg -i searchengine_<version>_amd64.deb
   # postinst prints: "searchengine installed. Services: searchengine-search, searchengine-admin, searchengine-crawl"
   ```

3. **Expect the services to come up unconfigured.** *(Expected — not a packaging bug.)* The shipped `searchengine.env` points at a local SQLite file and has blank `ADMIN_USER`/`ADMIN_PASSWORD`, so admin/crawl sign-in fails closed until both are set.
   ```
   systemctl status searchengine-search searchengine-admin searchengine-crawl
   journalctl -u searchengine-admin -n 50
   ```

4. **Set `DB_DRIVER`/`DB_DSN`.** *(Manual — edit `/etc/searchengine/searchengine.env`, mode `640`, already owned by `searchengine:searchengine`.)* All three binaries share one database and ping it as part of `/healthz`.
   ```
   # SQLite (default, already works out of the box):
   DB_DRIVER=sqlite
   DB_DSN=file:/var/lib/searchengine/search.db?cache=shared

   # Postgres (dev-deployment style):
   DB_DRIVER=postgres
   DB_DSN=postgres://user:pass@dbhost:5432/searchengine?sslmode=require
   ```

5. **Set `ADMIN_USER` / `ADMIN_PASSWORD`.** *(Manual.)* Until both are non-empty, sign-in to `/admin` and `/crawl` always refuses — the single most common reason a fresh install "looks broken."
   ```
   ADMIN_USER=admin
   ADMIN_PASSWORD=<strong random password>
   ```

6. **Set the optional hardening secrets.** *(Manual, recommended.)* Both default to blank/disabled if skipped.
   ```
   CRAWL_INTERNAL_TOKEN=$(openssl rand -hex 32)
   SETTINGS_ENCRYPTION_KEY=$(openssl rand -hex 32)
   ```

7. **Restart and verify.** `EnvironmentFile` changes need a restart — systemd doesn't hot-reload them.
   ```
   systemctl restart searchengine-search searchengine-admin searchengine-crawl
   systemctl is-active searchengine-search searchengine-admin searchengine-crawl
   curl -s http://127.0.0.1:8080/healthz
   curl -s http://127.0.0.1:8081/healthz
   curl -s http://127.0.0.1:8082/healthz   # /healthz stays open even with CRAWL_INTERNAL_TOKEN set
   ```

8. **Install and configure nginx.** *(Manual — the `.deb` never touches nginx.)* All three services bind loopback-only by design; nginx is the only intended entry point. `packaging/nginx/searchengine.conf` is the tracked source of truth for the routing split — see [packaging/nginx/README.md](../packaging/nginx/README.md) for the full gotcha about `/admin` being a plain string-prefix match.
   ```
   apt-get install nginx certbot python3-certbot-nginx
   sed 's/<DOMAIN>/your.domain.example/' packaging/nginx/searchengine.conf > /etc/nginx/sites-available/searchengine
   rm -f /etc/nginx/sites-enabled/default
   ln -sf /etc/nginx/sites-available/searchengine /etc/nginx/sites-enabled/searchengine
   nginx -t && systemctl reload nginx
   systemctl enable --now nginx
   ```

9. **Issue the TLS certificate.** *(Manual.)* Requires DNS for the domain to already resolve here. Rewrites the deployed nginx config in place (443 block + redirect) and installs a `certbot.timer` for auto-renewal.
   ```
   certbot --nginx -d your.domain.example --non-interactive --agree-tos --register-unsafely-without-email --redirect
   systemctl status certbot.timer
   certbot certificates
   ```

10. **Verify the public site end-to-end.**
    ```
    curl -sk https://your.domain.example/healthz
    curl -sk https://your.domain.example/admin/    # should hit admin-server's login page
    ```

11. **(Optional) Host/nginx/Postgres monitoring → IONOS pipeline.** *(Manual, independent of the package.)* See [packaging/prometheus/README.md](../packaging/prometheus/README.md).
    ```
    apt-get install prometheus prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter
    cp packaging/prometheus/prometheus.default /etc/default/prometheus
    cp packaging/prometheus/prometheus-node-exporter.default /etc/default/prometheus-node-exporter
    cp packaging/prometheus/prometheus-nginx-exporter.default /etc/default/prometheus-nginx-exporter
    cp packaging/nginx/stub_status.conf /etc/nginx/conf.d/stub_status.conf && nginx -t && systemctl reload nginx
    cp packaging/prometheus/postgres-exporter-datasource.sh /usr/local/bin/ && chmod 755 /usr/local/bin/postgres-exporter-datasource.sh
    mkdir -p /etc/systemd/system/prometheus-postgres-exporter.service.d
    cp packaging/prometheus/prometheus-postgres-exporter.override.conf /etc/systemd/system/prometheus-postgres-exporter.service.d/override.conf
    psql "$DB_DSN" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"   # Postgres only
    # fill in packaging/prometheus/prometheus.yml's placeholders -> /etc/prometheus/prometheus.yml, chown root:prometheus, chmod 640 -- never commit filled-in values
    systemctl daemon-reload
    systemctl enable --now prometheus-node-exporter prometheus-nginx-exporter prometheus-postgres-exporter prometheus
    promtool check config /etc/prometheus/prometheus.yml
    ```

12. **(Optional) Stand up SearXNG** for the chat feature's live web search. *(Manual, Docker-based.)* See [packaging/searxng/README.md](../packaging/searxng/README.md). Includes `searchengine`, a custom SearXNG engine ([packaging/searxng-engine/README.md](../packaging/searxng-engine/README.md)) that folds this deployment's own indexed corpus into the blended results, in place of the old, separate "RAG" mechanism.
    ```
    apt-get install docker.io docker-compose
    mkdir -p /opt/searxng
    cp packaging/searxng/docker-compose.yml packaging/searxng/settings.yml /opt/searxng/
    cp packaging/searxng-engine/searchengine_index.py /opt/searxng/
    cd /opt/searxng
    sed -i "s/REPLACE_WITH_OPENSSL_RAND_HEX_32/$(openssl rand -hex 32)/" settings.yml   # never commit the filled-in file
    sed -i "s/REPLACE_WITH_SEARCH_INTERNAL_API_KEY/$(openssl rand -hex 32)/" settings.yml   # same key must also become SEARCH_INTERNAL_API_KEY below
    cp packaging/searxng/searxng.service /etc/systemd/system/searxng.service
    systemctl daemon-reload
    systemctl enable --now searxng
    # set SEARCH_INTERNAL_API_KEY in /etc/searchengine/searchengine.env to the same value as internal_api_key above, then:
    systemctl restart searchengine-search
    curl -s 'http://127.0.0.1:8888/search?q=test&format=json' | head -c 300
    ```

13. **(Optional) Wire SearXNG metrics into Prometheus.** Only relevant if both steps 11 and 12 were done.
    ```
    # settings.yml: general.open_metrics: <password>
    # prometheus.yml: sed 's/<SEARXNG_METRICS_PASSWORD>/<same password>/'
    docker compose restart searxng   # config is read once at container startup
    ```

14. **(Optional) Install chat hook scripts.** *(Manual — `postinst` deliberately does not create `CHAT_HOOKS_DIR`.)* Two example scripts ship in the repo: `web_search.sh` (proxies to SearXNG) and `web_fetch.sh` (fetches a specific URL directly — has a documented SSRF caveat, only enable it if that residual risk is acceptable for your deployment). See [packaging/chat-hooks/README.md](../packaging/chat-hooks/README.md).
    ```
    mkdir -p /etc/searchengine/hooks
    cp packaging/chat-hooks/web_search.sh packaging/chat-hooks/web_fetch.sh /etc/searchengine/hooks/
    chmod +x /etc/searchengine/hooks/web_search.sh /etc/searchengine/hooks/web_fetch.sh
    # then configure the matching ChatHook (Pattern + Script=web_search.sh or web_fetch.sh) under Settings -> Chat -> Hooks in the admin UI
    ```

15. **(Optional) Configure an embedding and chat inference backend.** The admin UI's `domain.EmbeddingHTTPEndpoint` (edited on `admin_embedding_endpoint.html`) and `domain.ChatEndpoint` (edited on `admin_chat_settings.html`) each point at a plain OpenAI-compatible HTTP endpoint — `httpembed`/`httpchat` are generic clients, so any such API works, self-hosted or third-party. As a concrete reference, the `se.mo-sys.de` dev deployment points both at self-hosted [vLLM](https://github.com/vllm-project/vllm) server processes on a separate dedicated GPU host (one NVIDIA H200 NVL), each its own systemd unit gated by its own bearer API key:
    ```
    # Embeddings: Alibaba-NLP/gte-Qwen2-7B-instruct
    vllm serve Alibaba-NLP/gte-Qwen2-7B-instruct --runner pooling --convert embed

    # Chat: RedHatAI/Qwen2.5-72B-Instruct-FP8-dynamic
    vllm serve RedHatAI/Qwen2.5-72B-Instruct-FP8-dynamic --max-model-len 32768
    ```
    Point `EmbeddingHTTPEndpoint.BaseURL`/`Model` and `ChatEndpoint.BaseURL`/`Model` at these processes' `/v1` base URLs and set each endpoint's API key to the matching bearer token.

16. **Final smoke test of the whole stack.**
    ```
    systemctl is-active searchengine-search searchengine-admin searchengine-crawl nginx
    curl -sk https://your.domain.example/
    curl -sk https://your.domain.example/admin/
    curl -sk https://your.domain.example/healthz
    ```

## Environment variables

All variables are read once at process startup (`bootstrap.GetEnv`/`os.Getenv`), typically from `/etc/searchengine/searchengine.env` via systemd's `EnvironmentFile=`. Changing any of them requires restarting the affected service(s).

| Name | Default | Used by | Description |
|---|---|---|---|
| `DB_DRIVER` | `"sqlite"` | search, admin, crawl | Selects the SQL backend driver (`sqlite`/`postgres`/`mysql`) used to open the shared database connection. |
| `DB_DSN` | `"file:search.db?cache=shared"` | search, admin, crawl | The data-source-name/connection string for the SQL database (path for SQLite, connection URL for Postgres/MySQL). |
| `SETTINGS_ENCRYPTION_KEY` | none — empty string; encryption disabled if unset | search, admin, crawl | Hex-encoded AES-256 key used to encrypt/decrypt sensitive stored settings (e.g. the embedding/chat endpoint API keys) at rest in the DB. Each binary opens its own key independently at startup. |
| `CHAT_HOOKS_DIR` | `"/etc/searchengine/hooks"` | search | Directory on disk where every configured `ChatHook`'s script file must live for the hook runner to find and execute it. |
| `SEARCH_INTERNAL_API_KEY` | none — empty string; bypass disabled if unset | search | Optional pre-shared key letting a trusted local caller (a SearXNG engine plugin folding this instance's own index into SearXNG's aggregated search) call the public `/search` endpoint via an `X-Internal-API-Key` header instead of a browser session cookie. Empty by default: with no key configured, `/search` behaves exactly as it always has (session-cookie-only, `401` otherwise). |
| `SEARCH_LISTEN_ADDR` | `"127.0.0.1:8080"` | search | The host:port the public, internet-facing search HTTP server binds and listens on. |
| `ADMIN_USER` | none — empty string | admin | Username required for admin-server sign-in. If unset (with `ADMIN_PASSWORD`), `/crawl` and `/admin` refuse all sign-ins (logged as a warning, not fatal). |
| `ADMIN_PASSWORD` | none — empty string | admin | Password required for admin-server sign-in, paired with `ADMIN_USER`. |
| `CRAWL_INTERNAL_TOKEN` | none — empty string; auth check disabled if unset | admin, crawl | Shared secret authenticating admin-server's network calls to crawl-server's internal API. |
| `CRAWL_SERVER_URL` | `"http://127.0.0.1:8082"` | admin | Base URL admin-server uses to reach the crawl-server binary over the network for job/crawl operations. |
| `ADMIN_LISTEN_ADDR` | `"127.0.0.1:8081"` | admin | The host:port the internal admin HTTP server (reached only via nginx `/admin`, `/login`, `/logout`) binds and listens on. |
| `CRAWL_LISTEN_ADDR` | `"127.0.0.1:8082"` | crawl | The host:port the internal-only crawl HTTP server (never exposed by nginx) binds and listens on. |

## Config files

| Repo path | Deployed path | Install | Configures |
|---|---|---|---|
| `packaging/searchengine.env` | `/etc/searchengine/searchengine.env` | Auto (`.deb` conffile, edits preserved across upgrades) | Runtime config for all three binaries — see the [Environment variables](#environment-variables) table above. |
| `packaging/searchengine-search.service` | `/lib/systemd/system/searchengine-search.service` | Auto | systemd unit for the public search-server binary. |
| `packaging/searchengine-admin.service` | `/lib/systemd/system/searchengine-admin.service` | Auto | systemd unit for the internal admin-server binary. |
| `packaging/searchengine-crawl.service` | `/lib/systemd/system/searchengine-crawl.service` | Auto | systemd unit for the internal-only crawl-server binary. |
| `packaging/debian/{control,postinst,prerm}` | n/a — become the `.deb`'s own metadata/maintainer scripts | Auto (build-time) | Package metadata, dependencies, and install/removal behavior (user/dir creation, service enable/start, stop/disable on removal). |
| `packaging/nginx/searchengine.conf` | `/etc/nginx/sites-available/searchengine` (symlinked into `sites-enabled`) | Manual | nginx reverse-proxy routing split between search-server and admin-server. See [packaging/nginx/README.md](../packaging/nginx/README.md). |
| `packaging/nginx/stub_status.conf` | `/etc/nginx/conf.d/stub_status.conf` | Manual | Loopback-only nginx `stub_status` page for `prometheus-nginx-exporter`. See [packaging/nginx/README.md](../packaging/nginx/README.md). |
| `packaging/chat-hooks/web_search.sh` | `/etc/searchengine/hooks/web_search.sh` (default `CHAT_HOOKS_DIR`) | Manual | Example chat-hook script proxying to a self-hosted SearXNG instance. See [packaging/chat-hooks/README.md](../packaging/chat-hooks/README.md). |
| `packaging/chat-hooks/web_fetch.sh` | `/etc/searchengine/hooks/web_fetch.sh` (default `CHAT_HOOKS_DIR`) | Manual | Example chat-hook script fetching a specific `http`/`https` URL directly; has a documented SSRF caveat. See [packaging/chat-hooks/README.md](../packaging/chat-hooks/README.md). |
| `packaging/prometheus/prometheus.yml` | `/etc/prometheus/prometheus.yml` | Manual | Prometheus agent-mode scrape/`remote_write` config. See [packaging/prometheus/README.md](../packaging/prometheus/README.md). |
| `packaging/prometheus/prometheus.default` | `/etc/default/prometheus` | Manual | `prometheus.service` ARGS (agent mode). |
| `packaging/prometheus/prometheus-nginx-exporter.default` | `/etc/default/prometheus-nginx-exporter` | Manual | Exporter ARGS. |
| `packaging/prometheus/prometheus-node-exporter.default` | `/etc/default/prometheus-node-exporter` | Manual | Exporter ARGS. |
| `packaging/prometheus/prometheus-postgres-exporter.override.conf` | `/etc/systemd/system/prometheus-postgres-exporter.service.d/override.conf` | Manual | systemd drop-in wiring searchengine's `DB_DSN` into the postgres exporter. |
| `packaging/prometheus/postgres-exporter-datasource.sh` | `/usr/local/bin/postgres-exporter-datasource.sh` | Manual | Wrapper re-exporting `DB_DSN` as `DATA_SOURCE_NAME`. |
| `packaging/searxng/docker-compose.yml` | `/opt/searxng/docker-compose.yml` | Manual | Docker Compose service definition for self-hosted SearXNG. See [packaging/searxng/README.md](../packaging/searxng/README.md). |
| `packaging/searxng/settings.yml` | `/opt/searxng/settings.yml` | Manual | SearXNG application config (engines, `secret_key`, limiter, JSON output format). |
| `packaging/searxng/searxng.service` | `/etc/systemd/system/searxng.service` | Manual | systemd unit wrapping `docker compose up`/`down` for the SearXNG stack. |

## Runtime settings (admin UI)

Everything below lives in the shared SQL database and is edited only through `internal/adapters/restapi/admin_*.html`, never via env vars or files.

### Settings → Search & ranking → Ranking (`admin_settings.html`)

| Field | Default | Bounds | Description |
|---|---|---|---|
| `alpha` | `0.5` | `[0, 1]` | Blend weight between BM25 and semantic score (0 = pure semantic, 1 = pure BM25). |
| `k1` | `1.2` | floored at 0, no upper bound | BM25 term-frequency saturation parameter. |
| `b` | `0.75` | `[0, 1]` | BM25 document-length normalization factor. |
| `title-weight` | `2` | UI min `1` | How many times a title term match counts vs. once for a body match (BM25 side); applies only to documents crawled after the change. |

### Settings → Search & ranking → Search

| Field | Default | Bounds | Description |
|---|---|---|---|
| `default-top-k` | `5000` | substitutes default for ≤0 | Default number of results returned by a search that doesn't specify its own top-K. |
| `semantic-pool-size` | `200` | substitutes default for ≤0 | Caps how many documents get a semantic score per search (also the ANN top-K when active). |
| `ann-search-enabled` | `true` | boolean | Uses Postgres pgvector's ANN index to fill the semantic candidate pool instead of a brute-force sample, where available. |

### Settings → Search & ranking → Embeddings

| Field | Default | Bounds | Description |
|---|---|---|---|
| `embedding-hash-enabled` | `true` | boolean, restart required | Enables the built-in dependency-free hash embedding for every document, independent of configured HTTP endpoints. |
| `embedding-search-weights` | `{"hash": 1}` | no numeric clamp; self-heals against the live endpoint set | Per-provider (hash or HTTP endpoint ID) weight in the search-time semantic blend. |
| `embedding-title-weight` | `0.3` | `[0, 1]` | Blends a document's title into its embedding as `titleWeight*titleVector + (1-titleWeight)*bodyVector`. |

### Settings → Search & ranking → Link authority

| Field | Default | Bounds | Description |
|---|---|---|---|
| `pagerank-weight` | `0` | `[0, 1]` | Blends normalized PageRank into the final score (0 = no influence, 1 = ranking driven entirely by link authority). |
| `pagerank-interval` | `60` (minutes) | min `5`, no max | How often crawl-server recomputes PageRank, in addition to always recomputing after a crawl completes. |

### Settings → Search & ranking → Fuzzy matching

| Field | Default | Bounds | Description |
|---|---|---|---|
| `fuzzy-enabled` | `true` | boolean | Turns on typo-tolerant query matching (near-miss vocabulary substitution for zero-hit terms). |
| `fuzzy-max-edit-distance` | `2` | clamped to exactly `1` or `2` | Max edit distance allowed when substituting a vocabulary term for a zero-hit query term. |

### Settings → Crawling → Crawler

| Field | Default | Bounds | Description |
|---|---|---|---|
| `fetch-timeout` | `8s` | substitutes default for ≤0 | Per-page timeout before the crawler gives up fetching. |
| `user-agent` | `Mozilla/5.0 ... Firefox/131.0` | substitutes default for empty | `User-Agent` header sent with every crawl fetch. |
| `default-max-pages` | `20000` | substitutes default for ≤0 | Default max pages fetched by a crawl that doesn't specify its own limit. |
| `min-text-length` | `50` | floored at 0 | Minimum extracted text length; shorter pages are skipped as thin content. |
| `crawl-delay` | `250` (ms) | floored at 0 | Milliseconds waited before each fetch after the first, for politeness. |
| `max-response-kb` | `5*1024` KB (5 MB) | substitutes default for ≤0 | Max bytes read from a page response before truncating. |
| `max-retained-crawl-jobs` | `200` | substitutes default for ≤0 | How many past crawl jobs (with full page history) are retained before pruning the oldest. |
| `max-concurrent-crawls` | `3` | substitutes default for ≤0 | Max crawl jobs fetching pages at once; excess queues rather than opening unbounded connections. |
| `default-renderer` | `none` | self-heals to `none` if empty/invalid; values: `none`/`chromium`/`firefox` | Global default page-rendering mode for crawls unless a crawl overrides it. |
| `default-link-scope` | `tld` | self-heals to `tld` if empty/invalid; values: `host`/`domain`/`tld`/`any` | Global default link-following scope for crawls unless a crawl overrides it. |
| `url-alias-www-enabled` | `true` | boolean, future crawls only | Folds a leading `www.` into the bare domain when computing a crawled URL's canonical identity. |

### Settings → Crawling → Documents

| Field | Default | Bounds | Description |
|---|---|---|---|
| `max-document-versions` | `3` | substitutes default for ≤0 | How many versions (current + archived) of a document are kept when re-crawls change its content. |

### Settings → Crawling → Content dedup (see also `admin_content_dedup.html` for recompute/results)

| Field | Default | Bounds | Description |
|---|---|---|---|
| `content-dedup-enabled` | `true` | boolean | Turns on the periodic/on-demand batch job that merges duplicate/near-duplicate documents across URLs. |
| `content-dedup-method` | `exact` | self-heals to `exact` on unrecognized value; values: `exact`/`simhash` | Dedup matching method — byte-identical normalized text vs. near-duplicate fingerprint. |
| `content-dedup-simhash-max-distance` | `3` | `[1, 10]` | Max Hamming distance (of 64 bits) between two SimHash64 fingerprints still counted as near-duplicates (method=`simhash` only). |
| `content-dedup-interval` | `120` (minutes) | min `15`, no max | How often crawl-server runs the batch content-dedup pass, in addition to always running after a crawl completes. |

### Settings → Content rules → Blocked / Boosted

| Field | Default | Bounds | Description |
|---|---|---|---|
| `blocked-terms` | empty | no count limit; tokenized/normalized on save | Words that exclude a document from results entirely if its title/text contains one. |
| `blocked-domains` | empty | normalized (lowercased, host extracted) | Hosts/URLs that exclude a document from results entirely if served from one of them. |
| `boosted-terms` | empty | non-positive factors dropped | Word → multiplier map; a matching document's final score is multiplied by the factor (multiple matches multiply). |
| `boosted-domains` | empty | non-positive factors dropped | Host → multiplier map; a matching document's final score is multiplied by the factor. |

### Settings → System → Database connection pool / Session

| Field | Default | Bounds | Description |
|---|---|---|---|
| `db-max-open-conns` | `25` | substitutes default for ≤0 | Max open SQL connections in the pool (Postgres/MySQL; SQLite always clamps to 1). |
| `db-max-idle-conns` | `25` | substitutes default for ≤0 | Max idle SQL connections kept warm between requests. |
| `db-conn-max-lifetime` | `5` minutes | substitutes default for ≤0 | How long a pooled SQL connection is reused before being recycled. |
| `session-ttl` | `12` hours | substitutes default for ≤0 | How long an admin sign-in session lasts before expiring (applies to sessions created after saving). |

### Embedding HTTP endpoints (`admin_embedding_endpoint.html`; list/create on `admin_embedding_endpoints.html`)

| Field | Default | Bounds | Description |
|---|---|---|---|
| `endpoint-meta` (ID) | derived from name, minted once | must match `^[a-z0-9_]{1,20}$` | Provider key for this endpoint (`document_embeddings.provider` value / pgvector column suffix); never changes after creation. |
| `endpoint-name` | none, required | — | Human-readable display name for the endpoint. |
| `endpoint-base-url` | none, required | — | OpenAI-compatible embeddings API base URL; requests POST to `<BaseURL>/embeddings`. |
| `endpoint-api-key` | empty | optional; blank on update keeps stored value | Bearer token; encrypted at rest and never echoed back to the UI. |
| `endpoint-model` | none, required | — | Model name sent as the request body's `model` field. |
| `endpoint-dimensions` | none, required | — | Expected embedding vector length; validates responses and sizes the pgvector column. |
| `endpoint-rate-limit` | `0` (unlimited) | — | Caps real HTTP requests/sec against this endpoint. |
| `endpoint-enabled` | `false` | boolean | Whether this endpoint's embedding is kept current for every document. |
| `endpoint-chunk-size` | `0` (no chunking) | — | Bounds how much text one Embed call sends; longer text is chunked and mean-pooled. |
| `endpoint-tokenize-url` | empty (uses estimate) | used only when chunk size is nonzero | Optional endpoint (e.g. vLLM's `/tokenize`) for exact token counts. |

### Chat settings (`admin_chat_settings.html`)

| Field | Default | Bounds | Description |
|---|---|---|---|
| `chat-base-url` | none, required | — | Base URL of the single configured chat-completions backend. |
| `chat-api-key` | empty | blank on update keeps stored value | API key/bearer token for the chat backend; encrypted at rest. |
| `chat-model` | none, required | — | Model name used for chat-completions requests. |
| `chat-enabled` | `false` | boolean | Gates whether chat will call out to this endpoint at all. |
| `chat-max-context-tokens` | `0` (no trimming) | — | Bounds tokens' worth of conversation sent to the model (approximated by character count); oldest messages trimmed first. |
| `chat-web-search-enabled` | `false` | boolean | The "Web" toggle's default (overridable per question): when on, every chat hook with `gated_by_web_search` becomes available to the model. This setting never performs a search or fetch itself -- see "Chat hooks" below. |
| `chat-web-search-base-url` | none | required for a `web_search` hook to work | Base URL of the SearXNG instance; passed to every active hook's script as the `WEB_SEARCH_BASE_URL` environment variable. |
| `chat-system-prompt` | empty (none injected) | — | Optional leading system-role message injected ahead of the rest of the conversation on every turn; never dropped by context trimming. |

Chat no longer has a separate retrieval-augmented-generation (RAG) toggle
against this instance's own index. To blend this instance's own index into
chat's web-search results instead, add it as a SearXNG engine -- see
`packaging/searxng-engine/README.md`.

### Chat hooks (`admin_chat_hooks.html`)

| Field | Default | Bounds | Description |
|---|---|---|---|
| `hook-id` | derived, minted once | must match `^[a-z0-9_]{1,20}$` | Hook's identifier. |
| `hook-name` | none, required | — | Human label for the hook (e.g. `web_search`). |
| `hook-pattern` | none, required | must have exactly one capture group | Go regexp; when a chat answer matches it, the captured text is passed as an argv value to the script. |
| `hook-script` | none, required | filename only, no path | Script filename resolved against `CHAT_HOOKS_DIR` by the hook runner. |
| `hook-enabled` | `false` | boolean | Whether this hook is active and checked against chat answers. |

## Keeping this document current

Per the root `CLAUDE.md`, this file (and `docs/` more broadly) must be kept in sync in the same change as the config it describes: any change to an environment variable, a config file under `packaging/`, an install/deployment step, or an admin-configurable runtime setting should update this document alongside the code — the same way `openapi.yaml` and `packaging/nginx/searchengine.conf` are treated as hard requirements rather than nice-to-haves.