package restapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestRoutesCrawlInternal_NoTokenConfiguredAllowsAllRequests(t *testing.T) {
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore()})
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when no token is configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoutesCrawlInternal_TokenConfigured_RejectsMissingHeader(t *testing.T) {
	h := restapi.New(restapi.Config{
		Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore(),
		CrawlInternalToken: "shared-secret",
	})
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no token header, got %d", rec.Code)
	}
}

func TestRoutesCrawlInternal_TokenConfigured_RejectsWrongToken(t *testing.T) {
	h := restapi.New(restapi.Config{
		Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore(),
		CrawlInternalToken: "shared-secret",
	})
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("X-Internal-Token", "wrong")
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with a wrong token, got %d", rec.Code)
	}
}

func TestRoutesCrawlInternal_TokenConfigured_AcceptsCorrectToken(t *testing.T) {
	h := restapi.New(restapi.Config{
		Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore(),
		CrawlInternalToken: "shared-secret",
	})
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("X-Internal-Token", "shared-secret")
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with the correct token, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRoutesCrawlInternal_TokenConfigured_HealthzStaysOpen proves /healthz
// is exempt from the token check -- monitoring shouldn't need a credential
// to check liveness, same as every other Routes* mux in this codebase.
func TestRoutesCrawlInternal_TokenConfigured_HealthzStaysOpen(t *testing.T) {
	h := restapi.New(restapi.Config{
		Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore(),
		CrawlInternalToken: "shared-secret",
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected /healthz to stay open with no token header, got %d", rec.Code)
	}
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
	createErr, markRunningErr, appendPageErr, markDoneErr, markFailedErr, getErr, listErr, deleteEndedErr error
	createCalls, markRunningCalls, appendPageCalls, markDoneCalls, markFailedCalls                        int32
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
func (e *erroringCrawlJobStore) DeleteEndedCrawlJobs(ctx context.Context) (int, error) {
	if e.deleteEndedErr != nil {
		return 0, e.deleteEndedErr
	}
	return e.CrawlJobStore.DeleteEndedCrawlJobs(ctx)
}

// startCrawl calls TriggerScheduledCrawl directly -- crawl-server's own
// scheduler ticker is the only real caller now (there's no more
// admin-facing "just start a crawl directly" HTTP path -- see
// application.TriggerDueCrawls), so tests exercise the same entry point.
func startCrawl(t *testing.T, h *restapi.Handler, opts ports.CrawlOptions) string {
	t.Helper()
	jobID, err := h.TriggerScheduledCrawl(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("unexpected error starting crawl: %v", err)
	}
	if jobID == "" {
		t.Fatal("expected a non-empty job_id")
	}
	return jobID
}

// waitForJob polls GET /jobs/{id} through the real handler until the job
// reaches a terminal status, since the crawl itself runs in a background
// goroutine started by TriggerScheduledCrawl.
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

func TestHandleCrawlInternal_RecordsRendererOnTheJob(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{
		SeedURLs: []string{"http://a"}, Renderer: domain.RendererChromium,
	})
	job := waitForJob(t, h, jobID)

	if job.Request.Renderer != domain.RendererChromium {
		t.Errorf("expected the job to record renderer=chromium, got %+v", job.Request)
	}
	if fc.gotOptions.Renderer != domain.RendererChromium {
		t.Errorf("expected the crawler to receive renderer=chromium, got %+v", fc.gotOptions)
	}
}

func TestHandleCrawlInternal_RecordsLinkScopeAndSitemapOptionsOnTheJob(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{
		SeedURLs: []string{"http://a"}, LinkScope: domain.LinkScopeAny, UseSitemap: true,
	})
	job := waitForJob(t, h, jobID)

	if job.Request.LinkScope != domain.LinkScopeAny || !job.Request.UseSitemap {
		t.Errorf("expected the job to record link_scope/use_sitemap, got %+v", job.Request)
	}
	if fc.gotOptions.LinkScope != domain.LinkScopeAny || !fc.gotOptions.UseSitemap {
		t.Errorf("expected the crawler to receive link_scope/use_sitemap, got %+v", fc.gotOptions)
	}
}

// TestHandleCrawlInternal_RecordsAllowBlockDomainsAndFollowIndexedOnTheJob
// mirrors TestHandleCrawlInternal_RecordsLinkScopeAndSitemapOptionsOnTheJob
// for the new allow/block domain lists and FollowIndexedDomains.
func TestHandleCrawlInternal_RecordsAllowBlockDomainsAndFollowIndexedOnTheJob(t *testing.T) {
	fc := &fakeCrawler{count: 1}
	h := newCrawlServerHandler(fc)

	jobID := startCrawl(t, h, ports.CrawlOptions{
		SeedURLs:             []string{"http://a"},
		AllowedDomains:       []string{"allowed.example"},
		BlockedDomains:       []string{"blocked.example"},
		FollowIndexedDomains: true,
	})
	job := waitForJob(t, h, jobID)

	if len(job.Request.AllowedDomains) != 1 || job.Request.AllowedDomains[0] != "allowed.example" ||
		len(job.Request.BlockedDomains) != 1 || job.Request.BlockedDomains[0] != "blocked.example" ||
		!job.Request.FollowIndexedDomains {
		t.Errorf("expected the job to record allowed_domains/blocked_domains/follow_indexed_domains, got %+v", job.Request)
	}
	if len(fc.gotOptions.AllowedDomains) != 1 || fc.gotOptions.AllowedDomains[0] != "allowed.example" ||
		len(fc.gotOptions.BlockedDomains) != 1 || fc.gotOptions.BlockedDomains[0] != "blocked.example" ||
		!fc.gotOptions.FollowIndexedDomains {
		t.Errorf("expected the crawler to receive allowed_domains/blocked_domains/follow_indexed_domains, got %+v", fc.gotOptions)
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

func TestHandleDeleteEndedCrawlJobs_Success(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{count: 1})
	doneID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	waitForJob(t, h, doneID)

	req := httptest.NewRequest(http.MethodDelete, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Removed < 1 {
		t.Errorf("expected at least 1 ended job removed, got %d", resp.Removed)
	}
}

func TestHandleDeleteEndedCrawlJobs_StoreErrorReturns500(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), deleteEndedErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: store})

	req := httptest.NewRequest(http.MethodDelete, "/jobs", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when the store fails to delete ended jobs, got %d: %s", rec.Code, rec.Body.String())
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

// blockingCrawler blocks until its context is cancelled (or released),
// returning ctx.Err() -- the stand-in for a real crawl in flight when a
// test needs to cancel a job while it's actually running (or still queued
// behind maxConcurrentCrawls). started is closed the instant Crawl is
// entered, so a test can tell "queued, never started" apart from "running,
// then cancelled".
type blockingCrawler struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingCrawler() *blockingCrawler {
	return &blockingCrawler{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingCrawler) Crawl(ctx context.Context, _ ports.CrawlOptions, _ func(domain.CrawlPageEvent)) (int, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.release:
		return 0, nil
	}
}

// TestCancelCrawlJob_StopsARunningJob proves cancelling a job that's
// already fetching stops it promptly (via crawlLoop/the fetch's own
// context, not just a flag runCrawlJob happens to check later) and records
// it as cancelled, distinct from failed.
func TestCancelCrawlJob_StopsARunningJob(t *testing.T) {
	bc := newBlockingCrawler()
	h := restapi.New(restapi.Config{Crawler: bc, CrawlJobs: domain.NewCrawlJobStore()})
	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})

	select {
	case <-bc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the crawl to start")
	}

	if !h.CancelCrawlJob(jobID) {
		t.Fatal("expected CancelCrawlJob to find and cancel the running job")
	}

	job := waitForJobStatus(t, h, jobID, domain.CrawlJobCancelled)
	if job.FinishedAt == nil {
		t.Error("expected FinishedAt to be set on a cancelled job")
	}
}

