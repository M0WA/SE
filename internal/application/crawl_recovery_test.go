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
func (f *fakeCrawlJobStore) MarkDone(_ context.Context, id string) error {
	j := f.jobs[id]
	j.Status = domain.CrawlJobDone
	f.jobs[id] = j
	return nil
}
func (f *fakeCrawlJobStore) MarkFailed(_ context.Context, id string, failErr error) error {
	f.markFailed[id] = failErr
	j := f.jobs[id]
	j.Status = domain.CrawlJobFailed
	j.Error = failErr.Error()
	f.jobs[id] = j
	return nil
}
func (f *fakeCrawlJobStore) MarkCancelled(context.Context, string) error {
	return errors.New("not implemented")
}
func (f *fakeCrawlJobStore) DeleteEndedCrawlJobs(context.Context) (int, error) {
	return 0, errors.New("not implemented")
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

// TestRecoverInterruptedCrawls_ResumesQueuedAndRunningJobsInPlace proves the
// core behavior: an interrupted job without credentials is resumed under
// its own existing ID (via resume), never marked failed and never
// replaced by a separate new job -- so it keeps looking like the same
// crawl continuing, not a failure followed by an unrelated one starting
// over from zero.
func TestRecoverInterruptedCrawls_ResumesQueuedAndRunningJobsInPlace(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-queued", Status: domain.CrawlJobQueued, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 5}},
		domain.CrawlJobSummary{ID: "job-running", Status: domain.CrawlJobRunning, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://b"}, MaxPages: 10}},
		domain.CrawlJobSummary{ID: "job-done", Status: domain.CrawlJobDone, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://c"}}},
		domain.CrawlJobSummary{ID: "job-failed", Status: domain.CrawlJobFailed, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://d"}}},
	)

	resumedIDs := make(map[string]ports.CrawlOptions)
	resume := func(jobID string, opts ports.CrawlOptions) {
		resumedIDs[jobID] = opts
	}

	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 2 || abandoned != 0 {
		t.Fatalf("expected 2 recovered, 0 abandoned, got recovered=%d abandoned=%d", recovered, abandoned)
	}
	if len(resumedIDs) != 2 {
		t.Fatalf("expected 2 jobs resumed, got %d", len(resumedIDs))
	}
	if _, ok := resumedIDs["job-queued"]; !ok {
		t.Error("expected job-queued resumed under its own ID")
	}
	if _, ok := resumedIDs["job-running"]; !ok {
		t.Error("expected job-running resumed under its own ID")
	}
	for id, opts := range resumedIDs {
		if !opts.PrioritizeUnindexed {
			t.Errorf("expected PrioritizeUnindexed forced on for a recovery pass (job %s), got %+v", id, opts)
		}
	}

	// The resumed jobs must NOT be marked failed -- they're being resumed,
	// not abandoned, so their status stays whatever resume (runCrawlJob's
	// own MarkRunning) sets it to, not something this function overwrites.
	if store.jobs["job-queued"].Status == domain.CrawlJobFailed || store.jobs["job-running"].Status == domain.CrawlJobFailed {
		t.Errorf("expected resumed jobs never marked failed, got %+v / %+v", store.jobs["job-queued"], store.jobs["job-running"])
	}
	if len(store.markFailed) != 0 {
		t.Errorf("expected no MarkFailed calls for jobs that were resumed, got %+v", store.markFailed)
	}
	// Already-terminal jobs must be left alone entirely.
	if _, ok := resumedIDs["job-done"]; ok {
		t.Error("expected a done job never resumed")
	}
	if _, ok := resumedIDs["job-failed"]; ok {
		t.Error("expected an already-failed job never resumed")
	}
}

// TestRecoverInterruptedCrawls_AbandonsJobsThatNeededCredentials proves the
// safety boundary: a job whose original request needed a cookie or Basic
// auth is marked failed and never resumed, since those credentials were
// never persisted.
func TestRecoverInterruptedCrawls_AbandonsJobsThatNeededCredentials(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-cookie", Status: domain.CrawlJobRunning, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, HasCookie: true}},
		domain.CrawlJobSummary{ID: "job-basicauth", Status: domain.CrawlJobQueued, Request: domain.CrawlJobRequest{SeedURLs: []string{"http://b"}, HasBasicAuth: true}},
	)
	resumeCalls := 0
	resume := func(string, ports.CrawlOptions) { resumeCalls++ }

	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 0 || abandoned != 2 {
		t.Fatalf("expected 0 recovered, 2 abandoned, got recovered=%d abandoned=%d", recovered, abandoned)
	}
	if resumeCalls != 0 {
		t.Errorf("expected no resume for jobs that needed credentials, got %d calls", resumeCalls)
	}
	if store.jobs["job-cookie"].Status != domain.CrawlJobFailed || store.jobs["job-basicauth"].Status != domain.CrawlJobFailed {
		t.Errorf("expected both jobs marked failed since they can't be resumed, got %+v / %+v", store.jobs["job-cookie"], store.jobs["job-basicauth"])
	}
	if store.jobs["job-cookie"].Error != application.ErrCrawlInterruptedByRestart.Error() {
		t.Errorf("expected the interrupted-by-restart reason recorded, got %q", store.jobs["job-cookie"].Error)
	}
}

func TestRecoverInterruptedCrawls_NoInterruptedJobsIsANoOp(t *testing.T) {
	store := newFakeCrawlJobStore(
		domain.CrawlJobSummary{ID: "job-done", Status: domain.CrawlJobDone},
	)
	resumeCalls := 0
	resume := func(string, ports.CrawlOptions) { resumeCalls++ }
	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 0 || abandoned != 0 || resumeCalls != 0 {
		t.Errorf("expected a complete no-op, got recovered=%d abandoned=%d resumeCalls=%d", recovered, abandoned, resumeCalls)
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
			UserAgent: "custom/1.0", LinkScope: domain.LinkScopeAny, UseSitemap: true,
			FetchTimeoutSeconds: 45, MinTextLength: 100, CrawlDelayMs: 500, MaxResponseKB: 2048,
		},
	})
	var got ports.CrawlOptions
	resume := func(_ string, opts ports.CrawlOptions) { got = opts }
	if _, _, err := application.RecoverInterruptedCrawls(context.Background(), store, resume); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxPages != 7 || !got.RespectRobots || got.UserAgent != "custom/1.0" ||
		got.LinkScope != domain.LinkScopeAny || !got.UseSitemap || got.FetchTimeoutSeconds != 45 ||
		got.MinTextLength != 100 || got.CrawlDelayMs != 500 || got.MaxResponseKB != 2048 {
		t.Errorf("expected every setting override preserved on resume, got %+v", got)
	}
}

