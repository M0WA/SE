package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeAdminRepo struct {
	totalDocs      int
	avgDocLen      float64
	vocabularySize int
	topTerms       []domain.TermStat
	docs           []domain.IndexedDocument
	gotHost        string
	domains        []domain.DomainSummary
	gotQuery       string
	versions       []domain.DocumentVersion
	overview       domain.DocumentsOverview
	postings       []domain.PostingStats
	err            error
	deleteErr      error
	deletedID      string
}

func (f *fakeAdminRepo) CorpusStats(context.Context) (int, float64, error) {
	return f.totalDocs, f.avgDocLen, f.err
}
func (f *fakeAdminRepo) VocabularyStats(context.Context, int) (int, []domain.TermStat, error) {
	return f.vocabularySize, f.topTerms, f.err
}
func (f *fakeAdminRepo) ListDocuments(_ context.Context, _ int, host string) ([]domain.IndexedDocument, error) {
	f.gotHost = host
	return f.docs, f.err
}
func (f *fakeAdminRepo) SearchDomains(_ context.Context, q string, _ int) ([]domain.DomainSummary, error) {
	f.gotQuery = q
	return f.domains, f.err
}
func (f *fakeAdminRepo) DocumentVersions(context.Context, string) ([]domain.DocumentVersion, error) {
	return f.versions, f.err
}
func (f *fakeAdminRepo) DocumentsOverview(context.Context, int) (domain.DocumentsOverview, error) {
	return f.overview, f.err
}
func (f *fakeAdminRepo) DeleteDocument(_ context.Context, id string) error {
	f.deletedID = id
	return f.deleteErr
}
func (f *fakeAdminRepo) PostingsForTerm(context.Context, string) ([]domain.PostingStats, error) {
	return f.postings, f.err
}

type fakeDebugSearch struct {
	results []domain.HybridResult
	err     error
	gotOpts ports.SearchQuery
}

func (f *fakeDebugSearch) Search(_ context.Context, _ string, opts ports.SearchQuery) ([]domain.HybridResult, error) {
	f.gotOpts = opts
	return f.results, f.err
}

func adminAuthedHandler(t *testing.T, admin ports.AdminRepository, debug ports.DebugSearchService) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerWithSettings(t, admin, debug, nil, nil)
}

func adminAuthedHandlerWithSettings(t *testing.T, admin ports.AdminRepository, debug ports.DebugSearchService, settings *domain.TuningSettings, opSettings *domain.OperationalSettings) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerWithOverrides(t, admin, debug, settings, opSettings, nil)
}

func adminAuthedHandlerWithOverrides(t *testing.T, admin ports.AdminRepository, debug ports.DebugSearchService, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: admin, Debug: debug, Settings: settings, OpSettings: opSettings, Overrides: overrides, DBDriver: "pgx",
		AdminUser: testAdminUser, AdminPass: testAdminPass,
	})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	return h, rec.Result().Cookies()[0]
}

func TestHandleAdminPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
}

func TestHandleAdminAPI_Unauthenticated_Returns401(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	for _, path := range []string{"/admin/api/stats", "/admin/api/vocabulary", "/admin/api/documents", "/admin/api/postings?term=x", "/admin/api/search?q=x", "/admin/api/settings", "/admin/api/overrides"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.RoutesAdmin().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d", path, rec.Code)
		}
	}
}

func TestHandleAdminPage_Authenticated_ServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminStats_Success(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{totalDocs: 5, avgDocLen: 42.5}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Driver    string  `json:"driver"`
		TotalDocs int     `json:"total_docs"`
		AvgDocLen float64 `json:"avg_doc_len"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Driver != "pgx" || resp.TotalDocs != 5 || resp.AvgDocLen != 42.5 {
		t.Errorf("unexpected stats response: %+v", resp)
	}
}

func TestHandleAdminStats_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin repo isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminStats_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminStats_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/stats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminVocabulary_Success(t *testing.T) {
	terms := []domain.TermStat{{Term: "cats", DocFreq: 3, TotalFreq: 7}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{vocabularySize: 42, topTerms: terms}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		VocabularySize int `json:"vocabulary_size"`
		TopTerms       []struct {
			Term      string `json:"term"`
			DocFreq   int    `json:"doc_freq"`
			TotalFreq int    `json:"total_freq"`
		} `json:"top_terms"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.VocabularySize != 42 || len(resp.TopTerms) != 1 || resp.TopTerms[0].Term != "cats" ||
		resp.TopTerms[0].DocFreq != 3 || resp.TopTerms[0].TotalFreq != 7 {
		t.Errorf("unexpected vocabulary response: %+v", resp)
	}
}

