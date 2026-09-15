package domain

import (
	"sync"
	"time"
)

// OperationalSettingsValues is a snapshot of every admin-configurable
// operational knob outside the BM25/semantic ranking formula (see
// TuningSettings for that): how the crawler fetches pages, what a search
// returns by default, and how long a sign-in session lasts.
type OperationalSettingsValues struct {
	FetchTimeout     time.Duration
	UserAgent        string
	DefaultMaxPages  int
	MinTextLength    int
	DefaultTopK      int
	SessionTTL       time.Duration
	CrawlDelayMs     int
	MaxResponseBytes int
	// SemanticCandidatePoolSize bounds how many documents ever get a
	// semantic (cosine similarity) score computed and ranked per search,
	// regardless of corpus size -- see hybridSearchService.Search. It's the
	// union of every BM25-hit document (however many that is) plus a
	// bounded sample of the rest of the corpus, so a purely semantic match
	// (no BM25 hits at all) can still be found without scoring literally
	// every stored document on every request.
	SemanticCandidatePoolSize int
	// DBMaxOpenConns and DBMaxIdleConns bound the SQL connection pool
	// (sqlrepo.Repository.ConfigurePool), and DBConnMaxLifetime caps how
	// long a pooled connection is reused before being recycled. Applied at
	// startup with a dialect-aware default (Postgres/MySQL benefit from a
	// real pool; SQLite's single-writer locking model means
	// sqlrepo.Repository always clamps its own open-connection count to 1
	// regardless of what's configured here), and re-applied live whenever
	// an admin edit reaches this process via bootstrap.SyncSettings.
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	// FuzzyMatchEnabled turns on typo-tolerant query matching: a query term
	// with zero postings hits is looked up against the corpus vocabulary
	// for a near-miss term within FuzzyMaxEditDistance edits (Levenshtein)
	// and substituted into BM25 scoring for that term alone -- see
	// hybridSearchService.Search and domain.NearestTerm. A term that
	// already matched something is never touched. When false, search
	// behaves exactly as if this feature didn't exist.
	FuzzyMatchEnabled bool
	// FuzzyMaxEditDistance bounds how many edits (insertions, deletions,
	// substitutions) a substituted vocabulary term may be from the
	// original query term. Clamped to 1 or 2: 1 catches a single
	// typo/transposition-as-two-edits case conservatively, 2 is more
	// forgiving but risks matching an unrelated short word.
	FuzzyMaxEditDistance int
	// PageRankRecomputeIntervalMinutes is how often cmd/crawl's periodic
	// ticker recomputes every document's PageRank score from the current
	// link graph (see application.RunPageRankJob) -- in addition to that,
	// a recompute always runs once right after a crawl job completes
	// successfully, since that's when the graph actually changes. Clamped
	// to a minimum of 5 minutes so an overly aggressive setting can't spin
	// the recompute in a tight loop.
	PageRankRecomputeIntervalMinutes int
	// ANNSearchEnabled controls whether a search's semantic candidate pool
	// (see hybridSearchService.Search) is filled via Postgres pgvector's
	// approximate-nearest-neighbor index (repo.TopSemanticMatches) rather
	// than the bounded brute-force SampleEmbeddings sample it otherwise
	// falls back to. Defaults to true so ANN is used automatically
	// wherever it's actually available for this process (Postgres, the
	// pgvector extension installed, and sqlrepo.Repository.EnableANN
	// having succeeded at startup) -- it has zero effect either way on
	// SQLite/MySQL or a Postgres server without the extension, since those
	// never report ANN as available regardless of this setting. Set false
	// to force the brute-force fallback path even when ANN is available,
	// e.g. to troubleshoot a ranking difference between the two paths.
	ANNSearchEnabled bool
	// MaxRetainedCrawlJobs bounds how many crawl jobs (and their full
	// per-page event history) crawl-server's persistent store keeps --
	// cmd/crawl periodically prunes the oldest beyond this limit. Unlike
	// the old in-memory-only store's hard-coded 200-job cap, this is
	// admin-configurable now that history survives in the database rather
	// than being bounded only by process memory.
	MaxRetainedCrawlJobs int
	// DefaultRenderer is the crawler's global default page-rendering mode
	// (see Renderer* constants): RendererNone (plain HTTP fetch) unless an
	// admin turns on real browser rendering. A crawl's own Renderer
	// (ScheduledCrawl.Renderer / ports.CrawlOptions.Renderer) overrides
	// this when set to anything other than RendererDefault ("").
	DefaultRenderer string
	// LinkScope is the crawler's global default for how far a crawl
	// follows discovered links (see LinkScope* constants) -- defaults to
	// LinkScopeDomain (same registrable domain, any subdomain). A crawl's
	// own LinkScope (ScheduledCrawl.LinkScope / ports.CrawlOptions.
	// LinkScope) overrides this when set to anything other than
	// LinkScopeDefault ("").
	LinkScope string
	// MaxDocumentVersions bounds how many versions of a document (the
	// current one plus its archived predecessors in document_versions)
	// are kept whenever a re-crawl finds its content has changed -- see
	// sqlrepo.Repository.SaveDocument, which prunes the oldest archived
	// versions beyond this limit in the same write that archives a new
	// one. A document that's never changed always has exactly one version
	// regardless of this setting; it only bounds how much prior history a
	// frequently-changing page accumulates.
	MaxDocumentVersions int
	// TitleWeight is how many times a document's title is counted into its
	// indexed token stream, ahead of its body -- see
	// sqlrepo.Repository.SaveDocument. postings stores one merged term_freq
	// per (term, doc) rather than a separate per-field count (no
	// BM25F-style fielded formula), so the simplest way to give a title
	// match more weight than the same word appearing in the body is to make
	// it contribute that many times more to term_freq. 1 gives the title no
	// extra weight at all (counted once, same as any body mention); values
	// above 1 give it a progressively bigger edge, tempered by BM25's own
	// term-frequency saturation (the k1 parameter), which keeps a repeated
	// term from dominating a score outright. Like every other
	// indexing-time setting (min text length, crawl delay, ...), a change
	// here only takes effect for documents crawled or re-crawled
	// afterward -- it isn't retroactively applied to already-indexed
	// content.
	TitleWeight int
	// EmbeddingProvider selects which ports.EmbeddingProvider implementation
	// every process (cmd/search, cmd/admin, cmd/crawl) constructs at
	// startup -- EmbeddingProviderHash (the default: hashembed.Embedder's
	// dependency-free feature-hashing pseudo-embedding, "semantic" only in
	// that documents sharing tokens score similarly) or
	// EmbeddingProviderHTTP (httpembed.Embedder: calls an OpenAI-compatible
	// embeddings HTTP endpoint -- a local inference server such as Ollama/
	// llama.cpp/LM Studio, or a hosted API -- for a real trained model,
	// without adding any ML runtime dependency to this binary itself).
	//
	// Unlike every other field on this page, this one is NOT picked up
	// live by bootstrap.SyncSettings: each process reads it exactly once,
	// at startup, to build its embedder before calling
	// sqlrepo.Repository.EnableANN, which sizes its pgvector column and
	// HNSW index to that embedder's Dimensions() -- swapping providers (or
	// dimensions) without a restart would leave that column sized for the
	// wrong vector length. Changing this setting takes effect the next
	// time each of cmd/search/cmd/admin/cmd/crawl is restarted, same as
	// any other change that would require re-sizing that column.
	//
	// Switching providers (or, for EmbeddingProviderHTTP, changing model or
	// dimensions) also changes the vector space entirely: a similarity
	// score between an embedding computed by the old provider/model and one
	// computed by the new one is meaningless. Like TitleWeight above, there
	// is no automatic full-corpus re-embed migration here -- a document's
	// stored embedding only gets recomputed the next time that document is
	// crawled or re-crawled, so search stays internally consistent (BM25
	// keeps working normally) but semantic ranking degrades until the whole
	// corpus has been re-crawled under the new provider.
	EmbeddingProvider string
	// EmbeddingHTTPBaseURL is the OpenAI-compatible embeddings API's base
	// URL (e.g. "http://localhost:11434/v1" for a local Ollama server, or a
	// hosted provider's own base URL) -- httpembed.Embedder POSTs to
	// "<EmbeddingHTTPBaseURL>/embeddings". Meaningless unless
	// EmbeddingProvider is EmbeddingProviderHTTP.
	EmbeddingHTTPBaseURL string
	// EmbeddingHTTPAPIKey is sent as an "Authorization: Bearer <key>"
	// header on every embeddings request -- optional, since a local
	// inference server often needs none. This is a real credential, so
	// (like ScheduledCrawl's Cookie/BasicAuthPass) it's handled as one:
	// admin.go's operationalValues wire format never echoes the stored
	// value back in a GET response, and it's never logged. An empty value
	// passed to OperationalSettings.Set leaves whatever key is already
	// configured unchanged rather than clearing it -- see Set's doc
	// comment for why.
	EmbeddingHTTPAPIKey string
	// EmbeddingHTTPModel is sent as the embeddings request body's "model"
	// field.
	EmbeddingHTTPModel string
	// EmbeddingHTTPDimensions is the expected embedding vector length --
	// httpembed.Embedder errors clearly if an API response's actual vector
	// length doesn't match this, rather than silently corrupting every
	// downstream cosine-similarity calculation. Also what Dimensions()
	// reports to EnableANN for pgvector column sizing.
	EmbeddingHTTPDimensions int
}

