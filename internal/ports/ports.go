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
// session cookie or HTTP Basic auth, plus an optional UserAgent override
// (falls back to the process default when empty).
type FetchOptions struct {
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	UserAgent     string
	// FetchTimeoutSeconds and MaxResponseBytes override the operational
	// defaults for this fetch alone when positive; zero means use the
	// global default, same convention as UserAgent's empty-string case.
	FetchTimeoutSeconds int
	MaxResponseBytes    int
	// Renderer selects how this fetch is done: "" defers to the caller's
	// default, RendererNone/Chromium/Firefox override it. Only
	// RenderAwareFetcher consults this -- httpfetcher.Fetcher ignores it.
	Renderer string
	// NoRender forces the plain HTTP path regardless of Renderer -- set by
	// crawlLoop's sitemap.xml fetch, whose XML response a browser's viewer
	// would corrupt.
	NoRender bool
}

// AuthFetcher is a Fetcher that also accepts per-request credentials.
// httpfetcher.Fetcher implements both this and the plain Fetcher (used by
// the robots-checker, which never needs credentials).
type AuthFetcher interface {
	FetchWithOptions(ctx context.Context, url string, opts FetchOptions) (string, error)
}

// Renderer executes a page in a real headless browser and returns its
// rendered HTML, for a site whose content only exists after client-side JS
// runs. Implemented per browser engine; RenderAwareFetcher dispatches on
// FetchOptions.Renderer.
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
	// survive). titleWeight repeats the title ahead of the body in the
	// indexed token stream. embeddings are upserted in the same write.
	SaveDocument(ctx context.Context, doc domain.Document, embeddings map[string][]float32, maxVersions, titleWeight int) error
	// PostingsForTerms batch-fetches postings for every term in one query,
	// so a multi-term search issues one round trip. TotalDocs/AvgDocLen are
	// left zero -- the caller fills them from its own stats cache.
	PostingsForTerms(ctx context.Context, terms []string) (map[string][]domain.PostingStats, error)
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	// VocabularyStats reports the corpus's total distinct-term count plus a
	// limit/offset page of terms ordered by sortBy/sortDir -- backs the
	// admin vocabulary page. search restricts the listing/matchedCount.
	VocabularyStats(ctx context.Context, limit, offset int, search, sortBy, sortDir string) (vocabSize, matchedCount int, terms []domain.TermStat, err error)
	// AllTerms returns every distinct term with its doc/total frequency --
	// the full vocabulary, used by VocabularyCache for fuzzy matching.
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
	// EmbeddingsForDocs batch-fetches provider's embeddings for exactly the
	// given doc IDs, avoiding a full-corpus scan. Each norm was precomputed
	// at SaveDocument time.
	EmbeddingsForDocs(ctx context.Context, ids []string, provider string) (map[string]domain.EmbeddedVector, error)
	// SampleEmbeddings returns up to limit of provider's embeddings across
	// the corpus, so a purely semantic match can still be found, bounded
	// regardless of corpus size.
	SampleEmbeddings(ctx context.Context, limit int, provider string) (map[string]domain.EmbeddedVector, error)
	// TopSemanticMatches finds queryVec's nearest neighbors via pgvector's
	// HNSW index, used instead of SampleEmbeddings when ANN is available.
	// ok is false when it isn't; a non-nil error is a real query fault.
	TopSemanticMatches(ctx context.Context, queryVec []float32, limit int, provider string) (matches map[string]domain.EmbeddedVector, ok bool, err error)
	// DocumentsByIDs batch-fetches documents for the given IDs in one round
	// trip (a missing ID is simply absent, not an error) -- the only
	// lookup-by-ID this port exposes.
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	// DocumentsByIDsSortedByCrawledAt is DocumentsByIDs' counterpart for the
	// recency-sort path, ordered by crawled_at descending in SQL so the
	// caller never sorts candidates itself.
	DocumentsByIDsSortedByCrawledAt(ctx context.Context, ids []string) ([]domain.Document, error)
	// DocumentIDsByHost returns IDs of every document whose host matches
	// one of hosts (exact or subdomain) -- forces site: matches into the
	// search candidate set.
	DocumentIDsByHost(ctx context.Context, hosts []string) ([]string, error)
	// ResolveAliasHosts returns the real host of every canonical document
	// reachable through an alias matching hosts -- expands a site: filter
	// before SiteAllowed, which checks a candidate's real host.
	ResolveAliasHosts(ctx context.Context, hosts []string) ([]string, error)
	// HostsIndexed reports, for each host, whether any document is already
	// indexed for it -- used by FollowIndexedDomains to widen link scope.
	HostsIndexed(ctx context.Context, hosts []string) (map[string]bool, error)
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	DeleteDocument(ctx context.Context, docID string) error
	// RecordDocumentAlias upserts one document_aliases row: aliasURL's
	// content lives under canonicalID, not its own row -- used for both a
	// canonical-tag redirect and a content-dedup merge. canonicalID need
	// not already exist (a forward-declared alias resolves once saved).
	RecordDocumentAlias(ctx context.Context, aliasURL, canonicalID, reason string) error
}