func TestHandleAdminVocabulary_RespectsLimitParam(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?limit=5", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminVocabulary_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminVocabulary_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminVocabulary_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminDocuments_Success(t *testing.T) {
	docs := []domain.IndexedDocument{{ID: "doc-0", URL: "http://a", Title: "A", DocLength: 10}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{docs: docs}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0]["id"] != "doc-0" {
		t.Errorf("unexpected documents response: %v", resp)
	}
}

func TestHandleAdminDocuments_IncludesLinkStats(t *testing.T) {
	docs := []domain.IndexedDocument{{
		ID: "doc-0", URL: "http://a", Title: "A", DocLength: 10,
		InternalLinks: 3, ExternalLinks: 2, Backlinks: 5, PageRank: 0.125,
	}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{docs: docs}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	var resp []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 document, got %d", len(resp))
	}
	if resp[0]["internal_links"] != float64(3) || resp[0]["external_links"] != float64(2) || resp[0]["backlinks"] != float64(5) {
		t.Errorf("expected link stats to pass through, got %v", resp[0])
	}
	if resp[0]["pagerank"] != 0.125 {
		t.Errorf("expected pagerank to pass through, got %v", resp[0])
	}
}

func TestHandleAdminDocuments_RespectsLimitParam(t *testing.T) {
	repo := &fakeAdminRepo{docs: []domain.IndexedDocument{{ID: "doc-0"}}}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents?limit=5", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminDocuments_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDocuments_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDocuments_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminDocuments_PassesDomainFilterThrough(t *testing.T) {
	repo := &fakeAdminRepo{docs: []domain.IndexedDocument{{ID: "doc-0"}}}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents?domain=example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotHost != "example.com" {
		t.Errorf("expected domain filter passed through, got %q", repo.gotHost)
	}
}

func TestHandleAdminSearchDomains_Success(t *testing.T) {
	domains := []domain.DomainSummary{{Host: "example.com", DocCount: 3}}
	repo := &fakeAdminRepo{domains: domains}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/domains?q=example", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if repo.gotQuery != "example" {
		t.Errorf("expected query passed through, got %q", repo.gotQuery)
	}
	var resp []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0]["host"] != "example.com" {
		t.Errorf("unexpected domains response: %v", resp)
	}
}

func TestHandleAdminSearchDomains_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/domains?q=example", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminSearchDomains_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/domains?q=example", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminSearchDomains_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/domains", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentsOverview_Success(t *testing.T) {
	overview := domain.DocumentsOverview{
		TopDomains: []domain.DomainSummary{{Host: "example.com", DocCount: 2}},
		AgeBuckets: []domain.AgeBucket{{Label: "last 24h", Count: 2}},
	}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{overview: overview}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/overview", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		TopDomains []map[string]interface{} `json:"top_domains"`
		AgeBuckets []map[string]interface{} `json:"age_buckets"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.TopDomains) != 1 || resp.TopDomains[0]["host"] != "example.com" {
		t.Errorf("unexpected top_domains: %v", resp.TopDomains)
	}
	if len(resp.AgeBuckets) != 1 || resp.AgeBuckets[0]["label"] != "last 24h" {
		t.Errorf("unexpected age_buckets: %v", resp.AgeBuckets)
	}
}

func TestHandleAdminDocumentsOverview_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/overview", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentsOverview_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/overview", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentVersions_Success(t *testing.T) {
	versions := []domain.DocumentVersion{{Version: 1, Title: "Old", DocLength: 5}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{versions: versions}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1/versions", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp []map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0]["title"] != "Old" {
		t.Errorf("unexpected versions response: %v", resp)
	}
}

func TestHandleAdminDocumentVersions_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1/versions", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDocumentVersions_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents/doc-1/versions", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDomainPage_GetServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/documents/example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("expected html content type, got %q", ct)
	}
}

func TestHandleAdminDomainPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/documents/example.com", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
}

func TestHandleAdminPostings_Success(t *testing.T) {
	postings := []domain.PostingStats{{DocID: "doc-0", TermFreq: 3, DocLength: 10, DocFreq: 1}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{postings: postings}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Term     string `json:"term"`
		DocFreq  int    `json:"doc_freq"`
		Postings []struct {
			DocID    string `json:"doc_id"`
			TermFreq int    `json:"term_freq"`
		} `json:"postings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Term != "katzen" || resp.DocFreq != 1 || len(resp.Postings) != 1 || resp.Postings[0].TermFreq != 3 {
		t.Errorf("unexpected postings response: %+v", resp)
	}
}

