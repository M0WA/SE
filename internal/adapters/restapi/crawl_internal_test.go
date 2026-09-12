package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newCrawlServerHandler(crawler *fakeCrawler) *restapi.Handler {
	return restapi.New(restapi.Config{Crawler: crawler, CrawlJobs: domain.NewCrawlJobStore()})
}

// erroringCrawlJobStore wraps a real ports.CrawlJobStore, letting a test
// force any one method to fail (and counting how many times it was
// called) while every other method still call through normally -- proves
// both that TriggerCrawl/handleListCrawlJobs/handleGetCrawlJob surface a
// store error as a 500 (or 400, for TriggerCrawl) rather than crashing,
// and that runCrawlJob logs and continues past a store error mid-crawl
// rather than losing already-crawled pages over it.
type erroringCrawlJobStore struct {
	ports.CrawlJobStore
	createErr, markRunningErr, appendPageErr, markDoneErr, markFailedErr, getErr, listErr error
	createCalls, markRunningCalls, appendPageCalls, markDoneCalls, markFailedCalls        int32
}

func (e *erroringCrawlJobStore) Create(ctx context.Context, req domain.CrawlJobRequest) (domain.CrawlJob, error) {
	atomic.AddInt32(&e.createCalls, 1)
	if e.createErr != nil {
		return domain.CrawlJob{}, e.createErr
	}
	return e.CrawlJobStore.Create(ctx, req)
}
func (e *erroringCrawlJobStore) MarkRunning(ctx context.Context, id string) error {
	atomic.AddInt32(&e.markRunningCalls, 1)
	if e.markRunningErr != nil {
		return e.markRunningErr
	}
	return e.CrawlJobStore.MarkRunning(ctx, id)
}
func (e *erroringCrawlJobStore) AppendPage(ctx context.Context, id string, ev domain.CrawlPageEvent) error {
	atomic.AddInt32(&e.appendPageCalls, 1)
	if e.appendPageErr != nil {
		return e.appendPageErr
	}
	return e.CrawlJobStore.AppendPage(ctx, id, ev)
}
func (e *erroringCrawlJobStore) MarkDone(ctx context.Context, id string) error {
	atomic.AddInt32(&e.markDoneCalls, 1)
	if e.markDoneErr != nil {
		return e.markDoneErr
	}
	return e.CrawlJobStore.MarkDone(ctx, id)
}
func (e *erroringCrawlJobStore) MarkFailed(ctx context.Context, id string, failErr error) error {
	atomic.AddInt32(&e.markFailedCalls, 1)
	if e.markFailedErr != nil {
		return e.markFailedErr
	}
	return e.CrawlJobStore.MarkFailed(ctx, id, failErr)
}
func (e *erroringCrawlJobStore) Get(ctx context.Context, id string) (domain.CrawlJob, error) {
	if e.getErr != nil {
		return domain.CrawlJob{}, e.getErr
	}
	return e.CrawlJobStore.Get(ctx, id)
}
func (e *erroringCrawlJobStore) List(ctx context.Context) ([]domain.CrawlJobSummary, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}
	return e.CrawlJobStore.List(ctx)
}

func startCrawl(t *testing.T, h *restapi.Handler, opts ports.CrawlOptions) string {
	t.Helper()
	body, _ := json.Marshal(opts)
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["job_id"] == "" {
		t.Fatal("expected a non-empty job_id")
	}
	return resp["job_id"]
}

// waitForJob polls GET /jobs/{id} through the real handler until the job
// reaches a terminal status, since the crawl itself runs in a background
// goroutine started by handleCrawl.
func waitForJob(t *testing.T, h *restapi.Handler, jobID string) domain.CrawlJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/jobs/"+jobID, nil)
		rec := httptest.NewRecorder()
		h.RoutesCrawlInternal().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 fetching job, got %d: %s", rec.Code, rec.Body.String())
		}
		var job domain.CrawlJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
			t.Fatalf("decoding job: %v", err)
		}
		if job.Status == domain.CrawlJobDone || job.Status == domain.CrawlJobFailed {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for crawl job to finish")
	return domain.CrawlJob{}
}

func TestHandleCrawlInternal_Success(t *testing.T) {
	fc := &fakeCrawler{count: 7}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5})
	job := waitForJob(t, h, jobID)

	if job.Status != domain.CrawlJobDone {
		t.Fatalf("expected job done, got %s (err=%s)", job.Status, job.Error)
	}
	if job.StartedAt == nil || job.FinishedAt == nil {
		t.Error("expected StartedAt and FinishedAt to be set on a finished job")
	}
	if len(fc.gotOptions.SeedURLs) != 1 || fc.gotOptions.SeedURLs[0] != "http://a" || fc.gotOptions.MaxPages != 5 {
		t.Errorf("unexpected options passed to crawler: %+v", fc.gotOptions)
	}
}

