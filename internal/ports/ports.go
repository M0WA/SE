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
	// Renderer selects how this one fetch should be done: "" (the zero
	// value, domain.RendererDefault) defers to whatever renderer the
	// caller would otherwise use (typically the Tuning page's global
	// default); domain.RendererNone/RendererChromium/RendererFirefox
	// override it explicitly for this fetch. Consulted only by a
	// render-aware AuthFetcher (see application.RenderAwareFetcher) --
	// httpfetcher.Fetcher itself ignores it entirely, since it only ever
	// does plain HTTP.
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

// Renderer executes a page in a real (headless) browser -- running its
// JavaScript and waiting for it to finish loading -- before returning its
// final rendered HTML, for a site whose real content only exists after
// client-side rendering. Implemented by internal/adapters/browserfetcher,
// one instance per browser engine (Chromium, Firefox); application.
// RenderAwareFetcher dispatches to the right one based on FetchOptions.
// Renderer / the Tuning page's configured default.
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
	// document_versions first if it changed -- maxVersions bounds how
	// many versions (current plus archived) survive that archiving,
	// pruning the oldest beyond it in the same write. See
	// domain.OperationalSettingsValues.MaxDocumentVersions. titleWeight is
	// how many times the title is counted into the indexed token stream
	// ahead of the body -- see domain.OperationalSettingsValues.TitleWeight.
	// embeddings holds one vector per currently-enabled provider (see
	// domain.OperationalSettingsValues.EmbeddingHashEnabled and every
	// enabled domain.EmbeddingHTTPEndpoint, keyed by domain.
	// EmbeddingProviderHash or the endpoint's own ID) -- every one of them
	// is upserted into document_embeddings in the same write.
	SaveDocument(ctx context.Context, doc domain.Document, embeddings map[string][]float32, maxVersions, titleWeight int) error
	// PostingsForTerms batch-fetches postings for every given term in a
	// single query (a "WHERE term IN (...)" join against documents), so a
	// multi-term search issues one round trip regardless of how many unique
	// terms it has -- rather than one query per term. Returned PostingStats
	// carry TermFreq/DocLength/DocFreq only; TotalDocs/AvgDocLen are left
	// zero for the caller to fill in from its own corpus-wide stats (see
	// domain.CorpusStatsCache), since those don't vary per term and would
	// otherwise be refetched redundantly for every term in the batch.
	PostingsForTerms(ctx context.Context, terms []string) (map[string][]domain.PostingStats, error)
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	// VocabularyStats reports the corpus's total distinct-term count, plus a
	// limit/offset page of its terms ordered by sortBy ("term", "doc_freq",
	// or "total_freq"; anything else falls back to "doc_freq") and sortDir
	// ("asc" or "desc"; anything else falls back to "desc") -- backing the
	// admin vocabulary page's pagination and sortable columns. When search
	// is non-empty, the listing (and matchedCount) is additionally
	// restricted to terms containing search (a substring match); vocabSize
	// itself always reflects the whole corpus, unaffected by search.
	// matchedCount is the total number of terms matching search (before
	// limit/offset are applied, so the caller can compute a page count),
	// equal to vocabSize when search is empty.
	VocabularyStats(ctx context.Context, limit, offset int, search, sortBy, sortDir string) (vocabSize, matchedCount int, terms []domain.TermStat, err error)
	// AllTerms returns every distinct term the corpus's postings hold, each
	// with its doc/total frequency -- the full vocabulary, unlike
	// VocabularyStats' topN-bounded listing -- for domain.VocabularyCache
	// (refreshed by bootstrap.SyncVocabulary) to check a query term with
	// zero postings hits against for a bounded-edit-distance near-miss (see
	// domain.NearestTerm).
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
	// EmbeddingsForDocs batch-fetches provider's embeddings for exactly the
	// given doc IDs (typically a query's BM25-hit set), so scoring a
	// candidate never requires a full-corpus scan. Each result carries its
	// norm alongside its vector (see domain.EmbeddedVector) -- precomputed
	// once, at SaveDocument time, rather than recomputed from scratch on
	// every request that scores the document. provider is
	// domain.EmbeddingProviderHash or a configured domain.
	// EmbeddingHTTPEndpoint.ID -- typically whichever domain.
	// OperationalSettingsValues.EmbeddingProvider names as active for
	// search.
	EmbeddingsForDocs(ctx context.Context, ids []string, provider string) (map[string]domain.EmbeddedVector, error)
	// SampleEmbeddings returns up to limit of provider's embeddings from
	// across the corpus, so a purely semantic match (no BM25 hits at all)
	// can still be found -- bounded regardless of how large the corpus is,
	// unlike a full "every document" scan.
	SampleEmbeddings(ctx context.Context, limit int, provider string) (map[string]domain.EmbeddedVector, error)
	// TopSemanticMatches finds queryVec's approximate nearest neighbors
	// within provider's embeddings by cosine distance via Postgres
	// pgvector's HNSW-accelerated "ORDER BY embedding_vector_<provider>
	// <=> $1 LIMIT $2" query, used in place of SampleEmbeddings to fill a
	// search's semantic candidate pool whenever ANN is actually available
	// for provider. ok is false (with a nil error and nil map) when it
	// isn't -- a non-Postgres dialect, a Postgres server without the
	// pgvector extension, or this process's sqlrepo.Repository.EnableANN
	// never having succeeded for provider -- telling the caller
	// (hybridSearchService.Search) to fall back to SampleEmbeddings's
	// bounded brute-force sample exactly as it did before ANN existed. A
	// non-nil error means the ANN query itself failed (a real fault, not an
	// availability question) and should be treated like any other
	// repository error.
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
	// matches one of the given hosts, or is a subdomain of one (mirroring
	// domain.ParsedQuery.SiteAllowed's matching rule), served by
	// idx_documents_host. Used to force a query's site: matches into the
	// search candidate set directly, since they otherwise have no guarantee
	// of appearing in the BM25-hit set or the bounded semantic sample.
	DocumentIDsByHost(ctx context.Context, hosts []string) ([]string, error)
	// HostsIndexed reports, for each of hosts, whether any document is
	// already indexed for it (exact host match or a subdomain of it, same
	// matching rule as DocumentIDsByHost) -- used by a crawl's
	// FollowIndexedDomains option to widen its link scope to any domain the
	// corpus already has content for, without needing every document ID.
	// A host absent from the result was not found indexed.
	HostsIndexed(ctx context.Context, hosts []string) (map[string]bool, error)
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	DeleteDocument(ctx context.Context, docID string) error
	// RecordDocumentAlias upserts one document_aliases row: aliasURL's
	// content lives under canonicalID, not its own document row -- called
	// by application.crawlLoop when a fetched page's <link rel="canonical">
	// points elsewhere (reason domain.DocumentAliasReasonCanonicalTag), and
	// by application.RunContentDedupJob's MergeDocuments when two
	// independently-crawled documents turn out to have duplicate/near-
	// duplicate content (reason domain.DocumentAliasReasonContentExact/
	// ContentSimHash). canonicalID is deliberately NOT required to already
	// exist in documents -- the canonical target may not be crawled yet
	// (a forward-declared alias); it resolves itself once that document is
	// actually saved.
	RecordDocumentAlias(ctx context.Context, aliasURL, canonicalID, reason string) error
}

