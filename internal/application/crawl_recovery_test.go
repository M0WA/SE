package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeCrawlJobStore is an in-memory ports.CrawlJobStore -- enough to
// exercise RecoverInterruptedCrawls' status filtering, credential-aware
// abandon/resume split, and MarkFailed bookkeeping without a real database.
type fakeCrawlJobStore struct {
	jobs       map[string]domain.CrawlJobSummary
	listErr    error
	markFailed map[string]error // recorded per job ID as MarkFailed is called
}

func newFakeCrawlJobStore(jobs ...domain.CrawlJobSummary) *fakeCrawlJobStore {
	m := make(map[string]domain.CrawlJobSummary, len(jobs))
	for _, j := range jobs {
		m[j.ID] = j
	}
	return &fakeCrawlJobStore{jobs: m, markFailed: make(map[string]error)}
}

func (f *fakeCrawlJobStore) Create(context.Context, domain.CrawlJobRequest) (domain.CrawlJob, error) {
	return domain.CrawlJob{}, errors.New("not implemented")
}
func (f *fakeCrawlJobStore) MarkRunning(context.Context, string) error {
	return errors.New("not implemented")
}
func (f *fakeCrawlJobStore) AppendPage(context.Context, string, domain.CrawlPageEvent) error {
	return errors.New("not implemented")
}
func (f *fakeCrawlJobStore) MarkDone(context.Context, string) error {
	return errors.New("not implemented")
}
func (f *fakeCrawlJobStore) MarkFailed(_ context.Context, id string, failErr error) error {
	f.markFailed[id] = failErr
	j := f.jobs[id]
	j.Status = domain.CrawlJobFailed
	j.Error = failErr.Error()
	f.jobs[id] = j
	return nil
}
func (f *fakeCrawlJobStore) Get(_ context.Context, id string) (domain.CrawlJob, error) {
	j, ok := f.jobs[id]
	if !ok {
		return domain.CrawlJob{}, domain.ErrCrawlJobNotFound
	}
	return domain.CrawlJob{ID: j.ID, Request: j.Request, Status: j.Status, PagesCrawled: j.PagesCrawled, Error: j.Error}, nil
}
func (f *fakeCrawlJobStore) List(context.Context) ([]domain.CrawlJobSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.CrawlJobSummary, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, j)
	}
	return out, nil
}

