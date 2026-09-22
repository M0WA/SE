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
		ID:                   id,
		SeedURLs:             []string{"http://a.example", "http://b.example"},
		MaxPages:             20,
		RespectRobots:        true,
		UserAgent:            "test-agent",
		Cookie:               "session=abc123",
		BasicAuthUser:        "admin",
		BasicAuthPass:        "hunter2",
		LinkScope:            domain.LinkScopeHost,
		AllowedDomains:       []string{"allowed.example"},
		BlockedDomains:       []string{"blocked.example"},
		FollowIndexedDomains: true,
		UseSitemap:           true,
		FetchTimeoutSeconds:  10,
		MinTextLength:        100,
		CrawlDelayMs:         500,
		MaxResponseKB:        2048,
		PrioritizeUnindexed:  true,
		Recurring:            true,
		IntervalMinutes:      intervalMinutes,
		MaxRuns:              10,
		Renderer:             domain.RendererChromium,
		Enabled:              true,
		NextRunAt:            nextRunAt,
		CreatedAt:            time.Now().UTC(),
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
	assertScheduledCrawlOptionsRoundTrip(t, g, s)
	assertScheduledCrawlStateRoundTrip(t, g)
}

// assertScheduledCrawlOptionsRoundTrip checks that every crawl-option field
// (seed URLs, domain scoping, credentials, per-crawl overrides) came back
// out of the repository unchanged from what was saved.
func assertScheduledCrawlOptionsRoundTrip(t *testing.T, g, s domain.ScheduledCrawl) {
	t.Helper()
	if g.ID != s.ID || len(g.SeedURLs) != 2 || g.SeedURLs[0] != "http://a.example" {
		t.Errorf("unexpected seed urls round trip: %+v", g)
	}
	if g.MaxPages != 20 || !g.RespectRobots || g.UserAgent != "test-agent" || g.LinkScope != domain.LinkScopeHost || !g.UseSitemap {
		t.Errorf("unexpected option round trip: %+v", g)
	}
	if len(g.AllowedDomains) != 1 || g.AllowedDomains[0] != "allowed.example" ||
		len(g.BlockedDomains) != 1 || g.BlockedDomains[0] != "blocked.example" || !g.FollowIndexedDomains {
		t.Errorf("unexpected allow/block domain list round trip: %+v", g)
	}
	if g.Cookie != "session=abc123" || g.BasicAuthUser != "admin" || g.BasicAuthPass != "hunter2" {
		t.Errorf("expected credentials to round trip like any other option, got %+v", g)
	}
	if g.FetchTimeoutSeconds != 10 || g.MinTextLength != 100 || g.CrawlDelayMs != 500 ||
		g.MaxResponseKB != 2048 || !g.PrioritizeUnindexed {
		t.Errorf("unexpected per-crawl override round trip: %+v", g)
	}
}

// assertScheduledCrawlStateRoundTrip checks that every scheduling-state
// field (interval/enabled/recurring, run bookkeeping, timestamps) came back
// out of the repository unchanged from what a freshly created schedule
// should hold.
func assertScheduledCrawlStateRoundTrip(t *testing.T, g domain.ScheduledCrawl) {
	t.Helper()
	if g.IntervalMinutes != 30 || !g.Enabled || !g.Recurring {
		t.Errorf("unexpected interval/enabled/recurring round trip: %+v", g)
	}
	if g.MaxRuns != 10 || g.RunCount != 0 {
		t.Errorf("expected MaxRuns to round trip and a fresh RunCount of 0, got %+v", g)
	}
	if g.Renderer != domain.RendererChromium {
		t.Errorf("expected Renderer to round trip, got %q", g.Renderer)
	}
	if g.LastRunAt != nil {
		t.Errorf("expected a freshly created schedule to have no LastRunAt, got %v", g.LastRunAt)
	}
	if g.NextRunAt.IsZero() || g.CreatedAt.IsZero() {
		t.Errorf("expected NextRunAt/CreatedAt to round-trip, got %+v", g)
	}
}