// PageRankRepository is the narrow port application.RunPageRankJob needs:
// load the current link graph, then write back every document's freshly
// computed score. Implemented by the same *sqlrepo.Repository every
// process already opens -- cmd/crawl (which owns the periodic recompute
// ticker, and triggers one more run right after each crawl completes) is
// the only caller.
type PageRankRepository interface {
	// LinkGraph loads the entire crawled link graph as an adjacency map:
	// each document's ID to the IDs of every other indexed document it
	// links to (a link whose target URL was never crawled/indexed has no
	// document ID to report and is simply omitted -- domain.PageRank never
	// sees it). Loaded in one query rather than one row/document at a
	// time.
	LinkGraph(ctx context.Context) (map[string][]string, error)
	// UpdatePageRanks batch-writes every given document ID's freshly
	// computed PageRank score to documents.pagerank. A document not
	// mentioned in scores (e.g. one with neither an incoming nor an
	// outgoing link, so it never appeared in the link graph at all) is
	// left untouched, keeping whatever neutral default or prior score it
	// already had rather than being zeroed out.
	UpdatePageRanks(ctx context.Context, scores map[string]float64) error
}

// EmbeddingRepository is the narrow slice of *sqlrepo.Repository
// application.RunEmbeddingRecomputeJob needs -- iterate every document's
// ID, fetch each one's already-stored Text, and overwrite just its
// embedding, without touching anything else SaveDocument would (postings,
// links, document_versions, pagerank, host).
type EmbeddingRepository interface {
	// AllDocumentIDs lists every document ID in the corpus, ordered so
	// repeated calls (and the batches RunEmbeddingRecomputeJob fetches
	// against DocumentsByIDs) are stable and deterministic.
	AllDocumentIDs(ctx context.Context) ([]string, error)
	// DocumentsByIDs batch-fetches each document's URL/title/text -- see
	// AdminRepository's identical method (implemented once, satisfying
	// both narrow ports).
	DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error)
	// UpdateEmbedding overwrites one document's embedding for every
	// provider present in embeddings (and, when Postgres pgvector ANN is
	// enabled for this process for a given provider, that provider's own
	// embedding_vector_<provider> column too) -- the narrow write
	// SaveDocument's embedding-writing half performs, without re-tokenizing
	// text, without touching postings/links/versions/pagerank, and without
	// archiving a new document_versions row (the text itself hasn't
	// changed, only its vector representation).
	UpdateEmbedding(ctx context.Context, id string, embeddings map[string][]float32) error
}

