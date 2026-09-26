# searchengine

A self-hosted AI chatbot with search-engine features: a chat model that can
cite live web results and this instance's own crawled index side by side,
backed by a hybrid (BM25 + semantic) search engine also exposed as a plain
public search UI. Hexagonal architecture:
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

## Keep nginx's routing config in sync

`packaging/nginx/searchengine.conf` (+ its `README.md`) is the tracked source of
truth for how nginx splits traffic between search-server (`127.0.0.1:8080`) and
admin-server (`127.0.0.1:8081`) by path -- **whenever a route or served-asset path
changes in a way that affects which top-level path prefix it lives under, update
this file (and the README's routing list) in the same change.** postinst does
*not* install or reload nginx config -- a real deployment's `/etc/nginx/...` can
silently drift from this tracked copy unless someone manually re-syncs it, so
don't assume "I updated the Go route" is the whole job.

The concrete gotcha that already caused a real outage: nginx's `location /admin`
(no trailing slash, no exact/regex modifier) is a plain **string prefix** match,
not path-segment-aware -- it matches anything starting with those five
characters, not just `/admin/...`. A new admin-only static asset route "just
works" through the existing rule only if its path happens to literally start
with `/admin`, `/login`, or `/logout` (e.g. `admin_schedule.js` does,
`admin.js` does); anything else (e.g. a file that was named `crawl.js`) falls
through to the catch-all `location /` rule instead, which proxies to
search-server -- wrong backend, wrong auth requirements, and it fails in a
confusing way (an HTML login-redirect page served in place of the JS, so the
page's own script silently never runs) rather than a clean 404. When adding a
new served path, either name it so it already falls under an existing prefix,
or add a real `location` block for it in `packaging/nginx/searchengine.conf`.

## Keep the architecture documentation in sync

`docs/architecture/README.md` documents every component in the hexagonal
architecture -- `internal/domain`, `internal/ports`, `internal/application`,
every `internal/adapters/*` package, the three binaries, and how they connect
to external systems (Postgres/SQLite, nginx, the crawl-server network call,
HTTP embedding/chat endpoints -- including the self-hosted vLLM instances on
gpu.mo-sys.de, IONOS monitoring). **Whenever a component is added, removed,
or its relationships change -- a new adapter package, a new port/use-case, a
binary's responsibilities shifting, a new external dependency -- update
`docs/architecture/README.md` in the same change.** Treat an architectural
change without a matching doc update as incomplete work, same as
`openapi.yaml`/the nginx config above.

The diagram is **not** embedded as a Mermaid code block in the markdown --
GitHub's markdown renderer doesn't reliably render Mermaid diagrams (a real,
observed failure, not a hypothetical one), so it silently shows the raw code
instead. The diagram lives as its own source file,
`docs/architecture/architecture.mmd`, rendered to a real picture,
`docs/architecture/architecture.svg`, which the markdown embeds as a plain
image. **Editing the architecture means editing
`docs/architecture/architecture.mmd` and regenerating
`docs/architecture/architecture.svg` in the same change** (see
`docs/architecture/README.md`'s own top section for the exact `mmdc`
command) -- an `.mmd` edit with a stale `.svg` is incomplete work, same as
any other change here.

## Keep the Grafana dashboards in sync

`packaging/grafana/dashboards/*.json` are the tracked source of truth for
every Grafana dashboard backing this deployment's monitoring (see
`packaging/grafana/README.md` for the full list and their datasource/panel
layout) -- host and Postgres metrics, SearXNG per-engine metrics, and GPU/
vLLM metrics, each fed by `packaging/prometheus/` or
`packaging/prometheus-gpu/`. These dashboards get edited live (Grafana UI or
API) far more often than this repo's other config, which makes them easy to
silently drift: a dashboard that only ever exists in Grafana's own database
is not "documented" in any sense this repo's other sync rules assume.
**Whenever a dashboard is created or edited live -- a new panel, a new
template variable, a new dashboard entirely -- re-export it into
`packaging/grafana/dashboards/` in the same change** (see that directory's
README for the exact export command). Treat a live-only dashboard edit as
incomplete work, same as every other rule in this section.

## Keep firewall rules in sync

`packaging/firewall/` (`se-mo-sys-de.sh`, `gpu-mo-sys-de.sh`, and its own
README) is the tracked source of truth for each host's iptables `INPUT`
chain -- an optional, additional layer on top of every service's own
bind-address choice (nginx/sshd are the only things meant to be publicly
reachable; everything else already binds loopback or a private-LAN IP, so
this firewall is defense in depth, not the only thing preventing exposure
today). **Whenever a service's bind address changes on either host -- a
new port opened on a public interface, a new private-LAN peer this
deployment depends on -- update the matching script in the same change**,
same as every other rule in this file. Never apply a live firewall change
without following that README's verify-before-persist steps first: a
misapplied rule can permanently lock out the only way in, and there is no
console/KVM fallback assumed here.

## Keep the user manual in sync

`docs/manual/` is the single reference for everything about running and
using searchengine -- one markdown file per topic, not one giant page:

- **Installation & configuration** (`installation.md`, `docker-
  installation.md`, `environment-variables.md`, `config-files.md`,
  `infrastructure.md`) -- every environment variable any binary reads
  (`bootstrap.GetEnv`/`os.Getenv`), every config file under `packaging/`
  and its install/deploy path, and the end-to-end installation checklist,
  for both the `.deb` and Docker Compose install paths. **Whenever any of
  these changes -- a new env var, a new or renamed config file, a new
  required install step -- update the matching page in the same change.**
  `docker-installation.md` specifically is this manual's own walkthrough of
  `packaging/docker/README.md`'s practical how-to content (quickstart,
  seeding the admin account, configuring a provider) -- that README stays
  the deeper, maintainer-facing reference (image internals, publishing,
  why it doesn't reuse the nginx config), but whenever its quickstart
  steps, required `.env` vars, or documented image capabilities change,
  update `docker-installation.md` to match in the same change, same as
  every other rule in this section.
- **Every other page** -- one per admin settings page plus the public
  search/chat page and the self-service `/account*` pages, each
  screenshotted (`docs/manual/images/*.png`) and explained field by field
  (a database-backed setting an admin edits through the web UI --
  `domain.OperationalSettings`, `TuningSettings`, `EmbeddingHTTPEndpoint`,
  `ChatEndpoint`, `MCPServer`, ...). **Whenever an admin page changes -- a
  field added/removed/renamed, a new admin page, a changed default or
  validation rule, a UI redesign that changes what the page looks like --
  update the matching page in the same change**, including retaking that
  page's screenshot if its layout changed enough to make the old one
  misleading.

`docs/manual/README.md` is the index -- grouped links to every page below
it, in the same grouping as the real admin nav rail. Every page links back
to it (`[← Manual home](README.md)`) and to its immediate neighbors
(`← Previous · ↑ Manual home · Next →`) in that same group order -- keep
both when adding, removing, splitting, or reordering a page: update
`README.md`'s group list and the previous/next page's own footer links, not
just the page itself. Split a page that's grown too long (a good rule of
thumb: if a single settings page's own content stops fitting on one
reasonable scroll, look at whether it has a natural sub-grouping worth its
own pages, the way the giant `/admin/settings` page's four collapsible
groups each became `settings-search-ranking.md`/`settings-crawling.md`/
`settings-content-rules.md`/`settings-system.md` off of a `settings.md`
hub) rather than letting one file keep growing.

