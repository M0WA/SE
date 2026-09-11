package domain

import (
	"fmt"
	"sync/atomic"
	"time"
)

// ScheduledCrawl is an admin-configured crawl that repeats automatically on
// an interval, instead of being triggered manually once. It deliberately
// carries no Cookie/BasicAuth credentials -- those stay a one-off,
// per-triggered-crawl-only feature, since storing them at rest indefinitely
// for a recurring job is a real security concern this feature shouldn't
// introduce. LastRunAt is nil until the schedule's first run.
type ScheduledCrawl struct {
	ID                  string
	SeedURLs            []string
	MaxPages            int
	RespectRobots       bool
	UserAgent           string
	AllowOffDomainLinks bool
	UseSitemap          bool
	IntervalMinutes     int
	Enabled             bool
	LastRunAt           *time.Time
	NextRunAt           time.Time
	CreatedAt           time.Time
}

var scheduledCrawlSeq int64

// NewScheduledCrawlID mints an ID for a newly created schedule, same
// scheme as newCrawlJobID: unique within a process without needing a
// database round-trip first.
func NewScheduledCrawlID() string {
	return fmt.Sprintf("sched-%d-%d", time.Now().UnixNano(), atomic.AddInt64(&scheduledCrawlSeq, 1))
}
