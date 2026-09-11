package restapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newCrawlServerHandler(crawler *fakeCrawler) *restapi.Handler {
	return restapi.New(restapi.Config{Crawler: crawler, CrawlJobs: domain.NewCrawlJobStore()})
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