// TestCancelCrawlJob_StopsAQueuedJob proves a job cancelled before it ever
// acquires crawlSem never calls Crawl at all -- cancellation works on the
// queue, not just on an already-running fetch. All of crawlSem's
// concurrency slots (3 by default, see crawlConcurrencySemaphore in
// crawl_internal.go -- Config.OpSettings is nil in this test, so that
// default applies) are filled with jobs that never release, so one more
// job queues behind crawlSem instead of running immediately.
func TestCancelCrawlJob_StopsAQueuedJob(t *testing.T) {
	const maxConcurrentCrawlsForTest = 3
	dispatch := &dispatchingCrawler{}
	h := restapi.New(restapi.Config{Crawler: dispatch, CrawlJobs: domain.NewCrawlJobStore()})

	blockers := make([]*blockingCrawler, maxConcurrentCrawlsForTest)
	for i := range blockers {
		blockers[i] = newBlockingCrawler()
		dispatch.push(blockers[i])
		startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	}
	for _, bc := range blockers {
		select {
		case <-bc.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a blocking slot to start")
		}
	}
	defer func() {
		for _, bc := range blockers {
			close(bc.release)
		}
	}()

	queuedCrawler := newBlockingCrawler()
	dispatch.push(queuedCrawler)
	queuedJobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://b"}})

	// Give the queued job a moment to (incorrectly) start if cancellation
	// didn't actually stop it before crawlSem admitted it.
	time.Sleep(20 * time.Millisecond)

	if !h.CancelCrawlJob(queuedJobID) {
		t.Fatal("expected CancelCrawlJob to find and cancel the queued job")
	}
	waitForJobStatus(t, h, queuedJobID, domain.CrawlJobCancelled)

	select {
	case <-queuedCrawler.started:
		t.Error("expected the queued job's Crawl to never be entered once cancelled")
	default:
	}
}