func TestRecoverInterruptedCrawls_RestartsQueuedAndRunningJobsWithoutCredentials(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-queued", Status: domain.CrawlJobQueued, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 5}},
		domain.CrawlJobSummary{ID: "job-running", Status: domain.CrawlJobRunning, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://b"}, MaxPages: 10}},
		domain.CrawlJobSummary{ID: "job-done", Status: domain.CrawlJobDone, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://c"}}},
		domain.CrawlJobSummary{ID: "job-failed", Status: domain.CrawlJobFailed, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://d"}}},
	)

	var triggered []ports.CrawlOptions
	trigger := func(_ context.Context, opts ports.CrawlOptions) (string, error) {
		triggered = append(triggered, opts)
		return "new-job-id", nil
	}

	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 2 || abandoned != 0 {
		t.Fatalf("expected 2 recovered, 0 abandoned, got recovered=%d abandoned=%d", recovered, abandoned)
	}
	if len(triggered) != 2 {
		t.Fatalf("expected 2 triggered crawls, got %d", len(triggered))
	}
	for _, opts := range triggered {
		if !opts.PrioritizeUnindexed {
			t.Errorf("expected PrioritizeUnindexed forced on for a recovery pass, got %+v", opts)
		}
	}

	// The old queued/running jobs must be marked failed with the
	// interrupted-by-restart reason, not left showing as active forever.
	if store.jobs["job-queued"].Status != domain.CrawlJobFailed || store.jobs["job-running"].Status != domain.CrawlJobFailed {
		t.Errorf("expected the old interrupted jobs marked failed, got %+v / %+v", store.jobs["job-queued"], store.jobs["job-running"])
	}
	// Already-terminal jobs must be left alone entirely.
	if _, ok := store.markFailed["job-done"]; ok {
		t.Error("expected a done job never touched")
	}
	if _, ok := store.markFailed["job-failed"]; ok {
		t.Error("expected an already-failed job never re-marked")
	}
}

// TestRecoverInterruptedCrawls_AbandonsJobsThatNeededCredentials proves the
// safety boundary: a job whose original request needed a cookie or Basic
// auth is marked failed but never automatically re-triggered, since those
// credentials were never persisted.
func TestRecoverInterruptedCrawls_AbandonsJobsThatNeededCredentials(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-cookie", Status: domain.CrawlJobRunning, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, HasCookie: true}},
		domain.CrawlJobSummary{ID: "job-basicauth", Status: domain.CrawlJobQueued, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://b"}, HasBasicAuth: true}},
	)
	triggerCalls := 0
	trigger := func(context.Context, ports.CrawlOptions) (string, error) {
		triggerCalls++
		return "", nil
	}

	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 0 || abandoned != 2 {
		t.Fatalf("expected 0 recovered, 2 abandoned, got recovered=%d abandoned=%d", recovered, abandoned)
	}
	if triggerCalls != 0 {
		t.Errorf("expected no automatic re-trigger for jobs that needed credentials, got %d calls", triggerCalls)
	}
	if store.jobs["job-cookie"].Status != domain.CrawlJobFailed || store.jobs["job-basicauth"].Status != domain.CrawlJobFailed {
		t.Errorf("expected both jobs marked failed even though not resumed, got %+v / %+v", store.jobs["job-cookie"], store.jobs["job-basicauth"])
	}
}

func TestRecoverInterruptedCrawls_NoInterruptedJobsIsANoOp(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-done", Status: domain.CrawlJobDone},
	)
	triggerCalls := 0
	trigger := func(context.Context, ports.CrawlOptions) (string, error) {
		triggerCalls++
		return "", nil
	}
	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, trigger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 0 || abandoned != 0 || triggerCalls != 0 {
		t.Errorf("expected a complete no-op, got recovered=%d abandoned=%d triggerCalls=%d", recovered, abandoned, triggerCalls)
	}
}

func TestRecoverInterruptedCrawls_ListErrorPropagates(t *testing.T) {
	store := newFakeCrawlJobStore()
	store.listErr = errors.New("db unavailable")
	_, _, err := application.RecoverInterruptedCrawls(context.Background(), store, nil)
	if err == nil {
		t.Fatal("expected the List error to propagate")
	}
}

// TestRecoverInterruptedCrawls_PreservesOriginalSettingOverrides proves the
// reconstructed CrawlOptions carries every per-crawl setting override the
// original job's request had, not just SeedURLs/MaxPages.
func TestRecoverInterruptedCrawls_PreservesOriginalSettingOverrides(t *testing.T) {
	store := newFakeCrawlJobStore(domain.CrawlJobSummary{
		ID: "job-1", Status: domain.CrawlJobRunning,
		Request: domain.CrawlJobRequest{
			SeedURLs: []string{"http://a"}, MaxPages: 7, RespectRobots: true,
			UserAgent: "custom/1.0", AllowOffDomainLinks: true, UseSitemap: true,
			FetchTimeoutSeconds: 45, MinTextLength: 100, CrawlDelayMs: 500, MaxResponseKB: 2048,
		},
	})
	var got ports.CrawlOptions
	trigger := func(_ context.Context, opts ports.CrawlOptions) (string, error) {
		got = opts
		return "new-id", nil
	}
	if _, _, err := application.RecoverInterruptedCrawls(context.Background(), store, trigger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxPages != 7 || !got.RespectRobots || got.UserAgent != "custom/1.0" ||
		!got.AllowOffDomainLinks || !got.UseSitemap || got.FetchTimeoutSeconds != 45 ||
		got.MinTextLength != 100 || got.CrawlDelayMs != 500 || got.MaxResponseKB != 2048 {
		t.Errorf("expected every setting override preserved on resume, got %+v", got)
	}
}