func TestHandleAdminPostings_EmptyTerm(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing term, got %d", rec.Code)
	}
}

func TestHandleAdminPostings_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminPostings_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminPostings_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/postings?term=x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminSearch_Success(t *testing.T) {
	results := []domain.HybridResult{{DocID: "doc-0", URL: "http://a", Title: "A", BM25Score: 1.2, SemanticSim: 0.5, FinalScore: 0.9}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{results: results})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp []struct {
		BM25Score  float64 `json:"bm25_score"`
		FinalScore float64 `json:"final_score"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0].BM25Score != 1.2 || resp[0].FinalScore != 0.9 {
		t.Errorf("unexpected debug search response: %+v", resp)
	}
}

// TestHandleAdminSearch_SurfacesCorrectedTerms verifies a fuzzy correction
// made by the search service is passed through to the debug JSON response,
// so the admin UI can show it.
func TestHandleAdminSearch_SurfacesCorrectedTerms(t *testing.T) {
	results := []domain.HybridResult{{
		DocID: "doc-0", URL: "http://a", Title: "A", BM25Score: 1.2, SemanticSim: 0.5, FinalScore: 0.9,
		CorrectedTerms: []domain.CorrectedTerm{{Original: "katzn", Corrected: "katzen"}},
	}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{results: results})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzn", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp []struct {
		CorrectedTerms []struct {
			Original  string `json:"original"`
			Corrected string `json:"corrected"`
		} `json:"corrected_terms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp) != 1 || len(resp[0].CorrectedTerms) != 1 ||
		resp[0].CorrectedTerms[0].Original != "katzn" || resp[0].CorrectedTerms[0].Corrected != "katzen" {
		t.Errorf("expected corrected_terms katzn->katzen surfaced, got %+v", resp)
	}
}

func TestHandleAdminSearch_RespectsTopKParam(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen&top_k=3", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if fd.gotOpts.TopK != 3 {
		t.Errorf("expected top_k=3 to be passed through, got %d", fd.gotOpts.TopK)
	}
}

func TestHandleAdminSearch_DefaultSortIsRelevance(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if fd.gotOpts.Sort != ports.SortRelevance {
		t.Errorf("expected default sort=relevance, got %q", fd.gotOpts.Sort)
	}
}

func TestHandleAdminSearch_SortRecencyPassesThrough(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen&sort=recency", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if fd.gotOpts.Sort != ports.SortRecency {
		t.Errorf("expected sort=recency to pass through, got %q", fd.gotOpts.Sort)
	}
}

func TestHandleAdminSearch_UnrecognizedSortFallsBackToRelevance(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen&sort=bogus", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if fd.gotOpts.Sort != ports.SortRelevance {
		t.Errorf("expected an unrecognized sort value to fall back to relevance, got %q", fd.gotOpts.Sort)
	}
}

func TestHandleAdminSearch_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when debug search isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminSearch_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{err: errors.New("boom")})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminSearch_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminSubpages_RequireAuth(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	for _, path := range []string{"/admin/documents", "/admin/crawl", "/admin/tuning", "/admin/search", "/admin/overrides"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.RoutesAdmin().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: expected 303 redirect, got %d", path, rec.Code)
		}
	}
}

func TestHandleAdminSubpages_ServeWhenAuthenticated(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	for _, path := range []string{"/admin/documents", "/admin/crawl", "/admin/tuning", "/admin/search", "/admin/overrides"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.RoutesAdmin().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, rec.Code)
		}
	}
}

