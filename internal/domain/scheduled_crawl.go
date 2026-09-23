package domain

import (
	"time"
)

// ScheduledCrawl is the one representation of a crawl an admin sets up --
// no separate "just run once" entity. Recurring=false means "run once at
// NextRunAt, then Enabled goes false"; true means "repeat every
// IntervalMinutes forever". LastRunAt is nil until the first run.
type ScheduledCrawl struct {
	ID            string
	SeedURLs      []string
	MaxPages      int
	RespectRobots bool
	UserAgent     string
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
	// LinkScope overrides the Tuning page's global default -- ""
	// (LinkScopeDefault) means use the global default; see
	// ports.CrawlOptions.LinkScope.
	LinkScope string
	// AllowedDomains/BlockedDomains/FollowIndexedDomains mirror
	// ports.CrawlOptions' fields -- see its doc comment for precedence.
	AllowedDomains       []string
	BlockedDomains       []string
	FollowIndexedDomains bool
	UseSitemap           bool
	// FetchTimeoutSeconds, MinTextLength, CrawlDelayMs, MaxResponseKB and
	// PrioritizeUnindexed mirror ports.CrawlOptions' per-crawl overrides; 0
	// means use the Tuning page's configured default.
	FetchTimeoutSeconds int
	MinTextLength       int
	CrawlDelayMs        int
	MaxResponseKB       int
	PrioritizeUnindexed bool
	Recurring           bool
	IntervalMinutes     int
	// MaxRuns caps how many times a recurring schedule repeats before
	// disabling itself. 0 means unlimited. Meaningless for a non-recurring
	// entry, which always stops after run 1.
	MaxRuns  int
	RunCount int
	// Renderer overrides the Tuning page's global rendering mode for this
	// crawl alone -- "" (RendererDefault) means use the global default; see
	// ports.CrawlOptions.Renderer.
	Renderer string
	// Enabled is purely the admin's on/off toggle -- never flipped just
	// because a triggered run hasn't finished (see InProgress).
	Enabled bool
	// InProgress is true from trigger until the run's onDone callback
	// clears it -- prevents double-triggering by the next tick.
	InProgress bool
	// JobID is the CrawlJob this schedule's current run created, "" when
	// InProgress is false. Lets crash-recovery tell a resumed, still-running
	// job apart from a stale InProgress flag -- see
	// ResetStaleInProgress's doc comment for the incident this prevents
	// (two concurrent crawls of the same site).
	JobID     string
	LastRunAt *time.Time
	NextRunAt time.Time
	CreatedAt time.Time
}

var scheduledCrawlSeq int64

// NewScheduledCrawlID mints an ID for a newly created schedule, same
// scheme as newCrawlJobID: unique within a process without needing a
// database round-trip first.
func NewScheduledCrawlID() string {
	return newSeqID("sched", &scheduledCrawlSeq)
}
