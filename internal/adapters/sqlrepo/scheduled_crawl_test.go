package sqlrepo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newScheduledCrawl(id string, intervalMinutes int, nextRunAt time.Time) domain.ScheduledCrawl {
	return domain.ScheduledCrawl{
		ID:                  id,
		SeedURLs:            []string{"http://a.example", "http://b.example"},
		MaxPages:            20,
		RespectRobots:       true,
		UserAgent:           "test-agent",
		Cookie:              "session=abc123",
		BasicAuthUser:       "admin",
		BasicAuthPass:       "hunter2",
		AllowOffDomainLinks: false,
		UseSitemap:          true,
		FetchTimeoutSeconds: 10,
		MinTextLength:       100,
		CrawlDelayMs:        500,
		MaxResponseKB:       2048,
		PrioritizeUnindexed: true,
		Recurring:           true,
		IntervalMinutes:     intervalMinutes,
		MaxRuns:             10,
		Enabled:             true,
		NextRunAt:           nextRunAt,
		CreatedAt:           time.Now().UTC(),
	}
}

func TestCreateScheduledCrawl_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(30*time.Minute))

	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(got))
	}
	g := got[0]
	if g.ID != s.ID || len(g.SeedURLs) != 2 || g.SeedURLs[0] != "http://a.example" {
		t.Errorf("unexpected seed urls round trip: %+v", g)
	}
	if g.MaxPages != 20 || !g.RespectRobots || g.UserAgent != "test-agent" || g.AllowOffDomainLinks || !g.UseSitemap {
		t.Errorf("unexpected option round trip: %+v", g)
	}
	if g.Cookie != "session=abc123" || g.BasicAuthUser != "admin" || g.BasicAuthPass != "hunter2" {
		t.Errorf("expected credentials to round trip like any other option, got %+v", g)
	}
	if g.FetchTimeoutSeconds != 10 || g.MinTextLength != 100 || g.CrawlDelayMs != 500 ||
		g.MaxResponseKB != 2048 || !g.PrioritizeUnindexed {
		t.Errorf("unexpected per-crawl override round trip: %+v", g)
	}
	if g.IntervalMinutes != 30 || !g.Enabled || !g.Recurring {
		t.Errorf("unexpected interval/enabled/recurring round trip: %+v", g)
	}
	if g.MaxRuns != 10 || g.RunCount != 0 {
		t.Errorf("expected MaxRuns to round trip and a fresh RunCount of 0, got %+v", g)
	}
	if g.LastRunAt != nil {
		t.Errorf("expected a freshly created schedule to have no LastRunAt, got %v", g.LastRunAt)
	}
	if g.NextRunAt.IsZero() || g.CreatedAt.IsZero() {
		t.Errorf("expected NextRunAt/CreatedAt to round-trip, got %+v", g)
	}
}

func TestListScheduledCrawls_OrdersBySoonestNextRun(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.CreateScheduledCrawl(ctx, newScheduledCrawl("later", 60, now.Add(2*time.Hour))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateScheduledCrawl(ctx, newScheduledCrawl("sooner", 60, now.Add(1*time.Minute))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].ID != "sooner" || got[1].ID != "later" {
		t.Errorf("expected [sooner, later] order, got %+v", got)
	}
}

func TestUpdateScheduledCrawl_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := newScheduledCrawl("sched-1", 30, now.Add(30*time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Give it a real RunCount before editing, so the assertion below can
	// prove UpdateScheduledCrawl leaves it alone (like LastRunAt) even
	// though MaxRuns -- the cap it's compared against -- does change.
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, now.Add(30*time.Minute), true, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := s
	updated.SeedURLs = []string{"http://changed.example"}
	updated.MaxPages = 99
	updated.RespectRobots = false
	updated.Cookie = "session=changed"
	updated.BasicAuthPass = "changed"
	updated.PrioritizeUnindexed = false
	updated.Recurring = false
	updated.IntervalMinutes = 15
	updated.MaxRuns = 25
	updated.Enabled = false
	updated.NextRunAt = now.Add(15 * time.Minute)

	if err := repo.UpdateScheduledCrawl(ctx, updated); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(got))
	}
	g := got[0]
	if len(g.SeedURLs) != 1 || g.SeedURLs[0] != "http://changed.example" {
		t.Errorf("expected updated seed urls, got %+v", g.SeedURLs)
	}
	if g.MaxPages != 99 || g.RespectRobots || g.IntervalMinutes != 15 || g.Enabled {
		t.Errorf("expected updated fields, got %+v", g)
	}
	if g.Cookie != "session=changed" || g.BasicAuthPass != "changed" || g.PrioritizeUnindexed || g.Recurring {
		t.Errorf("expected updated credentials/override/recurring fields, got %+v", g)
	}
	if g.MaxRuns != 25 {
		t.Errorf("expected MaxRuns to be editable, got %d", g.MaxRuns)
	}
	if g.RunCount != 3 {
		t.Errorf("expected RunCount to be preserved across an edit (not user-editable), got %d", g.RunCount)
	}
}

