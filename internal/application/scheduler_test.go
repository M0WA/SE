package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeScheduledCrawlStore is an in-memory ports.ScheduledCrawlStore --
// enough state to exercise TriggerDueCrawls' due/not-due filtering and its
// last_run_at/next_run_at bookkeeping without a real database.
type fakeScheduledCrawlStore struct {
	schedules map[string]domain.ScheduledCrawl
	dueErr    error
	markErr   error
	// markCalls counts MarkScheduledCrawlRun calls so far; when
	// markErrOnCall is positive, only that specific call (1-indexed) fails
	// -- lets a test target the onDone completion-time call specifically,
	// without also failing the trigger-time placeholder call.
	markCalls     int
	markErrOnCall int
}

func newFakeScheduledCrawlStore(schedules ...domain.ScheduledCrawl) *fakeScheduledCrawlStore {
	m := make(map[string]domain.ScheduledCrawl, len(schedules))
	for _, s := range schedules {
		m[s.ID] = s
	}
	return &fakeScheduledCrawlStore{schedules: m}
}

func (f *fakeScheduledCrawlStore) CreateScheduledCrawl(context.Context, domain.ScheduledCrawl) error {
	return errors.New("not implemented")
}
func (f *fakeScheduledCrawlStore) ListScheduledCrawls(context.Context) ([]domain.ScheduledCrawl, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeScheduledCrawlStore) UpdateScheduledCrawl(context.Context, domain.ScheduledCrawl) error {
	return errors.New("not implemented")
}
func (f *fakeScheduledCrawlStore) DeleteScheduledCrawl(context.Context, string) error {
	return errors.New("not implemented")
}

func (f *fakeScheduledCrawlStore) DueScheduledCrawls(_ context.Context, now time.Time) ([]domain.ScheduledCrawl, error) {
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	var due []domain.ScheduledCrawl
	for _, s := range f.schedules {
		if s.Enabled && !s.NextRunAt.After(now) {
			due = append(due, s)
		}
	}
	return due, nil
}

func (f *fakeScheduledCrawlStore) MarkScheduledCrawlRun(_ context.Context, id string, lastRunAt, nextRunAt time.Time, enabled bool) error {
	f.markCalls++
	if f.markErr != nil {
		return f.markErr
	}
	if f.markErrOnCall != 0 && f.markCalls == f.markErrOnCall {
		return errors.New("mark failed on the targeted call")
	}
	s, ok := f.schedules[id]
	if !ok {
		return ports.ErrScheduledCrawlNotFound
	}
	s.LastRunAt = &lastRunAt
	s.NextRunAt = nextRunAt
	s.Enabled = enabled
	f.schedules[id] = s
	return nil
}

var _ ports.ScheduledCrawlStore = (*fakeScheduledCrawlStore)(nil)

func TestTriggerDueCrawls_TriggersOnlyDueEnabledSchedules(t *testing.T) {
	now := time.Now().UTC()
	due := domain.ScheduledCrawl{ID: "due", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	notYetDue := domain.ScheduledCrawl{ID: "not-yet-due", SeedURLs: []string{"http://b"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(time.Hour)}
	disabled := domain.ScheduledCrawl{ID: "disabled", SeedURLs: []string{"http://c"}, IntervalMinutes: 30, Enabled: false, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(due, notYetDue, disabled)

	var triggeredIDs []string
	trigger := func(_ context.Context, opts ports.CrawlOptions, _ func()) (string, error) {
		triggeredIDs = append(triggeredIDs, opts.SeedURLs[0])
		return "job-1", nil
	}

	n, err := application.TriggerDueCrawls(context.Background(), store, trigger, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 schedule triggered, got %d", n)
	}
	if len(triggeredIDs) != 1 || triggeredIDs[0] != "http://a" {
		t.Errorf("expected only the due schedule's seed to be triggered, got %v", triggeredIDs)
	}
}

func TestTriggerDueCrawls_AdvancesNextRunAtByInterval(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 45, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)

	trigger := func(context.Context, ports.CrawlOptions, func()) (string, error) { return "job-1", nil }
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := store.schedules["sched-1"]
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Errorf("expected LastRunAt to be set to now, got %v", updated.LastRunAt)
	}
	wantNext := now.Add(45 * time.Minute)
	if !updated.NextRunAt.Equal(wantNext) {
		t.Errorf("expected NextRunAt %v, got %v", wantNext, updated.NextRunAt)
	}
}

// TestTriggerDueCrawls_OnDoneCorrectsNextRunAtToFinishPlusInterval verifies
// the fix for schedules overlapping themselves when a crawl runs longer
// than its own interval: next_run_at is provisionally set to trigger+interval
// right away (so a scheduler tick mid-run never sees the schedule as due
// again), but once trigger's onDone callback fires (the job actually
// finished), next_run_at is corrected to that finish time plus the
// interval -- later than the provisional value here, since the run took
// longer than a tick.
func TestTriggerDueCrawls_OnDoneCorrectsNextRunAtToFinishPlusInterval(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)

	var onDone func()
	trigger := func(_ context.Context, _ ports.CrawlOptions, done func()) (string, error) {
		onDone = done
		return "job-1", nil
	}
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	provisional := store.schedules["sched-1"].NextRunAt
	if !provisional.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("expected a provisional next_run_at of trigger+interval, got %v", provisional)
	}

	if onDone == nil {
		t.Fatal("expected trigger to receive an onDone callback")
	}
	onDone()

	corrected := store.schedules["sched-1"].NextRunAt
	if !corrected.After(provisional) {
		t.Errorf("expected onDone to push next_run_at later than the provisional value (%v), got %v", provisional, corrected)
	}
}

