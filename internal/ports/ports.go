package ports

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"searchengine/internal/domain"
)

// ErrDocumentNotFound is returned by SQLRepository/AdminRepository's
// DeleteDocument when no document with the given ID exists.
var ErrDocumentNotFound = errors.New("document not found")

// --- Secondary (driven) ports ---

type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

// FetchOptions carries per-request credentials for sites that need a
// session cookie or HTTP Basic auth to crawl, plus an optional UserAgent
// override (falls back to the process's configured default when empty).
type FetchOptions struct {
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	UserAgent     string
	// FetchTimeoutSeconds and MaxResponseBytes override the operational
	// defaults (domain.OperationalSettingsValues' FetchTimeout/
	// MaxResponseBytes) for this fetch alone when positive; zero means "use
	// the global default," the same convention UserAgent's empty-string
	// case already uses above.
	FetchTimeoutSeconds int
	MaxResponseBytes    int
	// Renderer selects how this fetch is done: "" defers to the caller's
	// default (typically the Tuning page's), RendererNone/Chromium/Firefox
	// override it. Only application.RenderAwareFetcher consults this --
	// httpfetcher.Fetcher ignores it, since it only ever does plain HTTP.
	Renderer string
	// NoRender forces the plain HTTP path regardless of Renderer or any
	// configured default -- set by crawlLoop's own sitemap.xml fetch,
	// which must never go through a real browser (its response is XML,
	// not a page to render, and a browser's XML viewer would corrupt it).
	NoRender bool
}

// AuthFetcher is a Fetcher that also accepts per-request credentials.
// httpfetcher.Fetcher implements both this and the plain Fetcher (used by
// the robots-checker, which never needs credentials).
type AuthFetcher interface {
	FetchWithOptions(ctx context.Context, url string, opts FetchOptions) (string, error)
}

// Renderer executes a page in a real headless browser and returns its final
// rendered HTML, for a site whose real content only exists after
// client-side JS runs. Implemented per browser engine (Chromium, Firefox);
// application.RenderAwareFetcher dispatches based on FetchOptions.Renderer.
type Renderer interface {
	Render(ctx context.Context, url string, opts FetchOptions) (string, error)
}

type RobotsChecker interface {
	Allowed(ctx context.Context, url string) bool
}

// EmbeddingProvider converts text into a vector.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dimensions() int
}

// SQLRepository is the port to the relational database.
type SQLRepository interface {
	// SaveDocument upserts doc, archiving its previous content to
	// document_versions first if changed (maxVersions bounds how many
	// survive, pruning the oldest). titleWeight repeats the title in the
	// indexed token stream ahead of the body. embeddings (one vector per
	// enabled provider, keyed by provider ID) are upserted in the same write.
	SaveDocument(ctx context.Context, doc domain.Document, embeddings map[string][]float32, maxVersions, titleWeight int) error
	// PostingsForTerms batch-fetches postings for every term in one query,
	// so a multi-term search issues one round trip, not one per term.
	// TotalDocs/AvgDocLen are left zero -- the caller fills them in from its
	// own corpus-wide stats cache rather than refetching them per term.
	PostingsForTerms(ctx context.Context, terms []string) (map[string][]domain.PostingStats, error)
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	// VocabularyStats reports the corpus's total distinct-term count, plus a
	// limit/offset page of terms ordered by sortBy/sortDir (each falls back
	// to a default on an unrecognized value) -- backs the admin vocabulary
	// page. A non-empty search restricts the listing and matchedCount to a
	// substring match; vocabSize always reflects the whole corpus.
	VocabularyStats(ctx context.Context, limit, offset int, search, sortBy, sortDir string) (vocabSize, matchedCount int, terms []domain.TermStat, err error)
	// AllTerms returns every distinct term with its doc/total frequency --
	// the full vocabulary (unlike VocabularyStats' bounded listing), used by
	// domain.VocabularyCache for fuzzy near-miss matching (domain.NearestTerm).
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
	// EmbeddingsForDocs batch-fetches provider's embeddings for exactly the
	// given doc IDs (typically a query's BM25-hit set), avoiding a
	// full-corpus scan. Each result's norm (domain.EmbeddedVector) was
	// precomputed at SaveDocument time, not recomputed per request.
	EmbeddingsForDocs(ctx context.Context, ids []string, provider string) (map[string]domain.EmbeddedVector, error)
	// SampleEmbeddings returns up to limit of provider's embeddings from
	// across the corpus, so a purely semantic match (no BM25 hits at all)
	// can still be found -- bounded regardless of how large the corpus is,
	// unlike a full "every document" scan.
	SampleEmbeddings(ctx context.Context, limit int, provider string) (map[string]domain.EmbeddedVector, error)
	// TopSemanticMatches finds queryVec's nearest neighbors via Postgres
	// pgvector's HNSW index, used instead of SampleEmbeddings when ANN is
	// available for provider. ok is false when it isn't (caller falls back
	// to SampleEmbeddings); a non-nil error is a real query fault.
	TopSemanticMatches(ctx context.Context, queryVec []float32, limit int, provider string) (matches map[string]domain.EmbeddedVector, ok bool, err error)
	// DocumentsByIDs batch-fetches documents for the given IDs in one
	// round trip (a missing ID is simply absent from the result, not an
	// error) -- the only document-lookup-by-ID this port exposes, since
	// every caller either already has a batch of IDs or can trivially pass
	// a single-element slice, avoiding a second, N+1-shaped method.
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	// DocumentsByIDsSortedByCrawledAt is DocumentsByIDs' counterpart for the
	// recency-sort path: same batched "WHERE id IN (...)" fetch, but ordered
	// by crawled_at descending (ties broken by id ascending) directly in
	// SQL -- backed by idx_documents_crawled_at -- so the caller never needs
	// to sort the fetched candidates itself.
	DocumentsByIDsSortedByCrawledAt(ctx context.Context, ids []string) ([]domain.Document, error)
	// DocumentIDsByHost returns the IDs of every document whose host exactly
	// matches one of hosts or is a subdomain of one -- forces a query's
	// site: matches into the search candidate set, since they otherwise
	// have no guarantee of appearing in the BM25-hit or semantic sample.
	DocumentIDsByHost(ctx context.Context, hosts []string) ([]string, error)
	// ResolveAliasHosts returns the real documents.host of every canonical
	// document reachable through an alias whose host matches hosts (same
	// rule as DocumentIDsByHost) -- expands a site: host list before
	// SiteAllowed filtering, which compares a candidate's own real host,
	// not an alias host a user might type after a merge or www fold.
	ResolveAliasHosts(ctx context.Context, hosts []string) ([]string, error)
	// HostsIndexed reports, for each host, whether any document is already
	// indexed for it (same matching rule as DocumentIDsByHost) -- used by
	// FollowIndexedDomains to widen link scope without needing every ID.
	HostsIndexed(ctx context.Context, hosts []string) (map[string]bool, error)
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	DeleteDocument(ctx context.Context, docID string) error
	// RecordDocumentAlias upserts one document_aliases row: aliasURL's
	// content lives under canonicalID, not its own document row -- used for
	// both a <link rel="canonical"> redirect and a content-dedup merge.
	// canonicalID need not already exist in documents (a forward-declared
	// alias resolves once that document is actually saved).
	RecordDocumentAlias(ctx context.Context, aliasURL, canonicalID, reason string) error
}