// PageRankRepository is the narrow port RunPageRankJob needs: load the
// current link graph, then write back each document's fresh score.
// cmd/crawl (periodic ticker + one run after each crawl) is the only caller.
type PageRankRepository interface {
	// LinkGraph loads the whole crawled link graph as an adjacency map in
	// one query; a link to a never-crawled URL is simply omitted.
	LinkGraph(ctx context.Context) (map[string][]string, error)
	// UpdatePageRanks batch-writes each given ID's fresh score. A document
	// absent from scores is left untouched, not zeroed.
	UpdatePageRanks(ctx context.Context, scores map[string]float64) error
	// ResolvePendingLinks re-resolves a bounded batch of links whose target
	// wasn't crawled yet when first saved, so LinkGraph's plain
	// links(to_id) scan can pick up a target that's since been indexed.
	// Best-effort from RunPageRankJob's side -- see its own call site.
	ResolvePendingLinks(ctx context.Context) (int, error)
}

// ErrContentDedupAlreadyRunning is returned when
// TryAcquireContentDedupLock lost the race to another process's
// already-running call -- callers treat this as expected, not an error.
var ErrContentDedupAlreadyRunning = errors.New("content dedup is already running")

// ContentDedupRepository is the narrow port RunContentDedupJob needs: read
// every document's fingerprint, then merge duplicate groups. Two
// unsynchronized processes (cmd/crawl's scheduler, admin-server's
// "recompute now") can call this against the same DB, so
// TryAcquireContentDedupLock exists to keep them from running at once.
type ContentDedupRepository interface {
	// AllDocumentFingerprints lists every document's id/url/host/
	// content_hash/simhash/crawled_at in one query -- enough to group
	// duplicates without loading the full domain.Document.
	AllDocumentFingerprints(ctx context.Context) ([]domain.DocumentFingerprint, error)
	// MergeDocuments folds every loserIDs document into canonicalID: each
	// loser's aliases are repointed, a fresh alias recorded for its URL,
	// and its row removed via DeleteDocument's cascade.
	MergeDocuments(ctx context.Context, canonicalID string, loserIDs []string, reason string) error
	// TryAcquireContentDedupLock atomically claims the single DB-backed
	// content-dedup lock, returning true if this call got it. Without it,
	// two concurrent runs can each merge from a stale snapshot, one
	// recording an alias pointing at an id the other already deleted -- a
	// dangling canonical the admin alias-groups page then shows with an
	// empty URL (confirmed in production). Pair with
	// ReleaseContentDedupLock, deferred right after a successful acquire.
	TryAcquireContentDedupLock(ctx context.Context) (bool, error)
	// ReleaseContentDedupLock clears the lock. Idempotent: releasing an
	// already-released lock is a no-op, not an error.
	ReleaseContentDedupLock(ctx context.Context) error
}