Plain markdown throughout, deliberately -- GitHub renders it natively in
the repo browser, and it's also served as a real site via GitHub Pages
(`docs/_config.yml`, enabled in repo Settings -> Pages -> Deploy from a
branch -> main -> /docs); a single source avoids the drift risk a separate
hand-maintained HTML copy would carry. Treat a config- or UI-surface change
without a matching manual update as incomplete work, same as every other
rule in this section.

To retake a screenshot: run `cmd/search`/`cmd/admin`/`cmd/crawl` locally
against a scratch SQLite DB, log in, and drive a headless Chromium instance
over the Chrome DevTools Protocol (`Page.navigate` + `Page.captureScreenshot`
via a raw CDP WebSocket connection -- the `websocket-client` PyPI package
works well for this, no browser-automation framework needed). Admin pages
load `/style.css` and `/admin_*.js` from the *search-server's* mux in
production (see `packaging/nginx/README.md`'s routing split) -- screenshot
against a small local reverse proxy that mirrors that same
`/admin`|`/login`|`/logout` string-prefix split, or the pages will render
unstyled.

**Screenshots use dark mode.** The app has no in-app theme toggle -- both
palettes live behind one `@media (prefers-color-scheme: dark)` block in
`internal/adapters/restapi/style.css` -- so getting the dark palette means
emulating it at the CDP layer before navigating:
`Emulation.setEmulatedMedia({features: [{name: 'prefers-color-scheme',
value: 'dark'}]})`. Two more gotchas hit while building this pipeline,
worth not re-discovering the hard way:
- Screenshot the **login page before logging in**, and call
  `Network.clearBrowserCookies` right before it even if the Chromium
  profile is reused across runs -- a stale session cookie makes `/login`
  silently redirect to `/admin` and screenshot the wrong page entirely.
- Reset `Emulation.setDeviceMetricsOverride` to the target width/height at
  the *start* of every capture, not just for full-page ones -- it persists
  across navigations, so a fixed-height capture (e.g. the public chat page,
  deliberately not full-page since its background is mostly decorative)
  right after a full-page one otherwise inherits that previous page's tall
  viewport instead of the height actually requested.

## Keep the root README in sync

`README.md` (repo root) is a map of the documentation, not documentation
itself -- it links to `docs/manual/`, `docs/architecture/README.md`, and
every `packaging/*/README.md`, and lists
the current binaries. **Whenever a new top-level doc or `packaging/`
subdirectory with its own README is added, or a binary is added/removed,
update `README.md`'s tables in the same change.** Treat a new doc or
package that isn't linked from here as incomplete work, same as every other
rule in this section.

`docs/index.md` is this same map's GitHub Pages landing page (the Pages
build serves from `docs/`, so the repo-root `README.md` itself is outside
its reach -- see `docs/_config.yml`) -- a small, deliberately shorter
version of the same "Start here" list, with paths relative to `docs/`
instead of the repo root. Update it alongside `README.md` for the same
triggers; it doesn't need the Binaries/Development sections `README.md`
has, just the links.