// TestRecoverInterruptedCrawls_ShrinksMaxPagesByPagesAlreadyCrawled proves
// the fix for a real bug: crawlLoop's own page counter starts back at zero
// every time a job is resumed, so without shrinking MaxPages first, a job
// interrupted partway through its budget would get handed that same full
// budget all over again -- surviving enough restarts, its total pages
// crawled could run many times past the limit it was configured with,
// while still reporting that original limit in its request.
func TestRecoverInterruptedCrawls_ShrinksMaxPagesByPagesAlreadyCrawled(t *testing.T) {
	store := newFakeCrawlJobStore(domain.CrawlJobSummary{
		ID: "job-partial", Status: domain.CrawlJobRunning,
		Request:      domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 10000},
		PagesCrawled: 6000,
	})
	var got ports.CrawlOptions
	resume := func(_ string, opts ports.CrawlOptions) { got = opts }
	recovered, _, err := application.RecoverInterruptedCrawls(context.Background(), store, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("expected 1 recovered, got %d", recovered)
	}
	if got.MaxPages != 4000 {
		t.Errorf("expected the remaining budget (10000-6000=4000), got MaxPages=%d", got.MaxPages)
	}
}

// TestRecoverInterruptedCrawls_MarksDoneRatherThanResumingOnceBudgetIsSpent
// proves the other half of that same fix: a job that had already reached
// (or, from an earlier restart, overshot) its MaxPages budget before this
// restart has no budget left to spend, so it's marked done outright
// instead of being resumed for yet another full pass.
func TestRecoverInterruptedCrawls_MarksDoneRatherThanResumingOnceBudgetIsSpent(t *testing.T) {
	store := newFakeCrawlJobStore(domain.CrawlJobSummary{
		ID: "job-exhausted", Status: domain.CrawlJobRunning,
		Request:      domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 10000},
		PagesCrawled: 15883,
	})
	resumeCalls := 0
	resume := func(string, ports.CrawlOptions) { resumeCalls++ }
	recovered, abandoned, err := application.RecoverInterruptedCrawls(context.Background(), store, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if recovered != 1 || abandoned != 0 {
		t.Fatalf("expected 1 recovered, 0 abandoned, got recovered=%d abandoned=%d", recovered, abandoned)
	}
	if resumeCalls != 0 {
		t.Errorf("expected no resume once the budget is already spent, got %d calls", resumeCalls)
	}
	if store.jobs["job-exhausted"].Status != domain.CrawlJobDone {
		t.Errorf("expected the job marked done, got status %q", store.jobs["job-exhausted"].Status)
	}
}

// TestRecoverInterruptedCrawls_UnboundedMaxPagesIsUntouched proves
// MaxPages<=0 (crawlLoop applies the operational default instead of an
// explicit cap) is passed through unchanged on resume -- there's no fixed
// budget here to shrink against, unlike the explicit-MaxPages case above.
func TestRecoverInterruptedCrawls_UnboundedMaxPagesIsUntouched(t *testing.T) {
	store := newFakeCrawlJobStore(domain.CrawlJobSummary{
		ID: "job-unbounded", Status: domain.CrawlJobRunning,
		Request:      domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 0},
		PagesCrawled: 500,
	})
	var got ports.CrawlOptions
	resume := func(_ string, opts ports.CrawlOptions) { got = opts }
	if _, _, err := application.RecoverInterruptedCrawls(context.Background(), store, resume); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxPages != 0 {
		t.Errorf("expected MaxPages left at 0 (unbounded/default), got %d", got.MaxPages)
	}
}