func TestHandleCrawlInternal_CallsOnCrawlCompleteOnSuccess(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	called := make(chan struct{}, 1)
	h := restapi.New(restapi.Config{
		Crawler:         fc,
		CrawlJobs:       domain.NewCrawlJobStore(),
		OnCrawlComplete: func() { called <- struct{}{} },
	})

	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	waitForJob(t, h, jobID)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("expected OnCrawlComplete to be called after a successful crawl")
	}
}

func TestHandleCrawlInternal_DoesNotCallOnCrawlCompleteOnFailure(t *testing.T) {
	fc := &fakeCrawler{err: errors.New("boom")}
	called := make(chan struct{}, 1)
	h := restapi.New(restapi.Config{
		Crawler:         fc,
		CrawlJobs:       domain.NewCrawlJobStore(),
		OnCrawlComplete: func() { called <- struct{}{} },
	})

	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	waitForJob(t, h, jobID)

	select {
	case <-called:
		t.Fatal("expected OnCrawlComplete not to be called after a failed crawl")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHandleCrawlInternal_RecordsRespectRobotsAndUserAgentOnTheJob(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{
		SeedURLs: []string{"http://a"}, RespectRobots: true, UserAgent: "custom-bot/1.0",
	})
	job := waitForJob(t, h, jobID)

	if !job.Request.RespectRobots || job.Request.UserAgent != "custom-bot/1.0" {
		t.Errorf("expected the job to record respect_robots/user_agent, got %+v", job.Request)
	}
	if fc.gotOptions.UserAgent != "custom-bot/1.0" || !fc.gotOptions.RespectRobots {
		t.Errorf("expected the crawler to receive respect_robots/user_agent, got %+v", fc.gotOptions)
	}
}

func TestHandleCrawlInternal_RecordsOffDomainAndSitemapOptionsOnTheJob(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{
		SeedURLs: []string{"http://a"}, AllowOffDomainLinks: true, UseSitemap: true,
	})
	job := waitForJob(t, h, jobID)

	if !job.Request.AllowOffDomainLinks || !job.Request.UseSitemap {
		t.Errorf("expected the job to record allow_off_domain_links/use_sitemap, got %+v", job.Request)
	}
	if !fc.gotOptions.AllowOffDomainLinks || !fc.gotOptions.UseSitemap {
		t.Errorf("expected the crawler to receive allow_off_domain_links/use_sitemap, got %+v", fc.gotOptions)
	}
}

func TestHandleCrawlInternal_InvalidJSON(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader([]byte("{ungültig")))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCrawlInternal_EmptySeedURLs(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	body, _ := json.Marshal(ports.CrawlOptions{SeedURLs: []string{}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCrawlInternal_ServiceError(t *testing.T) {
	fc := &fakeCrawler{err: errors.New("crawl failed")}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	job := waitForJob(t, h, jobID)

	if job.Status != domain.CrawlJobFailed {
		t.Fatalf("expected job failed, got %s", job.Status)
	}
	if job.Error != "crawl failed" {
		t.Errorf("expected error message recorded, got %q", job.Error)
	}
}

func TestHandleCrawlInternal_MethodNotAllowed(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/crawl", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleListCrawlJobs_Success(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{count: 1})
	startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://b"}})

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var jobs []domain.CrawlJobSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decoding jobs list: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs listed, got %d", len(jobs))
	}
}

func TestHandleGetCrawlJob_NotFound(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/jobs/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleHealthz_CrawlServer_HealthyByDefault(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz_CrawlServer_UnhealthyWhenDBPingFails(t *testing.T) {
	h := restapi.New(restapi.Config{
		Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore(),
		Health: &fakeHealthChecker{err: errors.New("db unreachable")},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCrawlInternal_ConcurrentJobsAllComplete(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{count: 1})
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}}))
	}
	for _, id := range ids {
		job := waitForJob(t, h, id)
		if job.Status != domain.CrawlJobDone {
			t.Errorf("expected job %s done, got %s", id, job.Status)
		}
	}
}

// pageEmittingFakeCrawler actually invokes onPage (unlike fakeCrawler,
// which never does), for tests that need runCrawlJob's AppendPage call to
// actually fire.
type pageEmittingFakeCrawler struct{}

func (pageEmittingFakeCrawler) Crawl(_ context.Context, _ ports.CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error) {
	onPage(domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed})
	return 1, nil
}

