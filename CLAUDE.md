# searchengine

A self-hosted hybrid (BM25 + semantic) search engine. Hexagonal architecture:
`internal/domain` (pure logic), `internal/ports` (interfaces), `internal/application`
(use cases), `internal/adapters/*` (SQL repo, HTTP fetcher, REST API, etc.).

Three independent binaries share one SQL database (SQLite locally/CI, Postgres in
the dev deployment):

- `cmd/search` — public, internet-facing. Index page + `/search` API.
- `cmd/admin` — internal, reached via nginx `/admin`, `/login`, `/logout`. Every
  `/admin/api/*` endpoint. Talks to crawl-server over the network.
- `cmd/crawl` — internal only, never exposed by nginx. Runs crawls, tracks job state.

## Keep the OpenAPI spec up to date

`openapi.yaml` (repo root) documents every JSON REST endpoint across all three
binaries. **Whenever you add, remove, or change a REST endpoint's route, method,
parameters, request/response shape, or status codes, update `openapi.yaml` in the
same change.** This is a hard requirement, not a nice-to-have — treat a REST
change without a matching spec update as incomplete work. Validate after editing:

```
npx --yes @redocly/cli@latest lint openapi.yaml
```

(Missing `operationId` / missing 4xx-on-GET warnings are expected and fine to leave;
an actual error is not.)

## Before committing

- `go build ./...`, `go vet ./...`, `gofmt -l .` must be clean.
- `go test ./... -race -count=1` must pass. Prefer `-race` even for quick checks —
  this codebase has genuine concurrency (fire-and-forget background goroutines,
  concurrent crawl workers).
- Push, then watch CI to a genuinely green run before deploying.
- Commits end with:
  ```
  Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_011oQftaLHMZhXYAmkTegESW
  ```

## Test coverage

Keep test coverage close to 100% for both Go and JavaScript — not just "the
tests I thought of pass," but every branch a change adds (happy path, error
path, not-configured/empty-input edge cases).

- **Go**: `make coverage-check` runs the suite with a coverage profile and
  fails under 100%. Check new/changed code with
  `go test ./... -coverprofile=/tmp/cov.out -covermode=atomic && go tool cover
  -func=/tmp/cov.out | grep <yourFunc>` before calling a change done. The admin
  handler tests in `internal/adapters/restapi/admin_test.go` are the reference
  pattern: fakes implementing the relevant port + `httptest`, one test per
  status code/branch (success, not-configured/503, service-error/500,
  method-not-allowed/405, and any handler-specific branch like a partial
  failure or an empty-result case).
- **JavaScript** (`internal/adapters/restapi/*.js` and the inline `<script>`
  in `admin_*.html`/`crawl.html`): there's no automated JS test runner in this
  repo yet. Until one exists, "tested" means actually exercised, not just
  read: build the binaries, run them locally (or verify against
  `se.mo-sys.de` post-deploy) and drive the change through a real browser —
  headless Chromium over the DevTools Protocol works well for this
  (`Page.navigate`, then `Runtime.evaluate` to click/type and read back DOM
  state, then `Page.captureScreenshot`). Never report a JS/HTML change as
  working without having actually run it this way.

## Deployment

`se.mo-sys.de` is the dev/test deployment, not production — deploying there is
routine and doesn't need per-instance confirmation. Downwards compatibility does
not matter (no migration shims needed for breaking changes) — this does not excuse
actual correctness bugs.

## Patterns worth knowing

- **Fire-and-forget background work**: HTTP handlers that kick off long-running
  work (crawl jobs, bulk document delete) spawn `go func() { ctx :=
  context.Background(); ... }()` so the work survives the triggering request's
  context being cancelled by client disconnect/navigation. Never use `r.Context()`
  for detached background work — it cancels the instant the client disconnects.
- **Admin list-view search filters**: client-side, case-insensitive regex against
  an already-fetched in-memory array (see `filterPages`/`filterJobs`/`filterDocs`/
  `filterSchedules` in `internal/adapters/restapi/*.html`). Reuse this pattern for
  any new unbounded-feeling admin list rather than inventing a new one.
- **Backend list endpoints** take a `?limit=` query param via the shared
  `intQueryParam` helper (`internal/adapters/restapi/admin.go`) with a sane
  default constant — every admin list endpoint should have one.
