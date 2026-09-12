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
)

func defaultOperationalSettings() OperationalSettingsValues {
	return OperationalSettingsValues{
		FetchTimeout:              8 * time.Second,
		UserAgent:                 defaultUserAgent,
		DefaultMaxPages:           20,
		MinTextLength:             50,
		DefaultTopK:               10,
		SessionTTL:                12 * time.Hour,
		CrawlDelayMs:              defaultCrawlDelayMs,
		MaxResponseBytes:          defaultMaxResponseBytes,
		SemanticCandidatePoolSize: defaultSemanticCandidatePoolSize,
		DBMaxOpenConns:            defaultDBMaxOpenConns,
		DBMaxIdleConns:            defaultDBMaxIdleConns,
		DBConnMaxLifetime:         defaultDBConnMaxLifetime,
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

	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = v
}