// PageRankRepository is the narrow port application.RunPageRankJob needs:
// load the current link graph, then write back each document's fresh
// score. cmd/crawl (periodic ticker + one run right after each crawl) is
// the only caller.
type PageRankRepository interface {
	// LinkGraph loads the whole crawled link graph as an adjacency map (doc
	// ID -> IDs it links to) in one query; a link to a never-crawled URL is
	// simply omitted.
	LinkGraph(ctx context.Context) (map[string][]string, error)
	// UpdatePageRanks batch-writes each given ID's fresh score. A document
	// absent from scores (no in/out links at all) is left untouched, not
	// zeroed.
	UpdatePageRanks(ctx context.Context, scores map[string]float64) error
}

// ErrContentDedupAlreadyRunning is returned by
// application.RunContentDedupJobWithStatus when
// ContentDedupRepository.TryAcquireContentDedupLock lost the race to
// another process's already-running call -- callers (the admin recompute
// handler, the crawl-server scheduler) treat this as a normal, expected
// outcome, not a real error.
var ErrContentDedupAlreadyRunning = errors.New("content dedup is already running")

// ContentDedupRepository is the narrow port application.RunContentDedupJob
// needs: read every document's fingerprint, then merge whatever duplicate
// groups it finds. Two independent, unsynchronized callers can invoke this
// against the same database: cmd/crawl's own ticker/on-crawl-complete
// scheduler, and the admin-server's "recompute now" button -- two separate
// OS processes, so TryAcquireContentDedupLock exists specifically to keep
// them from ever running RunContentDedupJob at the same time (see its own
// doc comment for what goes wrong if they do).
type ContentDedupRepository interface {
	// AllDocumentFingerprints lists every document's id/url/host/
	// content_hash/simhash/crawled_at in one query -- just enough to group
	// duplicates, not the full domain.Document (wasted memory at this scale).
	AllDocumentFingerprints(ctx context.Context) ([]domain.DocumentFingerprint, error)
	// MergeDocuments folds every loserIDs document into canonicalID: each
	// loser's own aliases are repointed (path compression), a fresh alias
	// is recorded for its URL, and its document row is removed via the same
	// cascade DeleteDocument uses. reason records why on each alias row.
	MergeDocuments(ctx context.Context, canonicalID string, loserIDs []string, reason string) error
	// TryAcquireContentDedupLock atomically claims the single, DB-backed
	// (so it works across processes, unlike an in-memory bool)
	// content-dedup lock, returning true if this call got it. Without this,
	// two concurrent RunContentDedupJob calls each compute their own
	// duplicate groups from an independent snapshot of
	// AllDocumentFingerprints; if one call's merge deletes a document the
	// other call's snapshot still believes is a live canonical, that other
	// call goes on to record a fresh document_aliases row pointing at an
	// id that no longer exists in documents -- a dangling canonical the
	// admin alias-groups page then displays with an empty URL (confirmed
	// in production: exactly this pattern, traced to the admin-server
	// recompute button firing while cmd/crawl's own scheduler was already
	// mid-run). Must be paired with ReleaseContentDedupLock (defer it
	// immediately after a successful acquire).
	TryAcquireContentDedupLock(ctx context.Context) (bool, error)
	// ReleaseContentDedupLock clears the lock TryAcquireContentDedupLock
	// claimed. Idempotent: releasing an already-released lock is a no-op,
	// not an error, so a deferred call after a failed/short-circuited run
	// never itself needs error handling.
	ReleaseContentDedupLock(ctx context.Context) error
}