// EmbeddingRepository is the narrow slice of *sqlrepo.Repository
// RunEmbeddingRecomputeJob needs: iterate every document ID, fetch its
// stored Text, and overwrite just its embedding.
type EmbeddingRepository interface {
	// AllDocumentIDs lists every document ID, ordered so repeated calls
	// (and the batches fetched via DocumentsByIDs) are stable.
	AllDocumentIDs(ctx context.Context) ([]string, error)
	// DocumentIDsAfter lists every document ID that sorts after afterID in
	// AllDocumentIDs' own ordering -- lets RunEmbeddingRecomputeJob resume
	// an interrupted run from its last checkpoint instead of restarting
	// the whole corpus.
	DocumentIDsAfter(ctx context.Context, afterID string) ([]string, error)
	// DocumentsByIDs batch-fetches each document's URL/title/text -- see
	// AdminRepository's identical method (implemented once for both ports).
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	// UpdateEmbedding overwrites one document's embedding for every
	// provider in embeddings -- unlike SaveDocument, never re-tokenizes
	// text or touches postings/links/versions/pagerank.
	UpdateEmbedding(ctx context.Context, id string, embeddings map[string][]float32) error
}

// SessionStore backs the admin/search login system's session tokens, via a
// shared "sessions" table so a login on one process is recognized by every
// process serving the site. A session carries a role (RoleAdmin/RoleUser)
// and, for RoleUser, the User.ID it belongs to -- resolved fresh by
// ValidSession every request, never derived from the client's cookie.
type SessionStore interface {
	// CreateSession persists a freshly issued token, valid until expiresAt,
	// with the given role and userID ("" for a RoleAdmin session).
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

// UserStore persists every account (domain.User), admin and regular alike
// -- see domain.User's doc comment. A list of many, like MCPServerStore,
// CRUD over IDs.
type UserStore interface {
	ListUsers(ctx context.Context) ([]domain.User, error)
	GetUser(ctx context.Context, id string) (domain.User, error)
	// GetUserByUsername returns ErrUserNotFound if no row has that username.
	GetUserByUsername(ctx context.Context, username string) (domain.User, error)
	// CreateUser returns ErrUsernameTaken if u.Username is already in use.
	CreateUser(ctx context.Context, u domain.User) error
	// UpdateUser replaces u's stored fields wholesale (used for a password
	// reset). Returns ErrUserNotFound if no row with u.ID exists.
	UpdateUser(ctx context.Context, u domain.User) error
	// DeleteUser returns ErrUserNotFound if no row with id exists.
	DeleteUser(ctx context.Context, id string) error
}

// HealthChecker is a cheap liveness check for the shared database
// connection, used only by GET /healthz. Ping must stay a plain connection
// check, never a real query, so the endpoint stays safe for frequent polling.
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
	// term frequency descending, surfacing the strongest matches.
	PostingsForTerm(ctx context.Context, term string, limit int) ([]domain.PostingStats, error)
	// DocumentsByIDs backs the vocabulary term-detail view: given
	// PostingsForTerm's doc IDs, fetch URL/title/text to build an excerpt.
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	DeleteDocument(ctx context.Context, docID string) error
	// PageRankDistribution reports the min/max/average pagerank across the
	// corpus -- the admin PageRank debug page's headline numbers.
	PageRankDistribution(ctx context.Context) (min, max, avg float64, err error)
	// TableRowCounts reports each schema table's row count, keyed by name --
	// the admin database diagnostics page's per-table breakdown.
	TableRowCounts(ctx context.Context) (map[string]int64, error)
	// PoolStats reports the live DB connection pool's limits and usage --
	// the admin database diagnostics page's connection-pool panel.
	PoolStats() sql.DBStats
	// CrawlJobOutcomes reports how many crawl jobs created at or after
	// since finished in each terminal status -- the admin Overview page's
	// crawl-outcome donut. A still queued/running job is excluded.
	CrawlJobOutcomes(ctx context.Context, since time.Time) ([]domain.CrawlJobOutcomeCount, error)
	// DailyFetchOutcomes reports, per day since, how many crawl_job_pages
	// rows landed in each fetch outcome -- the Overview page's stacked bar.
	DailyFetchOutcomes(ctx context.Context, since time.Time) ([]domain.DailyFetchOutcome, error)
	// DocumentsIndexedByDay reports how many documents' crawled_at falls on
	// each day since -- the Overview page's documents-indexed-over-time trend.
	DocumentsIndexedByDay(ctx context.Context, since time.Time) ([]domain.DailyCount, error)
	// DailyFetchDuration reports each day's mean fetch duration since --
	// the Overview page's fetch-duration trend.
	DailyFetchDuration(ctx context.Context, since time.Time) ([]domain.DailyAvgDuration, error)
	// PageRankHistogram buckets every document's pagerank into equal-width
	// bins, plus how many sit at/below the orphan threshold and total docs.
	PageRankHistogram(ctx context.Context) (buckets []domain.PageRankBucket, orphanCount, totalDocs int, err error)
	// ListDocumentAliasGroups pages through every canonical document with
	// at least one alias, read live from document_aliases so the listing
	// stays accurate over time.
	ListDocumentAliasGroups(ctx context.Context, limit, offset int) (groups []domain.DocumentAliasGroup, total int, err error)
	// ClearContent permanently deletes every crawled document (cascading to
	// postings/links/versions/embeddings), every document_aliases row, and
	// every crawl_jobs row -- everything the "Clear content" button offers,
	// leaving every settings table untouched.
	ClearContent(ctx context.Context) error
	// ClearSettings permanently deletes every row of every settings table --
	// crawled content is untouched. Deliberately does NOT reset any
	// process's own in-memory settings: bootstrap.SyncSettings can't tell
	// "cleared on purpose" apart from a transient read error, so treating a
	// missing key as "reset to defaults" would risk wiping live settings on
	// a blip. The caller handling this request resets its own in-memory
	// settings immediately; other processes pick it up on restart.
	ClearSettings(ctx context.Context) error
}