// defaultUserAgent mimics a standard desktop Firefox so crawled sites treat
// requests like an ordinary browser visit rather than flagging or blocking
// an identifiable bot -- overridable process-wide via the tuning page, or
// per crawl via CrawlOptions.UserAgent.
const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0"

// defaultCrawlDelayMs and defaultMaxResponseBytes are the built-in
// politeness/safety defaults: a quarter-second between fetches, and a 5MB
// cap on how much of a response body gets read.
const (
	defaultCrawlDelayMs     = 250
	defaultMaxResponseBytes = 5 * 1024 * 1024
	// defaultSemanticCandidatePoolSize keeps every search's semantic
	// scoring/ranking/sorting step working over at most a few hundred
	// candidates, whether the corpus holds a thousand documents or ten
	// million.
	defaultSemanticCandidatePoolSize = 200
	// defaultDBMaxOpenConns/defaultDBMaxIdleConns/defaultDBConnMaxLifetime
	// are sane defaults for a real connection pool (Postgres/MySQL);
	// sqlrepo.Repository.ConfigurePool clamps SQLite down to a single
	// connection regardless of these values, since SQLite serializes
	// writers at the file level and a larger pool there just adds
	// "database is locked" contention instead of concurrency.
	defaultDBMaxOpenConns    = 25
	defaultDBMaxIdleConns    = 25
	defaultDBConnMaxLifetime = 5 * time.Minute
	// defaultFuzzyMaxEditDistance allows up to a 2-edit typo (e.g. two
	// substitutions, or one insertion plus one substitution) to still
	// resolve to the intended vocabulary term.
	defaultFuzzyMaxEditDistance = 2
	// defaultPageRankRecomputeIntervalMinutes and
	// minPageRankRecomputeIntervalMinutes bound how often the link graph
	// is re-scored: hourly by default, never more often than every 5
	// minutes even if an admin asks for tighter.
	defaultPageRankRecomputeIntervalMinutes = 60
	minPageRankRecomputeIntervalMinutes     = 5
	// defaultMaxRetainedCrawlJobs matches the old in-memory store's
	// hard-coded cap, kept as the default now that it's just a starting
	// point rather than a hard limit -- persistent storage can comfortably
	// hold far more history if an admin raises it.
	defaultMaxRetainedCrawlJobs = 200
	// defaultMaxDocumentVersions keeps a handful of prior versions around
	// for a changing page without letting document_versions grow
	// unbounded for a page that's re-crawled often.
	defaultMaxDocumentVersions = 5
	// defaultTitleWeight is a modest edge (title terms end up with roughly
	// double the term frequency they'd get from a single mention, on top
	// of however many times they separately occur in the body), not an
	// aggressive one.
	defaultTitleWeight = 2
	// defaultEmbeddingHTTPDimensions matches hashembed's own default, so an
	// admin switching EmbeddingProvider to EmbeddingProviderHTTP without
	// having yet set a dimensions count still gets a sane non-zero value
	// (EnableANN treats dims<=0 as "ANN unavailable") rather than silently
	// disabling ANN until they do.
	defaultEmbeddingHTTPDimensions = 128
)

