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
	// SemanticCandidatePoolSize bounds how many documents get a semantic
	// score per search, regardless of corpus size -- the union of every
	// BM25 hit plus a bounded sample of the rest, so a purely semantic
	// match can still be found without scoring the whole corpus.
	SemanticCandidatePoolSize int
	// DBMaxOpenConns/DBMaxIdleConns bound the SQL connection pool;
	// DBConnMaxLifetime caps how long a pooled connection is reused.
	// SQLite always clamps to 1 open connection regardless (single-writer
	// locking), and an admin edit re-applies live via bootstrap.SyncSettings.
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	// FuzzyMatchEnabled turns on typo-tolerant query matching: a term
	// with zero postings hits is looked up against the vocabulary for a
	// near-miss within FuzzyMaxEditDistance edits and substituted into
	// BM25 scoring for that term alone. False disables the feature
	// entirely.
	FuzzyMatchEnabled bool
	// FuzzyMaxEditDistance bounds how many edits (insertions, deletions,
	// substitutions) a substituted vocabulary term may be from the
	// original query term. Clamped to 1 or 2: 1 catches a single
	// typo/transposition-as-two-edits case conservatively, 2 is more
	// forgiving but risks matching an unrelated short word.
	FuzzyMaxEditDistance int
	// PageRankRecomputeIntervalMinutes is how often cmd/crawl recomputes
	// PageRank from the current link graph, in addition to always
	// recomputing once a crawl job completes. Clamped to a 5-minute
	// minimum so an aggressive setting can't spin the recompute in a loop.
	PageRankRecomputeIntervalMinutes int
	// ANNSearchEnabled controls whether the semantic candidate pool is
	// filled via Postgres pgvector's ANN index rather than a brute-force
	// sample. Defaults true; has no effect where ANN isn't available
	// (non-Postgres, or the extension/index missing). Set false to force
	// the brute-force path, e.g. to troubleshoot a ranking difference.
	ANNSearchEnabled bool
	// MaxRetainedCrawlJobs bounds how many crawl jobs (with full page
	// history) crawl-server's persistent store keeps; the oldest beyond
	// this are pruned periodically. Admin-configurable now that history
	// is DB-backed, not just memory-bounded.
	MaxRetainedCrawlJobs int
	// MaxConcurrentCrawls bounds how many crawl jobs fetch pages at once;
	// a burst beyond this queues rather than opening unbounded
	// connections. Live-reloadable: takes effect for the next job to
	// start, though an already-running job keeps its slot until done.
	MaxConcurrentCrawls int
	// DefaultRenderer is the crawler's global default page-rendering mode
	// (see Renderer* constants): RendererNone (plain HTTP fetch) unless an
	// admin turns on real browser rendering. A crawl's own Renderer
	// (ScheduledCrawl.Renderer / ports.CrawlOptions.Renderer) overrides
	// this when set to anything other than RendererDefault ("").
	DefaultRenderer string
	// LinkScope is the crawler's global default link-following scope (see
	// LinkScope* constants) -- defaults to LinkScopeTLD, broader than
	// LinkScopeDomain since most sites span multiple subdomains/TLDs. A
	// crawl's own LinkScope overrides this when set.
	LinkScope string
	// MaxDocumentVersions bounds how many versions of a document (current
	// plus archived predecessors) are kept when a re-crawl changes its
	// content -- SaveDocument prunes beyond this in the same write. An
	// unchanged document always has just one version regardless.
	MaxDocumentVersions int
	// TitleWeight is how many times a title match counts toward term_freq,
	// vs. once for the same word in the body (postings has no separate
	// per-field count) -- tempered by BM25's own k1 saturation. Only
	// takes effect for documents crawled/re-crawled afterward.
	TitleWeight int
	// EmbeddingHashEnabled controls whether the built-in dependency-free
	// hash embedding is computed for every document, independent of any
	// configured EmbeddingHTTPEndpoint rows. Unlike EmbeddingSearchWeights,
	// this is read once at startup (sizes pgvector columns via EnableANN)
	// -- enabling it only takes effect after a restart.
	EmbeddingHashEnabled bool
	// EmbeddingSearchWeights maps a provider (EmbeddingProviderHash or an
	// EmbeddingHTTPEndpoint.ID) to its weight in the search-time semantic
	// blend; weights normalize to sum 1, so only relative magnitude
	// matters. domain.ReconcileSearchWeights self-heals an invalid or
	// disabled entry, since validity depends on the dynamic endpoints table.
	EmbeddingSearchWeights map[string]float64
	// EmbeddingProvider is deprecated: it named the single active provider
	// before EmbeddingSearchWeights generalized to a weighted set. Kept
	// only so an old settings blob still decodes; bootstrap.applySettingsOnce
	// seeds EmbeddingSearchWeights from it once. No longer settable via
	// the admin API.
	EmbeddingProvider string
	// EmbeddingTitleWeight blends a document's title into its embedding as
	// titleWeight*titleVector + (1-titleWeight)*bodyVector, rather than one
	// call against concatenated text (whose title signal most poolers
	// dilute away). 0 disables it (body-only); 1 embeds the title alone,
	// doubling Embed call volume against any enabled HTTP endpoint.
	EmbeddingTitleWeight float64
	// URLAliasWWWEnabled folds a leading "www." into the bare domain when
	// computing a crawled URL's canonical identity, so www.example.com/x
	// and example.com/x resolve to the same document. Only affects future
	// crawls -- content_dedup_job.go's batch merge reconciles pre-existing
	// duplicates. Defaults true (lossless for most sites).
	URLAliasWWWEnabled bool
	// ContentDedupEnabled turns on RunContentDedupJob's periodic/on-demand
	// batch pass, which merges documents with duplicate/near-duplicate
	// content across different URLs (see MergeDocuments). Defaults true.
	// Unlike other fields here, disabling it matters because enabling it
	// can delete existing document rows (the merged-away losers).
	ContentDedupEnabled bool
	// ContentDedupMethod is "exact" (byte-identical normalized text, via
	// domain.ContentHash) or "simhash" (a similarity fingerprint tolerant
	// of minor differences -- tracking params, a different ad slot -- via
	// domain.SimHash64/ContentDedupSimHashMaxDistance below). Self-heals to
	// "exact" on an unrecognized value the same way DefaultRenderer/LinkScope
	// do for their own enums.
	ContentDedupMethod string
	// ContentDedupSimHashMaxDistance is the max Hamming distance (of 64
	// bits) two SimHash64 fingerprints may differ by and still count as
	// near-duplicates, when ContentDedupMethod is "simhash". Clamped to
	// [1,10].
	ContentDedupSimHashMaxDistance int
	// ContentDedupIntervalMinutes is how often cmd/crawl runs
	// RunContentDedupJob, mirroring PageRankRecomputeIntervalMinutes'
	// ticker. Clamped to a 15-minute minimum -- higher than PageRank's
	// floor since a merge is a destructive write.
	ContentDedupIntervalMinutes int
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
	// are sane defaults for a real pool (Postgres/MySQL); SQLite clamps to
	// a single connection regardless, since it serializes writers at the
	// file level and a larger pool there just adds lock contention.
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
	// defaultContentDedupSimHashMaxDistance is a moderately conservative
	// starting threshold -- close enough to catch real near-duplicates
	// (a changed timestamp/ad slot) without merging documents that only
	// happen to share some vocabulary.
	defaultContentDedupSimHashMaxDistance = 3
	minContentDedupSimHashMaxDistance     = 1
	maxContentDedupSimHashMaxDistance     = 10
	// defaultContentDedupIntervalMinutes/minContentDedupIntervalMinutes
	// bound how often the batch dedup pass runs: every two hours by
	// default, never more often than every 15 minutes -- see
	// OperationalSettingsValues.ContentDedupIntervalMinutes for why this
	// floor is higher than PageRank's.
	defaultContentDedupIntervalMinutes = 120
	minContentDedupIntervalMinutes     = 15
	// defaultMaxRetainedCrawlJobs matches the old in-memory store's
	// hard-coded cap, kept as the default now that it's just a starting
	// point rather than a hard limit -- persistent storage can comfortably
	// hold far more history if an admin raises it.
	defaultMaxRetainedCrawlJobs = 200
	// defaultMaxConcurrentCrawls matches this codebase's own previous
	// hard-coded constant of the same name, kept as the starting point
	// now that it's admin-adjustable rather than a hard limit.
	defaultMaxConcurrentCrawls = 3
	// defaultMaxDocumentVersions keeps a handful of prior versions around
	// for a changing page without letting document_versions grow
	// unbounded for a page that's re-crawled often.
	defaultMaxDocumentVersions = 3
	// defaultTitleWeight is a modest edge (title terms end up with roughly
	// double the term frequency they'd get from a single mention, on top
	// of however many times they separately occur in the body), not an
	// aggressive one.
	defaultTitleWeight = 2
	// defaultEmbeddingTitleWeight is a modest edge, matching
	// defaultTitleWeight's own "modest, not aggressive" philosophy --
	// title carries real topical signal, but a document's semantic vector
	// should still be driven mostly by its actual body content.
	defaultEmbeddingTitleWeight = 0.3
)