// --- Primary (driving) ports ---

// Search sort modes: SortRelevance (default) orders by blended BM25/
// semantic score; SortRecency orders strictly by crawl time, newest first.
// Any other/empty value is treated as SortRelevance.
const (
	SortRelevance = "relevance"
	SortRecency   = "recency"
)

// SearchQuery bundles a search request's options beyond the raw query text,
// so a new search-time option has one obvious place to live.
type SearchQuery struct {
	TopK int
	Sort string
	// ProviderWeights, when non-nil, fully replaces the admin default
	// EmbeddingSearchWeights for this request only; an empty-but-non-nil
	// map means pure BM25.
	ProviderWeights map[string]float64
}

type SearchService interface {
	Search(ctx context.Context, query string, opts SearchQuery) ([]domain.SearchResult, error)
}

// CrawlOptions is a single crawl request: seed URLs and page budget, plus
// optional credentials for sites needing a cookie or Basic auth.
// RespectRobots defaults false; UserAgent overrides the process default.
// UseSitemap also enqueues each seed's /sitemap.xml URLs.
type CrawlOptions struct {
	SeedURLs      []string
	MaxPages      int
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	RespectRobots bool
	UserAgent     string
	// LinkScope overrides the Tuning page's global default for this crawl --
	// "" means use the global default; see domain.LinkScope*.
	LinkScope string `json:"link_scope"`
	// AllowedDomains/BlockedDomains are a per-crawl allow/block list on top
	// of LinkScope: BlockedDomains always wins; AllowedDomains widens scope
	// even where LinkScope would reject it.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// FollowIndexedDomains additionally follows a link whose domain already
	// has an indexed document -- BlockedDomains still overrides this.
	FollowIndexedDomains bool `json:"follow_indexed_domains,omitempty"`
	UseSitemap           bool `json:"use_sitemap"`
	// FetchTimeoutSeconds, MinTextLength, CrawlDelayMs and MaxResponseKB
	// override the operational defaults for this crawl alone when
	// positive; zero means use the global default.
	FetchTimeoutSeconds int `json:"fetch_timeout_seconds"`
	MinTextLength       int `json:"min_text_length"`
	CrawlDelayMs        int `json:"crawl_delay_ms"`
	MaxResponseKB       int `json:"max_response_kb"`
	// PrioritizeUnindexed fetches not-yet-indexed URLs before already-
	// indexed ones within the same MaxPages budget -- changes order, not coverage.
	PrioritizeUnindexed bool `json:"prioritize_unindexed"`
	// Renderer overrides the Tuning page's global rendering mode for this
	// crawl alone -- "" means use the global default. See domain.Renderer*.
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
// cancel one -- every job is started by crawl-server's own scheduler
// ticker, never by admin-server directly, so aside from CancelCrawlJob
// this is read-only.
type CrawlJobService interface {
	ListCrawlJobs(ctx context.Context) ([]domain.CrawlJobSummary, error)
	GetCrawlJob(ctx context.Context, jobID string) (domain.CrawlJob, error)
	// CancelCrawlJob asks crawl-server to stop a queued or running job.
	// Returns ErrCrawlJobNotFound if unknown, ErrCrawlJobNotRunning if it
	// already finished.
	CancelCrawlJob(ctx context.Context, jobID string) error
	// DeleteEndedCrawlJobs asks crawl-server to delete every finished job,
	// leaving queued/running ones untouched, and reports how many were
	// removed -- the admin Jobs page's "Clear ended jobs" button.
	DeleteEndedCrawlJobs(ctx context.Context) (int, error)
}

// ErrCrawlJobNotRunning is returned by CrawlJobService.CancelCrawlJob (and
// crawl-server's own cancel handler) when the job exists but isn't
// currently queued or running, so there's nothing to cancel.
var ErrCrawlJobNotRunning = errors.New("crawl job is not currently running")

// CrawlJobStore is crawl-server's own persistence for crawl jobs and their
// per-page history -- distinct from CrawlJobService, the network contract
// admin-server's client uses. domain.CrawlJobStore (in-memory) satisfies
// this for tests; sqlrepo's DB-backed one runs in production. Get returns
// domain.ErrCrawlJobNotFound if unretained.
type CrawlJobStore interface {
	Create(ctx context.Context, req domain.CrawlJobRequest) (domain.CrawlJob, error)
	MarkRunning(ctx context.Context, id string) error
	AppendPage(ctx context.Context, id string, ev domain.CrawlPageEvent) error
	MarkDone(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, failErr error) error
	MarkCancelled(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (domain.CrawlJob, error)
	List(ctx context.Context) ([]domain.CrawlJobSummary, error)
	// ListActive returns every job currently Queued or Running -- a cheap
	// subset of List used to check for an already-active crawl of the same
	// seed before starting a new one.
	ListActive(ctx context.Context) ([]domain.CrawlJobSummary, error)
	// DeleteEndedCrawlJobs deletes every done/failed/cancelled job, leaving
	// queued/running ones untouched, and returns how many were removed.
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
	// three above, this is runtime status written by
	// RunPageRankJobWithStatus, not admin input.
	SettingsKeyPageRankStatus = "pagerank_status"
	// SettingsKeyEmbeddingRecomputeStatus holds a
	// domain.EmbeddingRecomputeStatus, same runtime-status pattern as
	// SettingsKeyPageRankStatus.
	SettingsKeyEmbeddingRecomputeStatus = "embedding_recompute_status"
	// SettingsKeyEmbeddingEndpointsMigrated holds "true" once the one-time
	// legacy-config migration has run -- distinct from "table has rows," so
	// deleting the migrated endpoint doesn't make it wrongly re-run.
	SettingsKeyEmbeddingEndpointsMigrated = "embedding_endpoints_migrated"
	// SettingsKeyContentDedupStatus holds a domain.ContentDedupStatus, same
	// runtime-status pattern as SettingsKeyPageRankStatus.
	SettingsKeyContentDedupStatus = "content_dedup_status"
)

// SettingsStore persists the admin-configurable tuning/operational/ranking
// settings blobs to the shared database, so every process reads the same
// values, not just the copy an admin edit updated in-memory.
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
	// schedule-detail/edit subpage.
	GetScheduledCrawl(ctx context.Context, id string) (domain.ScheduledCrawl, error)
	ListScheduledCrawls(ctx context.Context) ([]domain.ScheduledCrawl, error)
	UpdateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error
	DeleteScheduledCrawl(ctx context.Context, id string) error
	// DueScheduledCrawls lists every enabled, not-already-in-progress
	// schedule whose NextRunAt is at or before now.
	DueScheduledCrawls(ctx context.Context, now time.Time) ([]domain.ScheduledCrawl, error)
	// MarkScheduledCrawlRun records a trigger/finish, advancing the next
	// run and runCount; a one-off or MaxRuns-capped entry passes
	// enabled=false. inProgress stays true for the run's duration so
	// DueScheduledCrawls can't double-trigger a long crawl. jobID is the
	// CrawlJob this run created (cleared to "" by onDone) -- see
	// ScheduledCrawl.JobID/ResetStaleInProgress for why it's tracked.
	MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, enabled, inProgress bool, runCount int, jobID string) error
	// RunScheduledCrawlNow sets NextRunAt to now and re-enables if paused,
	// leaving other fields untouched. Deliberately does NOT force-clear
	// InProgress: if it's still true here (after ResetStaleInProgress
	// already self-healed a stale one at startup), a crawl for this
	// schedule is genuinely running, and forcing a second concurrent one
	// caused two crawlLoop goroutines to violate document_versions'
	// primary key archiving the same document (confirmed in production
	// twice, the second time a subtler recurrence where
	// ResetStaleInProgress cleared InProgress for a job
	// RecoverInterruptedCrawls had just resumed -- see its own doc
	// comment). Returns ErrScheduledCrawlNotFound if unknown,
	// ErrScheduledCrawlInProgress if already running.
	RunScheduledCrawlNow(ctx context.Context, id string, now time.Time) error
	// SetScheduledCrawlEnabled flips only Enabled, leaving NextRunAt
	// untouched -- unlike UpdateScheduledCrawl, which always reschedules.
	// The admin Jobs list's pause/resume checkbox uses this so toggling it
	// doesn't reorder the list or push the next run out further. Returns
	// ErrScheduledCrawlNotFound if unknown.
	SetScheduledCrawlEnabled(ctx context.Context, id string, enabled bool) error
	// ResetStaleInProgress clears a stuck-true InProgress flag -- run once
	// at crawl-server startup, before DueScheduledCrawls, and always AFTER
	// RecoverInterruptedCrawls. A schedule is only cleared when its JobID
	// doesn't correspond to a still-queued/running job -- one
	// RecoverInterruptedCrawls just resumed is left untouched, since
	// clearing it unconditionally let the next tick start a duplicate
	// crawl of the same site (see RunScheduledCrawlNow's doc comment for
	// the incident). Returns how many rows were reset.
	ResetStaleInProgress(ctx context.Context) (int, error)
}

