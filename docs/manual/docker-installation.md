# Docker Compose installation

[← Manual home](README.md)

*`docker compose up`*

An alternative to [Installation](installation.md)'s `.deb` + systemd + Postgres flow -- the same three binaries (`cmd/search`/`cmd/admin`/`cmd/crawl`), backed by a real Postgres (`pgvector/pgvector:pg16`), in one command. Skips nginx/TLS/systemd entirely -- bring your own reverse proxy if you want those -- so it's a faster way to get a real, working instance up, at the cost of two capabilities noted below.

`docker-compose.yml` and `Dockerfile` live at the repo root (Docker's own default discovery location for both); [packaging/docker/README.md](https://github.com/M0WA/SE/blob/main/packaging/docker/README.md) is the deeper, maintainer-facing reference for what's inside the image and how it's published -- this page is the practical "get it running" walkthrough.

## What this image can't do

`cmd/crawl`'s Chromium/Firefox rendering needs a real browser and shared libraries that don't fit in the distroless base image this uses -- a crawl-server run this way works for every plain-HTTP-fetch crawl, just never one needing JS rendering. The sandboxed code-execution MCP tool (`searchengine-mcp-sandbox`) similarly needs a real Docker socket to spawn the containers it runs code in, which this compose file doesn't mount through. Both work normally under the `.deb` + systemd install instead.

## Quickstart

```sh
cp .env.example .env
# edit .env: set SETTINGS_ENCRYPTION_KEY and CRAWL_INTERNAL_TOKEN
# (openssl rand -hex 32 for each)
docker compose up -d
```

This starts four containers: `postgres`, `search` (public, port 8080), `admin` (bound to `127.0.0.1:8081` on the host only -- never exposed publicly, same rule as the `.deb` deployment's own admin routing), and `crawl` (no host port at all -- reached only by `admin` over the compose network). `docker-compose.yml` already sets `DB_DRIVER=pgx` (the real `database/sql` driver name for Postgres -- see [Environment variables](environment-variables.md)'s own note on this).

## Seeding the first admin account

Same chicken-and-egg problem as the `.deb` install (see [Installation](installation.md)'s admin-seeding step): no account exists yet, so nothing can sign in to create one through the API. `packaging/create-admin.sh` needs `psql`/`htpasswd` on whatever machine runs it, neither of which is in the distroless app image. Two options:

**From the host**, if it already has `postgresql-client`/`apache2-utils` installed (`postgres`'s own port is published to `127.0.0.1:5432` for exactly this):

```sh
DB_DRIVER=pgx DB_DSN='postgres://searchengine:searchengine@127.0.0.1:5432/searchengine?sslmode=disable' \
  ./packaging/create-admin.sh admin
```

**From inside the `postgres` container**, if the host has neither tool -- get a bcrypt hash any way you can (`htpasswd -nbBC 10` on any machine with `apache2-utils`, or a throwaway `python3 -c` bcrypt call), then insert the row directly:

```sh
docker compose exec postgres psql -U searchengine -d searchengine -c "
INSERT INTO users (id, username, password_hash, is_admin, custom_prompt, created_at, updated_at)
VALUES ('admin', 'admin', '<bcrypt hash>', true, '', now(), now());
"
```

## Configuring a chat/embedding provider

None of the four containers run an inference model themselves -- unlike a `.deb` reference deployment, which typically pairs with a dedicated self-hosted GPU host (see [Infrastructure options](infrastructure.md)), this compose file has no such piece at all. Once signed in as the admin account above, [Chat settings](chat-settings.md) and [Embedding endpoints](embedding-endpoints.md) both just need a plain OpenAI-compatible HTTP endpoint (Base URL/Model/API key) -- either one you already self-host elsewhere, or a hosted provider you'd rather not run a GPU for at all ([IONOS AI Model Hub](infrastructure.md#hosted-ai-instead-of-self-hosting) is a concrete example of the latter).

## Pulling a specific version

`ghcr.io/m0wa/se:<version>` and `:latest` are published on every tagged release. Set `SEARCHENGINE_IMAGE` in `.env` (see `.env.example`) to pull a specific released version instead of always tracking `:latest`.

---
← [Installation](installation.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Environment variables](environment-variables.md) →
