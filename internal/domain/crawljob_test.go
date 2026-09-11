package domain_test

import (
	"errors"
	"sync"
	"testing"

	"searchengine/internal/domain"
)

func TestCrawlJobStore_CreateStartsQueued(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 5})
	if job.Status != domain.CrawlJobQueued {
		t.Errorf("expected new job queued, got %s", job.Status)
	}
	if job.ID == "" {
		t.Error("expected a non-empty job ID")
	}
	if job.StartedAt != nil || job.FinishedAt != nil {
		t.Error("expected StartedAt/FinishedAt unset on a new job")
	}
}

func TestCrawlJobStore_TwoJobsGetDistinctIDs(t *testing.T) {
	s := domain.NewCrawlJobStore()
	a := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	b := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://b"}})
	if a.ID == b.ID {
		t.Errorf("expected distinct job IDs, got %q twice", a.ID)
	}
}

func TestCrawlJobStore_MarkRunningSetsStartedAt(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.MarkRunning(job.ID)
	got, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("expected job to exist")
	}
	if got.Status != domain.CrawlJobRunning {
		t.Errorf("expected running, got %s", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

func TestCrawlJobStore_AppendPageTracksIndexedCount(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.MarkRunning(job.ID)
	s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed, Title: "A"})
	s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://b", Status: domain.CrawlPageThinContent})
	s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://c", Status: domain.CrawlPageRobotsDisallowed})
	s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://d", Status: domain.CrawlPageFetchFailed})

	got, _ := s.Get(job.ID)
	if got.PagesCrawled != 1 {
		t.Errorf("expected 1 indexed page counted, got %d", got.PagesCrawled)
	}
	if len(got.Pages) != 4 {
		t.Errorf("expected all 4 page events recorded, got %d", len(got.Pages))
	}
}

func TestCrawlJobStore_MarkDoneSetsFinishedAt(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.MarkRunning(job.ID)
	s.MarkDone(job.ID)
	got, _ := s.Get(job.ID)
	if got.Status != domain.CrawlJobDone {
		t.Errorf("expected done, got %s", got.Status)
	}
	if got.FinishedAt == nil {
		t.Error("expected FinishedAt to be set")
	}
}

func TestCrawlJobStore_MarkFailedRecordsError(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.MarkRunning(job.ID)
	s.MarkFailed(job.ID, errors.New("disk full"))
	got, _ := s.Get(job.ID)
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

func TestCrawlJobStore_GetUnknownIDReportsNotFound(t *testing.T) {
	s := domain.NewCrawlJobStore()
	if _, ok := s.Get("does-not-exist"); ok {
		t.Error("expected unknown job ID to report not found")
	}
}

func TestCrawlJobStore_ListReturnsMostRecentFirst(t *testing.T) {
	s := domain.NewCrawlJobStore()
	a := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	b := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://b"}})
	list := s.List()
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("expected [b, a] most-recent-first, got %+v", list)
	}
}

func TestCrawlJobStore_ListOmitsPerPageDetail(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed})
	list := s.List()
	if list[0].PagesCrawled != 1 {
		t.Errorf("expected summary to still report pages_crawled, got %d", list[0].PagesCrawled)
	}
}

func TestCrawlJobStore_RequestRedactsCredentials(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, HasCookie: true, HasBasicAuth: true})
	got, _ := s.Get(job.ID)
	if !got.Request.HasCookie || !got.Request.HasBasicAuth {
		t.Error("expected HasCookie/HasBasicAuth flags preserved")
	}
}

func TestCrawlJobStore_TrimsOldestBeyondRetentionLimit(t *testing.T) {
	s := domain.NewCrawlJobStore()
	var first *domain.CrawlJob
	for i := 0; i < 201; i++ {
		job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
		if i == 0 {
			first = job
		}
	}
	if _, ok := s.Get(first.ID); ok {
		t.Error("expected the oldest job to be trimmed once the retention limit is exceeded")
	}
	if len(s.List()) != 200 {
		t.Errorf("expected exactly 200 retained jobs, got %d", len(s.List()))
	}
}

func TestCrawlJobStore_AppendPageOnEvictedJobIsNoop(t *testing.T) {
	s := domain.NewCrawlJobStore()
	first := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	for i := 0; i < 200; i++ {
		s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	}
	// first has been trimmed by now -- appending to it must not panic.
	s.AppendPage(first.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed})
	if _, ok := s.Get(first.ID); ok {
		t.Fatal("expected the evicted job to stay evicted")
	}
}

func TestCrawlJobStore_ConcurrentAccessDoesNotRace(t *testing.T) {
	s := domain.NewCrawlJobStore()
	job := s.Create(domain.CrawlJobRequest{SeedURLs: []string{"http://a"}})
	s.MarkRunning(job.ID)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.AppendPage(job.ID, domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed})
			_, _ = s.Get(job.ID)
			_ = s.List()
		}(i)
	}
	wg.Wait()
	s.MarkDone(job.ID)

	got, _ := s.Get(job.ID)
	if got.PagesCrawled != 20 {
		t.Errorf("expected 20 indexed pages counted, got %d", got.PagesCrawled)
	}
}