// EmbeddingRepository is the narrow slice of *sqlrepo.Repository
// application.RunEmbeddingRecomputeJob needs: iterate every document ID,
// fetch its stored Text, and overwrite just its embedding.
type EmbeddingRepository interface {
	// AllDocumentIDs lists every document ID in the corpus, ordered so
	// repeated calls (and the batches RunEmbeddingRecomputeJob fetches
	// against DocumentsByIDs) are stable and deterministic.
	AllDocumentIDs(ctx context.Context) ([]string, error)
	// DocumentsByIDs batch-fetches each document's URL/title/text -- see
	// AdminRepository's identical method (implemented once, satisfying
	// both narrow ports).
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	// UpdateEmbedding overwrites one document's embedding for every provider
	// in embeddings (and its ANN pgvector column, when enabled) -- unlike
	// SaveDocument, never re-tokenizes text or touches postings/links/
	// versions/pagerank, since only the vector changed.
	UpdateEmbedding(ctx context.Context, id string, embeddings map[string][]float32) error
}

// SessionStore backs the admin/search login system's session tokens, via a
// shared "sessions" table so a login on one process (e.g. admin-server's
// /login) is recognized by every process serving the site -- something an
// in-memory store could never do across separate OS processes. A session
// carries a role (domain.RoleAdmin or domain.RoleUser) and, for a
// domain.RoleUser session, the domain.User.ID it belongs to (empty for
// domain.RoleAdmin) -- set once at CreateSession time and resolved fresh by
// ValidSession on every request, never derived from anything the client
// sends (the se_session cookie itself stays an opaque random token).
type SessionStore interface {
	// CreateSession persists a freshly issued token, valid until expiresAt,
	// with the given role and userID (userID is "" for a domain.RoleAdmin
	// session).
	CreateSession(ctx context.Context, token string, expiresAt time.Time, role string, userID string) error
	// ValidSession reports whether token names a session that hasn't
	// expired yet and, if so, the role and userID it was created with.
	ValidSession(ctx context.Context, token string) (valid bool, role string, userID string, err error)
	// RevokeSession deletes a session outright (a sign-out). Revoking an
	// unknown or already-expired token is not an error.
	RevokeSession(ctx context.Context, token string) error
}

// ErrUserNotFound is returned by UserStore's GetUser/GetUserByUsername/
// UpdateUser/DeleteUser when no matching row exists.
var ErrUserNotFound = errors.New("user not found")

// ErrUsernameTaken is returned by UserStore.CreateUser when username is
// already used by a different row (case-sensitive, unique).
var ErrUsernameTaken = errors.New("username already taken")

// UserStore persists DB-backed regular-user accounts -- see domain.User's
// doc comment for how these differ from the single hardcoded admin
// account. A list of many, like MCPServerStore, CRUD over IDs.
type UserStore interface {
	ListUsers(ctx context.Context) ([]domain.User, error)
	GetUser(ctx context.Context, id string) (domain.User, error)
	// GetUserByUsername returns ErrUserNotFound if no row has that username.
	GetUserByUsername(ctx context.Context, username string) (domain.User, error)
	// CreateUser returns ErrUsernameTaken if u.Username is already in use.
	CreateUser(ctx context.Context, u domain.User) error
	// UpdateUser replaces u's stored fields wholesale (used for a password
	// reset -- see restapi.handleAdminUpdateUser). Returns ErrUserNotFound
	// if no row with u.ID exists.
	UpdateUser(ctx context.Context, u domain.User) error
	// DeleteUser returns ErrUserNotFound if no row with id exists.
	DeleteUser(ctx context.Context, id string) error
}