func TestGetScheduledCrawl_ReturnsMatchingSchedule(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(30*time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "sched-1" || got.MaxPages != 20 || len(got.SeedURLs) != 2 {
		t.Errorf("unexpected schedule: %+v", got)
	}
}

func TestGetScheduledCrawl_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetScheduledCrawl(context.Background(), "missing")
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

// TestRunScheduledCrawlNow_SetsNextRunAtAndReEnablesWithoutTouchingOptions
// proves RunScheduledCrawlNow only ever changes next_run_at/enabled --
// every other stored field (recurring, interval, options) stays exactly as
// it was, so "run now" can't silently reset a schedule's configuration.
func TestRunScheduledCrawlNow_SetsNextRunAtAndReEnablesWithoutTouchingOptions(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(2*time.Hour))
	s.Enabled = false
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	now := time.Now().UTC()
	if err := repo.RunScheduledCrawlNow(ctx, "sched-1", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Enabled {
		t.Error("expected RunScheduledCrawlNow to re-enable a paused schedule")
	}
	if got.NextRunAt.Sub(now).Abs() > time.Second {
		t.Errorf("expected NextRunAt ~%v, got %v", now, got.NextRunAt)
	}
	if got.IntervalMinutes != 30 || got.MaxPages != 20 || got.LinkScope != domain.LinkScopeHost {
		t.Errorf("expected every other option untouched, got %+v", got)
	}
}

// TestSetScheduledCrawlEnabled_LeavesNextRunAtUntouched is the whole point
// of this method existing separately from UpdateScheduledCrawl: a plain
// pause/resume toggle must not reschedule the crawl (NextRunAt) or touch
// any other option -- see ports.ScheduledCrawlStore's doc comment on why
// (the admin Jobs list sorts by NextRunAt, so a PATCH-driven reschedule on
// every toggle reorders that list unexpectedly).
func TestSetScheduledCrawlEnabled_LeavesNextRunAtUntouched(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	fixedNextRun := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	s := newScheduledCrawl("sched-1", 30, fixedNextRun)
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.SetScheduledCrawlEnabled(ctx, "sched-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Enabled {
		t.Error("expected SetScheduledCrawlEnabled(false) to disable the schedule")
	}
	if !got.NextRunAt.Equal(fixedNextRun) {
		t.Errorf("expected NextRunAt untouched at %v, got %v", fixedNextRun, got.NextRunAt)
	}
	if got.IntervalMinutes != 30 || got.MaxPages != 20 || got.LinkScope != domain.LinkScopeHost {
		t.Errorf("expected every other option untouched, got %+v", got)
	}

	if err := repo.SetScheduledCrawlEnabled(ctx, "sched-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err = repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Enabled {
		t.Error("expected SetScheduledCrawlEnabled(true) to re-enable the schedule")
	}
	if !got.NextRunAt.Equal(fixedNextRun) {
		t.Errorf("expected NextRunAt still untouched at %v, got %v", fixedNextRun, got.NextRunAt)
	}
}

func TestSetScheduledCrawlEnabled_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.SetScheduledCrawlEnabled(context.Background(), "missing", true)
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

func TestRunScheduledCrawlNow_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.RunScheduledCrawlNow(context.Background(), "missing", time.Now())
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}

