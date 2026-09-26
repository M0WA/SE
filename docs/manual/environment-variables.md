# Environment variables

[← Manual home](README.md)

All variables are read once at startup, typically from `/etc/searchengine/searchengine.env` via systemd's `EnvironmentFile=`. Changing any requires restarting the affected service(s).

There is no `ADMIN_USER`/`ADMIN_PASSWORD` (or any other account) here --
every account, admin included, is a DB-backed row managed from
[Users](users.md)/[User detail](user-detail.md), with an `is_admin` flag.
Seed the very first one with `packaging/create-admin.sh` (see
[Installation](installation.md) step 7); every one after that can be
created from `/admin/users` by an existing admin.

## The full list

| Name | Default | Used by | Description |
|---|---|---|---|
| `DB_DRIVER` | `"sqlite"` | search, admin, crawl | Selects the SQL backend driver (`sqlite`/`pgx`/`mysql`) used to open the shared database connection -- `pgx` for Postgres, the actual `database/sql` driver name `github.com/jackc/pgx/v5/stdlib` registers (not `postgres`, which fails with "unknown driver"). |
| `DB_DSN` | `"file:search.db?cache=shared"` | search, admin, crawl | The data-source-name/connection string for the SQL database. |
| `SETTINGS_ENCRYPTION_KEY` | none, empty (encryption disabled) | search, admin, crawl | Hex-encoded AES-256 key used to encrypt/decrypt sensitive stored settings (e.g. endpoint/MCP server API keys) at rest in the DB. |
| `SEARCH_INTERNAL_API_KEY` | none, empty (bypass disabled) | search | Optional pre-shared key letting a trusted local caller call the public `/search` endpoint via an `X-Internal-API-Key` header instead of a browser session cookie. |
| `CHAT_VISION_INTERNAL_API_KEY` | none, empty (bypass disabled) | search | Same mechanism as `SEARCH_INTERNAL_API_KEY`, but a deliberately separate key: lets `cmd/mcp-vision` (spawned per chat turn) call the internal `/search/api/vision-similarity` endpoint. Required for the Chat settings page's "Image understanding -> Similarity search" to actually work, not just be enabled in the DB. |
| `SEARCH_LISTEN_ADDR` | `"127.0.0.1:8080"` | search | The host:port the public, internet-facing search HTTP server binds and listens on. |
| `CRAWL_INTERNAL_TOKEN` | none, empty (auth check disabled) | admin, crawl | Shared secret authenticating admin-server's network calls to crawl-server's internal API. |
| `CRAWL_SERVER_URL` | `"http://127.0.0.1:8082"` | admin | Base URL admin-server uses to reach the crawl-server binary over the network. |
| `ADMIN_LISTEN_ADDR` | `"127.0.0.1:8081"` | admin | The host:port the internal admin HTTP server binds and listens on. |
| `CRAWL_LISTEN_ADDR` | `"127.0.0.1:8082"` | crawl | The host:port the internal-only crawl HTTP server binds and listens on. |
| `SE_SANDBOX_NETWORK` | `"false"` | mcp-sandbox | Alternate way to set the `-network` flag -- see [MCP Server detail](mcp-server-detail.md). An explicit `-network` on the MCPServer row's Args still always wins. |
| `SE_SANDBOX_ALLOW_SYSTEM_PACKAGES` | `"false"` | mcp-sandbox | Alternate way to set the `-system-packages` flag. |
| `SE_SANDBOX_MEMORY` | `"512m"` | mcp-sandbox | Alternate way to set the `-memory` flag. |
| `SE_SANDBOX_CPUS` | `"1"` | mcp-sandbox | Alternate way to set the `-cpus` flag. |
| `SE_SANDBOX_PIDS_LIMIT` | `"128"` | mcp-sandbox | Alternate way to set the `-pids-limit` flag. |
| `SE_SANDBOX_TIMEOUT` | `"15s"` | mcp-sandbox | Alternate way to set the `-timeout` flag (a Go duration string, e.g. `"2m"`). |
| `SE_SANDBOX_DNS` | none, empty | mcp-sandbox | Alternate way to set the `-dns` flag (comma-separated). |
| `SE_SANDBOX_HOST_DNS` | `"false"` | mcp-sandbox | Alternate way to set the `-host-dns` flag. |
| `SE_SANDBOX_HOST_NETWORK` | `"false"` | mcp-sandbox | Alternate way to set the `-host-network` flag. |
| `SE_SANDBOX_BASE_URL` | `"http://127.0.0.1:8080"` | mcp-sandbox | Alternate way to set the `-base-url` flag. |
| `GPU_CONTROL_TOKEN` | none, empty (**refuses to start**) | gpu-control | Shared secret authenticating every `/gpu/api/*` request (`X-Internal-Token` header) to this root-privileged service -- unlike `CRAWL_INTERNAL_TOKEN`, there is no "disabled" fallback: a blank value is refused at startup. Read from `/etc/searchengine/gpu-control.env` on the separate GPU host only, never the main searchengine host's own env file. Enter the same value into the admin UI's GPU mode (Vision) -> Control endpoint token. |
| `GPU_CONTROL_LISTEN_ADDR` | `"10.7.226.11:8002"` | gpu-control | The private-LAN host:port this service binds and listens on -- deliberately never `0.0.0.0`; see `packaging/gpu-control/README.md`. |
| `GPU_CONTROL_SWITCH_TIMEOUT_SECONDS` | `300` | gpu-control | How long a single chat<->vision switch may take before it's reported as failed. |
| `GPU_CONTROL_IDLE_REVERT_MINUTES` | `15` | gpu-control | Automatically reverts to chat mode after this many idle minutes in Vision mode (`0` disables the revert). |
| `GPU_CONTROL_COMFY_READY_URL` | `"http://127.0.0.1:8188/system_stats"` | gpu-control | Readiness-probe URL polled after starting ComfyUI. |
| `GPU_CONTROL_VLLM_READY_URL` | `"http://10.7.226.11:8001/v1/models"` | gpu-control | Readiness-probe URL polled after starting the chat model. |

---
← [Docker Compose installation](docker-installation.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Config files](config-files.md) →