func defaultOperationalSettings() OperationalSettingsValues {
	return OperationalSettingsValues{
		FetchTimeout:                     8 * time.Second,
		UserAgent:                        defaultUserAgent,
		DefaultMaxPages:                  20000,
		MinTextLength:                    50,
		DefaultTopK:                      5000,
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
		MaxConcurrentCrawls:              defaultMaxConcurrentCrawls,
		DefaultRenderer:                  RendererNone,
		LinkScope:                        LinkScopeTLD,
		MaxDocumentVersions:              defaultMaxDocumentVersions,
		TitleWeight:                      defaultTitleWeight,
		EmbeddingHashEnabled:             true,
		EmbeddingSearchWeights:           map[string]float64{EmbeddingProviderHash: 1},
		EmbeddingTitleWeight:             defaultEmbeddingTitleWeight,
		URLAliasWWWEnabled:               true,
		ContentDedupEnabled:              true,
		ContentDedupMethod:               ContentDedupMethodExact,
		ContentDedupSimHashMaxDistance:   defaultContentDedupSimHashMaxDistance,
		ContentDedupIntervalMinutes:      defaultContentDedupIntervalMinutes,
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
	if v.MaxConcurrentCrawls <= 0 {
		v.MaxConcurrentCrawls = d.MaxConcurrentCrawls
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
		v.LinkScope = LinkScopeTLD
	}
	// EmbeddingProvider's validity depends on the dynamic
	// embedding_http_endpoints table, so it's left as-is here -- see
	// domain.ReconcileActiveProvider, applied by callers with both this
	// and the live endpoint list. EmbeddingTitleWeight's 0 is meaningful
	// (disables title blending), so it's clamped below, not substituted.
	v.EmbeddingTitleWeight = clamp(v.EmbeddingTitleWeight, 0, 1)
	// An unrecognized ContentDedupMethod self-heals to the always-valid
	// "exact" method, same convention as DefaultRenderer/LinkScope above.
	if v.ContentDedupMethod != ContentDedupMethodExact && v.ContentDedupMethod != ContentDedupMethodSimHash {
		v.ContentDedupMethod = ContentDedupMethodExact
	}
	if v.ContentDedupSimHashMaxDistance <= 0 {
		v.ContentDedupSimHashMaxDistance = d.ContentDedupSimHashMaxDistance
	} else if v.ContentDedupSimHashMaxDistance < minContentDedupSimHashMaxDistance {
		v.ContentDedupSimHashMaxDistance = minContentDedupSimHashMaxDistance
	} else if v.ContentDedupSimHashMaxDistance > maxContentDedupSimHashMaxDistance {
		v.ContentDedupSimHashMaxDistance = maxContentDedupSimHashMaxDistance
	}
	if v.ContentDedupIntervalMinutes <= 0 {
		v.ContentDedupIntervalMinutes = d.ContentDedupIntervalMinutes
	} else if v.ContentDedupIntervalMinutes < minContentDedupIntervalMinutes {
		v.ContentDedupIntervalMinutes = minContentDedupIntervalMinutes
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = v
}
