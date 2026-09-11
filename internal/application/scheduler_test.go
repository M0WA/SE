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
func (f *fakeScheduledCrawlStore) SetScheduledCrawlEnabled(context.Context, string, bool) error {
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

func (f *fakeScheduledCrawlStore) MarkScheduledCrawlRun(_ context.Context, id string, lastRunAt, nextRunAt time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	s, ok := f.schedules[id]
	if !ok {
		return ports.ErrScheduledCrawlNotFound
	}
	s.LastRunAt = &lastRunAt
	s.NextRunAt = nextRunAt
	f.schedules[id] = s
	return nil
}

var _ ports.ScheduledCrawlStore = (*fakeScheduledCrawlStore)(nil)

func TestTriggerDueCrawls_TriggersOnlyDueEnabledSchedules(t *testing.T) {
	now := time.Now().UTC()
	due := domain.ScheduledCrawl{ID: "due", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	notYetDue := domain.ScheduledCrawl{ID: "not-yet-due", SeedURLs: []string{"http://b"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(time.Hour)}
	disabled := domain.ScheduledCrawl{ID: "disabled", SeedURLs: []string{"http://c"}, IntervalMinutes: 30, Enabled: false, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(due, notYetDue, disabled)

	var triggeredIDs []string
	trigger := func(opts ports.CrawlOptions) (string, error) {
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
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 45, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)

	trigger := func(ports.CrawlOptions) (string, error) { return "job-1", nil }
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

func TestTriggerDueCrawls_PassesScheduleOptionsThrough(t *testing.T) {
	now := time.Now().UTC()
	s := domain.ScheduledCrawl{
		ID: "sched-1", SeedURLs: []string{"http://a", "http://b"}, MaxPages: 42,
		RespectRobots: true, UserAgent: "custom-agent", AllowOffDomainLinks: true, UseSitemap: true,
		IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(-time.Minute),
	}
	store := newFakeScheduledCrawlStore(s)

	var gotOpts ports.CrawlOptions
	trigger := func(opts ports.CrawlOptions) (string, error) {
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
	if gotOpts.Cookie != "" || gotOpts.BasicAuthUser != "" || gotOpts.BasicAuthPass != "" {
		t.Errorf("expected no credentials on a scheduled crawl's options, got %+v", gotOpts)
	}
}

func TestTriggerDueCrawls_NoScheduleDue(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeScheduledCrawlStore(domain.ScheduledCrawl{
		ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(time.Hour),
	})

	called := false
	trigger := func(ports.CrawlOptions) (string, error) {
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

	_, err := application.TriggerDueCrawls(context.Background(), store, func(ports.CrawlOptions) (string, error) { return "", nil }, time.Now())
	if err == nil {
		t.Error("expected the DueScheduledCrawls error to propagate")
	}
}

func TestTriggerDueCrawls_TriggerErrorSkipsThatScheduleButContinues(t *testing.T) {
	now := time.Now().UTC()
	failing := domain.ScheduledCrawl{ID: "failing", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	ok := domain.ScheduledCrawl{ID: "ok", SeedURLs: []string{"http://b"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(failing, ok)

	trigger := func(opts ports.CrawlOptions) (string, error) {
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
	s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 30, Enabled: true, NextRunAt: now.Add(-time.Minute)}
	store := newFakeScheduledCrawlStore(s)
	store.markErr = errors.New("db down")

	n, err := application.TriggerDueCrawls(context.Background(), store, func(ports.CrawlOptions) (string, error) { return "job-1", nil }, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 counted when MarkScheduledCrawlRun fails, got %d", n)
	}
}