func TestHandleAdminDeleteDocument_Success(t *testing.T) {
	repo := &fakeAdminRepo{}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents/doc-3", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if repo.deletedID != "doc-3" {
		t.Errorf("expected doc-3 to be deleted, got %q", repo.deletedID)
	}
}

func TestHandleAdminDeleteDocument_NotFound(t *testing.T) {
	repo := &fakeAdminRepo{deleteErr: ports.ErrDocumentNotFound}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents/doc-missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDocument_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Admin: &fakeAdminRepo{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents/doc-3", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDocument_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents/doc-3", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDocument_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{deleteErr: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents/doc-3", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminSettings_GetReturnsCurrentValues(t *testing.T) {
	settings := domain.NewTuningSettings(0.6, 1.3, 0.8)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		FetchTimeout: 5 * time.Second, UserAgent: "test-agent", DefaultMaxPages: 15,
		MinTextLength: 30, DefaultTopK: 7, SessionTTL: 6 * time.Hour,
		CrawlDelayMs: 400, MaxResponseBytes: 2048,
	})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, opSettings)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Tuning struct {
			Alpha float64 `json:"alpha"`
			K1    float64 `json:"k1"`
			B     float64 `json:"b"`
		} `json:"tuning"`
		Operational struct {
			FetchTimeoutSeconds int    `json:"fetch_timeout_seconds"`
			UserAgent           string `json:"user_agent"`
			DefaultMaxPages     int    `json:"default_max_pages"`
			MinTextLength       int    `json:"min_text_length"`
			DefaultTopK         int    `json:"default_top_k"`
			SessionTTLHours     int    `json:"session_ttl_hours"`
			CrawlDelayMs        int    `json:"crawl_delay_ms"`
			MaxResponseKB       int    `json:"max_response_kb"`
		} `json:"operational"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Tuning.Alpha != 0.6 || resp.Tuning.K1 != 1.3 || resp.Tuning.B != 0.8 {
		t.Errorf("unexpected tuning response: %+v", resp.Tuning)
	}
	if resp.Operational.FetchTimeoutSeconds != 5 || resp.Operational.UserAgent != "test-agent" ||
		resp.Operational.DefaultMaxPages != 15 || resp.Operational.MinTextLength != 30 ||
		resp.Operational.DefaultTopK != 7 || resp.Operational.SessionTTLHours != 6 ||
		resp.Operational.CrawlDelayMs != 400 || resp.Operational.MaxResponseKB != 2 {
		t.Errorf("unexpected operational response: %+v", resp.Operational)
	}
}

func TestHandleAdminSettings_PostUpdatesValues(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, opSettings)
	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 3, "user_agent": "custom-bot", "default_max_pages": 5,
			"min_text_length": 10, "default_top_k": 20, "session_ttl_hours": 2,
			"crawl_delay_ms": 100, "max_response_kb": 1024,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	alpha, k1, b := settings.Get()
	if alpha != 0.9 || k1 != 2.0 || b != 0.2 {
		t.Errorf("expected tuning to be updated, got (%v, %v, %v)", alpha, k1, b)
	}
	ov := opSettings.Get()
	if ov.FetchTimeout != 3*time.Second || ov.UserAgent != "custom-bot" || ov.DefaultMaxPages != 5 ||
		ov.MinTextLength != 10 || ov.DefaultTopK != 20 || ov.SessionTTL != 2*time.Hour ||
		ov.CrawlDelayMs != 100 || ov.MaxResponseBytes != 1024*1024 {
		t.Errorf("expected operational settings to be updated, got %+v", ov)
	}
}

// TestHandleAdminSettings_FuzzyFieldsRoundTrip verifies the two new
// admin-configurable fuzzy-matching knobs round-trip through the settings
// JSON: GET reports whatever's currently set, and a POST updates both.
func TestHandleAdminSettings_FuzzyFieldsRoundTrip(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{FuzzyMatchEnabled: true, FuzzyMaxEditDistance: 2})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, opSettings)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Operational struct {
			FuzzyMatchEnabled    bool `json:"fuzzy_match_enabled"`
			FuzzyMaxEditDistance int  `json:"fuzzy_max_edit_distance"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if !getResp.Operational.FuzzyMatchEnabled || getResp.Operational.FuzzyMaxEditDistance != 2 {
		t.Errorf("expected GET to report fuzzy_match_enabled=true, fuzzy_max_edit_distance=2, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"fuzzy_match_enabled": false, "fuzzy_max_edit_distance": 1,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	ov := opSettings.Get()
	if ov.FuzzyMatchEnabled {
		t.Errorf("expected fuzzy_match_enabled=false to be applied, got %+v", ov)
	}
	if ov.FuzzyMaxEditDistance != 1 {
		t.Errorf("expected fuzzy_max_edit_distance=1 to be applied, got %d", ov.FuzzyMaxEditDistance)
	}

	var postResp struct {
		Operational struct {
			FuzzyMatchEnabled    bool `json:"fuzzy_match_enabled"`
			FuzzyMaxEditDistance int  `json:"fuzzy_max_edit_distance"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.FuzzyMatchEnabled || postResp.Operational.FuzzyMaxEditDistance != 1 {
		t.Errorf("expected the POST response to echo back fuzzy_match_enabled=false, fuzzy_max_edit_distance=1, got %+v", postResp.Operational)
	}
}

// TestHandleAdminSettings_PageRankFieldsRoundTrip mirrors
// TestHandleAdminSettings_FuzzyFieldsRoundTrip for the two new PageRank
// admin knobs: GET reports whatever's currently set, and a POST updates
// both the tuning weight and the operational recompute interval.
func TestHandleAdminSettings_PageRankFieldsRoundTrip(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	settings.SetPageRankWeight(0.3)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{PageRankRecomputeIntervalMinutes: 90})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, opSettings)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Tuning struct {
			PageRankWeight float64 `json:"pagerank_weight"`
		} `json:"tuning"`
		Operational struct {
			PageRankRecomputeIntervalMinutes int `json:"pagerank_recompute_interval_minutes"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Tuning.PageRankWeight != 0.3 {
		t.Errorf("expected GET to report pagerank_weight=0.3, got %+v", getResp.Tuning)
	}
	if getResp.Operational.PageRankRecomputeIntervalMinutes != 90 {
		t.Errorf("expected GET to report pagerank_recompute_interval_minutes=90, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75, "pagerank_weight": 0.6},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"pagerank_recompute_interval_minutes": 30,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if w := settings.PageRankWeight(); w != 0.6 {
		t.Errorf("expected pagerank_weight=0.6 to be applied, got %v", w)
	}
	if ov := opSettings.Get(); ov.PageRankRecomputeIntervalMinutes != 30 {
		t.Errorf("expected pagerank_recompute_interval_minutes=30 to be applied, got %d", ov.PageRankRecomputeIntervalMinutes)
	}
}

// TestHandleAdminSettings_ANNSearchEnabledFieldRoundTrips mirrors
// TestHandleAdminSettings_FuzzyFieldsRoundTrip for the new
// ann_search_enabled troubleshooting knob: GET reports whatever's
// currently set, and a POST updates it.
func TestHandleAdminSettings_ANNSearchEnabledFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{ANNSearchEnabled: true})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, opSettings)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Operational struct {
			ANNSearchEnabled bool `json:"ann_search_enabled"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if !getResp.Operational.ANNSearchEnabled {
		t.Errorf("expected GET to report ann_search_enabled=true, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"ann_search_enabled": false,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if ov := opSettings.Get(); ov.ANNSearchEnabled {
		t.Errorf("expected ann_search_enabled=false to be applied, got %+v", ov)
	}

	var postResp struct {
		Operational struct {
			ANNSearchEnabled bool `json:"ann_search_enabled"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.ANNSearchEnabled {
		t.Errorf("expected the POST response to echo back ann_search_enabled=false, got %+v", postResp.Operational)
	}
}

func TestHandleAdminSettings_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when settings aren't configured, got %d", rec.Code)
	}
}

