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
	// Enabled is purely the admin's own on/off toggle for this schedule --
	// never flipped merely because a triggered run for it hasn't finished
	// yet (see InProgress for that). It only ever changes to reflect a
	// genuine end state: an admin toggling it directly, a one-off entry
	// that just ran once, or a recurring entry that just reached its
	// MaxRuns cap.
	Enabled bool
	// InProgress is true from the moment application.TriggerDueCrawls
	// triggers this entry until that same triggered run actually finishes,
	// at which point its onDone callback clears it back to false. It exists
	// so a schedule that runs longer than its own interval can't be
	// double-triggered by the next scheduler tick, without needing to
	// (mis)use Enabled as that mutex -- which used to make every trigger,
	// including a manual "Run now", visibly uncheck Enabled in the admin UI
	// even though the admin never touched it.
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