// HealthChecker is a cheap liveness check for the shared database
// connection, used only by GET /healthz. Ping must stay a plain connection
// check (what sql.DB.PingContext already does) -- never a real query against
// application tables -- so the endpoint stays safe for frequent automated
// polling.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// AdminRepository is the subset of SQLRepository the admin diagnostics UI
// needs -- a narrower dependency than the full port.
type AdminRepository interface {
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	VocabularyStats(ctx context.Context, limit, offset int, search, sortBy, sortDir string) (vocabSize, matchedCount int, terms []domain.TermStat, err error)
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	SearchDomains(ctx context.Context, q string, limit int) ([]domain.DomainSummary, error)
	DocumentVersions(ctx context.Context, docID string) ([]domain.DocumentVersion, error)
	DocumentsOverview(ctx context.Context, topDomains int) (domain.DocumentsOverview, error)
	// PostingsForTerm returns at most limit postings for term, ordered by
	// term frequency descending, so a limited result still surfaces the
	// strongest matches rather than an arbitrary subset.
	PostingsForTerm(ctx context.Context, term string, limit int) ([]domain.PostingStats, error)
	// DocumentsByIDs backs the vocabulary term-detail view: given
	// PostingsForTerm's doc IDs, fetch each document's URL/title/text so a
	// match excerpt can be built for it.
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	DeleteDocument(ctx context.Context, docID string) error
	// PageRankDistribution reports the min, max and average
	// documents.pagerank value across the whole corpus -- the admin
	// PageRank debug page's headline numbers. All three are 0 for an empty
	// corpus.
	PageRankDistribution(ctx context.Context) (min, max, avg float64, err error)
	// TableRowCounts reports how many rows each of the schema's tables
	// currently holds, keyed by table name -- the admin database
	// diagnostics page's per-table breakdown.
	TableRowCounts(ctx context.Context) (map[string]int64, error)
	// PoolStats reports the live DB connection pool's current limits and
	// usage (see sqlrepo.Repository.PoolStats) -- the admin database
	// diagnostics page's connection-pool panel.
	PoolStats() sql.DBStats
	// CrawlJobOutcomes reports how many crawl jobs created at or after
	// since finished in each terminal status (done/failed/cancelled) --
	// the admin Overview page's crawl-outcome donut. A job still queued or
	// running is excluded (see domain.CrawlJobOutcomeCount).
	CrawlJobOutcomes(ctx context.Context, since time.Time) ([]domain.CrawlJobOutcomeCount, error)
	// DailyFetchOutcomes reports, for each day at or after since, how many
	// crawl_job_pages rows landed in each fetch outcome -- the admin
	// Overview page's throughput/fetch-outcome stacked bar.
	DailyFetchOutcomes(ctx context.Context, since time.Time) ([]domain.DailyFetchOutcome, error)
	// DocumentsIndexedByDay reports how many documents' crawled_at falls on
	// each day at or after since -- the admin Overview page's
	// documents-indexed-over-time trend (DocumentsOverview's AgeBuckets
	// reads the same column bucketed coarsely instead of day-by-day).
	DocumentsIndexedByDay(ctx context.Context, since time.Time) ([]domain.DailyCount, error)
	// DailyFetchDuration reports each day's mean crawl_job_pages.duration_ms
	// at or after since -- the admin Overview page's fetch-duration trend.
	DailyFetchDuration(ctx context.Context, since time.Time) ([]domain.DailyAvgDuration, error)
	// PageRankHistogram buckets every document's pagerank into equal-width
	// bins spanning the corpus's observed range, plus how many sit at or
	// below the orphan threshold and the total doc count. All zero when empty.
	PageRankHistogram(ctx context.Context) (buckets []domain.PageRankBucket, orphanCount, totalDocs int, err error)
	// ListDocumentAliasGroups pages through every canonical document with
	// at least one alias, read live from document_aliases (not a cached
	// run) so the "what got merged" listing stays accurate over time.
	ListDocumentAliasGroups(ctx context.Context, limit, offset int) (groups []domain.DocumentAliasGroup, total int, err error)
	// ClearContent permanently deletes every crawled document (cascading to
	// postings/links/document_versions/document_embeddings), every
	// document_aliases row, and every crawl_jobs row (cascading to
	// crawl_job_pages) -- everything the admin database page's "Clear
	// content" button offers, and nothing else: every settings table
	// (app_settings, chat_endpoint, embedding_http_endpoints,
	// scheduled_crawls) is left untouched.
	ClearContent(ctx context.Context) error
	// ClearSettings permanently deletes every row of every settings table
	// (app_settings, chat_endpoint, embedding_http_endpoints,
	// scheduled_crawls) -- everything the admin database page's "Clear
	// settings" button offers; crawled content itself is left untouched.
	// Deliberately does NOT reset any process's own in-memory settings:
	// bootstrap.SyncSettings' loadSetting leaves a caller's existing value
	// untouched whenever a key is missing, the same as a transient read
	// error, so a later poll finding these rows gone can't tell "cleared on
	// purpose" apart from "DB hiccup" -- treating both as "reset to
	// defaults" would risk wiping live settings on a momentary blip. The
	// caller handling this request resets its own in-memory settings
	// immediately instead; other processes pick up the change once
	// restarted.
	ClearSettings(ctx context.Context) error
}

// --- Primary (driving) ports ---

