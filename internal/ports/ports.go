package ports

import (
	"context"
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
}

// AuthFetcher is a Fetcher that also accepts per-request credentials.
// httpfetcher.Fetcher implements both this and the plain Fetcher (used by
// the robots-checker, which never needs credentials).
type AuthFetcher interface {
	FetchWithOptions(ctx context.Context, url string, opts FetchOptions) (string, error)
}

type RobotsChecker interface {
	Allowed(ctx context.Context, url string) bool
}

type Repository interface {
	Save(ctx context.Context, doc domain.Document) error
	All(ctx context.Context) ([]domain.Document, error)
}

type Indexer interface {
	Add(doc domain.Document)
	Search(query string, topK int) []domain.SearchResult
	DocCount() int
}

// EmbeddingProvider converts text into a vector.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dimensions() int
}

// SQLRepository is the port to the relational database.
type SQLRepository interface {
	SaveDocument(ctx context.Context, doc domain.Document, embedding []float32) error
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
	VocabularyStats(ctx context.Context, topN int) (vocabularySize int, topTerms []domain.TermStat, err error)
	// AllTerms returns every distinct term the corpus's postings hold, each
	// with its doc/total frequency -- the full vocabulary, unlike
	// VocabularyStats' topN-bounded listing -- for domain.VocabularyCache
	// (refreshed by bootstrap.SyncVocabulary) to check a query term with
	// zero postings hits against for a bounded-edit-distance near-miss (see
	// domain.NearestTerm).
	AllTerms(ctx context.Context) ([]domain.TermStat, error)
	// EmbeddingsForDocs batch-fetches embeddings for exactly the given doc
	// IDs (typically a query's BM25-hit set), so scoring a candidate never
	// requires a full-corpus scan. Each result carries its norm alongside
	// its vector (see domain.EmbeddedVector) -- precomputed once, at
	// SaveDocument time, rather than recomputed from scratch on every
	// request that scores the document.
	EmbeddingsForDocs(ctx context.Context, ids []string) (map[string]domain.EmbeddedVector, error)
	// SampleEmbeddings returns up to limit embeddings from across the
	// corpus, so a purely semantic match (no BM25 hits at all) can still be
	// found -- bounded regardless of how large the corpus is, unlike a full
	// "every document" scan.
	SampleEmbeddings(ctx context.Context, limit int) (map[string]domain.EmbeddedVector, error)
	// TopSemanticMatches finds queryVec's approximate nearest neighbors by
	// cosine distance via Postgres pgvector's HNSW-accelerated "ORDER BY
	// embedding_vector <=> $1 LIMIT $2" query, used in place of
	// SampleEmbeddings to fill a search's semantic candidate pool whenever
	// ANN is actually available. ok is false (with a nil error and nil map)
	// when it isn't -- a non-Postgres dialect, a Postgres server without
	// the pgvector extension, or this process's sqlrepo.Repository.EnableANN
	// never having succeeded -- telling the caller
	// (hybridSearchService.Search) to fall back to SampleEmbeddings's
	// bounded brute-force sample exactly as it did before ANN existed. A
	// non-nil error means the ANN query itself failed (a real fault, not an
	// availability question) and should be treated like any other
	// repository error.
	TopSemanticMatches(ctx context.Context, queryVec []float32, limit int) (matches map[string]domain.EmbeddedVector, ok bool, err error)
	DocumentByID(ctx context.Context, docID string) (domain.Document, error)
	// DocumentsByIDs batch-fetches documents for the given IDs in one
	// round trip (a missing ID is simply absent from the result, not an
	// error), for callers that would otherwise call DocumentByID once per
	// candidate.
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
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	DeleteDocument(ctx context.Context, docID string) error
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
	VocabularyStats(ctx context.Context, topN int) (vocabularySize int, topTerms []domain.TermStat, err error)
	ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error)
	SearchDomains(ctx context.Context, q string, limit int) ([]domain.DomainSummary, error)
	DocumentVersions(ctx context.Context, docID string) ([]domain.DocumentVersion, error)
	DocumentsOverview(ctx context.Context, topDomains int) (domain.DocumentsOverview, error)
	PostingsForTerm(ctx context.Context, term string) ([]domain.PostingStats, error)
	DeleteDocument(ctx context.Context, docID string) error
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
}

type SearchService interface {
	Search(ctx context.Context, query string, opts SearchQuery) ([]domain.SearchResult, error)
}

// CrawlOptions is a single crawl request: seed URLs and page budget, plus
// optional credentials for sites that require a session cookie or HTTP
// Basic auth (applied to every fetch made during that crawl). RespectRobots
// defaults to false (robots.txt is ignored) unless explicitly set; UserAgent
// overrides the process's configured default for this crawl only.
// AllowOffDomainLinks defaults to false, so a crawl stays on the seed URLs'
// own host(s) unless explicitly allowed to wander to other domains via
// discovered links. UseSitemap defaults to false; when set, each seed's
// /sitemap.xml is fetched and its URLs enqueued alongside normally
// discovered links.
type CrawlOptions struct {
	SeedURLs            []string
	MaxPages            int
	Cookie              string
	BasicAuthUser       string
	BasicAuthPass       string
	RespectRobots       bool
	UserAgent           string
	AllowOffDomainLinks bool `json:"allow_off_domain_links"`
	UseSitemap          bool `json:"use_sitemap"`
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

// CrawlJobService lets a caller trigger a crawl asynchronously and poll its
// progress, without blocking on the crawl itself completing.
type CrawlJobService interface {
	StartCrawlJob(ctx context.Context, opts CrawlOptions) (jobID string, err error)
	ListCrawlJobs(ctx context.Context) ([]domain.CrawlJobSummary, error)
	GetCrawlJob(ctx context.Context, jobID string) (domain.CrawlJob, error)
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
)

// SettingsStore persists the admin-configurable tuning/operational/ranking
// settings blobs to the shared database, so every process reads the same
// values instead of only the copy an admin edit happened to update in its
// own in-memory instance.
type SettingsStore interface {
	SaveSetting(ctx context.Context, key, value string) error
	GetSetting(ctx context.Context, key string) (value string, found bool, err error)
}

// ErrScheduledCrawlNotFound is returned by ScheduledCrawlStore's Update,
// SetScheduledCrawlEnabled and Delete when no schedule with the given ID
// exists.
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
	ListScheduledCrawls(ctx context.Context) ([]domain.ScheduledCrawl, error)
	UpdateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error
	SetScheduledCrawlEnabled(ctx context.Context, id string, enabled bool) error
	DeleteScheduledCrawl(ctx context.Context, id string) error
	// DueScheduledCrawls lists every enabled schedule whose NextRunAt is at
	// or before now.
	DueScheduledCrawls(ctx context.Context, now time.Time) ([]domain.ScheduledCrawl, error)
	// MarkScheduledCrawlRun records that a schedule was just triggered,
	// advancing it to its next run.
	MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time) error
}
