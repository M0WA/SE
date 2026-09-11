package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"searchengine/internal/adapters/crawlclient"
	"searchengine/internal/adapters/htmlparser"
	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/adapters/memrepo"
	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/robots"
	"searchengine/internal/application"
	"searchengine/internal/domain"
)

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

	fetcher := httpfetcher.New(nil)
	repo := memrepo.New()
	index := domain.NewInvertedIndex()
	robotsChecker := robots.New(fetcher)
	parse := func(h, u string) (string, string, []string) { return htmlparser.Parse(strings.NewReader(h), u) }

	crawlerSvc := application.NewCrawlerService(fetcher, robotsChecker, repo, index, parse, nil)
	searchSvc := application.NewSearchService(index)

	// crawl-server: the only process that actually executes crawls.
	crawlHandler := restapi.New(restapi.Config{Crawler: crawlerSvc, CrawlJobs: domain.NewCrawlJobStore()})
	crawlServer := httptest.NewServer(crawlHandler.RoutesCrawlInternal())
	defer crawlServer.Close()

	// search-server and admin-server, standing in for the two separate
	// production processes -- admin-server reaches crawl-server the same
	// way it would in production: over HTTP, via crawlclient.
	handler := restapi.New(restapi.Config{
		Search: searchSvc, Jobs: crawlclient.New(crawlServer.URL),
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
		"seed_urls": []string{site.URL + "/"},
		"max_pages": 10,
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