// Search sort modes: SortRelevance (the default) orders by blended
// BM25/semantic score; SortRecency orders strictly by crawl time, most
// recently crawled first, ignoring relevance entirely. Any other (or empty)
// value is treated as SortRelevance.
const (
	SortRelevance = "relevance"
	SortRecency   = "recency"
)

// SearchQuery bundles a search request's options beyond the raw query text
// itself, so a new search-time option has one obvious place to live rather
// than growing the Search method's parameter list.
type SearchQuery struct {
	TopK int
	Sort string
	// ProviderWeights, when non-nil, fully replaces the admin default
	// EmbeddingSearchWeights for this request only; an empty-but-non-nil
	// map means pure BM25 (no semantic scoring), same as every weight <= 0.
	ProviderWeights map[string]float64
}

type SearchService interface {
	Search(ctx context.Context, query string, opts SearchQuery) ([]domain.SearchResult, error)
}

// CrawlOptions is a single crawl request: seed URLs and page budget, plus
// optional credentials for sites needing a cookie or Basic auth.
// RespectRobots defaults false; UserAgent overrides the process default for
// this crawl only. UseSitemap, when set, also enqueues each seed's
// /sitemap.xml URLs.
type CrawlOptions struct {
	SeedURLs      []string
	MaxPages      int
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	RespectRobots bool
	UserAgent     string
	// LinkScope overrides the Tuning page's global link-following default
	// for this crawl -- "" means "use the global default"; see domain.
	// LinkScope* and crawlLoop's onDomain.
	LinkScope string `json:"link_scope"`
	// AllowedDomains/BlockedDomains are a per-crawl allow/block list on top
	// of LinkScope: BlockedDomains always wins; AllowedDomains widens scope
	// even where LinkScope would reject it. Both empty leaves LinkScope as
	// the only check.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// FollowIndexedDomains additionally follows a link whose domain already
	// has an indexed document, even where LinkScope/AllowedDomains wouldn't
	// otherwise allow it -- BlockedDomains still overrides this.
	FollowIndexedDomains bool `json:"follow_indexed_domains,omitempty"`
	UseSitemap           bool `json:"use_sitemap"`
	// FetchTimeoutSeconds, MinTextLength, CrawlDelayMs and MaxResponseKB
	// override the same-named operational defaults for this crawl alone
	// when positive; zero means "use the global default."
	FetchTimeoutSeconds int `json:"fetch_timeout_seconds"`
	MinTextLength       int `json:"min_text_length"`
	CrawlDelayMs        int `json:"crawl_delay_ms"`
	MaxResponseKB       int `json:"max_response_kb"`
	// PrioritizeUnindexed fetches not-yet-indexed URLs before already-
	// indexed ones within the same MaxPages budget -- changes order, not
	// coverage; already-indexed pages still get crawled once the rest are
	// attempted.
	PrioritizeUnindexed bool `json:"prioritize_unindexed"`
	// Renderer overrides the Tuning page's global rendering mode for this
	// crawl alone -- "" means "use the global default." See domain.
	// Renderer* and ports.Renderer.
	Renderer string `json:"renderer"`
}

// CrawlerService actually executes a crawl. onPage, when non-nil, is
// called once per URL attempted (indexed, skipped, or failed) so a caller
// can report live progress; it may be nil for a fire-and-forget crawl.
type CrawlerService interface {
	Crawl(ctx context.Context, opts CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error)
}

// ErrCrawlJobNotFound is returned by CrawlJobService.GetCrawlJob when no
// job with the given ID exists (or is no longer retained).
var ErrCrawlJobNotFound = errors.New("crawl job not found")

// CrawlJobService lets admin-server poll crawl-server's job progress, and
// cancel one -- every job is started by crawl-server's own scheduler ticker
// (see application.TriggerDueCrawls), never by admin-server directly, so
// aside from CancelCrawlJob this is read-only.
type CrawlJobService interface {
	ListCrawlJobs(ctx context.Context) ([]domain.CrawlJobSummary, error)
	GetCrawlJob(ctx context.Context, jobID string) (domain.CrawlJob, error)
	// CancelCrawlJob asks crawl-server to stop a queued or running job.
	// Returns ports.ErrCrawlJobNotFound if no such job exists, and
	// ErrCrawlJobNotRunning if it exists but already finished (done, failed,
	// or already cancelled) -- there's nothing left to cancel.
	CancelCrawlJob(ctx context.Context, jobID string) error
	// DeleteEndedCrawlJobs asks crawl-server to delete every job that's
	// already finished (done, failed, or cancelled), leaving queued/running
	// jobs untouched, and reports how many were removed -- the admin Jobs
	// page's "Clear ended jobs" button, for trimming a long history down to
	// what's still active without waiting for maxRetainedCrawlJobs eviction.
	DeleteEndedCrawlJobs(ctx context.Context) (int, error)
}

// ErrCrawlJobNotRunning is returned by CrawlJobService.CancelCrawlJob (and
// crawl-server's own cancel handler) when the job exists but isn't
// currently queued or running, so there's nothing to cancel.
var ErrCrawlJobNotRunning = errors.New("crawl job is not currently running")

