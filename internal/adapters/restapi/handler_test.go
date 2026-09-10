package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
)

type fakeSearch struct {
	results []domain.SearchResult
	err     error
	gotQ    string
	gotTopK int
}

func (f *fakeSearch) Search(_ context.Context, q string, topK int) ([]domain.SearchResult, error) {
	f.gotQ, f.gotTopK = q, topK
	return f.results, f.err
}

type fakeCrawler struct {
	count int
	err   error
}

func (f *fakeCrawler) Crawl(_ context.Context, _ []string, _ int) (int, error) {
	return f.count, f.err
}

func TestHandleSearch_Success(t *testing.T) {
	fs := &fakeSearch{results: []domain.SearchResult{{URL: "http://a", Score: 1}}}
	h := restapi.New(fs, &fakeCrawler{})

	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen&top_k=5", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("erwartet 200, bekam %d: %s", rec.Code, rec.Body.String())
	}
	if fs.gotQ != "katzen" || fs.gotTopK != 5 {
		t.Errorf("unerwartete Argumente an Search: %q %d", fs.gotQ, fs.gotTopK)
	}
}

func TestHandleSearch_DefaultTopK(t *testing.T) {
	fs := &fakeSearch{}
	h := restapi.New(fs, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if fs.gotTopK != 10 {
		t.Errorf("erwartet Standard top_k=10, bekam %d", fs.gotTopK)
	}
}

func TestHandleSearch_MethodNotAllowed(t *testing.T) {
	h := restapi.New(&fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodPost, "/search", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("erwartet 405, bekam %d", rec.Code)
	}
}

func TestHandleSearch_ServiceError(t *testing.T) {
	fs := &fakeSearch{err: errors.New("ungültige Anfrage")}
	h := restapi.New(fs, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/search?q=", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("erwartet 400, bekam %d", rec.Code)
	}
}

func TestHandleCrawl_Success(t *testing.T) {
	fc := &fakeCrawler{count: 3}
	h := restapi.New(&fakeSearch{}, fc)

	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "max_pages": 5})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("erwartet 200, bekam %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["crawled_count"] != 3 {
		t.Errorf("erwartet crawled_count=3, bekam %v", resp)
	}
}

func TestHandleCrawl_MethodNotAllowed(t *testing.T) {
	h := restapi.New(&fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/crawl", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("erwartet 405, bekam %d", rec.Code)
	}
}

func TestHandleCrawl_InvalidJSON(t *testing.T) {
	h := restapi.New(&fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader([]byte("{ungültig")))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("erwartet 400, bekam %d", rec.Code)
	}
}

func TestHandleCrawl_EmptySeedURLs(t *testing.T) {
	h := restapi.New(&fakeSearch{}, &fakeCrawler{})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("erwartet 400, bekam %d", rec.Code)
	}
}

func TestHandleCrawl_ServiceError(t *testing.T) {
	fc := &fakeCrawler{err: errors.New("crawl fehlgeschlagen")}
	h := restapi.New(&fakeSearch{}, fc)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/crawl", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("erwartet 500, bekam %d", rec.Code)
	}
}