func TestHandleAdminSettings_InvalidJSON(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminSettings_MethodNotAllowed(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, settings, nil)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminOverrides_GetReturnsCurrentValues(t *testing.T) {
	overrides := domain.NewRankingOverrides(domain.RankingOverridesValues{
		BlockedTerms:   []string{"casino"},
		BoostedTerms:   map[string]float64{"official": 1.5},
		BlockedDomains: []string{"spammy.example"},
		BoostedDomains: map[string]float64{"trusted.example": 2.0},
	})
	h, cookie := adminAuthedHandlerWithOverrides(t, &fakeAdminRepo{}, &fakeDebugSearch{}, nil, nil, overrides)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overrides", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		BlockedTerms   []string           `json:"blocked_terms"`
		BoostedTerms   map[string]float64 `json:"boosted_terms"`
		BlockedDomains []string           `json:"blocked_domains"`
		BoostedDomains map[string]float64 `json:"boosted_domains"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.BlockedTerms) != 1 || resp.BlockedTerms[0] != "casino" {
		t.Errorf("unexpected blocked terms: %+v", resp.BlockedTerms)
	}
	if resp.BoostedTerms["official"] != 1.5 {
		t.Errorf("unexpected boosted terms: %+v", resp.BoostedTerms)
	}
	if len(resp.BlockedDomains) != 1 || resp.BlockedDomains[0] != "spammy.example" {
		t.Errorf("unexpected blocked domains: %+v", resp.BlockedDomains)
	}
	if resp.BoostedDomains["trusted.example"] != 2.0 {
		t.Errorf("unexpected boosted domains: %+v", resp.BoostedDomains)
	}
}

func TestHandleAdminOverrides_PostUpdatesValues(t *testing.T) {
	overrides := domain.DefaultRankingOverrides()
	h, cookie := adminAuthedHandlerWithOverrides(t, &fakeAdminRepo{}, &fakeDebugSearch{}, nil, nil, overrides)
	body, _ := json.Marshal(map[string]interface{}{
		"blocked_terms":   []string{"Casino"},
		"boosted_terms":   map[string]float64{"Official": 1.5},
		"blocked_domains": []string{"Spammy.example"},
		"boosted_domains": map[string]float64{"Trusted.example": 2.0},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/overrides", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	v := overrides.Get()
	if len(v.BlockedTerms) != 1 || v.BlockedTerms[0] != "casino" {
		t.Errorf("expected blocked terms updated and normalized, got %+v", v.BlockedTerms)
	}
	if v.BoostedTerms["official"] != 1.5 {
		t.Errorf("expected boosted terms updated and normalized, got %+v", v.BoostedTerms)
	}
	if len(v.BlockedDomains) != 1 || v.BlockedDomains[0] != "spammy.example" {
		t.Errorf("expected blocked domains updated and normalized, got %+v", v.BlockedDomains)
	}
	if v.BoostedDomains["trusted.example"] != 2.0 {
		t.Errorf("expected boosted domains updated and normalized, got %+v", v.BoostedDomains)
	}
}

func TestHandleAdminOverrides_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overrides", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when overrides aren't configured, got %d", rec.Code)
	}
}

func TestHandleAdminOverrides_InvalidJSON(t *testing.T) {
	overrides := domain.DefaultRankingOverrides()
	h, cookie := adminAuthedHandlerWithOverrides(t, &fakeAdminRepo{}, &fakeDebugSearch{}, nil, nil, overrides)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/overrides", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminOverrides_MethodNotAllowed(t *testing.T) {
	overrides := domain.DefaultRankingOverrides()
	h, cookie := adminAuthedHandlerWithOverrides(t, &fakeAdminRepo{}, &fakeDebugSearch{}, nil, nil, overrides)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/overrides", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func newSettingsStoreTestRepo(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	repo, err := sqlrepo.New(context.Background(), "sqlite", "file:admintest_"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func adminAuthedHandlerWithSettingsStore(t *testing.T, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides, store ports.SettingsStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: settings, OpSettings: opSettings, Overrides: overrides, SettingsStore: store,
		DBDriver: "sqlite", AdminUser: testAdminUser, AdminPass: testAdminPass,
	})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	return h, rec.Result().Cookies()[0]
}

func TestHandleAdminSettings_PostPersistsToSettingsStore(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	h, cookie := adminAuthedHandlerWithSettingsStore(t, settings, opSettings, nil, repo)

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 3, "user_agent": "custom-bot", "default_max_pages": 5,
			"min_text_length": 10, "default_top_k": 20, "session_ttl_hours": 2,
			"crawl_delay_ms": 100, "max_response_kb": 1024,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rawTuning, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyTuning)
	if err != nil || !found {
		t.Fatalf("expected the tuning setting to be persisted, found=%v err=%v", found, err)
	}
	var storedTuning domain.TuningValues
	if err := json.Unmarshal([]byte(rawTuning), &storedTuning); err != nil {
		t.Fatalf("failed to decode persisted tuning: %v", err)
	}
	if storedTuning.Alpha != 0.9 || storedTuning.K1 != 2.0 || storedTuning.B != 0.2 {
		t.Errorf("unexpected persisted tuning: %+v", storedTuning)
	}

	rawOp, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyOperational)
	if err != nil || !found {
		t.Fatalf("expected the operational setting to be persisted, found=%v err=%v", found, err)
	}
	var storedOp domain.OperationalSettingsValues
	if err := json.Unmarshal([]byte(rawOp), &storedOp); err != nil {
		t.Fatalf("failed to decode persisted operational settings: %v", err)
	}
	if storedOp.UserAgent != "custom-bot" || storedOp.DefaultMaxPages != 5 || storedOp.CrawlDelayMs != 100 {
		t.Errorf("unexpected persisted operational settings: %+v", storedOp)
	}
}

func TestHandleAdminSettings_PostWithoutSettingsStoreStillSucceeds(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	h, cookie := adminAuthedHandlerWithSettingsStore(t, settings, domain.DefaultOperationalSettings(), nil, nil)
	body, _ := json.Marshal(map[string]interface{}{
		"tuning":      map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5, "min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1, "crawl_delay_ms": 1, "max_response_kb": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with no settings store configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminOverrides_PostPersistsToSettingsStore(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	overrides := domain.DefaultRankingOverrides()
	h, cookie := adminAuthedHandlerWithSettingsStore(t, nil, nil, overrides, repo)

	body, _ := json.Marshal(map[string]interface{}{
		"blocked_terms":   []string{"Casino"},
		"boosted_terms":   map[string]float64{"Official": 1.5},
		"blocked_domains": []string{"Spammy.example"},
		"boosted_domains": map[string]float64{"Trusted.example": 2.0},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/overrides", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	raw, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyOverrides)
	if err != nil || !found {
		t.Fatalf("expected the overrides setting to be persisted, found=%v err=%v", found, err)
	}
	var stored domain.RankingOverridesValues
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("failed to decode persisted overrides: %v", err)
	}
	if len(stored.BlockedTerms) != 1 || stored.BlockedTerms[0] != "casino" {
		t.Errorf("expected persisted overrides to be normalized, got %+v", stored)
	}
	if stored.BoostedDomains["trusted.example"] != 2.0 {
		t.Errorf("unexpected persisted overrides: %+v", stored)
	}
}

// TestSyncSettings_PicksUpAdminPersistedValues proves the two ends of the
// propagation path actually connect: what handleAdminSettings persists via
// SaveSetting is exactly what bootstrap.SyncSettings' GetSetting-based load
// later applies to a *different* TuningSettings instance -- simulating
// another process's next poll picking up this admin edit.
func TestSyncSettings_PicksUpAdminPersistedValues(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	adminSettings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	h, cookie := adminAuthedHandlerWithSettingsStore(t, adminSettings, domain.DefaultOperationalSettings(), nil, repo)

	body, _ := json.Marshal(map[string]interface{}{
		"tuning":      map[string]float64{"alpha": 0.42, "k1": 1.5, "b": 0.6},
		"operational": map[string]interface{}{"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5, "min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1, "crawl_delay_ms": 1, "max_response_kb": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	otherProcessSettings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	syncCtx, cancelSync := context.WithCancel(context.Background())
	t.Cleanup(cancelSync)
	bootstrap.SyncSettings(syncCtx, repo, otherProcessSettings, nil, nil, nil)
	alpha, k1, b := otherProcessSettings.Get()
	if alpha != 0.42 || k1 != 1.5 || b != 0.6 {
		t.Errorf("expected another process's settings to pick up the admin edit, got (%v, %v, %v)", alpha, k1, b)
	}
}
