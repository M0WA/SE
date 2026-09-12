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
	gotOpts ports.SearchQuery
}

func (f *fakeSearch) Search(_ context.Context, q string, opts ports.SearchQuery) ([]domain.SearchResult, error) {
	f.gotQ, f.gotOpts = q, opts
	return f.results, f.err
}

// fakeCrawler is used by crawl-server-side tests (RoutesCrawlInternal, in
// crawl_internal_test.go) -- the real, synchronous crawl executor.
type fakeCrawler struct {
	count      int
	err        error
	gotOptions ports.CrawlOptions
}

func (f *fakeCrawler) Crawl(_ context.Context, opts ports.CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error) {
	f.gotOptions = opts
	return f.count, f.err
}

// fakeJobService is used by admin-server-side tests -- the HTTP client
// admin-server would otherwise use to reach crawl-server.
type fakeJobService struct {
	jobID      string
	startErr   error
	jobs       []domain.CrawlJobSummary
	listErr    error
	job        domain.CrawlJob
	getErr     error
	gotOptions ports.CrawlOptions
}

func (f *fakeJobService) StartCrawlJob(_ context.Context, opts ports.CrawlOptions) (string, error) {
	f.gotOptions = opts
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.jobID, nil
}

func (f *fakeJobService) ListCrawlJobs(_ context.Context) ([]domain.CrawlJobSummary, error) {
	return f.jobs, f.listErr
}

func (f *fakeJobService) GetCrawlJob(_ context.Context, _ string) (domain.CrawlJob, error) {
	return f.job, f.getErr
}

// fakeHealthChecker is used by TestHandleHealthz_* -- a stand-in for the
// repository's real DB ping.
type fakeHealthChecker struct {
	err error
}

func (f *fakeHealthChecker) Ping(context.Context) error {
	return f.err
}

const (
	testAdminUser = "admin"
	testAdminPass = "test-password"
)

// authedHandler returns a Handler with an admin account configured, plus a
// valid session cookie obtained via a real POST /login -- so tests exercise
// the actual login flow rather than bypassing it.
func authedHandler(t *testing.T, search *fakeSearch, jobs ports.CrawlJobService) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Search: search, Jobs: jobs,
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
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
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

func TestHandleIndex_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2F" {
		t.Errorf("expected redirect to login with next=/, got %q", loc)
	}
}

func TestHandleIndex_HeadRequestAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	req.AddCookie(cookie)
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
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleIndex_UnknownPathStill404s(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown path, got %d", rec.Code)
	}
}