// TestRunScheduledCrawlNow_RefusesWhenAlreadyInProgress proves "run now"
// no longer force-clears in_progress: a real production incident (two
// concurrent crawls of the same site racing on document_versions'
// primary key -- see RunScheduledCrawlNow's own doc comment) traced back
// to this method unconditionally clearing the flag, which let the
// scheduler's ticker start a second crawl while one was still genuinely
// running. A crash-stuck flag is handled once, correctly, at crawl-server
// startup by ResetStaleInProgress -- not here, since this layer has no
// way to tell "genuinely running" from "stuck" apart. NextRunAt/Enabled
// must be left exactly as they were: this must be a true no-op, not a
// partial update.
func TestRunScheduledCrawlNow_RefusesWhenAlreadyInProgress(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	fixedNextRun := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	s := newScheduledCrawl("sched-1", 30, fixedNextRun)
	s.InProgress = true
	s.Enabled = false
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := repo.RunScheduledCrawlNow(ctx, "sched-1", time.Now().UTC())
	if !errors.Is(err, ports.ErrScheduledCrawlInProgress) {
		t.Fatalf("expected ErrScheduledCrawlInProgress, got %v", err)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.InProgress {
		t.Error("expected InProgress to remain true -- RunScheduledCrawlNow must not clear it")
	}
	if got.Enabled {
		t.Error("expected Enabled to remain untouched on a refused call")
	}
	if !got.NextRunAt.Equal(fixedNextRun) {
		t.Errorf("expected NextRunAt untouched at %v, got %v", fixedNextRun, got.NextRunAt)
	}
}

// TestRunScheduledCrawlNow_SucceedsWhenNotInProgress is the companion to
// the refusal test above: a schedule genuinely not running right now
// still gets the normal next_run_at/enabled treatment.
func TestRunScheduledCrawlNow_SucceedsWhenNotInProgress(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(2*time.Hour))
	s.InProgress = false
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	now := time.Now().UTC()
	if err := repo.RunScheduledCrawlNow(ctx, "sched-1", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.NextRunAt.Sub(now).Abs() > time.Second {
		t.Errorf("expected NextRunAt ~%v, got %v", now, got.NextRunAt)
	}

	due, err := repo.DueScheduledCrawls(ctx, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 1 || due[0].ID != "sched-1" {
		t.Errorf("expected the schedule to now be selectable by DueScheduledCrawls, got %+v", due)
	}
}

// TestResetStaleInProgress_ClearsFlagsWithNoMatchingActiveJob mirrors what
// cmd/crawl/main.go runs once at startup, after RecoverInterruptedCrawls:
// every schedule stuck in_progress=true whose JobID doesn't correspond to a
// still-queued/running crawl_jobs row (from a restart that interrupted its
// triggered run, with nothing left to ever clear it -- see
// ResetStaleInProgress's own doc comment) is reset, while a schedule that
// was never in progress is left alone. Neither stuck row here has a JobID
// at all (as if it predates that column, or its job was deleted), which is
// exactly the "no matching active job" case.
func TestResetStaleInProgress_ClearsFlagsWithNoMatchingActiveJob(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	stuckA := newScheduledCrawl("sched-stuck-a", 30, time.Now().UTC().Add(-time.Hour))
	stuckA.InProgress = true
	stuckB := newScheduledCrawl("sched-stuck-b", 30, time.Now().UTC().Add(-time.Hour))
	stuckB.InProgress = true
	healthy := newScheduledCrawl("sched-healthy", 30, time.Now().UTC().Add(-time.Hour))
	healthy.InProgress = false

	for _, s := range []domain.ScheduledCrawl{stuckA, stuckB, healthy} {
		if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
			t.Fatalf("unexpected error creating %s: %v", s.ID, err)
		}
	}

	reset, err := repo.ResetStaleInProgress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reset != 2 {
		t.Errorf("expected 2 rows reset, got %d", reset)
	}

	for _, id := range []string{"sched-stuck-a", "sched-stuck-b", "sched-healthy"} {
		got, err := repo.GetScheduledCrawl(ctx, id)
		if err != nil {
			t.Fatalf("unexpected error fetching %s: %v", id, err)
		}
		if got.InProgress {
			t.Errorf("expected %s to have InProgress cleared, got true", id)
		}
	}

	// Now that every stale flag is cleared, every enabled, due schedule
	// (all three, all with a past NextRunAt) becomes selectable again.
	due, err := repo.DueScheduledCrawls(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 3 {
		t.Errorf("expected all 3 schedules to be due after the reset, got %d", len(due))
	}
}

// TestResetStaleInProgress_LeavesFlagAloneWhenJobStillActive is the
// regression test for a real production incident: a schedule's InProgress
// flag must NOT be cleared when its JobID still points to a queued/running
// crawl_jobs row -- exactly the shape left behind when
// RecoverInterruptedCrawls resumes a job moments before this runs at
// crawl-server startup. The earlier, job-unaware version of
// ResetStaleInProgress cleared every stuck-true row unconditionally, which
// let the very next scheduler tick start a second, duplicate crawl of the
// same site while the resumed one was still running.
func TestResetStaleInProgress_LeavesFlagAloneWhenJobStillActive(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"https://cnn.com"}, MaxPages: 20000})
	if err != nil {
		t.Fatalf("unexpected error creating crawl job: %v", err)
	}
	if err := repo.MarkRunning(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error marking job running: %v", err)
	}

	stillRunning := newScheduledCrawl("sched-cnn", 30, time.Now().UTC().Add(-time.Hour))
	stillRunning.InProgress = true
	stillRunning.JobID = job.ID
	if err := repo.CreateScheduledCrawl(ctx, stillRunning); err != nil {
		t.Fatalf("unexpected error creating schedule: %v", err)
	}

	reset, err := repo.ResetStaleInProgress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reset != 0 {
		t.Errorf("expected 0 rows reset while the referenced job is still running, got %d", reset)
	}

	got, err := repo.GetScheduledCrawl(ctx, "sched-cnn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.InProgress || got.JobID != job.ID {
		t.Errorf("expected InProgress and JobID left untouched, got %+v", got)
	}

	// The whole point: it must still be excluded from DueScheduledCrawls,
	// so a second, duplicate crawl of the same site is never triggered
	// while the resumed one is still running.
	due, err := repo.DueScheduledCrawls(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected the still-running schedule to stay excluded from DueScheduledCrawls, got %+v", due)
	}

	// Once the job actually finishes, a subsequent ResetStaleInProgress
	// call (e.g. the next restart) correctly clears it.
	if err := repo.MarkDone(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error marking job done: %v", err)
	}
	reset, err = repo.ResetStaleInProgress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reset != 1 {
		t.Errorf("expected the row to be reset once its job has finished, got %d", reset)
	}
}

