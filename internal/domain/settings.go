package domain

import (
	"sync"
	"time"
)

// OperationalSettingsValues is a snapshot of every admin-configurable
// operational knob outside the BM25/semantic ranking formula (see
// TuningSettings): how the crawler fetches pages, what a search returns by
// default, and how long a sign-in session lasts.
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
	// score per search regardless of corpus size -- BM25 hits plus a
	// bounded sample of the rest, so a purely semantic match can still be found.
	SemanticCandidatePoolSize int
	// SemanticRescoreCap bounds how many of a query's BM25 hits also get a
	// semantic similarity score fetched -- the top-scoring N by BM25 alone.
	// Every BM25 hit is still scored and ranked by keyword relevance
	// regardless; this only stops a term matching a large fraction of the
	// corpus from forcing every single hit's embedding to be fetched.
	SemanticRescoreCap int
	// DBMaxOpenConns/DBMaxIdleConns bound the SQL pool; DBConnMaxLifetime
	// caps reuse. SQLite always clamps to 1 open connection regardless.
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	// FuzzyMatchEnabled turns on typo-tolerant query matching: a zero-hit
	// term is looked up against the vocabulary within FuzzyMaxEditDistance
	// edits and substituted for BM25 scoring.
	FuzzyMatchEnabled bool
	// FuzzyMaxEditDistance bounds edits a substituted term may be from the
	// original. Clamped to 1 or 2.
	FuzzyMaxEditDistance int
	// PageRankRecomputeIntervalMinutes is how often cmd/crawl recomputes
	// PageRank, in addition to always doing so once a crawl completes.
	// Clamped to a 5-minute minimum.
	PageRankRecomputeIntervalMinutes int
	// ANNSearchEnabled fills the semantic candidate pool via pgvector's ANN
	// index rather than brute force. Defaults true; no effect where ANN
	// isn't available.
	ANNSearchEnabled bool
	// MaxRetainedCrawlJobs bounds how many crawl jobs (with full page
	// history) crawl-server's store keeps; older ones are pruned periodically.
	MaxRetainedCrawlJobs int
	// MaxConcurrentCrawls bounds how many crawl jobs fetch pages at once; a
	// burst beyond this queues. Live-reloadable for the next job to start.
	MaxConcurrentCrawls int
	// DefaultRenderer is the crawler's global default rendering mode:
	// RendererNone unless an admin turns on real browser rendering. A
	// crawl's own Renderer overrides this when set.
	DefaultRenderer string
	// LinkScope is the crawler's global default link-following scope --
	// defaults to LinkScopeTLD, broader than Domain since most sites span
	// multiple subdomains/TLDs. A crawl's own LinkScope overrides this.
	LinkScope string
	// MaxDocumentVersions bounds how many versions of a document are kept
	// when a re-crawl changes content -- SaveDocument prunes beyond this.
	MaxDocumentVersions int
	// TitleWeight is how many times a title match counts toward term_freq
	// vs. once in the body, tempered by BM25's k1 saturation. Only affects
	// documents crawled/re-crawled afterward.
	TitleWeight int
	// EmbeddingHashEnabled controls whether the built-in hash embedding is
	// computed for every document. Read once at startup (sizes pgvector
	// columns), so enabling it takes effect only after a restart.
	EmbeddingHashEnabled bool
	// EmbeddingSearchWeights maps a provider to its weight in the
	// search-time semantic blend; weights normalize to sum 1.
	// ReconcileSearchWeights self-heals an invalid/disabled entry.
	EmbeddingSearchWeights map[string]float64
	// EmbeddingProvider is deprecated: named the single active provider
	// before EmbeddingSearchWeights generalized to a weighted set. Kept
	// only so an old settings blob still decodes.
	EmbeddingProvider string
	// EmbeddingTitleWeight blends a document's title into its embedding as
	// titleWeight*titleVector + (1-titleWeight)*bodyVector rather than one
	// call against concatenated text. 0 disables it; 1 embeds the title alone.
	EmbeddingTitleWeight float64
	// URLAliasWWWEnabled folds a leading "www." into the bare domain for a
	// crawled URL's canonical identity. Only affects future crawls --
	// content_dedup_job.go reconciles pre-existing duplicates.
	URLAliasWWWEnabled bool
	// ContentDedupEnabled turns on RunContentDedupJob's periodic/on-demand
	// merge of duplicate/near-duplicate content across URLs. Defaults true.
	// Disabling it matters because enabling it can delete document rows.
	ContentDedupEnabled bool
	// ContentDedupMethod is "exact" (byte-identical via ContentHash) or
	// "simhash" (similarity fingerprint tolerant of minor differences, via
	// SimHash64/ContentDedupSimHashMaxDistance). Self-heals to "exact".
	ContentDedupMethod string
	// ContentDedupSimHashMaxDistance is the max Hamming distance (of 64
	// bits) two SimHash64 fingerprints may differ by and still count as
	// near-duplicates. Clamped to [1,10].
	ContentDedupSimHashMaxDistance int
	// ContentDedupIntervalMinutes is how often cmd/crawl runs
	// RunContentDedupJob. Clamped to a 15-minute minimum -- higher than
	// PageRank's floor since a merge is a destructive write.
	ContentDedupIntervalMinutes int
	// EmbeddingRecomputeConcurrency bounds how many documents a corpus
	// embedding recompute (RunEmbeddingRecomputeJob) processes at once --
	// each in-flight document still pays its own real Embed HTTP round
	// trip per enabled provider, so raising this mainly hides network
	// latency rather than adding real load beyond what each endpoint's own
	// RateLimitPerSecond already allows through. Safe to raise freely for
	// an endpoint with a configured limit (httpembed.Embedder throttles to
	// it regardless of how many goroutines are waiting); for an
	// unlimited (0) endpoint, this concurrency IS the only throttle, so
	// raise it carefully there.
	EmbeddingRecomputeConcurrency int
}

