package bootstrap

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestPollRefresh_RunsImmediatelyThenOnEveryTick proves both halves of
// pollRefresh's contract: refresh runs synchronously once before it
// returns (callers rely on this for their own "seed it now" guarantee),
// and keeps running on the given interval afterward, in the background.
func TestPollRefresh_RunsImmediatelyThenOnEveryTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int64
	pollRefresh(ctx, 10*time.Millisecond, func() { atomic.AddInt64(&calls, 1) })

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("expected exactly 1 synchronous call before pollRefresh returns, got %d", got)
	}

	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt64(&calls) < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&calls); got < 3 {
		t.Errorf("expected the background ticker to keep calling refresh, got only %d calls", got)
	}
}

// TestPollRefresh_StopsAfterContextCancellation proves the background
// goroutine actually exits once ctx is done, rather than continuing to
// call refresh (and leaking the goroutine) forever.
func TestPollRefresh_StopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls int64
	pollRefresh(ctx, 5*time.Millisecond, func() { atomic.AddInt64(&calls, 1) })
	cancel()

	// Give any in-flight tick a moment to land, then snapshot the count --
	// it must never advance again after this point.
	time.Sleep(20 * time.Millisecond)
	after := atomic.LoadInt64(&calls)

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&calls); got != after {
		t.Errorf("expected no further calls after ctx cancellation, went from %d to %d", after, got)
	}
}
