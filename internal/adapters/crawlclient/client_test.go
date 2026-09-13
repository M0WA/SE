package crawlclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestClient_ListCrawlJobs_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/jobs" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]domain.CrawlJobSummary{
			{ID: "job-1", Status: domain.CrawlJobDone, PagesCrawled: 3},
		})
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	jobs, err := c.ListCrawlJobs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "job-1" || jobs[0].PagesCrawled != 3 {
		t.Errorf("unexpected jobs list: %+v", jobs)
	}
}

func TestClient_ListCrawlJobs_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.ListCrawlJobs(context.Background()); err == nil {
		t.Error("expected error for a non-200 response")
	}
}

func TestClient_ListCrawlJobs_InvalidResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.ListCrawlJobs(context.Background()); err == nil {
		t.Error("expected error for an invalid response body")
	}
}

func TestClient_ListCrawlJobs_ConnectionError(t *testing.T) {
	c := crawlclient.New("http://127.0.0.1:1")
	if _, err := c.ListCrawlJobs(context.Background()); err == nil {
		t.Error("expected error for an unreachable crawl server")
	}
}

func TestClient_GetCrawlJob_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/jobs/job-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(domain.CrawlJob{
			ID: "job-1", Status: domain.CrawlJobDone,
			Pages: []domain.CrawlPageEvent{{URL: "http://a", Status: domain.CrawlPageIndexed}},
		})
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	job, err := c.GetCrawlJob(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.ID != "job-1" || len(job.Pages) != 1 {
		t.Errorf("unexpected job detail: %+v", job)
	}
}

func TestClient_GetCrawlJob_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "crawl job not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.GetCrawlJob(context.Background(), "missing"); !errors.Is(err, ports.ErrCrawlJobNotFound) {
		t.Errorf("expected ErrCrawlJobNotFound, got %v", err)
	}
}

func TestClient_GetCrawlJob_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.GetCrawlJob(context.Background(), "job-1"); err == nil {
		t.Error("expected error for a non-200/404 response")
	}
}

func TestClient_GetCrawlJob_InvalidResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := crawlclient.New(srv.URL)
	if _, err := c.GetCrawlJob(context.Background(), "job-1"); err == nil {
		t.Error("expected error for an invalid response body")
	}
}

func TestClient_GetCrawlJob_ConnectionError(t *testing.T) {
	c := crawlclient.New("http://127.0.0.1:1")
	if _, err := c.GetCrawlJob(context.Background(), "job-1"); err == nil {
		t.Error("expected error for an unreachable crawl server")
	}
}

func TestClient_GetCrawlJob_InvalidBaseURL(t *testing.T) {
	c := crawlclient.New("http://\x7f")
	if _, err := c.GetCrawlJob(context.Background(), "job-1"); err == nil {
		t.Error("expected error when the base URL contains an invalid control character")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type erroringReadCloser struct{}

func (erroringReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (erroringReadCloser) Close() error             { return nil }

func TestClient_ListCrawlJobs_ResponseBodyReadError(t *testing.T) {
	c := crawlclient.New("http://crawl-server.invalid")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: erroringReadCloser{}, Header: make(http.Header)}, nil
	})}
	if _, err := c.ListCrawlJobs(context.Background()); err == nil {
		t.Error("expected error when the response body fails to read")
	}
}
