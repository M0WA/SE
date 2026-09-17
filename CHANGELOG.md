# Changelog

## v4.4.0 - 2026-09-17

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Default a new crawl's "prioritize pages not yet indexed" checkbox to on by @M0WA in https://github.com/M0WA/SE/pull/82


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.3.0...v4.4.0


## v4.3.0 - 2026-09-17

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Settings page: fix spacing hierarchy, add hover help/defaults, update defaults by @M0WA in https://github.com/M0WA/SE/pull/80


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.2.0...v4.3.0


## v4.2.0 - 2026-09-17

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Switch pgvector columns from vector to halfvec, fixing >2000-dim indexing by @M0WA in https://github.com/M0WA/SE/pull/76
* Add per-endpoint configurable chunking for HTTP embedding endpoints by @M0WA in https://github.com/M0WA/SE/pull/77
* Fix pgvector migration race and embedding-chunking correctness bugs by @M0WA in https://github.com/M0WA/SE/pull/78


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.1.0...v4.2.0


## v4.1.0 - 2026-09-17

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Add URL canonicalization and content fingerprints (PR1/2: dedup foundation) by @M0WA in https://github.com/M0WA/SE/pull/73
* Add content-dedup batch job, merge, and admin surface (PR2) by @M0WA in https://github.com/M0WA/SE/pull/74


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.40...v4.1.0


## v4.0.40 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Declare browser-rendering runtime library dependencies for the .deb package by @M0WA in https://github.com/M0WA/SE/pull/70
* Fix multi-row admin forms rendering as a cramped row instead of a stacked list by @M0WA in https://github.com/M0WA/SE/pull/71


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.39...v4.0.40


## v4.0.39 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Fix embedding endpoint "Test connection" always failing with a blank API key by @M0WA in https://github.com/M0WA/SE/pull/68


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.38...v4.0.39


## v4.0.38 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Generalize "active for search" to a weighted multi-provider blend by @M0WA in https://github.com/M0WA/SE/pull/66


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.37...v4.0.38


## v4.0.37 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Tolerate the concurrent-CREATE-TABLE race during migrate() by @M0WA in https://github.com/M0WA/SE/pull/62
* Add external_labels.site to prevent cross-host metric collisions by @M0WA in https://github.com/M0WA/SE/pull/63
* Support any number of configurable HTTP embedding endpoints by @M0WA in https://github.com/M0WA/SE/pull/64


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.36...v4.0.37


## v4.0.36 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Add real weighted title/body embedding blending by @M0WA in https://github.com/M0WA/SE/pull/56
* Add independent hash/HTTP embedding enable toggles and storage table (Phase 1/4) by @M0WA in https://github.com/M0WA/SE/pull/57
* Store, compute, and search both hash and HTTP embeddings simultaneously (Phase 2+3/4) by @M0WA in https://github.com/M0WA/SE/pull/58
* Add admin UI for independently enabling hash/HTTP embeddings (Phase 4/4) by @M0WA in https://github.com/M0WA/SE/pull/59
* Fix upgrade regression: existing EmbeddingProvider=http would silently revert to hash by @M0WA in https://github.com/M0WA/SE/pull/60


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.35...v4.0.36


## v4.0.35 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* add admin embeddings page by @M0WA in https://github.com/M0WA/SE/pull/54


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.34...v4.0.35


## v4.0.34 - 2026-09-16

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Make the embedding rate limit cover crawling too, not just recompute by @M0WA in https://github.com/M0WA/SE/pull/52


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.33...v4.0.34


## v4.0.33 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Respect the embeddings provider's rate limit during recompute by @M0WA in https://github.com/M0WA/SE/pull/49
* Run BM25 and query embedding concurrently; make the recompute rate limit configurable by @M0WA in https://github.com/M0WA/SE/pull/50


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.32...v4.0.33


## v4.0.32 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Recreate the pgvector column when the embedding dimension changes by @M0WA in https://github.com/M0WA/SE/pull/47


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.31...v4.0.32


## v4.0.31 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Prefill the embedding model field from the provider's /models endpoint by @M0WA in https://github.com/M0WA/SE/pull/44
* Reset a stale in-progress embedding recompute flag left by a killed process by @M0WA in https://github.com/M0WA/SE/pull/45


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.30...v4.0.31


## v4.0.30 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Add a background image to the public search page by @M0WA in https://github.com/M0WA/SE/pull/40
* Fix "Search syntax" rendering struck through by the form's divider by @M0WA in https://github.com/M0WA/SE/pull/41
* Add a recompute-embeddings feature so a provider change doesn't need a re-crawl by @M0WA in https://github.com/M0WA/SE/pull/42


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.29...v4.0.30


