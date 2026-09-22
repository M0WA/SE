# Environment variables

[← Manual home](README.md)

All variables are read once at process startup, typically from `/etc/searchengine/searchengine.env` via systemd's `EnvironmentFile=`. Changing any of them requires restarting the affected service(s).

## The full list

| Name | Default | Used by | Description |
|---|---|---|---|
| `DB_DRIVER` | `"sqlite"` | search, admin, crawl | Selects the SQL backend driver (`sqlite`/`postgres`/`mysql`) used to open the shared database connection. |
| `DB_DSN` | `"file:search.db?cache=shared"` | search, admin, crawl | The data-source-name/connection string for the SQL database. |
| `SETTINGS_ENCRYPTION_KEY` | none, empty (encryption disabled) | search, admin, crawl | Hex-encoded AES-256 key used to encrypt/decrypt sensitive stored settings (e.g. endpoint/MCP server API keys) at rest in the DB. |
| `SEARCH_INTERNAL_API_KEY` | none, empty (bypass disabled) | search | Optional pre-shared key letting a trusted local caller call the public `/search` endpoint via an `X-Internal-API-Key` header instead of a browser session cookie. |
| `SEARCH_LISTEN_ADDR` | `"127.0.0.1:8080"` | search | The host:port the public, internet-facing search HTTP server binds and listens on. |
| `ADMIN_USER` | none, empty | admin | Username required for admin-server sign-in. If unset, sign-in refuses everyone. |
| `ADMIN_PASSWORD` | none, empty | admin | Password required for admin-server sign-in, paired with `ADMIN_USER`. |
| `CRAWL_INTERNAL_TOKEN` | none, empty (auth check disabled) | admin, crawl | Shared secret authenticating admin-server's network calls to crawl-server's internal API. |
| `CRAWL_SERVER_URL` | `"http://127.0.0.1:8082"` | admin | Base URL admin-server uses to reach the crawl-server binary over the network. |
| `ADMIN_LISTEN_ADDR` | `"127.0.0.1:8081"` | admin | The host:port the internal admin HTTP server binds and listens on. |
| `CRAWL_LISTEN_ADDR` | `"127.0.0.1:8082"` | crawl | The host:port the internal-only crawl HTTP server binds and listens on. |

---
← [Installation](installation.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Config files](config-files.md) →
