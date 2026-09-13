package domain

import (
	"fmt"
	"sync/atomic"
	"time"
)

// ScheduledCrawl is the one representation of a crawl an admin sets up --
// there's no separate "just run this once" entity. Recurring distinguishes
// the two shapes: false means "run once, at NextRunAt, then Enabled goes
// false" (a plain one-off crawl); true means "keep running every
// IntervalMinutes forever" (a genuinely recurring schedule). It carries
// every option a crawl can have, credentials included -- Cookie/
// BasicAuthUser/BasicAuthPass are parameters to the crawl like any other,
// stored and reused on every run the same way UserAgent is. LastRunAt is
// nil until the first (and for a non-recurring entry, only) run.
type ScheduledCrawl struct {
	ID                  string
	SeedURLs            []string
	MaxPages            int
	RespectRobots       bool
	UserAgent           string
	Cookie              string
	BasicAuthUser       string
	BasicAuthPass       string
	AllowOffDomainLinks bool
	UseSitemap          bool
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
	MaxRuns   int
	RunCount  int
	Enabled   bool
	LastRunAt *time.Time
	NextRunAt time.Time
	CreatedAt time.Time
}

var scheduledCrawlSeq int64

// NewScheduledCrawlID mints an ID for a newly created schedule, same
// scheme as newCrawlJobID: unique within a process without needing a
// database round-trip first.
func NewScheduledCrawlID() string {
	return fmt.Sprintf("sched-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&scheduledCrawlSeq, 1))
}
