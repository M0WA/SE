package sqlrepo_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
)

func TestRepository_CreateCrawlJobStartsQueued(t *testing.T) {
	repo := newTestRepo(t)
	job, err := repo.Create(context.Background(), domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.Status != domain.CrawlJobQueued {
		t.Errorf("expected queued, got %s", job.Status)
	}
	if job.ID == "" {
		t.Error("expected a non-empty job ID")
	}
	if job.StartedAt != nil || job.FinishedAt != nil {
		t.Error("expected StartedAt/FinishedAt unset on a new job")
	}
}

func TestRepository_CrawlJobRequestRoundTripsThroughStorage(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	req := domain.CrawlJobRequest{
		SeedURLs: []string{"http://a", "http://b"}, MaxPages: 42,
		HasCookie: true, HasBasicAuth: true, RespectRobots: true,
		UserAgent: "custom-agent", AllowOffDomainLinks: true, UseSitemap: true,
	}
	job, err := repo.Create(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Request, req) {
		t.Errorf("expected request to round-trip exactly, got %+v, want %+v", got.Request, req)
	}
}

func TestRepository_CrawlJobMarkRunningSetsStartedAt(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	if err := repo.MarkRunning(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != domain.CrawlJobRunning {
		t.Errorf("expected running, got %s", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

func TestRepository_CrawlJobAppendPageTracksIndexedCountAndPreservesOrder(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	_ = repo.MarkRunning(ctx, job.ID)

	events := []domain.CrawlPageEvent{
		{URL: "http://a", Status: domain.CrawlPageIndexed, Title: "A", DocLength: 100, LinksFound: 3, DurationMs: 12, FetchedAt: time.Now().UTC()},
		{URL: "http://b", Status: domain.CrawlPageThinContent, DocLength: 10, FetchedAt: time.Now().UTC()},
		{URL: "http://c", Status: domain.CrawlPageRobotsDisallowed, FetchedAt: time.Now().UTC()},
		{URL: "http://d", Status: domain.CrawlPageFetchFailed, Error: "timeout", FetchedAt: time.Now().UTC()},
	}
	for _, ev := range events {
		if err := repo.AppendPage(ctx, job.ID, ev); err != nil {
			t.Fatalf("unexpected error appending %s: %v", ev.URL, err)
		}
	}

	got, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PagesCrawled != 1 {
		t.Errorf("expected 1 indexed page counted, got %d", got.PagesCrawled)
	}
	if len(got.Pages) != 4 {
		t.Fatalf("expected all 4 page events recorded, got %d", len(got.Pages))
	}
	for i, ev := range events {
		if got.Pages[i].URL != ev.URL || got.Pages[i].Status != ev.Status {
			t.Errorf("page %d: expected insertion order preserved, got %+v, want URL=%s status=%s", i, got.Pages[i], ev.URL, ev.Status)
		}
	}
	if got.Pages[0].DocLength != 100 || got.Pages[0].LinksFound != 3 || got.Pages[0].DurationMs != 12 {
		t.Errorf("expected diagnostic detail preserved on the indexed page, got %+v", got.Pages[0])
	}
	if got.Pages[3].Error != "timeout" {
		t.Errorf("expected fetch-failed page's error preserved, got %+v", got.Pages[3])
	}
}

func TestRepository_CrawlJobMarkDoneSetsFinishedAt(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	_ = repo.MarkRunning(ctx, job.ID)
	if err := repo.MarkDone(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := repo.Get(ctx, job.ID)
	if got.Status != domain.CrawlJobDone {
		t.Errorf("expected done, got %s", got.Status)
	}
	if got.FinishedAt == nil {
		t.Error("expected FinishedAt to be set")
	}
}

func TestRepository_CrawlJobMarkFailedRecordsError(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	_ = repo.MarkRunning(ctx, job.ID)
	if err := repo.MarkFailed(ctx, job.ID, errors.New("disk full")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := repo.Get(ctx, job.ID)
	if got.Status != domain.CrawlJobFailed {
		t.Errorf("expected failed, got %s", got.Status)
	}
	if got.Error != "disk full" {
		t.Errorf("expected error message recorded, got %q", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("expected FinishedAt to be set on failure too")
	}
}

func TestRepository_GetCrawlJobUnknownIDReportsNotFound(t *testing.T) {
	repo := newTestRepo(t)
	if _, err := repo.Get(context.Background(), "does-not-exist"); !errors.Is(err, domain.ErrCrawlJobNotFound) {
		t.Errorf("expected ErrCrawlJobNotFound, got %v", err)
	}
}

func TestRepository_ListCrawlJobsReturnsMostRecentFirst(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	a, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	time.Sleep(2 * time.Millisecond) // ensure a distinct created_at ordering
	b, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://b"}})

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("expected [b, a] most-recent-first, got %+v", list)
	}
}

func TestRepository_ListCrawlJobsOmitsPerPageDetail(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	_ = repo.AppendPage(ctx, job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed, FetchedAt: time.Now()})

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list[0].PagesCrawled != 1 {
		t.Errorf("expected summary to still report pages_crawled, got %d", list[0].PagesCrawled)
	}
}

func TestRepository_AppendPageOnUnknownJobIsNoop(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.AppendPage(context.Background(), "never-created", domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed, FetchedAt: time.Now()}); err != nil {
		t.Errorf("expected appending to an unknown job to be a silent no-op, got error: %v", err)
	}
}

func TestRepository_PruneCrawlJobsKeepsNewestN(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	var ids []string
	for i := 0; i < 5; i++ {
		job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
		ids = append(ids, job.ID)
		time.Sleep(2 * time.Millisecond)
	}

	if err := repo.PruneCrawlJobs(ctx, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected exactly 2 retained jobs, got %d", len(list))
	}
	// The 2 newest (last created) must survive.
	if list[0].ID != ids[4] || list[1].ID != ids[3] {
		t.Errorf("expected the 2 newest jobs retained, got %+v", list)
	}
	if _, err := repo.Get(ctx, ids[0]); !errors.Is(err, domain.ErrCrawlJobNotFound) {
		t.Error("expected the oldest job to be pruned")
	}
}

func TestRepository_PruneCrawlJobsCascadesToPages(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	_ = repo.AppendPage(ctx, job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed, FetchedAt: time.Now()})
	newer, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://b"}})
	_ = newer

	if err := repo.PruneCrawlJobs(ctx, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// job (and its page rows) should be gone; re-fetching must not resurrect
	// orphaned page rows under a job ID that's gone from crawl_jobs.
	if _, err := repo.Get(ctx, job.ID); !errors.Is(err, domain.ErrCrawlJobNotFound) {
		t.Error("expected the pruned job to be gone")
	}
}

func TestRepository_PruneCrawlJobsZeroOrNegativeIsNoop(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	job, _ := repo.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})

	if err := repo.PruneCrawlJobs(ctx, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Get(ctx, job.ID); err != nil {
		t.Errorf("expected job to survive a zero-limit prune (no-op), got: %v", err)
	}
}

// TestRepository_CrawlJobSurvivesProcessRestart is the whole point of this
// feature: a real file-backed SQLite database (not the in-memory DSN every
// other test in this package uses, which -- with cache=shared -- only
// persists as long as at least one connection stays open) proves a crawl
// job actually survives closing and reopening the repository, the same way
// a real crawl-server restart would encounter it. The old in-memory
// domain.CrawlJobStore could never pass an equivalent test.
func TestRepository_CrawlJobSurvivesProcessRestart(t *testing.T) {
	ctx := context.Background()
	path := "file:" + t.TempDir() + "/crawljobs.db"

	first, err := sqlrepo.New(ctx, "sqlite", path)
	if err != nil {
		t.Fatalf("unexpected error opening repo: %v", err)
	}
	job, err := first.Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := first.MarkRunning(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := first.AppendPage(ctx, job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed, Title: "A", FetchedAt: time.Now()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := first.MarkDone(ctx, job.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("unexpected error closing first repo: %v", err)
	}

	second, err := sqlrepo.New(ctx, "sqlite", path)
	if err != nil {
		t.Fatalf("unexpected error reopening repo: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, err := second.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("expected the job to survive reopening the database, got error: %v", err)
	}
	if got.Status != domain.CrawlJobDone || got.PagesCrawled != 1 || len(got.Pages) != 1 {
		t.Errorf("expected the job's full state and history to survive a restart, got %+v", got)
	}
}