// CrawlJobStore is crawl-server's own persistence for crawl jobs and their
// per-page history -- distinct from CrawlJobService, the network contract
// admin-server's client uses. domain.CrawlJobStore (in-memory) satisfies
// this for tests; sqlrepo.Repository's DB-backed one is what production
// runs. Get returns domain.ErrCrawlJobNotFound if unretained.
type CrawlJobStore interface {
	Create(ctx context.Context, req domain.CrawlJobRequest) (domain.CrawlJob, error)
	MarkRunning(ctx context.Context, id string) error
	AppendPage(ctx context.Context, id string, ev domain.CrawlPageEvent) error
	MarkDone(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, failErr error) error
	MarkCancelled(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (domain.CrawlJob, error)
	List(ctx context.Context) ([]domain.CrawlJobSummary, error)
	// ListActive returns every job currently Queued or Running -- a cheap,
	// targeted subset of List (never scans ended jobs) used to check for
	// an already-active crawl of the same seed before starting a new one.
	ListActive(ctx context.Context) ([]domain.CrawlJobSummary, error)
	// DeleteEndedCrawlJobs deletes every job in domain.CrawlJobDone,
	// CrawlJobFailed, or CrawlJobCancelled status, leaving queued/running
	// jobs untouched, and returns how many were removed.
	DeleteEndedCrawlJobs(ctx context.Context) (int, error)
}

// DebugSearchService exposes the raw, unblended hybrid search results
// (BM25/semantic/final score breakdown) for admin diagnostics.
type DebugSearchService interface {
	Search(ctx context.Context, query string, opts SearchQuery) ([]domain.HybridResult, error)
}

// Settings store keys: each names one JSON-encoded blob in SettingsStore.
const (
	SettingsKeyTuning      = "tuning"
	SettingsKeyOperational = "operational"
	SettingsKeyOverrides   = "overrides"
	// SettingsKeyPageRankStatus holds a domain.PageRankStatus -- unlike the
	// three above (admin-edited configuration, synced by
	// bootstrap.SyncSettings' poll loop), this one is runtime status
	// written by application.RunPageRankJobWithStatus, not admin input.
	SettingsKeyPageRankStatus = "pagerank_status"
	// SettingsKeyEmbeddingRecomputeStatus holds a
	// domain.EmbeddingRecomputeStatus -- the same runtime-status pattern
	// as SettingsKeyPageRankStatus, written by
	// application.RunEmbeddingRecomputeJobWithStatus.
	SettingsKeyEmbeddingRecomputeStatus = "embedding_recompute_status"
	// SettingsKeyEmbeddingEndpointsMigrated holds "true" once the one-time
	// legacy-config migration has run -- distinct from "table has rows,"
	// since deleting the migrated endpoint would otherwise make the
	// migration wrongly re-run (resurrecting it) on the next restart.
	SettingsKeyEmbeddingEndpointsMigrated = "embedding_endpoints_migrated"
	// SettingsKeyContentDedupStatus holds a domain.ContentDedupStatus --
	// the same runtime-status pattern as SettingsKeyPageRankStatus, written
	// by application.RunContentDedupJobWithStatus.
	SettingsKeyContentDedupStatus = "content_dedup_status"
)

// SettingsStore persists the admin-configurable tuning/operational/ranking
// settings blobs to the shared database, so every process reads the same
// values instead of only the copy an admin edit happened to update in its
// own in-memory instance.
type SettingsStore interface {
	SaveSetting(ctx context.Context, key, value string) error
	GetSetting(ctx context.Context, key string) (value string, found bool, err error)
}

// ErrScheduledCrawlNotFound is returned by ScheduledCrawlStore's Update and
// Delete when no schedule with the given ID exists.
var ErrScheduledCrawlNotFound = errors.New("scheduled crawl not found")

// ErrScheduledCrawlInProgress is returned by RunScheduledCrawlNow when the
// schedule already has a crawl actively running -- see its own doc comment
// for why forcing a second concurrent run of the same schedule is unsafe.
var ErrScheduledCrawlInProgress = errors.New("scheduled crawl is already in progress")

// ScheduledCrawlStore persists recurring crawl schedules an admin creates.
// Both admin-server and crawl-server talk to the shared scheduled_crawls
// table directly, so the ticker and the CRUD endpoints never need an HTTP
// round-trip to reach each other.
type ScheduledCrawlStore interface {
	CreateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error
	// GetScheduledCrawl returns one schedule by ID, or
	// ErrScheduledCrawlNotFound if none exists -- used by the admin
	// schedule-detail/edit subpage to load a single entry's current
	// options without fetching every schedule.
	GetScheduledCrawl(ctx context.Context, id string) (domain.ScheduledCrawl, error)
	ListScheduledCrawls(ctx context.Context) ([]domain.ScheduledCrawl, error)
	UpdateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error
	DeleteScheduledCrawl(ctx context.Context, id string) error
	// DueScheduledCrawls lists every enabled, not-already-in-progress
	// schedule whose NextRunAt is at or before now.
	DueScheduledCrawls(ctx context.Context, now time.Time) ([]domain.ScheduledCrawl, error)
	// MarkScheduledCrawlRun records a trigger/finish, advancing the next
	// run and runCount; a one-off or MaxRuns-capped entry passes
	// enabled=false. inProgress, separate from enabled, stays true for the
	// run's duration so DueScheduledCrawls can't double-trigger a long crawl.
	// jobID is the domain.CrawlJob this run created (set at trigger time,
	// cleared to "" by onDone) -- see domain.ScheduledCrawl.JobID and
	// ResetStaleInProgress below for why it's tracked.
	MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, enabled, inProgress bool, runCount int, jobID string) error
	// RunScheduledCrawlNow sets NextRunAt to now and re-enables if paused,
	// leaving every other field untouched -- picked up by the next
	// scheduler tick. Deliberately does NOT force-clear InProgress: a
	// stale-after-crash InProgress is already self-healed once at
	// crawl-server startup (see ResetStaleInProgress), so if InProgress is
	// still true here it means a crawl for this schedule is genuinely
	// running right now -- forcing NextRunAt=now regardless would let the
	// ticker start a second concurrent crawl of the same site, and two
	// crawlLoop goroutines racing to archive the same document's previous
	// version via SaveDocument can violate document_versions' primary key
	// (confirmed in production: two concurrent jobs for the same site,
	// one crashed with a duplicate-key error -- and confirmed a second
	// time, a subtler recurrence: ResetStaleInProgress used to clear
	// InProgress unconditionally for every stuck-true row regardless of
	// whether its job had actually stopped, including one a same-startup
	// RecoverInterruptedCrawls pass had just resumed and was still
	// genuinely running -- see ResetStaleInProgress's own doc comment).
	// Returns ErrScheduledCrawlNotFound if unknown, ErrScheduledCrawlInProgress if
	// a crawl is already running for it.
	RunScheduledCrawlNow(ctx context.Context, id string, now time.Time) error
	// SetScheduledCrawlEnabled flips only Enabled, leaving NextRunAt (and
	// every other field) untouched -- unlike UpdateScheduledCrawl, which
	// always reschedules (NextRunAt = interval from now) since it's meant
	// for a genuine field edit from the schedule detail page. The admin
	// Jobs list's plain pause/resume checkbox uses this instead, so
	// toggling it doesn't reorder the list (sorted by NextRunAt) or push a
	// paused-then-resumed crawl's next run further out than expected.
	// Returns ErrScheduledCrawlNotFound if unknown.
	SetScheduledCrawlEnabled(ctx context.Context, id string, enabled bool) error
	// ResetStaleInProgress clears a stuck-true InProgress flag -- run once
	// at crawl-server startup, before anything queries DueScheduledCrawls,
	// and always AFTER RecoverInterruptedCrawls (whose resumed jobs must
	// already be reflected in crawl_jobs' status by the time this runs).
	// A schedule is only cleared when its JobID doesn't correspond to a
	// still-queued/running job -- one RecoverInterruptedCrawls just resumed
	// is left untouched, since it really is still in progress. Clearing it
	// anyway (the original, job-unaware version of this method) let the
	// very next scheduler tick start a second, duplicate crawl of the same
	// site while the resumed one was still running -- see
	// RunScheduledCrawlNow's doc comment for the production incident this
	// guards against. Returns how many rows were reset.
	ResetStaleInProgress(ctx context.Context) (int, error)
}

