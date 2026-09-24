package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
)

// pageRankEnabledSettings builds the minimal *domain.OperationalSettings
// pageRankRecomputer.recompute now reads on every call (PageRankEnabled) --
// every existing test below wants it enabled, so this is the shared
// default; TestRecompute_DisabledSkipsEntirely below builds its own with
// it false instead.
func pageRankEnabledSettings() *domain.OperationalSettings {
	return domain.NewOperationalSettings(domain.OperationalSettingsValues{PageRankEnabled: true})
}

// fakePageRankRepo is a minimal ports.PageRankRepository whose LinkGraph
// call can be paused mid-flight: the first call blocks on release (once
// armed) after signaling entered, so a test can deterministically observe
// "recompute() called while a run is in flight" without a real race.
type fakePageRankRepo struct {
	calls   int
	entered chan struct{}
	release chan struct{}
	// errFromCall, when > 0, makes LinkGraph return an error starting from
	// that 1-indexed call number onward (0 disables this -- every call
	// succeeds), so a test can force either the first pass or a later
	// coalesced pass to fail without a real repository.
	errFromCall int
}

func (f *fakePageRankRepo) LinkGraph(ctx context.Context) (map[string][]string, error) {
	f.calls++
	n := f.calls
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if n == 1 && f.release != nil {
		<-f.release
	}
	if f.errFromCall > 0 && n >= f.errFromCall {
		return nil, errors.New("boom")
	}
	return map[string][]string{"a": {"b"}}, nil
}

func (f *fakePageRankRepo) UpdatePageRanks(ctx context.Context, scores map[string]float64) error {
	return nil
}

func (f *fakePageRankRepo) ResolvePendingLinks(ctx context.Context) (int, error) {
	return 0, nil
}

// TestRecomputeNormalRun covers the not-already-running case: a single
// call runs through once and leaves running/pending clear.
func TestRecomputeNormalRun(t *testing.T) {
	repo := &fakePageRankRepo{}
	p := &pageRankRecomputer{repo: repo, opSettings: pageRankEnabledSettings()}

	p.recompute(context.Background())

	if repo.calls != 1 {
		t.Fatalf("LinkGraph calls = %d, want 1", repo.calls)
	}
	if p.running {
		t.Fatal("running should be false after recompute returns")
	}
	if p.pending {
		t.Fatal("pending should be false after a normal run")
	}
	if p.lastRun().IsZero() {
		t.Fatal("last should be set after a normal run")
	}
}

// TestRecomputeCoalescesWhileRunning covers the coalescing gap: a
// recompute() call that arrives while one is already in flight must not
// block, and must not be dropped -- it should cause exactly one extra
// pass once the in-flight run finishes, not zero and not more than one.
func TestRecomputeCoalescesWhileRunning(t *testing.T) {
	repo := &fakePageRankRepo{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	p := &pageRankRecomputer{repo: repo, opSettings: pageRankEnabledSettings()}

	done := make(chan struct{})
	go func() {
		p.recompute(context.Background())
		close(done)
	}()

	// Wait for the first run to actually be in flight (blocked inside
	// LinkGraph) before racing a second call against it.
	select {
	case <-repo.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first recompute() never entered LinkGraph")
	}

	// A second call while running must set pending and return immediately
	// rather than blocking for the in-flight run to finish.
	start := time.Now()
	p.recompute(context.Background())
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("recompute() while running blocked for %v, want immediate return", elapsed)
	}

	p.mu.Lock()
	pending, running := p.pending, p.running
	p.mu.Unlock()
	if !pending {
		t.Fatal("pending should be true after a call arrives while running")
	}
	if !running {
		t.Fatal("running should still be true -- the in-flight run hasn't finished")
	}

	// Let the first (in-flight) run finish. Since pending is set, the same
	// goroutine must run one more pass before releasing running.
	close(repo.release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recompute() never returned after release")
	}

	if repo.calls != 2 {
		t.Fatalf("LinkGraph calls = %d, want exactly 2 (one normal pass + one coalesced pass)", repo.calls)
	}
	p.mu.Lock()
	pending, running = p.pending, p.running
	p.mu.Unlock()
	if pending {
		t.Fatal("pending should be cleared once the follow-up pass has run")
	}
	if running {
		t.Fatal("running should be cleared once the follow-up pass has run")
	}
	if p.lastRun().IsZero() {
		t.Fatal("last should be set after the follow-up pass")
	}

	// A subsequent call should behave like an ordinary, uncoalesced run.
	p.recompute(context.Background())
	if repo.calls != 3 {
		t.Fatalf("LinkGraph calls = %d, want 3 after a normal follow-on call", repo.calls)
	}
}