## v4.0.29 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Document the release PR's recurring action_required gate by @M0WA in https://github.com/M0WA/SE/pull/27
* Escape crawled text before highlighting search snippets; add CSP/security headers by @M0WA in https://github.com/M0WA/SE/pull/28
* Block SSRF to loopback/private/reserved targets from every fetch path by @M0WA in https://github.com/M0WA/SE/pull/29
* Fix shell-injection via template interpolation in release workflows by @M0WA in https://github.com/M0WA/SE/pull/30
* Redact scheduled-crawl cookie/basic-auth credentials from admin responses by @M0WA in https://github.com/M0WA/SE/pull/31
* Rate-limit POST /login to stop unthrottled password guessing by @M0WA in https://github.com/M0WA/SE/pull/32
* Add an opt-in shared-secret check for crawl-server's internal API by @M0WA in https://github.com/M0WA/SE/pull/33
* Hash session tokens before storing them in the database by @M0WA in https://github.com/M0WA/SE/pull/35
* Redact the embedding API key from error bodies before they're persisted by @M0WA in https://github.com/M0WA/SE/pull/37
* Encrypt the embedding HTTP API key at rest by @M0WA in https://github.com/M0WA/SE/pull/34
* Quote log-injection-prone values written from request input by @M0WA in https://github.com/M0WA/SE/pull/36
* Fix "Run now" silently doing nothing for a schedule stuck in_progress by @M0WA in https://github.com/M0WA/SE/pull/38


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.28...v4.0.29


## v4.0.28 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* Add 8 Overview-page metrics: crawl health, throughput, PageRank spread by @M0WA in https://github.com/M0WA/SE/pull/23
* Fix silent mis-parsing of negated phrases and negated site: filters by @M0WA in https://github.com/M0WA/SE/pull/24
* Add optional support for a real trained embedding model by @M0WA in https://github.com/M0WA/SE/pull/25


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.27...v4.0.28


## v4.0.27 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Other Changes
* tag-release.yml: wait for the merge commit's own CI before tagging by @M0WA in https://github.com/M0WA/SE/pull/20
* Fix the site's type scale, label sizing, and a stray divider by @M0WA in https://github.com/M0WA/SE/pull/21


**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.26...v4.0.27


## v4.0.26 - 2026-09-15

<!-- Release notes generated using configuration in .github/release.yml at main -->

## What's Changed
### Dependencies
* Bump golang from 1.25-bookworm to 1.27-bookworm by @dependabot[bot] in https://github.com/M0WA/SE/pull/5
* Bump actions/setup-node from 4.4.0 to 7.0.0 by @dependabot[bot] in https://github.com/M0WA/SE/pull/6
* Bump actions/setup-go from 5.6.0 to 7.0.0 by @dependabot[bot] in https://github.com/M0WA/SE/pull/7
* Bump actions/upload-artifact from 4.6.2 to 7.0.1 by @dependabot[bot] in https://github.com/M0WA/SE/pull/8
* Bump actions/download-artifact from 4.3.0 to 8.0.1 by @dependabot[bot] in https://github.com/M0WA/SE/pull/10
* Bump actions/cache from 4.3.0 to 6.1.0 by @dependabot[bot] in https://github.com/M0WA/SE/pull/11
* Bump actions/checkout from 4.4.0 to 7.0.1 by @dependabot[bot] in https://github.com/M0WA/SE/pull/13
* Bump softprops/action-gh-release from 2.6.2 to 3.0.3 by @dependabot[bot] in https://github.com/M0WA/SE/pull/14
* Bump jsdom from 25.0.1 to 30.0.1 by @dependabot[bot] in https://github.com/M0WA/SE/pull/9
* Bump the go-minor-and-patch group with 4 updates by @dependabot[bot] in https://github.com/M0WA/SE/pull/12
### Other Changes
* Add a branch+PR release system, pin workflow actions to commit SHAs by @M0WA in https://github.com/M0WA/SE/pull/2
* Fix CLAUDE.md: the release ruleset no longer requires an approving review by @M0WA in https://github.com/M0WA/SE/pull/3
* Add Dependabot for Go, npm, GitHub Actions, and Docker version updates by @M0WA in https://github.com/M0WA/SE/pull/4
* Bump CI's Go and Node toolchain versions by @M0WA in https://github.com/M0WA/SE/pull/15
* Auto-merge Dependabot PRs once their CI check goes green by @M0WA in https://github.com/M0WA/SE/pull/16
* Redesign the Crawl page to match Settings, rename it to Schedule by @M0WA in https://github.com/M0WA/SE/pull/17
* Fix SQLITE_BUSY on the very first DB ping, not just WAL setup by @M0WA in https://github.com/M0WA/SE/pull/18

## New Contributors
* @M0WA made their first contribution in https://github.com/M0WA/SE/pull/2
* @dependabot[bot] made their first contribution in https://github.com/M0WA/SE/pull/5

**Full Changelog**: https://github.com/M0WA/SE/compare/v4.0.25...v4.0.26