// ErrEmbeddingEndpointNotFound is returned by EmbeddingEndpointStore's
// Update and Delete when no endpoint with the given ID exists.
var ErrEmbeddingEndpointNotFound = errors.New("embedding endpoint not found")

// EmbeddingEndpointStore persists the admin-configured HTTP embedding
// endpoints (domain.EmbeddingHTTPEndpoint) -- one row per endpoint, each
// independently enabled and rate-limited, alongside the built-in hash
// provider.
type EmbeddingEndpointStore interface {
	CreateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error
	// GetEmbeddingEndpoint returns one endpoint by ID, or
	// ErrEmbeddingEndpointNotFound if none exists -- used by the admin
	// endpoint edit subpage to load a single entry's current config.
	GetEmbeddingEndpoint(ctx context.Context, id string) (domain.EmbeddingHTTPEndpoint, error)
	// ListEmbeddingEndpoints returns every configured endpoint, in no
	// particular guaranteed order beyond what the implementation's query
	// happens to return -- callers needing a stable order sort it
	// themselves.
	ListEmbeddingEndpoints(ctx context.Context) ([]domain.EmbeddingHTTPEndpoint, error)
	UpdateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error
	DeleteEmbeddingEndpoint(ctx context.Context, id string) error
}

// ErrChatEndpointNotConfigured is returned by ChatEndpointStore.GetChatEndpoint
// when no chat endpoint has ever been saved, and by ChatService.Chat when the
// saved endpoint exists but is disabled.
var ErrChatEndpointNotConfigured = errors.New("chat endpoint not configured")