func defaultOperationalSettings() OperationalSettingsValues {
	return OperationalSettingsValues{
		FetchTimeout:                     8 * time.Second,
		UserAgent:                        defaultUserAgent,
		DefaultMaxPages:                  20,
		MinTextLength:                    50,
		DefaultTopK:                      10,
		SessionTTL:                       12 * time.Hour,
		CrawlDelayMs:                     defaultCrawlDelayMs,
		MaxResponseBytes:                 defaultMaxResponseBytes,
		SemanticCandidatePoolSize:        defaultSemanticCandidatePoolSize,
		DBMaxOpenConns:                   defaultDBMaxOpenConns,
		DBMaxIdleConns:                   defaultDBMaxIdleConns,
		DBConnMaxLifetime:                defaultDBConnMaxLifetime,
		FuzzyMatchEnabled:                true,
		FuzzyMaxEditDistance:             defaultFuzzyMaxEditDistance,
		PageRankRecomputeIntervalMinutes: defaultPageRankRecomputeIntervalMinutes,
		ANNSearchEnabled:                 true,
		MaxRetainedCrawlJobs:             defaultMaxRetainedCrawlJobs,
		DefaultRenderer:                  RendererNone,
		LinkScope:                        LinkScopeDomain,
		MaxDocumentVersions:              defaultMaxDocumentVersions,
		TitleWeight:                      defaultTitleWeight,
		EmbeddingProvider:                EmbeddingProviderHash,
		EmbeddingHTTPDimensions:          defaultEmbeddingHTTPDimensions,
	}
}

