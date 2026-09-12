package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

var dsnCounter int64

// TestEndToEnd_CrawlThenSearch exercises the exact same wiring the three
// production binaries use -- application.NewSQLCrawlerService,
// application.NewHybridAsSearchService, and sqlrepo.Repository (SQLite
// in-memory here, Postgres in production) -- rather than the old
// in-memory-only memrepo/domain.InvertedIndex stack, which no production
// binary actually used.
func TestEndToEnd_CrawlThenSearch(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Write([]byte("User-agent: *\nDisallow: /geheim\n"))
		case "/":
			w.Write([]byte(`<html><head><title>Startseite</title></head><body>
                <p>Willkommen auf der Startseite. Hier geht es um Katzen und Hunde.</p>
                <a href="/seite2">weiter</a>
                <a href="/geheim">geheim</a>
            </body></html>`))
		case "/seite2":
			w.Write([]byte(`<html><head><title>Seite Zwei</title></head><body>
                <p>Diese Seite handelt ausschließlich von Hunde und ihrem Training.</p>
            </body></html>`))
		case "/geheim":
			w.Write([]byte(`<html><head><title>Geheim</title></head><body>
                <p>Dieser Inhalt darf laut robots.txt nicht gecrawlt werden.</p>
            </body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	ctx := context.Background()
	n := atomic.AddInt64(&dsnCounter, 1)
	repo, err := sqlrepo.New(ctx, "sqlite", fmt.Sprintf("file:integrationtest%d?mode=memory&cache=shared", n))
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	defer repo.Close()

	fetcher := httpfetcher.New(nil)
	embedder := hashembed.New(8)
	opSettings := domain.DefaultOperationalSettings()
	robotsChecker := robots.New(fetcher)
	parse := func(h, u string) (string, string, []string) { return htmlparser.Parse(strings.NewReader(h), u) }

	crawlerSvc := application.NewSQLCrawlerService(fetcher, robotsChecker, repo, embedder, parse, opSettings)

	// crawl-server: the only process that actually executes crawls.
	crawlHandler := restapi.New(restapi.Config{Crawler: crawlerSvc, CrawlJobs: repo, Health: repo})
	crawlServer := httptest.NewServer(crawlHandler.RoutesCrawlInternal())
	defer crawlServer.Close()

	settings := domain.NewTuningSettings(0.5, domain.DefaultBM25K1, domain.DefaultBM25B)
	overrides := domain.DefaultRankingOverrides()
	corpusStats := domain.NewCorpusStatsCache(0, 1)
	vocabulary := domain.NewVocabularyCache(nil)
	searchSvc := application.NewHybridAsSearchService(repo, embedder, settings, opSettings, overrides, corpusStats, vocabulary)

	// search-server and admin-server, standing in for the two separate
	// production processes -- admin-server reaches crawl-server the same
	// way it would in production: over HTTP, via crawlclient.
	handler := restapi.New(restapi.Config{
		Search: searchSvc, OpSettings: opSettings, Jobs: crawlclient.New(crawlServer.URL),
		AdminUser: "admin", AdminPass: "test-password",
	})
	searchAPI := httptest.NewServer(handler.RoutesSearch())
	defer searchAPI.Close()
	adminAPI := httptest.NewServer(handler.RoutesAdmin())
	defer adminAPI.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "test-password"})
	loginResp, err := client.Post(adminAPI.URL+"/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /login, got %d", loginResp.StatusCode)
	}

	crawlBody, _ := json.Marshal(map[string]interface{}{
		"seed_urls":      []string{site.URL + "/"},
		"max_pages":      10,
		"respect_robots": true,
	})
	resp, err := client.Post(adminAPI.URL+"/admin/api/crawl", "application/json", bytes.NewReader(crawlBody))
	if err != nil {
		t.Fatalf("crawl request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from /admin/api/crawl, got %d", resp.StatusCode)
	}
	var startResp struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&startResp)
	if startResp.JobID == "" {
		t.Fatal("expected a non-empty job_id")
	}

	job := waitForCrawlJob(t, client, adminAPI.URL, startResp.JobID)
	if job.Status != domain.CrawlJobDone {
		t.Fatalf("expected job done, got %s (err=%s)", job.Status, job.Error)
	}
	if job.PagesCrawled != 2 {
		t.Fatalf("expected 2 crawled pages, got %d", job.PagesCrawled)
	}

	// The crawl just populated the corpus -- refresh the in-memory caches
	// hybrid search reads from synchronously (in production these catch up
	// via a ~10s background poll; a test shouldn't need to wait for that).
	bootstrap.SyncCorpusStats(ctx, repo, corpusStats)
	bootstrap.SyncVocabulary(ctx, repo, vocabulary)

	searchResp, err := http.Get(searchAPI.URL + "/search?q=Hunde")
	if err != nil {
		t.Fatalf("search request failed: %v", err)
	}
	defer searchResp.Body.Close()
	var result struct {
		Results []domain.SearchResult `json:"results"`
	}
	_ = json.NewDecoder(searchResp.Body).Decode(&result)
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 search results for 'Hunde', got %d: %+v", len(result.Results), result.Results)
	}

	searchResp2, _ := http.Get(searchAPI.URL + "/search?q=Training")
	var result2 struct {
		Results []domain.SearchResult `json:"results"`
	}
	_ = json.NewDecoder(searchResp2.Body).Decode(&result2)
	if len(result2.Results) == 0 || !strings.Contains(result2.Results[0].URL, "seite2") {
		t.Fatalf("expected seite2 first for unique term, got %+v", result2.Results)
	}

	for _, doc := range result.Results {
		if strings.Contains(doc.URL, "geheim") {
			t.Errorf("robots.txt-disallowed page was indexed: %s", doc.URL)
		}
	}
}

// waitForCrawlJob polls GET /admin/api/crawl/jobs/{id} until the job
// reaches a terminal status, since the crawl now runs asynchronously.
func waitForCrawlJob(t *testing.T, client *http.Client, adminBaseURL, jobID string) domain.CrawlJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(adminBaseURL + "/admin/api/crawl/jobs/" + jobID)
		if err != nil {
			t.Fatalf("job status request failed: %v", err)
		}
		var job domain.CrawlJob
		err = json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("decoding job status: %v", err)
		}
		if job.Status == domain.CrawlJobDone || job.Status == domain.CrawlJobFailed {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for crawl job to finish")
	return domain.CrawlJob{}
}