// TestRunCrawlJob_RespectsConfiguredMaxConcurrentCrawls proves a
// higher-than-default OperationalSettingsValues.MaxConcurrentCrawls is
// honored: 5 blocking jobs all start concurrently (none queue), where the
// old hard-coded 3 would have queued the last 2.
func TestRunCrawlJob_RespectsConfiguredMaxConcurrentCrawls(t *testing.T) {
	const limit = 5
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MaxConcurrentCrawls: limit})
	dispatch := &dispatchingCrawler{}
	h := restapi.New(restapi.Config{Crawler: dispatch, CrawlJobs: domain.NewCrawlJobStore(), OpSettings: opSettings})

	blockers := make([]*blockingCrawler, limit)
	for i := range blockers {
		blockers[i] = newBlockingCrawler()
		dispatch.push(blockers[i])
		startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	}
	defer func() {
		for _, bc := range blockers {
			close(bc.release)
		}
	}()
	for i, bc := range blockers {
		select {
		case <-bc.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for blocking slot %d to start -- expected all %d to run concurrently", i, limit)
		}
	}
}

// TestRunCrawlJob_MaxConcurrentCrawlsResizeAdmitsAlreadyQueuedJob proves
// raising MaxConcurrentCrawls while a job is already queued behind the
// old (smaller) limit still helps that job -- not just jobs that start
// queuing afterward -- since crawlConcurrencySemaphore.acquire re-checks
// the configured limit periodically rather than only once per queued
// call.
func TestRunCrawlJob_MaxConcurrentCrawlsResizeAdmitsAlreadyQueuedJob(t *testing.T) {
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MaxConcurrentCrawls: 2})
	dispatch := &dispatchingCrawler{}
	h := restapi.New(restapi.Config{Crawler: dispatch, CrawlJobs: domain.NewCrawlJobStore(), OpSettings: opSettings})

	blockers := make([]*blockingCrawler, 2)
	for i := range blockers {
		blockers[i] = newBlockingCrawler()
		dispatch.push(blockers[i])
		startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	}
	defer func() {
		for _, bc := range blockers {
			close(bc.release)
		}
	}()
	for _, bc := range blockers {
		select {
		case <-bc.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a blocking slot to start")
		}
	}

	queued := newBlockingCrawler()
	dispatch.push(queued)
	startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://b"}})

	select {
	case <-queued.started:
		t.Fatal("expected the third job to queue behind the limit of 2, not start immediately")
	case <-time.After(100 * time.Millisecond):
	}

	opSettings.Set(domain.OperationalSettingsValues{MaxConcurrentCrawls: 3})

	select {
	case <-queued.started:
	case <-time.After(3 * time.Second):
		t.Fatal("expected the already-queued job to start once the limit was raised to 3")
	}
	close(queued.release)
}

// dispatchingCrawler hands out a queue of *blockingCrawler in FIFO order,
// one per Crawl call -- lets a test control exactly which call gets which
// controllable fake, needed when several jobs are in flight against
// crawlSem at once.
type dispatchingCrawler struct {
	mu    sync.Mutex
	queue []*blockingCrawler
}

func (d *dispatchingCrawler) push(bc *blockingCrawler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queue = append(d.queue, bc)
}

func (d *dispatchingCrawler) Crawl(ctx context.Context, opts ports.CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error) {
	d.mu.Lock()
	bc := d.queue[0]
	d.queue = d.queue[1:]
	d.mu.Unlock()
	return bc.Crawl(ctx, opts, onPage)
}