// OperationalSettings holds those values, safe for concurrent use: read on
// every fetch/crawl/search/login, written from the admin tuning panel.
type OperationalSettings struct {
	mu sync.RWMutex
	v  OperationalSettingsValues
}

func NewOperationalSettings(v OperationalSettingsValues) *OperationalSettings {
	s := &OperationalSettings{}
	s.Set(v)
	return s
}

// DefaultOperationalSettings constructs settings using the built-in
// defaults, for callers that don't need to override anything at startup.
func DefaultOperationalSettings() *OperationalSettings {
	return NewOperationalSettings(defaultOperationalSettings())
}

// Get returns the current values. A nil *OperationalSettings (e.g. a test
// that doesn't care about these knobs) returns the built-in defaults
// rather than a zero-value struct, so callers never need a separate nil
// check before reading.
func (s *OperationalSettings) Get() OperationalSettingsValues {
	if s == nil {
		return defaultOperationalSettings()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v
}

// Set updates the settings, substituting the default for any field left
// at its zero value rather than rejecting the update -- this is an admin
// convenience knob, not a user-facing form that needs field-level
// validation errors.
func (s *OperationalSettings) Set(v OperationalSettingsValues) {
	if s == nil {
		return
	}
	d := defaultOperationalSettings()
	if v.FetchTimeout <= 0 {
		v.FetchTimeout = d.FetchTimeout
	}
	if v.UserAgent == "" {
		v.UserAgent = d.UserAgent
	}
	if v.DefaultMaxPages <= 0 {
		v.DefaultMaxPages = d.DefaultMaxPages
	}
	if v.MinTextLength < 0 {
		v.MinTextLength = 0
	}
	if v.DefaultTopK <= 0 {
		v.DefaultTopK = d.DefaultTopK
	}
	if v.SessionTTL <= 0 {
		v.SessionTTL = d.SessionTTL
	}
	if v.CrawlDelayMs < 0 {
		v.CrawlDelayMs = 0
	}
	if v.MaxResponseBytes <= 0 {
		v.MaxResponseBytes = d.MaxResponseBytes
	}
	if v.SemanticCandidatePoolSize <= 0 {
		v.SemanticCandidatePoolSize = d.SemanticCandidatePoolSize
	}
	if v.DBMaxOpenConns <= 0 {
		v.DBMaxOpenConns = d.DBMaxOpenConns
	}
	if v.DBMaxIdleConns <= 0 {
		v.DBMaxIdleConns = d.DBMaxIdleConns
	}
	if v.DBConnMaxLifetime <= 0 {
		v.DBConnMaxLifetime = d.DBConnMaxLifetime
	}
	if v.FuzzyMaxEditDistance <= 0 {
		v.FuzzyMaxEditDistance = d.FuzzyMaxEditDistance
	} else if v.FuzzyMaxEditDistance > 2 {
		v.FuzzyMaxEditDistance = 2
	}
	if v.PageRankRecomputeIntervalMinutes <= 0 {
		v.PageRankRecomputeIntervalMinutes = d.PageRankRecomputeIntervalMinutes
	} else if v.PageRankRecomputeIntervalMinutes < minPageRankRecomputeIntervalMinutes {
		v.PageRankRecomputeIntervalMinutes = minPageRankRecomputeIntervalMinutes
	}
	if v.MaxRetainedCrawlJobs <= 0 {
		v.MaxRetainedCrawlJobs = d.MaxRetainedCrawlJobs
	}
	if v.MaxDocumentVersions <= 0 {
		v.MaxDocumentVersions = d.MaxDocumentVersions
	}
	if v.TitleWeight <= 0 {
		v.TitleWeight = d.TitleWeight
	}
	// RendererDefault ("") isn't a valid global default -- there's nothing
	// for the global setting itself to inherit from -- so an empty or
	// unrecognized value falls back to RendererNone, same self-healing
	// convention as every other field above.
	if v.DefaultRenderer == RendererDefault || !ValidRenderer(v.DefaultRenderer) {
		v.DefaultRenderer = RendererNone
	}
	// LinkScopeDefault ("") isn't valid for the global default either --
	// same reasoning as DefaultRenderer above.
	if v.LinkScope == LinkScopeDefault || !ValidLinkScope(v.LinkScope) {
		v.LinkScope = LinkScopeDomain
	}
	if !ValidEmbeddingProvider(v.EmbeddingProvider) {
		v.EmbeddingProvider = EmbeddingProviderHash
	}
	if v.EmbeddingHTTPDimensions <= 0 {
		v.EmbeddingHTTPDimensions = d.EmbeddingHTTPDimensions
	}

	s.mu.Lock()
	// EmbeddingHTTPAPIKey is a secret that's never round-tripped back to
	// the admin UI (see the field's own doc comment and admin.go's
	// toOperationalValues) -- the settings page always resubmits every
	// field on every save, including ones the admin didn't touch, so an
	// empty value here means "the admin didn't type a new one," not "clear
	// the configured key," and is replaced with whatever's already stored
	// rather than wiping it. There is deliberately no way to explicitly
	// clear a configured key back to empty through this API; switching
	// EmbeddingProvider away from EmbeddingProviderHTTP makes it moot.
	if v.EmbeddingHTTPAPIKey == "" {
		v.EmbeddingHTTPAPIKey = s.v.EmbeddingHTTPAPIKey
	}
	defer s.mu.Unlock()
	s.v = v
}