// ChatEndpointStore persists the single admin-configured domain.ChatEndpoint.
// Unlike EmbeddingEndpointStore (a list of many blended endpoints), chat only
// ever has ONE active configuration, so this is Get/Set on one row, not CRUD
// on a collection.
type ChatEndpointStore interface {
	// GetChatEndpoint returns ErrChatEndpointNotConfigured if never saved.
	GetChatEndpoint(ctx context.Context) (domain.ChatEndpoint, error)
	// SetChatEndpoint upserts the single chat endpoint row.
	SetChatEndpoint(ctx context.Context, e domain.ChatEndpoint) error
}

// ChatCompleter calls an OpenAI-compatible chat-completions endpoint,
// optionally with a native "tools" list (see domain.ToolDef) -- tools is
// nil/empty for a turn with no active MCP servers/tools, in which case the
// implementation must omit the request's tools field entirely (never send
// an empty array with a tool_choice), for compatibility with any
// OpenAI-compatible endpoint that isn't configured for tool-calling at all.
// The returned domain.ChatMessage's ToolCalls is set when (and only when)
// the model chose to invoke one or more tools this turn instead of
// answering directly -- Content is typically empty in that case.
type ChatCompleter interface {
	Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error)
}

// ErrMCPServerNotFound is returned by MCPServerStore's Update and Delete
// when no server with the given ID exists -- MCPServerStore's sibling of
// ErrEmbeddingEndpointNotFound above.
var ErrMCPServerNotFound = errors.New("mcp server not found")

// MCPServerStore persists the admin-configured domain.MCPServer rows -- a
// list of many, like EmbeddingEndpointStore, unlike the single-row
// ChatEndpointStore above: an admin can define several servers over time.
type MCPServerStore interface {
	ListMCPServers(ctx context.Context) ([]domain.MCPServer, error)
	CreateMCPServer(ctx context.Context, s domain.MCPServer) error
	// UpdateMCPServer returns ErrMCPServerNotFound if no server with s.ID exists.
	UpdateMCPServer(ctx context.Context, s domain.MCPServer) error
	// DeleteMCPServer returns ErrMCPServerNotFound if no server with id exists.
	DeleteMCPServer(ctx context.Context, id string) error
}

// ErrAgentNotFound is returned by AgentStore's Update and Delete when no
// agent with the given ID exists -- AgentStore's sibling of
// ErrMCPServerNotFound above.
var ErrAgentNotFound = errors.New("agent not found")

// AgentStore persists the admin-configured domain.Agent rows -- a list of
// many, like MCPServerStore: an admin can define several agents over time.
type AgentStore interface {
	ListAgents(ctx context.Context) ([]domain.Agent, error)
	CreateAgent(ctx context.Context, a domain.Agent) error
	// UpdateAgent returns ErrAgentNotFound if no agent with a.ID exists.
	UpdateAgent(ctx context.Context, a domain.Agent) error
	// DeleteAgent returns ErrAgentNotFound if no agent with id exists.
	DeleteAgent(ctx context.Context, id string) error
}

// MCPToolProvider opens one session per chat turn, spanning tool discovery
// through every follow-up round's tool calls -- MCP's own session-oriented
// usage pattern (initialize once per connection, then reuse it), not a
// fresh spawn+handshake per call. Open is best-effort per server: one that
// fails to connect or list its tools is skipped (logged), never fails the
// whole turn -- same convention ChatService.Chat already applies to a
// ListMCPServers error. A tool name collision across two different active
// servers is resolved by skipping (logging) the later one, never silently
// misrouting a call to the wrong server.
//
// env carries ADMIN-CONFIGURED configuration (e.g. the endpoint's own
// WebSearchBaseURL) as additional process environment variables for every
// spawned "stdio"-transport server -- never anything derived from the
// model's own output or a tool call's arguments, so this does not reopen
// the injection surface CallTool's own arguments guard against: env is set
// by ChatService.Chat from domain.ChatEndpoint fields the admin configured
// ahead of time, not from a chat turn's content. A nil or empty map adds
// nothing beyond the implementation's own base environment.
type MCPToolProvider interface {
	Open(ctx context.Context, servers []domain.MCPServer, env map[string]string) (MCPSession, []domain.MCPTool)
}

// MCPSession is one chat turn's live connections to every active MCP
// server, returned by MCPToolProvider.Open alongside the tools discovered
// across all of them.
type MCPSession interface {
	// CallTool invokes toolName (looked up among every tool discovered by
	// the Open call that returned this session) against its originating
	// server, with argumentsJSON as the model supplied it (a raw JSON
	// object, NEVER shell-interpolated or otherwise reinterpreted -- see
	// mcpclient's own security doc comment), returning the result content
	// as text. Returns an error for an unknown toolName, a call that fails,
	// or one that times out.
	CallTool(ctx context.Context, toolName string, argumentsJSON string) (string, error)
	// Close closes every underlying server connection this session opened.
	// Safe to call even if Open connected to zero servers.
	Close()
}
