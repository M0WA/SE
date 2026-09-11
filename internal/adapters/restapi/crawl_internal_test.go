package restapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/ports"
)

func TestHandleCrawlInternal_Success(t *testing.T) {
	fc := &fakeCrawler{count: 7}
	h := restapi.New(restapi.Config{Crawler: fc})

	body, _ := json.Marshal(ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["crawled_count"] != 7 {
		t.Errorf("expected crawled_count=7, got %v", resp)
	}
	if len(fc.gotOptions.SeedURLs) != 1 || fc.gotOptions.SeedURLs[0] != "http://a" || fc.gotOptions.MaxPages != 5 {
		t.Errorf("unexpected options passed to crawler: %+v", fc.gotOptions)
	}
}

func TestHandleCrawlInternal_InvalidJSON(t *testing.T) {
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader([]byte("{ungültig")))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCrawlInternal_EmptySeedURLs(t *testing.T) {
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}})
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
	h := restapi.New(restapi.Config{Crawler: fc})
	body, _ := json.Marshal(ports.CrawlOptions{SeedURLs: []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleCrawlInternal_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/crawl", nil)
	rec := httptest.NewRecorder()
	h.RoutesCrawlInternal().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
