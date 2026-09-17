package domain

import (
	"fmt"
	"sync/atomic"
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
	// LinkScope overrides the Tuning page's global default for how far
	// this crawl follows discovered links -- "" (domain.LinkScopeDefault)
	// means "use the global default"; LinkScopeHost/LinkScopeDomain/
	// LinkScopeAny choose explicitly. See ports.CrawlOptions.LinkScope
	// (the same field, carried through by application.scheduledCrawlOptions).
	LinkScope string
	// AllowedDomains/BlockedDomains/FollowIndexedDomains mirror
	// ports.CrawlOptions' fields of the same name -- see their doc comments
	// there for the exact allow/block precedence.
	AllowedDomains       []string
	BlockedDomains       []string
	FollowIndexedDomains bool
	UseSitemap           bool
	// FetchTimeoutSeconds, MinTextLength, CrawlDelayMs, MaxResponseKB and
	// PrioritizeUnindexed mirror ports.CrawlOptions' per-crawl overrides --
	// a scheduled crawl accepts every option a one-off crawl does (0 means
	// "use whatever's configured on the Tuning page," same as a one-off
	// crawl leaving these blank).
	FetchTimeoutSeconds int
	MinTextLength       int
	CrawlDelayMs        int
	MaxResponseKB       int
	PrioritizeUnindexed bool
	Recurring           bool
	IntervalMinutes     int
	// MaxRuns caps how many times a recurring schedule repeats before
	// disabling itself, same as a non-recurring entry already disables
	// after its one run -- 0 means unlimited (repeats forever until an
	// admin disables or deletes it). Meaningless for a non-recurring entry,
	// which already stops after run 1 regardless of this value.
	MaxRuns  int
	RunCount int
	// Renderer overrides the Tuning page's global default rendering mode
	// for this crawl alone -- "" (domain.RendererDefault) means "use the
	// global default"; RendererNone/RendererChromium/RendererFirefox
	// choose explicitly. See ports.CrawlOptions.Renderer (the same field,
	// carried through by application.scheduledCrawlOptions).
	Renderer string
	// Enabled is purely the admin's on/off toggle -- never flipped just
	// because a triggered run hasn't finished (see InProgress); only
	// changes on a genuine end state (toggle, one-off ran, MaxRuns reached).
	Enabled bool
	// InProgress is true from trigger until the run's onDone callback
	// clears it -- prevents a slow run from being double-triggered by the
	// next tick, without (mis)using Enabled as that mutex.
	InProgress bool
	LastRunAt  *time.Time
	NextRunAt  time.Time
	CreatedAt  time.Time
}

var scheduledCrawlSeq int64

// NewScheduledCrawlID mints an ID for a newly created schedule, same
// scheme as newCrawlJobID: unique within a process without needing a
// database round-trip first.
func NewScheduledCrawlID() string {
	return fmt.Sprintf("sched-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&scheduledCrawlSeq, 1))
}