func TestHandleStyle_Success(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
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
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
	req := httptest.NewRequest(http.MethodPost, "/style.css", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleStyle_HeadRequestAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
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
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
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
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
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
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
	req := httptest.NewRequest(http.MethodPost, "/admin.js", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSearch_Success(t *testing.T) {
	fs := &fakeSearch{results: []domain.SearchResult{{URL: "http://a", Score: 1}}}
	h, cookie := authedHandler(t, fs, nil)

	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen&top_k=5", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fs.gotQ != "katzen" || fs.gotOpts.TopK != 5 {
		t.Errorf("unexpected arguments to Search: %q %d", fs.gotQ, fs.gotOpts.TopK)
	}
}

func TestHandleSearch_Unauthenticated_Returns401(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestHandleSearch_SurfacesCorrectedTerms verifies a fuzzy correction made
// by the search service reaches the public /search JSON response, so a
// caller/UI can show it transparently rather than the query being silently
// rewritten.
func TestHandleSearch_SurfacesCorrectedTerms(t *testing.T) {
	fs := &fakeSearch{results: []domain.SearchResult{{
		URL: "http://a", Score: 1,
		CorrectedTerms: []domain.CorrectedTerm{{Original: "katzn", Corrected: "katzen"}},
	}}}
	h, cookie := authedHandler(t, fs, nil)

	req := httptest.NewRequest(http.MethodGet, "/search?q=katzn", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			CorrectedTerms []struct {
				Original  string `json:"original"`
				Corrected string `json:"corrected"`
			} `json:"corrected_terms"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Results) != 1 || len(resp.Results[0].CorrectedTerms) != 1 ||
		resp.Results[0].CorrectedTerms[0].Original != "katzn" || resp.Results[0].CorrectedTerms[0].Corrected != "katzen" {
		t.Errorf("expected corrected_terms katzn->katzen surfaced in the JSON response, got %+v", resp)
	}
}

func TestHandleSearch_DefaultTopK(t *testing.T) {
	fs := &fakeSearch{}
	h, cookie := authedHandler(t, fs, nil)
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if fs.gotOpts.TopK != 10 {
		t.Errorf("expected default top_k=10, got %d", fs.gotOpts.TopK)
	}
}

func TestHandleSearch_DefaultSortIsRelevance(t *testing.T) {
	fs := &fakeSearch{}
	h, cookie := authedHandler(t, fs, nil)
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if fs.gotOpts.Sort != ports.SortRelevance {
		t.Errorf("expected default sort=relevance, got %q", fs.gotOpts.Sort)
	}
}

func TestHandleSearch_SortRecencyPassesThrough(t *testing.T) {
	fs := &fakeSearch{}
	h, cookie := authedHandler(t, fs, nil)
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen&sort=recency", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if fs.gotOpts.Sort != ports.SortRecency {
		t.Errorf("expected sort=recency to pass through, got %q", fs.gotOpts.Sort)
	}
}

func TestHandleSearch_UnrecognizedSortFallsBackToRelevance(t *testing.T) {
	fs := &fakeSearch{}
	h, cookie := authedHandler(t, fs, nil)
	req := httptest.NewRequest(http.MethodGet, "/search?q=katzen&sort=bogus", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if fs.gotOpts.Sort != ports.SortRelevance {
		t.Errorf("expected an unrecognized sort value to fall back to relevance, got %q", fs.gotOpts.Sort)
	}
}

func TestHandleSearch_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/search", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleSearch_ServiceError(t *testing.T) {
	fs := &fakeSearch{err: errors.New("invalid request")}
	h, cookie := authedHandler(t, fs, nil)
	req := httptest.NewRequest(http.MethodGet, "/search?q=", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleHealthz_SearchServer_HealthyByDefault(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf(`expected status "ok", got %q`, resp.Status)
	}
}

func TestHandleHealthz_SearchServer_UnhealthyWhenDBPingFails(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Health: &fakeHealthChecker{err: errors.New("db unreachable")}})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz_SearchServer_HealthyWhenDBPingSucceeds(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, Health: &fakeHealthChecker{}})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz_AdminServer_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /healthz to bypass admin auth entirely, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz_AdminServer_UnhealthyWhenDBPingFails(t *testing.T) {
	h := restapi.New(restapi.Config{
		Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass,
		Health: &fakeHealthChecker{err: errors.New("db unreachable")},
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleHealthz_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
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
	h := restapi.New(restapi.Config{Search: &fakeSearch{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlPage_NoAdminConfigured_AlwaysUnauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Search: &fakeSearch{}})
	req := httptest.NewRequest(http.MethodGet, "/admin/crawl", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected redirect to login when no admin account is configured, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_Success(t *testing.T) {
	fj := &fakeJobService{jobID: "job-42"}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)

	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "max_pages": 5,
		"respect_robots": true, "user_agent": "custom-bot/1.0",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["job_id"] != "job-42" {
		t.Errorf("expected job_id=job-42, got %v", resp)
	}
	if len(fj.gotOptions.SeedURLs) != 1 || fj.gotOptions.SeedURLs[0] != "http://a" || fj.gotOptions.MaxPages != 5 {
		t.Errorf("unexpected options passed to job service: %+v", fj.gotOptions)
	}
	if !fj.gotOptions.RespectRobots || fj.gotOptions.UserAgent != "custom-bot/1.0" {
		t.Errorf("expected respect_robots/user_agent to pass through, got %+v", fj.gotOptions)
	}
}

func TestHandleAdminCrawl_PassesOffDomainAndSitemapOptionsThrough(t *testing.T) {
	fj := &fakeJobService{jobID: "job-42"}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)

	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "allow_off_domain_links": true, "use_sitemap": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fj.gotOptions.AllowOffDomainLinks || !fj.gotOptions.UseSitemap {
		t.Errorf("expected allow_off_domain_links/use_sitemap to pass through, got %+v", fj.gotOptions)
	}
}

func TestHandleAdminCrawl_OffDomainAndSitemapOptionsDefaultFalse(t *testing.T) {
	fj := &fakeJobService{jobID: "job-42"}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)

	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if fj.gotOptions.AllowOffDomainLinks || fj.gotOptions.UseSitemap {
		t.Errorf("expected allow_off_domain_links/use_sitemap to default false, got %+v", fj.gotOptions)
	}
}

func TestHandleAdminCrawlPage_GetServesPage(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
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
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
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
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodPut, "/admin/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_InvalidJSON(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader([]byte("{ungültig")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_EmptySeedURLs(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
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
	fj := &fakeJobService{startErr: errors.New("crawl failed")}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminCrawl_NotConfigured(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no job service is configured, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlJobs_Success(t *testing.T) {
	fj := &fakeJobService{jobs: []domain.CrawlJobSummary{
		{ID: "job-1", Status: domain.CrawlJobDone, PagesCrawled: 2},
		{ID: "job-2", Status: domain.CrawlJobRunning},
	}}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var jobs []domain.CrawlJobSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decoding jobs list: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestHandleAdminCrawlJobs_ServiceError(t *testing.T) {
	fj := &fakeJobService{listErr: errors.New("boom")}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlJobs_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/crawl/jobs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlJob_Success(t *testing.T) {
	fj := &fakeJobService{job: domain.CrawlJob{
		ID: "job-1", Status: domain.CrawlJobDone,
		Pages: []domain.CrawlPageEvent{{URL: "http://a", Status: domain.CrawlPageIndexed}},
	}}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs/job-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var job domain.CrawlJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("decoding job: %v", err)
	}
	if job.ID != "job-1" || len(job.Pages) != 1 {
		t.Errorf("unexpected job detail: %+v", job)
	}
}

func TestHandleAdminCrawlJob_NotFound(t *testing.T) {
	fj := &fakeJobService{getErr: ports.ErrCrawlJobNotFound}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlJob_NotConfigured(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs/job-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminCrawlJob_ServiceError(t *testing.T) {
	fj := &fakeJobService{getErr: errors.New("boom")}
	h, cookie := authedHandler(t, &fakeSearch{}, fj)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/crawl/jobs/job-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