## Keep cmd/e2e-check up to date

`cmd/e2e-check` (see its own doc comments, and the root `README.md`'s
"Development" section for how to run it) is a manual, dev-host-only,
never-CI-wired smoke test that logs into a real deployment and drives real
chat/MCP/agent/search/account/admin traffic end to end. It is real
verification, not a nice-to-have, and it goes stale exactly like
`openapi.yaml`/the nginx config/the architecture doc/the user manual do if
left alone. **Whenever a change adds, removes, or changes the behavior of a
user-facing feature it's positioned to cover -- a chat/search/account/admin
REST endpoint, an MCP server or agent capability, a login/session/role
rule, a persistent-chat or file-attachment flow -- update `cmd/e2e-check` in
the same change**: add a new check for new behavior, fix an existing one
that now asserts something no longer true, or remove one for behavior that
no longer exists. Treat a feature change that leaves `cmd/e2e-check`
silently asserting the old behavior (or simply not covering the new one) as
incomplete work, same as every other rule in this section. This doesn't
replace the Go/JS unit test coverage bar below -- it's the live,
cross-service layer neither of those reaches on their own.

## Before committing

- `go build ./...`, `go vet ./...`, `gofmt -l .` must be clean.
- `go test ./... -race -count=1` must pass. Prefer `-race` even for quick checks —
  this codebase has genuine concurrency (fire-and-forget background goroutines,
  concurrent crawl workers).
- `npm test` (run `npm install` once first) must pass for any change touching
  `internal/adapters/restapi/*.js` — see "Test coverage" below.
- Push your branch and open a PR (`gh pr create`), then watch CI go green on
  it — **not** a direct push to `main`; see "Branch, PR & release workflow"
  below, since `main` is ruleset-protected with no bypass for anyone.
- Commits end with:
  ```
  Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_011oQftaLHMZhXYAmkTegESW
  ```

## Branch, PR & release workflow