// ErrEmbeddingEndpointNotFound is returned by EmbeddingEndpointStore's
// Update and Delete when no endpoint with the given ID exists.
var ErrEmbeddingEndpointNotFound = errors.New("embedding endpoint not found")

// EmbeddingEndpointStore persists the admin-configured HTTP embedding
// endpoints -- one row per endpoint, each independently enabled and
// rate-limited, alongside the built-in hash provider.
type EmbeddingEndpointStore interface {
	CreateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error
	// GetEmbeddingEndpoint returns one endpoint by ID, or
	// ErrEmbeddingEndpointNotFound if none exists.
	GetEmbeddingEndpoint(ctx context.Context, id string) (domain.EmbeddingHTTPEndpoint, error)
	// ListEmbeddingEndpoints returns every configured endpoint in no
	// guaranteed order -- callers needing stable order sort it themselves.
	ListEmbeddingEndpoints(ctx context.Context) ([]domain.EmbeddingHTTPEndpoint, error)
	UpdateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error
	DeleteEmbeddingEndpoint(ctx context.Context, id string) error
}

// ErrChatEndpointNotConfigured is returned by ChatEndpointStore.GetChatEndpoint
// when no chat endpoint has ever been saved, and by ChatService.Chat when the
// saved endpoint exists but is disabled.
var ErrChatEndpointNotConfigured = errors.New("chat endpoint not configured")