// TestRecomputeLogsAndContinuesOnError proves a RunPageRankJobWithStatus
// error from the (only, uncoalesced) pass is logged rather than panicking,
// and running/pending/last are still cleared/set exactly as a successful
// run would leave them -- a failed recompute must never leave the
// recomputer stuck "running" forever.
func TestRecomputeLogsAndContinuesOnError(t *testing.T) {
	repo := &fakePageRankRepo{errFromCall: 1}
	p := &pageRankRecomputer{repo: repo, opSettings: pageRankEnabledSettings()}

	p.recompute(context.Background())

	if repo.calls != 1 {
		t.Fatalf("LinkGraph calls = %d, want 1", repo.calls)
	}
	if p.running {
		t.Fatal("running should be false after recompute returns, even on error")
	}
	if p.pending {
		t.Fatal("pending should be false after a normal (errored) run")
	}
	if p.lastRun().IsZero() {
		t.Fatal("last should still be set even when the run errored")
	}
}

// TestRecomputeCoalescedPassLogsError mirrors
// TestRecomputeCoalescesWhileRunning, but the coalesced (second) pass is
// the one that errors -- proving that error is logged too, not just the
// first pass's, and running/pending still end up cleared.
func TestRecomputeCoalescedPassLogsError(t *testing.T) {
	repo := &fakePageRankRepo{
		entered:     make(chan struct{}, 2),
		release:     make(chan struct{}),
		errFromCall: 2,
	}
	p := &pageRankRecomputer{repo: repo, opSettings: pageRankEnabledSettings()}

	done := make(chan struct{})
	go func() {
		p.recompute(context.Background())
		close(done)
	}()

	select {
	case <-repo.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first recompute() never entered LinkGraph")
	}

	// A second call while running must set pending so the in-flight call
	// runs one extra (here, erroring) pass for it.
	p.recompute(context.Background())

	close(repo.release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recompute() never returned after release")
	}

	if repo.calls != 2 {
		t.Fatalf("LinkGraph calls = %d, want exactly 2 (one normal pass + one erroring coalesced pass)", repo.calls)
	}
	p.mu.Lock()
	pending, running := p.pending, p.running
	p.mu.Unlock()
	if pending {
		t.Fatal("pending should be cleared even though the coalesced pass errored")
	}
	if running {
		t.Fatal("running should be cleared even though the coalesced pass errored")
	}
	if p.lastRun().IsZero() {
		t.Fatal("last should still be set even when the coalesced pass errored")
	}
}

// TestRecompute_DisabledSkipsEntirely proves PageRankEnabled=false makes
// recompute() a no-op -- no LinkGraph call, no running/pending/last state
// touched at all -- so every caller (the scheduler's initial run, its
// ticker, and the post-crawl trigger in main()) is safe to call
// unconditionally regardless of this setting, mirroring
// contentDedupRecomputer.recompute's identical gate.
func TestRecompute_DisabledSkipsEntirely(t *testing.T) {
	repo := &fakePageRankRepo{}
	p := &pageRankRecomputer{repo: repo, opSettings: domain.NewOperationalSettings(domain.OperationalSettingsValues{PageRankEnabled: false})}

	p.recompute(context.Background())

	if repo.calls != 0 {
		t.Fatalf("LinkGraph calls = %d, want 0 -- a disabled recomputer must never touch the repository", repo.calls)
	}
	if p.running {
		t.Fatal("running should stay false when disabled")
	}
	if !p.lastRun().IsZero() {
		t.Fatal("last should stay zero when disabled -- nothing actually ran")
	}
}