`main` is protected by a repository ruleset (`main-protection`) with **no
bypass for anyone, admins included** (`bypass_actors: []`,
`current_user_can_bypass: "never"` — checked via `gh api
repos/M0WA/SE/rulesets`). It requires, for every change: a PR (no direct
pushes, no force-pushes) with a green "Build, vet & test" status check
(`ci.yml`'s `test` job — its `name:` must keep matching that exact string,
since the ruleset names it literally, not by job ID). An approving review
is *not* required (`required_approving_review_count: 0` — deliberately
dropped from an initial `1`, since a solo/single-session workflow can never
supply a second approver to self-approve against), so a PR can be merged
the moment CI goes green on it, same-session, with no one else needed — but
one still always has to exist and pass CI first; there is no path that
skips that. A companion ruleset (`release-tag-immutability`) makes every
`v*.*.*` tag immutable the same way — once published, a release tag can
never be deleted or moved, only superseded by a new one.

**Standard change flow** — this applies to every change from here on,
including a routine fix or a small doc update, not just large features:
```
git checkout -b <descriptive-branch-name>
# ... commit ...
git push -u origin <descriptive-branch-name>
gh pr create --fill
# wait for CI to go green on the PR
gh pr merge --squash   # or --merge/--rebase; any of the three is allowed
```

**Cutting a release** is the same PR flow, automated end to end, and now
starts itself:
1. `prepare-release.yml` runs automatically on every push to `main` (not
   just `gh workflow run prepare-release.yml -f version=X.Y.Z`, though that
   still works for an explicit version — leave `version` blank to get the
   same auto-bump a push gets). A `gate` job decides whether to actually
   open a release PR: it skips if the last commit on `main` is itself a
   `release/vX.Y.Z` branch merging in (that's what `tag-release.yml` reacts
   to — opening another release PR right after would just propose
   re-releasing nothing new) or if a "Release vX.Y.Z" PR is already open
   (waits for that one first rather than stacking a second). Otherwise it
   bumps the latest `vX.Y.Z` tag's patch version by one, generates a
   changelog (the same "generate release notes" GitHub API call
   `release.yml`'s publish step uses), prepends a dated section to
   `CHANGELOG.md` on a new `release/vX.Y.Z` branch, and opens a "Release
   vX.Y.Z" PR into `main` with that changelog as its body. Don't hand-edit
   `CHANGELOG.md` outside of reviewing that PR — it's regenerated by the
   workflow every time. Automating this only automates picking a version
   number and opening the PR — it does not skip either safety gate below,
   both of which still require a person.
2. Once its CI run (the same "Build, vet & test" job as always — a real,
   if usually uneventful, check that nothing about `main` is broken right
   before cutting a release from it) goes green, merge it. **This PR's CI
   run commonly comes up stuck at `action_required` instead of running at
   all** (`gh run list --branch release/vX.Y.Z`) — the release branch's
   HEAD commit is authored by `github-actions[bot]` (`prepare-release.yml`
   pushed it), and that alone is enough for GitHub to hold the workflow
   run for manual approval on this repo, even though the PR isn't from a
   fork. No ruleset here controls it (checked both — neither
   `main-protection` nor `release-tag-immutability` mention it), so
   there's nothing to permanently disable short of finding the right
   org/repo Actions setting, which wasn't locatable via the API. Approve
   it directly to unblock: `gh api -X POST
   repos/M0WA/SE/actions/runs/<run-id>/approve`, then wait for it to
   actually complete before merging.
3. Merging triggers `tag-release.yml`, which first *waits for the merge
   commit's own "Build, vet & test" run on `main`* to finish (a squash/
   merge/rebase produces a new commit with its own separate push-triggered
   CI run — distinct from whatever ran on the PR branch pre-merge, and not
   necessarily even started yet the instant the merge lands) and aborts,
   untagged, if that doesn't pass. Only then does it tag the merge commit
   as `vX.Y.Z` and dispatch `release.yml` for that tag (a direct tag push
   from `tag-release.yml`'s own `GITHUB_TOKEN` deliberately would *not*
   trigger `release.yml`'s push trigger — see the comments on both
   workflows for why `workflow_dispatch` is used instead). `release.yml`
   builds the `.deb` packages and publishes the GitHub Release, with
   `generate_release_notes: true` categorized by `.github/release.yml`
   (labels: `feature`/`enhancement`, `fix`/`bug`, `release`, else "Other
   Changes").
4. Deploying that release to `se.mo-sys.de` is a separate, manual step (see
   "Deployment" below) — nothing here does that automatically.

**Workflow files pin every `uses:` action to a commit SHA, never a mutable
version tag** (e.g. `actions/checkout@11d5960a326750d5838078e36cf38b85af677262
# v4.4.0`, not `actions/checkout@v4`) — supply-chain hardening: a tag can be
force-moved by the action's own maintainer (or anyone who compromises that
account) to point at different code without anything in *this* repo's
history showing a change. When adding a new `uses:` line or bumping a
pinned action, resolve the tag to its commit SHA first (e.g. `gh api
repos/<owner>/<action>/commits/<tag> --jq .sha`) and update the trailing
version comment to match — never add a bare `@v<N>` reference.