// SessionStore backs the admin/search login system's session tokens.
// Implemented by *sqlrepo.Repository (a "sessions" table any process
// sharing the database can read) so a login on one process -- e.g.
// admin-server's /login -- is recognized by every other process serving
// the same site -- e.g. search-server, once the public search page also
// requires authentication -- rather than only the process that issued the
// token, which an in-memory store could never do across separate OS
// processes.
type SessionStore interface {
	// CreateSession persists a freshly issued token, valid until expiresAt.
	CreateSession(ctx context.Context, token string, expiresAt time.Time) error
	// ValidSession reports whether token names a session that hasn't
	// expired yet.
	ValidSession(ctx context.Context, token string) (bool, error)
	// RevokeSession deletes a session outright (a sign-out). Revoking an
	// unknown or already-expired token is not an error.
	RevokeSession(ctx context.Context, token string) error
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
	// PageRankHistogram buckets every document's pagerank into
	// domain.PageRankHistogramBuckets equal-width bins spanning the
	// corpus's own observed [min, max] range, alongside how many documents
	// sit at or below domain.PageRankOrphanThreshold and the corpus's total
	// document count -- the admin Overview page's PageRank distribution
	// histogram and orphan-rate stat tile. All zero for an empty corpus.
	PageRankHistogram(ctx context.Context) (buckets []domain.PageRankBucket, orphanCount, totalDocs int, err error)
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
	// ProviderWeights, when non-nil, fully replaces domain.
	// OperationalSettingsValues.EmbeddingSearchWeights for this request
	// only -- see hybridSearchService.Search. nil means "use the admin
	// default"; an empty-but-non-nil map means "no semantic scoring at
	// all for this request" (pure BM25), same as every weight being <= 0.
	ProviderWeights map[string]float64
}

type SearchService interface {
	Search(ctx context.Context, query string, opts SearchQuery) ([]domain.SearchResult, error)
}

