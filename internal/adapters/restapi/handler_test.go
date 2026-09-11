package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
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
	count      int
	err        error
	gotOptions ports.CrawlOptions
}

func (f *fakeCrawler) Crawl(_ context.Context, opts ports.CrawlOptions) (int, error) {
	f.gotOptions = opts
	return f.count, f.err
}

const (
	testAdminUser = "admin"
	testAdminPass = "test-password"
)

// authedHandler returns a Handler with an admin account configured, plus a
// valid session cookie obtained via a real POST /login -- so tests exercise
// the actual login flow rather than bypassing it.
func authedHandler(t *testing.T, search *fakeSearch, crawler *fakeCrawler) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Search: search, Crawler: crawler,
		AdminUser: testAdminUser, AdminPass: testAdminPass,
	})

	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected a session cookie after login")
	}
	return h, cookies[0]
}

func TestHandleIndex_Success(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("expected html content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("expected an HTML document, got: %s", rec.Body.String())
	}
}

func TestHandleIndex_HeadRequestAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestHandleIndex_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleIndex_UnknownPathStill404s(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown path, got %d", rec.Code)
	}
}

func TestHandleStyle_Success(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("expected css content type, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty stylesheet body")
	}
}

func TestHandleStyle_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodPost, "/style.css", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleStyle_HeadRequestAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodHead, "/style.css", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestHandleAdminJS_Success(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/admin.js", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("expected js content type, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty script body")
	}
}

func TestHandleAdminJS_HeadRequestAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodHead, "/admin.js", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestHandleAdminJS_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodPost, "/admin.js", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSearch_Success(t *testing.T) {
	fs := &fakeSearch{results: []domain.SearchResult{{URL: "http://a", Score: 1}}}
	h := restapi.New(restapi.Config{Search: fs, Crawler: &fakeCrawler{}})

	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen&top_k=5", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.gotQ != "katzen" || fs.gotTopK != 5 {
		t.Errorf("unexpected arguments to Search: %q %d", fs.gotQ, fs.gotTopK)
	}
}

func TestHandleSearch_DefaultTopK(t *testing.T) {
	fs := &fakeSearch{}
	h := restapi.New(restapi.Config{Search: fs, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if fs.gotTopK != 10 {
		t.Errorf("expected default top_k=10, got %d", fs.gotTopK)
	}
}

func TestHandleSearch_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodPost, "/search", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSearch_ServiceError(t *testing.T) {
	fs := &fakeSearch{err: errors.New("invalid request")}
	h := restapi.New(restapi.Config{Search: fs, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/search?q=", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/crawl", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fadmin%2Fcrawl" {
		t.Errorf("expected redirect to login with next=/admin/crawl, got %q", loc)
	}
}

func TestHandleAdminCrawl_Unauthenticated_PostReturns401(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlPage_NoAdminConfigured_AlwaysUnauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Crawler: &fakeCrawler{}})
	req := httptest.NewRequest(http.MethodGet, "/admin/crawl", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected redirect to login when no admin account is configured, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_Success(t *testing.T) {
	fc := &fakeCrawler{count: 3}
	h, cookie := authedHandler(t, &fakeSearch{}, fc)

	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "max_pages": 5})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["crawled_count"] != 3 {
		t.Errorf("expected crawled_count=3, got %v", resp)
	}
}

func TestHandleAdminCrawlPage_GetServesPage(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/admin/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("expected html content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "crawl-form") {
		t.Errorf("expected the crawl form in the page, got: %s", rec.Body.String())
	}
}

func TestHandleAdminCrawlPage_HeadServesNoBody(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodHead, "/admin/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestHandleAdminCrawlPage_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodPut, "/admin/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_InvalidJSON(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader([]byte("{ungültig")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_EmptySeedURLs(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeCrawler{})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_ServiceError(t *testing.T) {
	fc := &fakeCrawler{err: errors.New("crawl failed")}
	h, cookie := authedHandler(t, &fakeSearch{}, fc)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