func TestResetStaleInProgress_NoOpWhenNothingStuck(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newScheduledCrawl("sched-1", 30, time.Now().UTC().Add(time.Hour))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reset, err := repo.ResetStaleInProgress(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reset != 0 {
		t.Errorf("expected 0 rows reset when nothing is stuck, got %d", reset)
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
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, now.Add(30*time.Minute), true, false, 3, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := s
	updated.SeedURLs = []string{"http://changed.example"}
	updated.MaxPages = 99
	updated.RespectRobots = false
	updated.Cookie = "session=changed"
	updated.BasicAuthPass = "changed"
	updated.PrioritizeUnindexed = false
	updated.AllowedDomains = []string{"changed-allowed.example"}
	updated.BlockedDomains = []string{"changed-blocked.example"}
	updated.FollowIndexedDomains = false
	updated.Recurring = false
	updated.IntervalMinutes = 15
	updated.MaxRuns = 25
	updated.Renderer = domain.RendererFirefox
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
	if len(g.AllowedDomains) != 1 || g.AllowedDomains[0] != "changed-allowed.example" ||
		len(g.BlockedDomains) != 1 || g.BlockedDomains[0] != "changed-blocked.example" || g.FollowIndexedDomains {
		t.Errorf("expected updated allow/block domain lists, got %+v", g)
	}
	if g.MaxRuns != 25 {
		t.Errorf("expected MaxRuns to be editable, got %d", g.MaxRuns)
	}
	if g.RunCount != 3 {
		t.Errorf("expected RunCount to be preserved across an edit (not user-editable), got %d", g.RunCount)
	}
	if g.Renderer != domain.RendererFirefox {
		t.Errorf("expected Renderer to be editable, got %q", g.Renderer)
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

// TestDueScheduledCrawls_ExcludesInProgress proves an enabled, overdue
// entry that's currently in_progress is still excluded -- in_progress is a
// second, independent gate alongside enabled, not something enabled alone
// covers.
func TestDueScheduledCrawls_ExcludesInProgress(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	overdueIdle := newScheduledCrawl("overdue-idle", 30, now.Add(-1*time.Minute))
	overdueInProgress := newScheduledCrawl("overdue-in-progress", 30, now.Add(-1*time.Minute))
	overdueInProgress.InProgress = true

	for _, s := range []domain.ScheduledCrawl{overdueIdle, overdueInProgress} {
		if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
			t.Fatalf("unexpected error creating %s: %v", s.ID, err)
		}
	}

	due, err := repo.DueScheduledCrawls(ctx, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 1 || due[0].ID != "overdue-idle" {
		t.Errorf("expected only the idle (not in-progress) overdue schedule, got %+v", due)
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
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, nextRun, true, false, 1, ""); err != nil {
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
// (non-recurring) entry's onDone relies on to disable itself for good once
// its triggered run actually completes (see application.TriggerDueCrawls).
func TestMarkScheduledCrawlRun_PersistsEnabled(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := newScheduledCrawl("once", 0, now)
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.MarkScheduledCrawlRun(ctx, "once", now, now, false, false, 1, ""); err != nil {
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

// TestMarkScheduledCrawlRun_PersistsInProgress proves the inProgress and
// jobID params are actually written -- what application.TriggerDueCrawls'
// trigger-time call relies on to keep DueScheduledCrawls from picking this
// entry up again while it's still running, without touching enabled at
// all, and what ResetStaleInProgress relies on at the next crawl-server
// startup to tell a genuinely-still-running run apart from a stale one.
func TestMarkScheduledCrawlRun_PersistsInProgress(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := newScheduledCrawl("sched-1", 30, now.Add(-time.Minute))
	if err := repo.CreateScheduledCrawl(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate TriggerDueCrawls' trigger-time call: enabled stays true,
	// inProgress becomes true, jobID records the triggered run.
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, now.Add(30*time.Minute), true, true, 1, "job-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || !got[0].Enabled || !got[0].InProgress || got[0].JobID != "job-1" {
		t.Errorf("expected enabled=true, in_progress=true, job_id=job-1 after the trigger-time call, got %+v", got)
	}

	// The entry must not be due again while in_progress, even though
	// next_run_at has already passed.
	due, err := repo.DueScheduledCrawls(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected the in-progress entry to be excluded from DueScheduledCrawls, got %+v", due)
	}

	// Simulate onDone: inProgress and jobID both clear.
	if err := repo.MarkScheduledCrawlRun(ctx, "sched-1", now, now.Add(30*time.Minute), true, false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err = repo.ListScheduledCrawls(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].InProgress || got[0].JobID != "" {
		t.Errorf("expected in_progress and job_id cleared after the onDone-style call, got %+v", got)
	}
}

func TestMarkScheduledCrawlRun_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.MarkScheduledCrawlRun(context.Background(), "missing", time.Now(), time.Now(), true, false, 1, "")
	if !errors.Is(err, ports.ErrScheduledCrawlNotFound) {
		t.Errorf("expected ErrScheduledCrawlNotFound, got %v", err)
	}
}