// CrawlOptions is a single crawl request: seed URLs and page budget, plus
// optional credentials for sites that require a session cookie or HTTP
// Basic auth (applied to every fetch made during that crawl). RespectRobots
// defaults to false (robots.txt is ignored) unless explicitly set; UserAgent
// overrides the process's configured default for this crawl only.
// UseSitemap defaults to false; when set, each seed's /sitemap.xml is
// fetched and its URLs enqueued alongside normally discovered links.
type CrawlOptions struct {
	SeedURLs      []string
	MaxPages      int
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	RespectRobots bool
	UserAgent     string
	// LinkScope overrides the Tuning page's global default for how far
	// this crawl follows discovered links -- "" (domain.LinkScopeDefault)
	// means "use the global default," same convention Renderer already
	// uses; domain.LinkScopeHost/LinkScopeDomain/LinkScopeAny choose
	// explicitly. See domain.LinkScope* and crawlLoop's onDomain.
	LinkScope string `json:"link_scope"`
	// AllowedDomains/BlockedDomains are a per-crawl allow/block list of
	// domains to follow discovered links to, on top of LinkScope: a domain
	// (or any of its subdomains) in BlockedDomains is never followed, even
	// if LinkScope or AllowedDomains would otherwise allow it -- BlockedDomains
	// always wins. A domain in AllowedDomains is followed even if LinkScope
	// itself would reject it, widening scope rather than narrowing it. Both
	// nil/empty (the default) leave LinkScope as the only scope check.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	// FollowIndexedDomains additionally follows a discovered link whose
	// domain already has at least one indexed document (see
	// ports.SQLRepository.HostsIndexed), even if LinkScope/AllowedDomains
	// wouldn't otherwise allow it -- useful for a crawl that should keep
	// refreshing any site already in the corpus without having to name
	// every one of them in AllowedDomains. BlockedDomains still overrides
	// this, same as it overrides LinkScope/AllowedDomains.
	FollowIndexedDomains bool `json:"follow_indexed_domains,omitempty"`
	UseSitemap           bool `json:"use_sitemap"`
	// FetchTimeoutSeconds, MinTextLength, CrawlDelayMs and MaxResponseKB
	// override the same-named operational defaults for this crawl alone
	// when positive; zero means "use the global default" -- the same
	// convention MaxPages<=0 and UserAgent=="" already use above. Every
	// operational setting that's actually crawl-specific (as opposed to
	// search- or database-related) is overridable here, so a crawl is
	// never stuck with the global default for a site that needs a gentler
	// delay, a longer timeout, or a larger page.
	FetchTimeoutSeconds int `json:"fetch_timeout_seconds"`
	MinTextLength       int `json:"min_text_length"`
	CrawlDelayMs        int `json:"crawl_delay_ms"`
	MaxResponseKB       int `json:"max_response_kb"`
	// PrioritizeUnindexed reorders discovery so URLs not already in the
	// index are fetched before ones that are, within the same MaxPages
	// budget -- useful when recrawling a large, already-mostly-indexed
	// site and the goal is to find new pages rather than spend the budget
	// refreshing old ones. Already-indexed pages still get crawled once
	// every not-yet-indexed one has been attempted; this only changes
	// order, never coverage.
	PrioritizeUnindexed bool `json:"prioritize_unindexed"`
	// Renderer overrides the Tuning page's global default rendering mode
	// for this crawl alone -- "" (domain.RendererDefault) means "use the
	// global default," same convention as every other override above;
	// domain.RendererNone/RendererChromium/RendererFirefox choose
	// explicitly. See domain.Renderer* and ports.Renderer.
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
}

// ErrCrawlJobNotRunning is returned by CrawlJobService.CancelCrawlJob (and
// crawl-server's own cancel handler) when the job exists but isn't
// currently queued or running, so there's nothing to cancel.
var ErrCrawlJobNotRunning = errors.New("crawl job is not currently running")