**Dependabot** (`.github/dependabot.yml`) opens weekly PRs bumping
`gomod`/`npm`/`github-actions`/`docker` dependencies, each labeled
`dependencies` (its own category in `.github/release.yml`'s generated
notes). `dependabot-auto-merge.yml` arms auto-merge on every one of them as
soon as it's opened, so it merges itself the moment its "Build, vet &
test" check goes green — it doesn't bypass the PR-required ruleset, it
just automates clicking the same "merge" a human otherwise would once
that check passes. A bump that fails CI (a real incompatibility, not a
fluke) needs an actual fix landed on `main` first — typically a CI
toolchain version bump (Go/Node) — after which re-running or resyncing
that Dependabot PR's branch lets it pass and auto-merge on its own; it's
not something to force through red.

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
- **JavaScript** (`internal/adapters/restapi/*.js` — every admin/search page's
  script now lives in its own external file, e.g. `admin_crawl.js`/`admin_schedule.js`,
  loaded via `<script src="...">` rather than inline; `admin.js` holds the
  helpers every page shares): a real test runner exists — Node's built-in
  `node:test`, no framework dependency beyond `jsdom` (the one `devDependency`
  in the root `package.json`, installed with `npm install`). Run the suite
  with `npm test`; run it with a coverage report via `npm run test:coverage`
  (plain `--experimental-test-coverage` — Node's own
  `--test-coverage-include`/`--test-coverage-exclude` flags, which would
  scope the report to just this project's files, turned out not to exist
  on either Node 18.19 or 20.20 despite being documented, so don't rely on
  them without checking the actual installed Node first. The report as-is
  can include a couple of unrelated node_modules-of-node_modules rows the
  test runner's own reporter loads for terminal color detection
  (`has-flag`/`supports-color`) — noise, not a real gap in this project's
  own `internal/adapters/restapi/*.js` coverage).
  - Test files are colocated as `<name>.test.js` next to the script they
    cover (e.g. `admin.test.js`, `admin_crawl.test.js`) — `node --test`'s default
    discovery pattern, and safely excluded from every `//go:embed` directive
    in `handler.go` (which names exact files, never a glob) so a `.test.js`
    file is never accidentally shipped in a binary.
  - `admin.js`/`admin_crawl.js`/etc. are still plain scripts meant for a `<script>`
    tag, not CommonJS modules — each ends with `if (typeof module !== 'undefined'
    && module.exports) { module.exports = {...} }`, a no-op in a browser
    (`typeof module` is undefined there) that lets a test `require()` the
    file directly. `internal/adapters/restapi/dom_helper.test_util.js`
    provides `setupDOM()`/`teardownDOM()` (a fresh jsdom Document assigned to
    `global.document`/`global.window` before each test) and `requireFresh()`
    (bypasses `require`'s module cache, since a script that runs
    `document.getElementById(...)` at top level — as several page scripts do
    — must see *this* test's fixture DOM, not one cached from an earlier
    test). A page script that expects `admin.js`'s helpers as ambient
    globals (the same way loading `<script src="/admin.js">` before its own
    `<script>` tag works in the real page) needs those assigned onto
    `global` before it's required — see `admin_crawl.test.js`'s `loadFixture()`
    for the pattern, including using the real `crawl.html` as the jsdom
    fixture so the test never drifts from the actual page structure.
  - Keep coverage close to 100% here the same way as Go. **Every script in
    `internal/adapters/restapi/*.js` must have a `<name>.test.js`** — this
    applies to existing untested files too, not just new/changed ones: if
    you touch a page that has no test file yet, add one as part of that
    change rather than leaving it untested. `admin.js` (the shared helpers)
    is fully covered; treat that, and `admin_crawl.test.js`, as the
    reference pattern for a new page's tests.
  - Unit tests check logic (parsing, filtering, formatting, DOM structure
    built from given data) in isolation. They don't replace actually
    exercising a change end-to-end in a real browser for anything that
    depends on real browser/network behavior, layout, or a full page's
    wiring — headless Chromium over the DevTools Protocol still works well
    for that (`Page.navigate`, then `Runtime.evaluate` to click/type and read
    back DOM state, then `Page.captureScreenshot`). Never report a JS/HTML
    change as working from unit tests alone if it touches anything a unit
    test can't see.