// TestResumeCrawlJob_RunsUnderTheSameExistingJobID proves ResumeCrawlJob
// (used by application.RecoverInterruptedCrawls) reuses the given job ID
// rather than creating a new one -- a pre-existing job (simulating one
// left "running" by a crawl-server restart) reaches CrawlJobDone under
// its own ID, and no second job is ever created.
func TestResumeCrawlJob_RunsUnderTheSameExistingJobID(t *testing.T) {
	store := domain.NewCrawlJobStore()
	existing, err := store.Create(context.Background(), domain.CrawlJobRequest{SeedURLs: []string{"http://a"}, MaxPages: 5})
	if err != nil {
		t.Fatalf("unexpected error seeding the existing job: %v", err)
	}
	if err := store.MarkRunning(context.Background(), existing.ID); err != nil {
		t.Fatalf("unexpected error marking the seeded job running: %v", err)
	}

	fc := &fakeCrawler{count: 3}
	h := restapi.New(restapi.Config{Crawler: fc, CrawlJobs: store})

	h.ResumeCrawlJob(existing.ID, ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5})
	job := waitForJob(t, h, existing.ID)

	if job.ID != existing.ID {
		t.Fatalf("expected the resumed job to keep its original ID %s, got %s", existing.ID, job.ID)
	}
	if job.Status != domain.CrawlJobDone {
		t.Fatalf("expected the resumed job done, got %s (err=%s)", job.Status, job.Error)
	}

	all, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error listing jobs: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected exactly 1 job to exist after resuming (no new job created), got %d: %+v", len(all), all)
	}
}

func TestTriggerCrawl_StoreCreateErrorPropagates(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), createErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: store})

	body, _ := json.Marshal(ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when the store fails to create a job, got %d: %s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&store.createCalls) != 1 {
		t.Errorf("expected exactly 1 Create call, got %d", atomic.LoadInt32(&store.createCalls))
	}
}

func TestHandleListCrawlJobs_StoreErrorReturns500(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), listErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: store})

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when the store fails to list jobs, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetCrawlJob_StoreErrorReturns500(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), getErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: store})

	req := httptest.NewRequest(http.MethodGet, "/jobs/job-1", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for a non-not-found store error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRunCrawlJob_StoreErrorsAreLoggedNotFatal proves runCrawlJob's own
// doc comment: a store failure on any single bookkeeping call (marking
// running, appending a page, marking done) is logged and the crawl
// continues to completion rather than losing already-crawled pages over
// it.
func TestRunCrawlJob_StoreErrorsAreLoggedNotFatal(t *testing.T) {
	store := &erroringCrawlJobStore{
		CrawlJobStore:  domain.NewCrawlJobStore(),
		markRunningErr: errors.New("mark running failed"),
		appendPageErr:  errors.New("append page failed"),
	}
	h := restapi.New(restapi.Config{Crawler: pageEmittingFakeCrawler{}, CrawlJobs: store})

	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	job := waitForJob(t, h, jobID)

	if job.Status != domain.CrawlJobDone {
		t.Errorf("expected the crawl to still complete despite MarkRunning/AppendPage errors, got %s", job.Status)
	}
	if atomic.LoadInt32(&store.markRunningCalls) != 1 {
		t.Errorf("expected exactly 1 MarkRunning call, got %d", atomic.LoadInt32(&store.markRunningCalls))
	}
	if atomic.LoadInt32(&store.appendPageCalls) != 1 {
		t.Errorf("expected exactly 1 AppendPage call, got %d", atomic.LoadInt32(&store.appendPageCalls))
	}
	if atomic.LoadInt32(&store.markDoneCalls) != 1 {
		t.Errorf("expected MarkDone still called despite the earlier errors, got %d", atomic.LoadInt32(&store.markDoneCalls))
	}
}

// TestRunCrawlJob_MarkDoneStoreErrorIsLoggedNotFatal exercises the
// MarkDone-fails-on-an-otherwise-successful-crawl branch: logged, not
// fatal -- the crawl goroutine still exits cleanly rather than panicking
// or hanging.
func TestRunCrawlJob_MarkDoneStoreErrorIsLoggedNotFatal(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), markDoneErr: errors.New("mark done failed")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{count: 1}, CrawlJobs: store})

	startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&store.markDoneCalls) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(&store.markDoneCalls) != 1 {
		t.Errorf("expected exactly 1 MarkDone call, got %d", atomic.LoadInt32(&store.markDoneCalls))
	}
}

// TestRunCrawlJob_MarkFailedStoreErrorIsLoggedNotFatal exercises the
// MarkFailed-also-errors branch: a crawl that itself fails, on a store
// that also fails to record the failure, must not panic or hang.
func TestRunCrawlJob_MarkFailedStoreErrorIsLoggedNotFatal(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), markFailedErr: errors.New("mark failed failed")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{err: errors.New("crawl failed")}, CrawlJobs: store})

	startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&store.markFailedCalls) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if atomic.LoadInt32(&store.markFailedCalls) != 1 {
		t.Errorf("expected exactly 1 MarkFailed call, got %d", atomic.LoadInt32(&store.markFailedCalls))
	}
}
