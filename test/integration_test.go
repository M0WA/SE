package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

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

	fetcher := httpfetcher.New()
	repo := memrepo.New()
	index := domain.NewInvertedIndex()
	robotsChecker := robots.New(fetcher)
	parse := func(h, u string) (string, string, []string) { return htmlparser.Parse(strings.NewReader(h), u) }

	crawlerSvc := application.NewCrawlerService(fetcher, robotsChecker, repo, index, parse)
	searchSvc := application.NewSearchService(index)
	handler := restapi.New(restapi.Config{
		Search: searchSvc, Crawler: crawlerSvc,
		AdminUser: "admin", AdminPass: "test-password",
	})
	api := httptest.NewServer(handler.Routes())
	defer api.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "test-password"})
	loginResp, err := client.Post(api.URL+"/login", "application/json", bytes.NewReader(loginBody))
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
	resp, err := client.Post(api.URL+"/crawl", "application/json", bytes.NewReader(crawlBody))
	if err != nil {
		t.Fatalf("crawl request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /crawl, got %d", resp.StatusCode)
	}
	var crawlResp struct {
		CrawledCount int `json:"crawled_count"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&crawlResp)
	if crawlResp.CrawledCount != 2 {
		t.Fatalf("expected 2 crawled pages, got %d", crawlResp.CrawledCount)
	}

	searchResp, err := http.Get(api.URL + "/search?q=Hunde")
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

	searchResp2, _ := http.Get(api.URL + "/search?q=Training")
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