// ChatEndpointStore persists the single admin-configured domain.ChatEndpoint.
// Unlike EmbeddingEndpointStore, chat only ever has ONE active
// configuration, so this is Get/Set on one row, not CRUD on a collection.
type ChatEndpointStore interface {
	// GetChatEndpoint returns ErrChatEndpointNotConfigured if never saved.
	GetChatEndpoint(ctx context.Context) (domain.ChatEndpoint, error)
	// SetChatEndpoint upserts the single chat endpoint row.
	SetChatEndpoint(ctx context.Context, e domain.ChatEndpoint) error
}

// ChatCompleter calls an OpenAI-compatible chat-completions endpoint,
// optionally with a native "tools" list -- tools is nil/empty for a turn
// with no active MCP tools, in which case the implementation must omit the
// request's tools field entirely, for compatibility with endpoints not
// configured for tool-calling. The returned ChatMessage's ToolCalls is set
// only when the model chose to invoke tools instead of answering directly.
type ChatCompleter interface {
	Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error)
}

// ErrMCPServerNotFound is returned by MCPServerStore's Update and Delete
// when no server with the given ID exists -- MCPServerStore's sibling of
// ErrEmbeddingEndpointNotFound above.
var ErrMCPServerNotFound = errors.New("mcp server not found")

// MCPServerStore persists the admin-configured domain.MCPServer rows -- a
// list of many, like EmbeddingEndpointStore, unlike single-row ChatEndpointStore.
type MCPServerStore interface {
	ListMCPServers(ctx context.Context) ([]domain.MCPServer, error)
	CreateMCPServer(ctx context.Context, s domain.MCPServer) error
	// UpdateMCPServer returns ErrMCPServerNotFound if no server with s.ID exists.
	UpdateMCPServer(ctx context.Context, s domain.MCPServer) error
	// DeleteMCPServer returns ErrMCPServerNotFound if no server with id exists.
	DeleteMCPServer(ctx context.Context, id string) error
}