// waitForJobStatus polls GET /jobs/{id} until it reaches want, failing the
// test if it times out or reaches a different terminal status first.
func waitForJobStatus(t *testing.T, h *restapi.Handler, jobID string, want domain.CrawlJobStatus) domain.CrawlJob {
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
		if job.Status == want {
			return job
		}
		if job.Status == domain.CrawlJobDone || job.Status == domain.CrawlJobFailed {
			t.Fatalf("expected job status %s, got terminal status %s instead", want, job.Status)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for crawl job to reach status %s", want)
	return domain.CrawlJob{}
}

// TestCancelCrawlJob_UnknownJobReturnsFalse proves cancelling a job ID this
// process never registered a cancel func for (never existed, or already
// finished and was unregistered) is a clean no-op, not a panic.
func TestCancelCrawlJob_UnknownJobReturnsFalse(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	if h.CancelCrawlJob("does-not-exist") {
		t.Error("expected CancelCrawlJob to report false for an unknown job")
	}
}

func TestHandleCancelCrawlJob_NotFound(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{})
	req := httptest.NewRequest(http.MethodPost, "/jobs/does-not-exist/cancel", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCancelCrawlJob_AlreadyFinishedConflicts proves cancelling a job
// that already reached a terminal status (nothing left to cancel) is a 409,
// not silently a 200 or a 404 (it does exist).
func TestHandleCancelCrawlJob_AlreadyFinishedConflicts(t *testing.T) {
	h := newCrawlServerHandler(&fakeCrawler{count: 1})
	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	waitForJob(t, h, jobID)

	req := httptest.NewRequest(http.MethodPost, "/jobs/"+jobID+"/cancel", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCancelCrawlJob_Success(t *testing.T) {
	bc := newBlockingCrawler()
	h := restapi.New(restapi.Config{Crawler: bc, CrawlJobs: domain.NewCrawlJobStore()})
	jobID := startCrawl(t, h, ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	select {
	case <-bc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the crawl to start")
	}

	req := httptest.NewRequest(http.MethodPost, "/jobs/"+jobID+"/cancel", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	waitForJobStatus(t, h, jobID, domain.CrawlJobCancelled)
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

// TestTriggerScheduledCrawl_CallsOnDoneAfterSuccess proves the scheduler's
// completion callback fires only once the job actually finishes (not the
// instant it's started) -- application.TriggerDueCrawls relies on this to
// correct a schedule's next_run_at to reflect the real finish time.
func TestTriggerScheduledCrawl_CallsOnDoneAfterSuccess(t *testing.T) {
	store := domain.NewCrawlJobStore()
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{count: 3}, CrawlJobs: store})

	done := make(chan struct{})
	jobID, err := h.TriggerScheduledCrawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}, func() { close(done) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onDone to be called")
	}

	job, err := store.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("unexpected error fetching job: %v", err)
	}
	if job.Status != domain.CrawlJobDone {
		t.Errorf("expected the job done by the time onDone fires, got %s", job.Status)
	}
}

// TestTriggerScheduledCrawl_CallsOnDoneAfterFailure proves onDone fires
// even when the crawl itself fails -- a schedule must still reschedule
// (rather than getting stuck retrying every tick forever) when the site
// it crawls starts erroring.
func TestTriggerScheduledCrawl_CallsOnDoneAfterFailure(t *testing.T) {
	store := domain.NewCrawlJobStore()
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{err: errors.New("fetch failed")}, CrawlJobs: store})

	done := make(chan struct{})
	jobID, err := h.TriggerScheduledCrawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}, func() { close(done) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onDone to be called")
	}

	job, err := store.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("unexpected error fetching job: %v", err)
	}
	if job.Status != domain.CrawlJobFailed {
		t.Errorf("expected the job failed by the time onDone fires, got %s", job.Status)
	}
}

func TestTriggerScheduledCrawl_EmptySeedURLsErrors(t *testing.T) {
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: domain.NewCrawlJobStore()})
	called := false
	_, err := h.TriggerScheduledCrawl(context.Background(), ports.CrawlOptions{}, func() { called = true })
	if err == nil {
		t.Fatal("expected an error for empty seed_urls")
	}
	if called {
		t.Error("expected onDone not to be called when no job was ever created")
	}
}

func TestTriggerScheduledCrawl_StoreCreateErrorPropagates(t *testing.T) {
	store := &erroringCrawlJobStore{CrawlJobStore: domain.NewCrawlJobStore(), createErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}, CrawlJobs: store})

	if _, err := h.TriggerScheduledCrawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}}, nil); err == nil {
		t.Error("expected the store's Create error to propagate")
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
