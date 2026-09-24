# Docker Compose: an alternative to the `.deb` install

`docker-compose.yml` and `Dockerfile` live at the repo root (Docker's own
default discovery location for both), not under this directory --
this README is just their documentation home, matching every other
`packaging/*/README.md`.

This is a genuine alternative to `docs/manual/installation.md`'s `.deb` +
systemd + Postgres path, not a toy: the same three binaries
(`cmd/search`/`cmd/admin`/`cmd/crawl`), backed by a real Postgres
(`pgvector/pgvector:pg16`, the same image `.github/workflows/ci.yml` tests
against), in one `docker compose up`.

## What's in the image

One multi-stage `Dockerfile`, one `gcr.io/distroless/static-debian12`
final image, all eight of this repo's binaries copied in
(`searchengine-search`/`-admin`/`-crawl`/`-mcp-web`/`-mcp-datetime`/
`-mcp-sandbox`/`-mcp-files`/`-mcp-vision`) -- pick which one a container
actually runs via `--entrypoint`/compose's own `entrypoint:`, the same way
`packaging/searchengine-*.service` picks one per systemd unit. Defaults to
`searchengine-search`.

**What this image can't do:** `cmd/crawl`'s Chromium/Firefox rendering
(`internal/adapters/browserfetcher`) needs a real browser and its shared
library dependencies, none of which fit in a distroless static base --
see the `.deb`'s own `Recommends` in `packaging/debian/control` for the
full list a rendering-capable crawl-server needs. A crawl-server run from
this image works for every plain-HTTP-fetch crawl, just never one needing
JS rendering. `searchengine-mcp-sandbox` similarly needs a real Docker
socket to spawn the sandboxed containers it runs code in
(`internal/adapters/dockersandbox`) -- not available to a process already
running inside this same image without deliberately mounting
`/var/run/docker.sock` through (docker-outside-of-docker), which this
compose file doesn't attempt.

## Quickstart

```sh
cp .env.example .env
# edit .env: set SETTINGS_ENCRYPTION_KEY and CRAWL_INTERNAL_TOKEN
# (openssl rand -hex 32 for each)
docker compose up -d
```

This starts four containers: `postgres`, `search` (public, port 8080),
`admin` (internal, bound to `127.0.0.1:8081` on the host only -- never
`0.0.0.0`, same "never exposed publicly" rule
`packaging/nginx/README.md` gives for the `.deb` deployment's own admin
routing), and `crawl` (no host port at all -- reached only by `admin`
over the compose network, exactly like the `.deb` deployment's crawl-server
being unreachable through nginx).

**`DB_DRIVER=pgx`, not `DB_DRIVER=postgres`** -- `pgx` is the actual
`database/sql` driver name `github.com/jackc/pgx/v5/stdlib` registers;
`sql.Open("postgres", ...)` fails outright with "unknown driver" (this
tripped up an earlier draft of this same file -- see
`docs/manual/environment-variables.md`'s own note). `docker-compose.yml`
already gets this right; it's called out here because every other doc
mentioning Postgres needs to say `pgx` too.

## Seeding the first admin account

Same chicken-and-egg problem as the `.deb` install (see
`docs/manual/installation.md` step 7): no account exists yet, so nothing
can sign in to create one through the API.
`packaging/create-admin.sh` needs `psql`/`htpasswd` on whatever machine
runs it -- neither is in the distroless app image. Two options:

**From the host**, if it already has `postgresql-client`/`apache2-utils`
installed (`postgres`'s own port is published to `127.0.0.1:5432` for
exactly this):

```sh
DB_DRIVER=pgx DB_DSN='postgres://searchengine:searchengine@127.0.0.1:5432/searchengine?sslmode=disable' \
  ./packaging/create-admin.sh admin
```

**From inside the `postgres` container**, if the host has neither tool
(this container does have `psql`, since it's the official Postgres image)
-- run `create-admin.sh`'s own `htpasswd -nbBC 10` step separately (any
machine with `apache2-utils`, or a throwaway `python3 -c` bcrypt call) to
get a hash, then insert the row directly:

```sh
docker compose exec postgres psql -U searchengine -d searchengine -c "
INSERT INTO users (id, username, password_hash, is_admin, custom_prompt, created_at, updated_at)
VALUES ('admin', 'admin', '<bcrypt hash>', true, '', now(), now());
"
```

## Configuring a chat/embedding provider

None of the four containers above run an inference model themselves --
unlike the `.deb` reference deployment, which typically pairs with a
dedicated self-hosted GPU host (see
[Infrastructure options](../../docs/manual/infrastructure.md)), this
compose file has no such piece at all. Once you're signed in as the admin
account above, [Chat settings](../../docs/manual/chat-settings.md) and
[Embedding endpoints](../../docs/manual/embedding-endpoints.md) both just
need a plain OpenAI-compatible HTTP endpoint (Base URL/Model/API key) --
either one you already self-host elsewhere, or a hosted provider you'd
rather not run a GPU for at all. [IONOS AI Model
Hub](../../docs/manual/infrastructure.md#hosted-ai-instead-of-self-hosting)
is a concrete example of the latter: no infrastructure of your own to run
alongside these four containers, just an API key.

## Publishing the image

`.github/workflows/release.yml`'s `package-docker` job builds this same
`Dockerfile` and pushes `ghcr.io/m0wa/se:<version>` plus `:latest` on every
tagged release, alongside the `.deb` packages -- see that job for the
exact `docker/build-push-action` invocation. Pull a specific released
version instead of `:latest` by setting `SEARCHENGINE_IMAGE` in `.env`
(see `.env.example`).

## Why this doesn't reuse `packaging/nginx/searchengine.conf`

That config's `proxy_pass http://127.0.0.1:8081` (etc.) assumes nginx and
both Go binaries share one host's loopback interface -- true for the
`.deb` + systemd deployment, false here, where `admin`/`search`/`crawl`
are separate containers reachable only by their compose service name.
Maintaining a second, subtly-different nginx config just for compose
would drift from the real one exactly the way `CLAUDE.md`'s own nginx
sync rule warns about, for no real benefit: this compose file exposes
`search`/`admin` directly instead, and leaves TLS/reverse-proxying (if
wanted) to whatever the person running it already uses in front of other
containers -- see `packaging/nginx/README.md`'s routing rules if you're
setting one up by hand.