// ErrUserMCPServerNotFound is returned by UserMCPServerStore's Update and
// Delete when no server with the given (userID, id) pair exists --
// MCPServerStore's per-user sibling.
var ErrUserMCPServerNotFound = errors.New("user mcp server not found")

// UserMCPServerStore persists per-user, self-service domain.MCPServer rows
// -- same shape as MCPServerStore, but scoped to one userID, with IDs
// unique only within that owner's rows, not globally. Never restricted by
// an Agent's MCPServerIDs scope. ChatService.Chat enforces "http" transport
// only for these, since "stdio" grants arbitrary local command execution, a
// trust tier no non-admin user should be handed.
type UserMCPServerStore interface {
	ListUserMCPServers(ctx context.Context, userID string) ([]domain.MCPServer, error)
	CreateUserMCPServer(ctx context.Context, userID string, s domain.MCPServer) error
	// UpdateUserMCPServer returns ErrUserMCPServerNotFound if no server with
	// (userID, s.ID) exists.
	UpdateUserMCPServer(ctx context.Context, userID string, s domain.MCPServer) error
	// DeleteUserMCPServer returns ErrUserMCPServerNotFound if no server with
	// (userID, id) exists.
	DeleteUserMCPServer(ctx context.Context, userID string, id string) error
}

// ErrFileNotFound is returned by FileStore's GetFile and DeleteFile when no
// file with the given (ownerUserID, id) pair exists.
var ErrFileNotFound = errors.New("file not found")