// CrawlJobStore is crawl-server's own persistence for crawl jobs and their
// per-page event history -- distinct from CrawlJobService, which is the
// network contract admin-server's client uses to talk to crawl-server.
// domain.CrawlJobStore (in-memory, lost on restart) satisfies this
// structurally for tests; sqlrepo.Repository's DB-backed implementation is
// what crawl-server actually runs in production, so a job's full history
// survives a restart instead of disappearing with it. Get returns
// domain.ErrCrawlJobNotFound if no job with that ID is retained.
type CrawlJobStore interface {
	Create(ctx context.Context, req domain.CrawlJobRequest) (domain.CrawlJob, error)
	MarkRunning(ctx context.Context, id string) error
	AppendPage(ctx context.Context, id string, ev domain.CrawlPageEvent) error
	MarkDone(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string, failErr error) error
	MarkCancelled(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (domain.CrawlJob, error)
	List(ctx context.Context) ([]domain.CrawlJobSummary, error)
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
	// SettingsKeyEmbeddingEndpointsMigrated holds the plain string "true"
	// once the one-time legacy-HTTP-config-to-embedding_http_endpoints
	// migration (sqlrepo.Repository.migrateLegacyHTTPEmbeddingConfig) has
	// run -- a marker distinct from "does embedding_http_endpoints have any
	// rows," since an admin deleting the migrated endpoint afterward would
	// otherwise make that table empty again and the migration would
	// wrongly re-run (and resurrect the deleted endpoint) on the next
	// restart.
	SettingsKeyEmbeddingEndpointsMigrated = "embedding_endpoints_migrated"
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

// ScheduledCrawlStore persists recurring crawl schedules an admin creates
// through the admin UI. It's implemented by the same *sqlrepo.Repository
// admin-server and crawl-server each already open their own DB connection
// to (for AdminRepository and SettingsStore respectively) -- both
// processes talk to the shared scheduled_crawls table directly rather than
// crawl-server's ticker or admin-server's CRUD endpoints needing an HTTP
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
	// MarkScheduledCrawlRun records that a schedule was just triggered (or
	// just finished), advancing it to its next run, storing runCount (how
	// many times it has now run), and setting whether it stays enabled -- a
	// one-off (non-recurring) entry, or one that just reached its MaxRuns
	// cap, passes false so it's never picked up again. inProgress is
	// separate from enabled: true from the moment a run is triggered until
	// that same run's completion clears it, keeping DueScheduledCrawls from
	// double-triggering an entry that runs longer than its own interval,
	// without touching (or visibly flickering) the admin's own enabled
	// toggle to do it.
	MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, enabled, inProgress bool, runCount int) error
	// RunScheduledCrawlNow marks a schedule due immediately -- sets
	// NextRunAt to now, re-enables it if it was paused, and force-clears
	// InProgress -- without touching any other field (recurring/interval/
	// run count/options all stay exactly as they were). crawl-server's own
	// scheduler ticker (TriggerDueCrawls) picks it up on its next tick, the
	// same path a freshly created one-off crawl already goes through.
	// Returns ErrScheduledCrawlNotFound if id doesn't exist.
	RunScheduledCrawlNow(ctx context.Context, id string, now time.Time) error
	// ResetStaleInProgress clears InProgress back to false for every
	// schedule that has it stuck true -- meant to run once at crawl-server
	// startup, before anything else can query DueScheduledCrawls. Returns
	// how many rows were reset.
	ResetStaleInProgress(ctx context.Context) (int, error)
}

// ErrEmbeddingEndpointNotFound is returned by EmbeddingEndpointStore's
// Update and Delete when no endpoint with the given ID exists.
var ErrEmbeddingEndpointNotFound = errors.New("embedding endpoint not found")

// EmbeddingEndpointStore persists the admin-configured HTTP embedding
// endpoints (see domain.EmbeddingHTTPEndpoint) -- one row per endpoint an
// admin has added, each independently enabled and rate-limited, alongside
// the always-available built-in hash provider. Implemented by the same
// *sqlrepo.Repository every process already opens, the same way
// ScheduledCrawlStore is.
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
