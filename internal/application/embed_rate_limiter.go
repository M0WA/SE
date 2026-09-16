package application

import (
	"context"
	"sync"
	"time"
)

// embedRateLimiter paces calls to a shared ports.EmbeddingProvider so
// their combined rate -- not just one caller's own -- respects a
// configured requests-per-second cap (domain.OperationalSettingsValues.
// EmbeddingRateLimitPerSecond). Safe for concurrent use: sqlCrawlerService
// holds one instance shared across every crawl job running concurrently
// on this process (up to maxConcurrentCrawls at once -- see
// internal/adapters/restapi/crawl_internal.go), so their Embed calls
// interleave at the shared rate instead of each job independently pacing
// itself and letting the combined rate exceed the limit. The zero value
// is ready to use.
type embedRateLimiter struct {
	mu   sync.Mutex
	next time.Time
}

// wait blocks until this call's reserved slot in the timeline arrives, or
// returns early if ctx is cancelled first -- callers don't need to check
// which happened: an early return here just means the actual Embed call
// fails fast against the same cancelled context instead. ratePerSecond <=
// 0 disables pacing entirely -- domain.OperationalSettings.Set never
// actually produces that in production (it self-heals to a positive
// default), but this lets a test opt out of real waits.
//
// Each call atomically reserves the next available slot (now, or right
// after whichever slot was most recently reserved, whichever is later)
// before sleeping outside the lock -- so concurrent callers queue up
// correctly spaced by embedRateLimitInterval regardless of goroutine
// scheduling order, rather than racing to read the same "last call time"
// and under-pacing.
func (r *embedRateLimiter) wait(ctx context.Context, ratePerSecond int) {
	interval := embedRateLimitInterval(ratePerSecond)
	if interval <= 0 {
		return
	}

	r.mu.Lock()
	now := time.Now()
	start := r.next
	if start.Before(now) {
		start = now
	}
	r.next = start.Add(interval)
	r.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}