func TestUpdateScheduledCrawl_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateScheduledCrawl(context.Background(), newScheduledCrawl("missing", 30, time.Now()))
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

func TestDeleteScheduledCrawl_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(30*time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteScheduledCrawl(ctx, "sched-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected the schedule to be gone, got %+v", got)
	}
}

func TestDeleteScheduledCrawl_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteScheduledCrawl(context.Background(), "missing")
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

func TestDueScheduledCrawls_OnlyReturnsEnabledAndOverdue(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	overdueEnabled := newScheduledCrawl("overdue-enabled", 30, now.Add(-1*time.Minute))
	overdueDisabled := newScheduledCrawl("overdue-disabled", 30, now.Add(-1*time.Minute))
	overdueDisabled.Enabled = false
	notYetDue := newScheduledCrawl("not-yet-due", 30, now.Add(1*time.Hour))

	for _, s := range []domain.ScheduledCrawl{overdueEnabled, overdueDisabled, notYetDue} {
		if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
			t.Fatalf("unexpected error creating %s: %v", s.ID, err)
		}
	}

	due, err := repo.DueScheduledCrawls(ctx, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 1 || due[0].ID != "overdue-enabled" {
		t.Errorf("expected only the overdue, enabled schedule, got %+v", due)
	}
}

func TestMarkScheduledCrawlRun_AdvancesLastAndNextRun(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := newScheduledCrawl("sched-1", 30, now.Add(-1*time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	nextRun := now.Add(30 * time.Minute)
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, nextRun, true, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(got))
	}
	g := got[0]
	if g.LastRunAt == nil {
		t.Fatal("expected LastRunAt to be set")
	}
	if g.NextRunAt.Sub(nextRun).Abs() > time.Second {
		t.Errorf("expected NextRunAt around %v, got %v", nextRun, g.NextRunAt)
	}
	if g.RunCount != 1 {
		t.Errorf("expected RunCount 1, got %d", g.RunCount)
	}

	due, err := repo.DueScheduledCrawls(ctx, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected the schedule to no longer be due right after running, got %+v", due)
	}
}

// TestMarkScheduledCrawlRun_PersistsEnabled proves the enabled param is
// actually written, not just last_run_at/next_run_at -- what a one-off
// (non-recurring) entry relies on to take itself out of contention right
// at trigger time (see application.TriggerDueCrawls).
func TestMarkScheduledCrawlRun_PersistsEnabled(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := newScheduledCrawl("once", 0, now)
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.MarkScheduledCrawlRun(ctx, "once", now, now, false, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Enabled {
		t.Errorf("expected the entry to be disabled after MarkScheduledCrawlRun(enabled=false), got %+v", got)
	}
}

func TestMarkScheduledCrawlRun_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.MarkScheduledCrawlRun(context.Background(), "missing", time.Now(), time.Now(), true, 1)
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}