// FileStore persists domain.UploadedFile rows -- every operation scoped by
// ownerUserID, so one user can never list, read, or delete another's file.
// Backs the self-service /account/api/files HTTP endpoints and, via a
// short-lived per-turn bearer token, cmd/mcp-files' tools -- see
// ChatOptions.FileAccessToken's doc comment for why cmd/mcp-files calls
// back over HTTP rather than holding its own DB connection.
type FileStore interface {
	// ListFiles lists ownerUserID's own files, most recent upload first --
	// metadata only, never file content.
	ListFiles(ctx context.Context, ownerUserID string) ([]domain.UploadedFile, error)
	// ListFilesForChat is ListFiles narrowed to one PersistedChat -- used
	// for the in-chat file strip and cmd/mcp-files' list_files.
	ListFilesForChat(ctx context.Context, ownerUserID, chatID string) ([]domain.UploadedFile, error)
	// SaveFile stores a new file owned by ownerUserID, attached to chatID
	// (required, since only a pinned chat may call this), and returns its
	// fully-populated UploadedFile -- the caller never picks the ID.
	SaveFile(ctx context.Context, ownerUserID, chatID, filename, contentType string, data []byte) (domain.UploadedFile, error)
	// GetFile returns one of ownerUserID's own files, metadata and content
	// together -- ErrFileNotFound if it doesn't exist.
	GetFile(ctx context.Context, ownerUserID, id string) (domain.UploadedFile, []byte, error)
	// DeleteFile removes one of ownerUserID's own files -- ErrFileNotFound
	// if it doesn't exist.
	DeleteFile(ctx context.Context, ownerUserID, id string) error
}

var ErrChatNotFound = errors.New("chat not found")

// ChatStore persists domain.PersistedChat rows -- every operation scoped
// by ownerUserID, same discipline as FileStore. Backs the self-service
// /account/api/chats HTTP endpoints.
type ChatStore interface {
	// ListChats lists ownerUserID's own pinned chats, most recently
	// updated first -- reloaded automatically on the chat page.
	ListChats(ctx context.Context, ownerUserID string) ([]domain.PersistedChat, error)
	// CreateChat pins a new chat and returns its fully-populated
	// PersistedChat -- the caller never picks the ID.
	CreateChat(ctx context.Context, c domain.PersistedChat) (domain.PersistedChat, error)
	// UpdateChat replaces c's editable fields and bumps UpdatedAt --
	// ErrChatNotFound if no chat with (c.OwnerUserID, c.ID) exists.
	UpdateChat(ctx context.Context, c domain.PersistedChat) error
	// DeleteChat removes one of ownerUserID's own pinned chats --
	// ErrChatNotFound if it doesn't exist. Also deletes every file
	// attached (sqlrepo does this explicitly, not via a foreign-key
	// cascade -- see its dialect.go for why that isn't reliable everywhere).
	DeleteChat(ctx context.Context, ownerUserID, id string) error
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
// through every follow-up round's tool calls -- MCP's session-oriented
// usage pattern, not a fresh spawn+handshake per call. Open is best-effort
// per server: one that fails to connect is skipped (logged), never fails
// the whole turn. A tool name collision across servers is resolved by
// skipping the later one, never misrouting a call.
//
// env carries ADMIN-CONFIGURED configuration (e.g. WebSearchBaseURL) as
// process environment variables for every spawned stdio server -- never
// anything derived from the model's output, so this doesn't reopen the
// injection surface CallTool's arguments guard against.
type MCPToolProvider interface {
	Open(ctx context.Context, servers []domain.MCPServer, env map[string]string) (MCPSession, []domain.MCPTool)
}

// MCPSession is one chat turn's live connections to every active MCP
// server, returned by MCPToolProvider.Open alongside the tools discovered.
type MCPSession interface {
	// CallTool invokes toolName against its originating server, with
	// argumentsJSON as the model supplied it (raw JSON, NEVER
	// shell-interpolated -- see mcpclient's security doc comment).
	// Returns an error for an unknown tool, a failed call, or a timeout.
	CallTool(ctx context.Context, toolName string, argumentsJSON string) (string, error)
	// Close closes every underlying server connection this session opened.
	// Safe to call even if Open connected to zero servers.
	Close()
}