// TestTriggerDueCrawls_OnDoneMarkErrorIsLoggedNotFatal proves onDone's own
// MarkScheduledCrawlRun failure (the completion-time correction) is only
// logged, matching every other store-error path in this file -- calling it
// must never panic, even though by then TriggerDueCrawls itself has long
// since returned.
func TestTriggerDueCrawls_OnDoneMarkErrorIsLoggedNotFatal(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)
	store.markErrOnCall = 2 // 1st call is the trigger-time placeholder; 2nd is onDone's correction.

	var onDone func()
	trigger := func(_ context.Context, _ ports.CrawlOptions, done func()) (string, error) {
		onDone = done
		return "job-1", nil
	}
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	onDone() // must not panic despite the forced error
}

func TestTriggerDueCrawls_PassesScheduleOptionsThrough(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{
		ID: "sched-1", SeedURLs: []string{"http://a", "http://b"}, MaxPages: 42,
		RespectRobots: true, UserAgent: "custom-agent",
		Cookie: "session=abc", BasicAuthUser: "admin", BasicAuthPass: "hunter2",
		AllowOffDomainLinks: true, UseSitemap: true,
		IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute),
	}
	store := newFakeScheduledCrawlStore(s)

	var gotOpts ports.CrawlOptions
	trigger := func(_ context.Context, opts ports.CrawlOptions, _ func()) (string, error) {
		gotOpts = opts
		return "job-1", nil
	}
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gotOpts.SeedURLs) != 2 || gotOpts.MaxPages != 42 || !gotOpts.RespectRobots ||
		gotOpts.UserAgent != "custom-agent" || !gotOpts.AllowOffDomainLinks || !gotOpts.UseSitemap {
		t.Errorf("expected the schedule's options to pass through untouched, got %+v", gotOpts)
	}
	if gotOpts.Cookie != "session=abc" || gotOpts.BasicAuthUser != "admin" || gotOpts.BasicAuthPass != "hunter2" {
		t.Errorf("expected the schedule's stored credentials to pass through like any other option, got %+v", gotOpts)
	}
}

// TestTriggerDueCrawls_NonRecurringDisablesItselfAtTriggerTime proves a
// one-off entry (Recurring false) is taken out of contention the instant
// it's triggered, not left enabled with a meaningless next_run_at that the
// very next tick would treat as due all over again.
func TestTriggerDueCrawls_NonRecurringDisablesItselfAtTriggerTime(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "once", SeedURLs: []string{"http://a"}, Recurring: false, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)

	trigger := func(context.Context, ports.CrawlOptions, func()) (string, error) { return "job-1", nil }
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.schedules["once"].Enabled {
		t.Error("expected a non-recurring entry to disable itself immediately at trigger time")
	}
}

// TestTriggerDueCrawls_NonRecurringStaysDisabledOnDone proves onDone's
// completion-time correction re-affirms disabled for a one-off entry
// rather than re-enabling it (which the recurring path's onDone does, via
// s.Recurring being true there instead).
func TestTriggerDueCrawls_NonRecurringStaysDisabledOnDone(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "once", SeedURLs: []string{"http://a"}, Recurring: false, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)

	var onDone func()
	trigger := func(_ context.Context, _ ports.CrawlOptions, done func()) (string, error) {
		onDone = done
		return "job-1", nil
	}
	if _, err := application.TriggerDueCrawls(context.Background(), store, trigger, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	onDone()

	if store.schedules["once"].Enabled {
		t.Error("expected a non-recurring entry to stay disabled after onDone")
	}
}

func TestTriggerDueCrawls_NoScheduleDue(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeScheduledCrawlStore(domain.ScheduledCrawl{
		ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(time.Hour),
	})

	called := false
	trigger := func(context.Context, ports.CrawlOptions, func()) (string, error) {
		called = true
		return "job-1", nil
	}
	n, err := application.TriggerDueCrawls(context.Background(), store, trigger, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || called {
		t.Errorf("expected nothing triggered, got n=%d called=%v", n, called)
	}
}

func TestTriggerDueCrawls_DueScheduledCrawlsErrorPropagates(t *testing.T) {
	store := newFakeScheduledCrawlStore()
	store.dueErr = errors.New("db down")

	_, err := application.TriggerDueCrawls(context.Background(), store, func(context.Context, ports.CrawlOptions, func()) (string, error) { return "", nil }, time.Now())
	if err == nil {
		t.Error("expected the DueScheduledCrawls error to propagate")
	}
}

func TestTriggerDueCrawls_TriggerErrorSkipsThatScheduleButContinues(t *testing.T) {
	now := time.Now().UTC()
	failing := domain.ScheduledCrawl{ID: "failing", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	ok := domain.ScheduledCrawl{ID: "ok", SeedURLs: []string{"http://b"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(failing, ok)

	trigger := func(_ context.Context, opts ports.CrawlOptions, _ func()) (string, error) {
		if opts.SeedURLs[0] == "http://a" {
			return "", errors.New("crawl-server unreachable")
		}
		return "job-1", nil
	}

	n, err := application.TriggerDueCrawls(context.Background(), store, trigger, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 successful trigger despite the other's failure, got %d", n)
	}
	if store.schedules["failing"].LastRunAt != nil {
		t.Error("expected the failing schedule's LastRunAt to be left untouched, so it's retried next tick")
	}
}

func TestTriggerDueCrawls_MarkRunErrorSkipsCountingThatSchedule(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Recurring: true, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)
	store.markErr = errors.New("db down")

	n, err := application.TriggerDueCrawls(context.Background(), store, func(context.Context, ports.CrawlOptions, func()) (string, error) { return "job-1", nil }, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 counted when MarkScheduledCrawlRun fails, got %d", n)
	}
}
