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
		AllowOffDomainLinks: false,
		UseSitemap:          true,
		IntervalMinutes:     intervalMinutes,
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
	if g.IntervalMinutes != 30 || !g.Enabled {
		t.Errorf("unexpected interval/enabled round trip: %+v", g)
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

	updated := s
	updated.SeedURLs = []string{"http://changed.example"}
	updated.MaxPages = 99
	updated.RespectRobots = false
	updated.IntervalMinutes = 15
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
}

func TestUpdateScheduledCrawl_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateScheduledCrawl(context.Background(), newScheduledCrawl("missing", 30, time.Now()))
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

func TestSetScheduledCrawlEnabled_TogglesWithoutTouchingOtherFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(30*time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.SetScheduledCrawlEnabled(ctx, "sched-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Enabled {
		t.Fatalf("expected schedule to be disabled, got %+v", got)
	}
	if got[0].MaxPages != s.MaxPages || got[0].IntervalMinutes != s.IntervalMinutes {
		t.Errorf("expected other fields untouched, got %+v", got[0])
	}
}

func TestSetScheduledCrawlEnabled_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.SetScheduledCrawlEnabled(context.Background(), "missing", true)
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
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, nextRun); err != nil {
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

	due, err := repo.DueScheduledCrawls(ctx, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected the schedule to no longer be due right after running, got %+v", due)
	}
}

func TestMarkScheduledCrawlRun_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.MarkScheduledCrawlRun(context.Background(), "missing", time.Now(), time.Now())
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}