## SonarCloud

Analysis runs CI-based, not via SonarCloud's own "Automatic Analysis" GitHub
App (which is deliberately disabled on this project — the two conflict if both
run). `sonar-project.properties` (repo root) is the config `ci.yml`'s
`SonarSource/sonarqube-scan-action` step reads; it feeds real Go coverage
(`go test -coverprofile=build/coverage.out`) and real JS coverage
(`scripts/js-test-coverage.js`'s lcov output) into the Quality Gate rather than
leaving coverage/duplication conditions permanently green from having no data
at all. "SonarCloud Code Analysis" is a required status check on `main`
(alongside "Build, vet & test") — a PR cannot merge without it passing, same as
any other required check, no bypass.

**Coverage/duplication exclusions** (`sonar.coverage.exclusions`/
`sonar.cpd.exclusions`) are not the bar being lowered — each one documents a
specific, already-established reason a file/pattern structurally can't or
shouldn't be held to the Quality Gate's coverage or duplication condition (a
manual smoke tool never wired into CI, a wiring-only `main.go` entrypoint, a
test file's own lines — `go test -coverprofile` never instruments the test
file itself, only the package it tests, so a test file's new lines can never
show as "covered" and would otherwise fail every PR that adds Go test code).
See the properties file's own comments for the full, current list and
reasoning per entry — keep both in sync with each other and with this
paragraph's summary whenever the list changes.

**False positives**: a real finding gets fixed, not suppressed. A finding
confirmed to be a false positive for this codebase (e.g. `go:S2077` on
`internal/adapters/sqlrepo`'s `r.ph()` placeholder-substitution idiom, which
looks like dynamic SQL but only ever substitutes a fixed placeholder syntax,
never user input) is resolved directly on the SonarCloud issue via its API
(`do_transition` to `falsepositive`/`wontfix`, plus an `add_comment`
explaining why) — never via a blanket rule/path exclusion for that rule, since
an exclusion would also hide a real future SQL-injection bug landing in that
same file.

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
  an already-fetched in-memory array (see `filterPages`/`filterJobs`/`filterCrawls`
  in `internal/adapters/restapi/admin_jobs.js`, `filterDocs` in `admin_domain.js`).
  Reuse this pattern for any new unbounded-feeling admin list rather than
  inventing a new one.
- **Backend list endpoints** take a `?limit=` query param via the shared
  `intQueryParam` helper (`internal/adapters/restapi/admin.go`) with a sane
  default constant — every admin list endpoint should have one.
- **Icon conventions**: every admin-page icon (list-view action buttons, header
  buttons, chat-tab actions) must be monochrome black/white, never a colorful
  pictograph — a plain Unicode character is fine where the font renders one
  genuinely monochrome (e.g. "▶" run, "✎" edit), but the closest codepoint for
  some actions (magnifying glass, wastebasket, paperclip, gear, person, door)
  renders as a full-color emoji in most fonts, which breaks a page's otherwise
  monochrome icon language — use an inline SVG instead (`stroke="currentColor"`,
  sized via a dedicated `<selector> svg` CSS rule) so it matches the
  surrounding text color, hover, and focus states like a character glyph
  would. Reuse an existing icon rather than inventing a new one for the same
  action: `internal/adapters/restapi/admin.js`'s `ICON_SVGS`/`ACTION_GLYPHS`/
  `setIconLabel`/`actionsCell` are the shared registry and helpers behind
  every list page's Edit/Delete column (MCP servers, agents, users) and the
  Jobs page's View/Cancel/Run now/Edit/Delete icons — loaded as ambient
  globals via `<script src="/admin.js">`, same as `buildTable`/`getJSON`. An
  icon-only button carries its real action name via `title` (hover tooltip)
  and `aria-label` (screen reader), never only the glyph.