// defaultUserAgent mimics a standard desktop Firefox so crawled sites treat
// requests as an ordinary browser visit -- overridable via the tuning page
// or per crawl via CrawlOptions.UserAgent.
const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0"

// defaultCrawlDelayMs and defaultMaxResponseBytes are the built-in
// politeness/safety defaults.
const (
	defaultCrawlDelayMs     = 250
	defaultMaxResponseBytes = 5 * 1024 * 1024
	// defaultSemanticCandidatePoolSize keeps semantic scoring/ranking
	// working over at most a few hundred candidates regardless of corpus size.
	defaultSemanticCandidatePoolSize = 200
	// defaultSemanticRescoreCap bounds how many BM25 hits get a semantic
	// score fetched per search, regardless of how many documents a query
	// term matches.
	defaultSemanticRescoreCap = 500
	// defaultDBMaxOpenConns/defaultDBMaxIdleConns/defaultDBConnMaxLifetime
	// are sane defaults for a real pool; SQLite clamps to a single
	// connection regardless (file-level write serialization).
	defaultDBMaxOpenConns    = 25
	defaultDBMaxIdleConns    = 25
	defaultDBConnMaxLifetime = 5 * time.Minute
	// defaultFuzzyMaxEditDistance allows up to a 2-edit typo to still
	// resolve to the intended vocabulary term.
	defaultFuzzyMaxEditDistance = 2
	// defaultPageRankRecomputeIntervalMinutes/
	// minPageRankRecomputeIntervalMinutes: hourly by default, never more
	// often than every 5 minutes.
	defaultPageRankRecomputeIntervalMinutes = 60
	minPageRankRecomputeIntervalMinutes     = 5
	// defaultContentDedupSimHashMaxDistance is a moderately conservative
	// threshold: catches real near-duplicates without merging documents
	// that only share some vocabulary.
	defaultContentDedupSimHashMaxDistance = 3
	minContentDedupSimHashMaxDistance     = 1
	maxContentDedupSimHashMaxDistance     = 10
	// defaultContentDedupIntervalMinutes/minContentDedupIntervalMinutes:
	// every two hours by default, never more often than every 15 minutes --
	// higher floor than PageRank's since a merge is destructive.
	defaultContentDedupIntervalMinutes = 120
	minContentDedupIntervalMinutes     = 15
	// defaultMaxRetainedCrawlJobs matches the old in-memory store's
	// hard-coded cap, now just a starting point since persistent storage
	// can hold far more if an admin raises it.
	defaultMaxRetainedCrawlJobs = 200
	// defaultMaxConcurrentCrawls matches this codebase's previous
	// hard-coded constant, now a starting point rather than a hard limit.
	defaultMaxConcurrentCrawls = 3
	// defaultMaxDocumentVersions keeps a handful of prior versions around
	// without letting document_versions grow unbounded for a page
	// re-crawled often.
	defaultMaxDocumentVersions = 3
	// defaultTitleWeight is a modest edge (roughly double term frequency
	// from a single mention), not an aggressive one.
	defaultTitleWeight = 2
	// defaultEmbeddingTitleWeight is similarly modest: title carries real
	// topical signal, but the semantic vector should still be driven
	// mostly by body content.
	defaultEmbeddingTitleWeight = 0.3
	// defaultEmbeddingRecomputeConcurrency is conservative on purpose: an
	// admin-configured HTTP embedding endpoint with no RateLimitPerSecond
	// set has no OTHER throttle at all, so a too-high default here could
	// genuinely overwhelm a single shared inference GPU the moment a
	// recompute starts.
	defaultEmbeddingRecomputeConcurrency = 4
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
		SemanticRescoreCap:               defaultSemanticRescoreCap,
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
		EmbeddingRecomputeConcurrency:    defaultEmbeddingRecomputeConcurrency,
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

// Get returns the current values. A nil *OperationalSettings returns the
// built-in defaults rather than a zero-value struct, so callers never need
// a separate nil check.
func (s *OperationalSettings) Get() OperationalSettingsValues {
	if s == nil {
		return defaultOperationalSettings()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v
}

// Set updates the settings, substituting the default for any field left at
// its zero value rather than rejecting the update -- an admin convenience
// knob, not a validated form.
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
	if v.SemanticRescoreCap <= 0 {
		v.SemanticRescoreCap = d.SemanticRescoreCap
	}
	if v.EmbeddingRecomputeConcurrency <= 0 {
		v.EmbeddingRecomputeConcurrency = d.EmbeddingRecomputeConcurrency
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
	// RendererDefault ("") isn't valid for the global setting itself, so it
	// self-heals to RendererNone, same as every field above.
	if v.DefaultRenderer == RendererDefault || !ValidRenderer(v.DefaultRenderer) {
		v.DefaultRenderer = RendererNone
	}
	// LinkScopeDefault ("") isn't valid for the global default either.
	if v.LinkScope == LinkScopeDefault || !ValidLinkScope(v.LinkScope) {
		v.LinkScope = LinkScopeTLD
	}
	// EmbeddingProvider's validity depends on the dynamic endpoints table,
	// so it's left as-is -- see ReconcileSearchWeights. EmbeddingTitleWeight's
	// 0 is meaningful, so it's clamped, not substituted.
	v.EmbeddingTitleWeight = clamp(v.EmbeddingTitleWeight, 0, 1)
	// An unrecognized ContentDedupMethod self-heals to "exact".
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
