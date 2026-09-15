package restapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeAdminRepo struct {
	totalDocs      int
	avgDocLen      float64
	vocabularySize int
	matchedCount   int
	topTerms       []domain.TermStat
	docs           []domain.IndexedDocument
	gotHost        string
	domains        []domain.DomainSummary
	gotQuery       string
	versions       []domain.DocumentVersion
	overview       domain.DocumentsOverview
	postings       []domain.PostingStats
	postingsDocs   map[string]domain.Document
	err            error
	deleteErr      error
	deletedID      string
	gotLimit       int
	gotOffset      int
	gotSearch      string
	gotSortBy      string
	gotSortDir     string

	pageRankMin    float64
	pageRankMax    float64
	pageRankAvg    float64
	pageRankErr    error
	tableRowCounts map[string]int64
	poolStats      sql.DBStats

	jobOutcomes            []domain.CrawlJobOutcomeCount
	jobOutcomesErr         error
	gotJobOutcomesSince    time.Time
	dailyFetchOutcomes     []domain.DailyFetchOutcome
	dailyFetchOutcomesErr  error
	gotFetchOutcomesSince  time.Time
	documentsByDay         []domain.DailyCount
	documentsByDayErr      error
	gotDocumentsByDaySince time.Time
	fetchDurationByDay     []domain.DailyAvgDuration
	fetchDurationErr       error
	gotFetchDurationSince  time.Time
	pageRankBuckets        []domain.PageRankBucket
	pageRankOrphanCount    int
	pageRankTotalDocs      int
	pageRankHistErr        error

	// mu guards deletedIDs, written from handleAdminDeleteDomainDocuments'
	// own background goroutine and read back from a test's polling
	// goroutine -- unlike deletedID above (only ever touched synchronously
	// by the single-document-delete tests), this needs real synchronization
	// to be race-free.
	mu         sync.Mutex
	deletedIDs []string
}

// DeletedIDs returns every ID DeleteDocument has been called with so far,
// safe to call concurrently with DeleteDocument itself.
func (f *fakeAdminRepo) DeletedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletedIDs...)
}

func (f *fakeAdminRepo) CorpusStats(context.Context) (int, float64, error) {
	return f.totalDocs, f.avgDocLen, f.err
}
func (f *fakeAdminRepo) VocabularyStats(_ context.Context, limit, offset int, search, sortBy, sortDir string) (int, int, []domain.TermStat, error) {
	f.gotLimit = limit
	f.gotOffset = offset
	f.gotSearch = search
	f.gotSortBy = sortBy
	f.gotSortDir = sortDir
	matched := f.matchedCount
	if matched == 0 {
		matched = f.vocabularySize
	}
	return f.vocabularySize, matched, f.topTerms, f.err
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
	f.mu.Lock()
	f.deletedIDs = append(f.deletedIDs, id)
	f.mu.Unlock()
	return f.deleteErr
}
func (f *fakeAdminRepo) PostingsForTerm(_ context.Context, _ string, limit int) ([]domain.PostingStats, error) {
	f.gotLimit = limit
	return f.postings, f.err
}
func (f *fakeAdminRepo) DocumentsByIDs(context.Context, []string) (map[string]domain.Document, error) {
	return f.postingsDocs, f.err
}
func (f *fakeAdminRepo) PageRankDistribution(context.Context) (float64, float64, float64, error) {
	if f.pageRankErr != nil {
		return 0, 0, 0, f.pageRankErr
	}
	return f.pageRankMin, f.pageRankMax, f.pageRankAvg, f.err
}
func (f *fakeAdminRepo) TableRowCounts(context.Context) (map[string]int64, error) {
	return f.tableRowCounts, f.err
}
func (f *fakeAdminRepo) PoolStats() sql.DBStats {
	return f.poolStats
}
func (f *fakeAdminRepo) CrawlJobOutcomes(_ context.Context, since time.Time) ([]domain.CrawlJobOutcomeCount, error) {
	f.gotJobOutcomesSince = since
	return f.jobOutcomes, f.jobOutcomesErr
}
func (f *fakeAdminRepo) DailyFetchOutcomes(_ context.Context, since time.Time) ([]domain.DailyFetchOutcome, error) {
	f.gotFetchOutcomesSince = since
	return f.dailyFetchOutcomes, f.dailyFetchOutcomesErr
}
func (f *fakeAdminRepo) DocumentsIndexedByDay(_ context.Context, since time.Time) ([]domain.DailyCount, error) {
	f.gotDocumentsByDaySince = since
	return f.documentsByDay, f.documentsByDayErr
}
func (f *fakeAdminRepo) DailyFetchDuration(_ context.Context, since time.Time) ([]domain.DailyAvgDuration, error) {
	f.gotFetchDurationSince = since
	return f.fetchDurationByDay, f.fetchDurationErr
}
func (f *fakeAdminRepo) PageRankHistogram(context.Context) ([]domain.PageRankBucket, int, int, error) {
	if f.pageRankHistErr != nil {
		return nil, 0, 0, f.pageRankHistErr
	}
	return f.pageRankBuckets, f.pageRankOrphanCount, f.pageRankTotalDocs, f.err
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

// fakeEmbeddingProvider is a minimal ports.EmbeddingProvider a test can
// inject via restapi.Config.NewEmbedder (see stubNewEmbedder) so
// handleAdminSettings' embedding-connectivity probe (testEmbeddingConnectivity)
// never makes a real network call from the test suite.
type fakeEmbeddingProvider struct {
	err error
}

func (f fakeEmbeddingProvider) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, f.err
}
func (f fakeEmbeddingProvider) Dimensions() int { return 1 }

// stubNewEmbedder returns a restapi.Config.NewEmbedder that always hands
// back a fakeEmbeddingProvider failing with err (nil for success),
// regardless of the settings snapshot it's given.
func stubNewEmbedder(err error) func(domain.OperationalSettingsValues) ports.EmbeddingProvider {
	return func(domain.OperationalSettingsValues) ports.EmbeddingProvider {
		return fakeEmbeddingProvider{err: err}
	}
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
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: admin, Debug: debug, Settings: settings, OpSettings: opSettings, Overrides: overrides, DBDriver: "pgx",
	})
}

// adminAuthedHandlerFromConfig builds a Handler from cfg (forcing in the
// test admin credentials every other helper here hard-codes) and logs in,
// for tests that need a Config field none of the narrower helpers expose
// (e.g. NewEmbedder, to stub out handleAdminSettings' embedding-connectivity
// probe).
func adminAuthedHandlerFromConfig(t *testing.T, cfg restapi.Config) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	cfg.AdminUser = testAdminUser
	cfg.AdminPass = testAdminPass
	h := restapi.New(cfg)
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	return h, rec.Result().Cookies()[0]
}

// adminAuthedHandlerWithOverviewDeps wires up the three dependencies
// handleAdminOverviewMetrics reads from -- admin (required), jobs and
// scheduledCrawls (each independently optional; see its own doc comment).
func adminAuthedHandlerWithOverviewDeps(t *testing.T, admin ports.AdminRepository, jobs ports.CrawlJobService, scheduledCrawls ports.ScheduledCrawlStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: admin, Jobs: jobs, ScheduledCrawls: scheduledCrawls, DBDriver: "pgx",
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
		MatchedCount   int `json:"matched_count"`
		Terms          []struct {
			Term      string `json:"term"`
			DocFreq   int    `json:"doc_freq"`
			TotalFreq int    `json:"total_freq"`
		} `json:"terms"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.VocabularySize != 42 || resp.MatchedCount != 42 || len(resp.Terms) != 1 || resp.Terms[0].Term != "cats" ||
		resp.Terms[0].DocFreq != 3 || resp.Terms[0].TotalFreq != 7 {
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
	if repo.gotLimit != 5 {
		t.Errorf("expected limit=5 to reach VocabularyStats, got %d", repo.gotLimit)
	}
}

func TestHandleAdminVocabulary_PassesSearchParamThrough(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?search=cat", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotSearch != "cat" {
		t.Errorf("expected search=cat to reach VocabularyStats, got %q", repo.gotSearch)
	}
}

func TestHandleAdminVocabulary_SearchParamIsLowercased(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?search=CaT", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotSearch != "cat" {
		t.Errorf("expected search=CaT to reach VocabularyStats lowercased as \"cat\", got %q", repo.gotSearch)
	}
}

func TestHandleAdminVocabulary_AbsentSearchParamIsEmptyString(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotSearch != "" {
		t.Errorf("expected an absent search param to reach VocabularyStats as \"\", got %q", repo.gotSearch)
	}
}

func TestHandleAdminVocabulary_DefaultsLimitOffsetSortDir(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotLimit != 20 || repo.gotOffset != 0 || repo.gotSortBy != "doc_freq" || repo.gotSortDir != "desc" {
		t.Errorf("expected default limit=20 offset=0 sort=doc_freq dir=desc, got limit=%d offset=%d sort=%q dir=%q",
			repo.gotLimit, repo.gotOffset, repo.gotSortBy, repo.gotSortDir)
	}
}

func TestHandleAdminVocabulary_RespectsOffsetParam(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?offset=40", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotOffset != 40 {
		t.Errorf("expected offset=40 to reach VocabularyStats, got %d", repo.gotOffset)
	}
}

func TestHandleAdminVocabulary_RespectsSortAndDirParams(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?sort=term&dir=asc", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotSortBy != "term" || repo.gotSortDir != "asc" {
		t.Errorf("expected sort=term dir=asc to reach VocabularyStats, got sort=%q dir=%q", repo.gotSortBy, repo.gotSortDir)
	}
}

func TestHandleAdminVocabulary_InvalidSortAndDirFallBackToDefaults(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?sort=bogus&dir=sideways", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if repo.gotSortBy != "doc_freq" || repo.gotSortDir != "desc" {
		t.Errorf("expected an invalid sort/dir to fall back to doc_freq/desc, got sort=%q dir=%q", repo.gotSortBy, repo.gotSortDir)
	}
}

func TestHandleAdminVocabulary_ReportsMatchedCountSeparateFromVocabularySize(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 500, matchedCount: 3}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?search=cat", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		VocabularySize int `json:"vocabulary_size"`
		MatchedCount   int `json:"matched_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.VocabularySize != 500 || resp.MatchedCount != 3 {
		t.Errorf("expected vocabulary_size=500 matched_count=3 (a filtered search matching far fewer than the whole corpus), got %+v", resp)
	}
}

// TestHandleAdminVocabulary_NonNumericLimitFallsBackToDefault proves
// intQueryParam's parse-failure branch: a non-numeric ?limit= is treated
// the same as an absent one, not a 400.
func TestHandleAdminVocabulary_NonNumericLimitFallsBackToDefault(t *testing.T) {
	repo := &fakeAdminRepo{vocabularySize: 1}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/vocabulary?limit=not-a-number", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (fallback to default), got %d", rec.Code)
	}
	if repo.gotLimit != 20 {
		t.Errorf("expected the built-in default limit (20) on a non-numeric value, got %d", repo.gotLimit)
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
		TopDomains:          []domain.DomainSummary{{Host: "example.com", DocCount: 2}},
		AgeBuckets:          []domain.AgeBucket{{Label: "last 24h", Count: 2}},
		TotalDomains:        5,
		VersionCounts:       []domain.VersionCount{{Version: 1, Count: 8}, {Version: 2, Count: 3}},
		StoredVersionCounts: []domain.StoredVersionsCount{{StoredVersions: 1, DocCount: 8}, {StoredVersions: 2, DocCount: 3}},
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
		TopDomains          []map[string]interface{} `json:"top_domains"`
		AgeBuckets          []map[string]interface{} `json:"age_buckets"`
		TotalDomains        int                      `json:"total_domains"`
		VersionCounts       []map[string]interface{} `json:"version_counts"`
		StoredVersionCounts []map[string]interface{} `json:"stored_version_counts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.TopDomains) != 1 || resp.TopDomains[0]["host"] != "example.com" {
		t.Errorf("unexpected top_domains: %v", resp.TopDomains)
	}
	if len(resp.AgeBuckets) != 1 || resp.AgeBuckets[0]["label"] != "last 24h" {
		t.Errorf("unexpected age_buckets: %v", resp.AgeBuckets)
	}
	if resp.TotalDomains != 5 {
		t.Errorf("expected total_domains=5, got %d", resp.TotalDomains)
	}
	if len(resp.VersionCounts) != 2 || resp.VersionCounts[0]["version"] != float64(1) || resp.VersionCounts[0]["count"] != float64(8) {
		t.Errorf("unexpected version_counts: %v", resp.VersionCounts)
	}
	if len(resp.StoredVersionCounts) != 2 || resp.StoredVersionCounts[0]["stored_versions"] != float64(1) || resp.StoredVersionCounts[0]["doc_count"] != float64(8) {
		t.Errorf("unexpected stored_version_counts: %v", resp.StoredVersionCounts)
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

func TestHandleAdminOverviewMetrics_Success(t *testing.T) {
	now := time.Now().UTC()
	jobs := &fakeJobService{jobs: []domain.CrawlJobSummary{
		{ID: "job-running", Status: domain.CrawlJobRunning, PagesCrawled: 7, Request: domain.CrawlJobRequest{SeedURLs: []string{"https://a.example"}}},
		{ID: "job-queued", Status: domain.CrawlJobQueued},
		{ID: "job-done", Status: domain.CrawlJobDone},
	}}
	schedules := &fakeScheduledCrawlStore{schedules: []domain.ScheduledCrawl{
		{ID: "s-enabled-overdue", Enabled: true, NextRunAt: now.Add(-time.Hour)},
		{ID: "s-enabled-future", Enabled: true, NextRunAt: now.Add(time.Hour)},
		{ID: "s-disabled", Enabled: false, NextRunAt: now.Add(-time.Hour)}, // overdue-looking but disabled -- must not count
		{ID: "s-in-progress", Enabled: true, InProgress: true, NextRunAt: now.Add(time.Hour)},
	}}
	admin := &fakeAdminRepo{
		poolStats:   sql.DBStats{MaxOpenConnections: 10, OpenConnections: 4, InUse: 2, Idle: 2},
		jobOutcomes: []domain.CrawlJobOutcomeCount{{Status: domain.CrawlJobDone, Count: 3}, {Status: domain.CrawlJobFailed, Count: 1}},
		dailyFetchOutcomes: []domain.DailyFetchOutcome{
			{Date: "2025-01-01", Status: domain.CrawlPageIndexed, Count: 5},
			{Date: "2025-01-01", Status: domain.CrawlPageFetchFailed, Count: 1},
			{Date: "2025-01-02", Status: domain.CrawlPageIndexed, Count: 2},
		},
		documentsByDay:      []domain.DailyCount{{Date: "2025-01-01", Count: 4}, {Date: "2025-01-02", Count: 6}},
		fetchDurationByDay:  []domain.DailyAvgDuration{{Date: "2025-01-01", AvgDurationMs: 120.5}},
		pageRankBuckets:     []domain.PageRankBucket{{Label: "0e+00–1e-01", Count: 9}},
		pageRankOrphanCount: 1,
		pageRankTotalDocs:   10,
	}
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, admin, jobs, schedules)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		RunningCrawlJobs int `json:"running_crawl_jobs"`
		QueuedCrawlJobs  int `json:"queued_crawl_jobs"`
		RunningJobs      []struct {
			ID           string   `json:"id"`
			SeedURLs     []string `json:"seed_urls"`
			PagesCrawled int      `json:"pages_crawled"`
		} `json:"running_jobs"`
		SchedulesEnabled    int `json:"schedules_enabled"`
		SchedulesDisabled   int `json:"schedules_disabled"`
		SchedulesInProgress int `json:"schedules_in_progress"`
		SchedulesOverdue    int `json:"schedules_overdue"`
		Pool                struct {
			MaxOpenConnections int `json:"max_open_connections"`
			InUse              int `json:"in_use"`
			Idle               int `json:"idle"`
		} `json:"pool"`
		JobOutcomes []struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		} `json:"job_outcomes"`
		DailyFetchOutcomes []struct {
			Date     string         `json:"date"`
			Outcomes map[string]int `json:"outcomes"`
		} `json:"daily_fetch_outcomes"`
		DocumentsByDay []struct {
			Date  string `json:"date"`
			Count int    `json:"count"`
		} `json:"documents_by_day"`
		FetchDurationByDay []struct {
			Date          string  `json:"date"`
			AvgDurationMs float64 `json:"avg_duration_ms"`
		} `json:"fetch_duration_by_day"`
		PageRankBuckets []struct {
			Label string `json:"label"`
			Count int    `json:"count"`
		} `json:"pagerank_buckets"`
		PageRankOrphanThreshold float64 `json:"pagerank_orphan_threshold"`
		PageRankOrphanCount     int     `json:"pagerank_orphan_count"`
		PageRankOrphanPercent   float64 `json:"pagerank_orphan_percent"`
		PageRankTotalDocs       int     `json:"pagerank_total_docs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if resp.RunningCrawlJobs != 1 || resp.QueuedCrawlJobs != 1 {
		t.Errorf("expected 1 running and 1 queued job, got running=%d queued=%d", resp.RunningCrawlJobs, resp.QueuedCrawlJobs)
	}
	if len(resp.RunningJobs) != 1 || resp.RunningJobs[0].ID != "job-running" || resp.RunningJobs[0].PagesCrawled != 7 ||
		len(resp.RunningJobs[0].SeedURLs) != 1 || resp.RunningJobs[0].SeedURLs[0] != "https://a.example" {
		t.Errorf("unexpected running_jobs: %+v", resp.RunningJobs)
	}
	if resp.SchedulesEnabled != 3 || resp.SchedulesDisabled != 1 || resp.SchedulesInProgress != 1 || resp.SchedulesOverdue != 1 {
		t.Errorf("unexpected schedule health: enabled=%d disabled=%d in_progress=%d overdue=%d",
			resp.SchedulesEnabled, resp.SchedulesDisabled, resp.SchedulesInProgress, resp.SchedulesOverdue)
	}
	if resp.Pool.MaxOpenConnections != 10 || resp.Pool.InUse != 2 || resp.Pool.Idle != 2 {
		t.Errorf("unexpected pool passthrough: %+v", resp.Pool)
	}
	if len(resp.JobOutcomes) != 2 || resp.JobOutcomes[0].Status != "done" || resp.JobOutcomes[0].Count != 3 {
		t.Errorf("unexpected job_outcomes: %+v", resp.JobOutcomes)
	}
	if len(resp.DailyFetchOutcomes) != 2 ||
		resp.DailyFetchOutcomes[0].Date != "2025-01-01" || resp.DailyFetchOutcomes[0].Outcomes["indexed"] != 5 || resp.DailyFetchOutcomes[0].Outcomes["fetch_failed"] != 1 ||
		resp.DailyFetchOutcomes[1].Date != "2025-01-02" || resp.DailyFetchOutcomes[1].Outcomes["indexed"] != 2 {
		t.Errorf("unexpected daily_fetch_outcomes grouping: %+v", resp.DailyFetchOutcomes)
	}
	if len(resp.DocumentsByDay) != 2 || resp.DocumentsByDay[1].Count != 6 {
		t.Errorf("unexpected documents_by_day: %+v", resp.DocumentsByDay)
	}
	if len(resp.FetchDurationByDay) != 1 || resp.FetchDurationByDay[0].AvgDurationMs != 120.5 {
		t.Errorf("unexpected fetch_duration_by_day: %+v", resp.FetchDurationByDay)
	}
	if len(resp.PageRankBuckets) != 1 || resp.PageRankBuckets[0].Count != 9 {
		t.Errorf("unexpected pagerank_buckets: %+v", resp.PageRankBuckets)
	}
	if resp.PageRankOrphanThreshold != domain.PageRankOrphanThreshold {
		t.Errorf("expected pagerank_orphan_threshold=%v, got %v", domain.PageRankOrphanThreshold, resp.PageRankOrphanThreshold)
	}
	if resp.PageRankOrphanCount != 1 || resp.PageRankTotalDocs != 10 || resp.PageRankOrphanPercent != 10 {
		t.Errorf("unexpected pagerank orphan stats: count=%d total=%d percent=%v",
			resp.PageRankOrphanCount, resp.PageRankTotalDocs, resp.PageRankOrphanPercent)
	}

	// CrawlJobOutcomes' lookback window is documented as 30 days
	// (overviewJobOutcomeDays) -- assert the cutoff it actually received
	// reflects that, not some other tier-2 query's window.
	wantJobOutcomeSince := now.AddDate(0, 0, -30)
	if admin.gotJobOutcomesSince.Sub(wantJobOutcomeSince).Abs() > time.Minute {
		t.Errorf("expected CrawlJobOutcomes since ~%v, got %v", wantJobOutcomeSince, admin.gotJobOutcomesSince)
	}
}

func TestHandleAdminOverviewMetrics_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin repo isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminOverviewMetrics_JobsAndSchedulesNilDegradeGracefully proves
// the endpoint still succeeds (with tier-1 crawl-job/schedule fields simply
// zeroed) when h.jobs/h.scheduledCrawls aren't configured -- only h.admin is
// required.
func TestHandleAdminOverviewMetrics_JobsAndSchedulesNilDegradeGracefully(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RunningCrawlJobs int `json:"running_crawl_jobs"`
		SchedulesEnabled int `json:"schedules_enabled"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.RunningCrawlJobs != 0 || resp.SchedulesEnabled != 0 {
		t.Errorf("expected zeroed tier-1 fields with jobs/scheduledCrawls unconfigured, got %+v", resp)
	}
}

func TestHandleAdminOverviewMetrics_JobsListError(t *testing.T) {
	jobs := &fakeJobService{listErr: errors.New("boom")}
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{}, jobs, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_SchedulesListError(t *testing.T) {
	schedules := &fakeScheduledCrawlStore{listErr: errors.New("boom")}
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{}, nil, schedules)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_CrawlJobOutcomesError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{jobOutcomesErr: errors.New("boom")}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_DailyFetchOutcomesError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{dailyFetchOutcomesErr: errors.New("boom")}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_DocumentsIndexedByDayError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{documentsByDayErr: errors.New("boom")}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_DailyFetchDurationError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{fetchDurationErr: errors.New("boom")}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminOverviewMetrics_PageRankHistogramError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{pageRankHistErr: errors.New("boom")}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// TestHandleAdminOverviewMetrics_EmptyPageRankHistogramSkipsPercent proves
// pagerank_orphan_percent stays 0 (rather than a NaN/divide-by-zero) for an
// empty corpus, where PageRankHistogram reports totalDocs=0.
func TestHandleAdminOverviewMetrics_EmptyPageRankHistogramSkipsPercent(t *testing.T) {
	h, cookie := adminAuthedHandlerWithOverviewDeps(t, &fakeAdminRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview/metrics", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		PageRankOrphanPercent float64 `json:"pagerank_orphan_percent"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.PageRankOrphanPercent != 0 {
		t.Errorf("expected pagerank_orphan_percent=0 for an empty corpus, got %v", resp.PageRankOrphanPercent)
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

func TestHandleAdminVocabularyTermPage_GetServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/vocabulary/term?term=cats", nil)
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

func TestHandleAdminVocabularyTermPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/vocabulary/term?term=cats", nil)
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

// TestHandleAdminPostings_Limit verifies the ?limit= override reaches
// PostingsForTerm, and that a missing/non-numeric value falls back to
// defaultPostingsLimit -- the same convention as the vocabulary and
// document-list endpoints' ?limit= handling.
func TestHandleAdminPostings_Limit(t *testing.T) {
	repo := &fakeAdminRepo{postings: []domain.PostingStats{{DocID: "doc-0"}}}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=katzen&limit=5", nil)
	req.AddCookie(cookie)
	h.RoutesAdmin().ServeHTTP(httptest.NewRecorder(), req)
	if repo.gotLimit != 5 {
		t.Errorf("expected limit=5 to reach PostingsForTerm, got %d", repo.gotLimit)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=katzen", nil)
	req.AddCookie(cookie)
	h.RoutesAdmin().ServeHTTP(httptest.NewRecorder(), req)
	if repo.gotLimit != 500 {
		t.Errorf("expected the built-in default limit (500), got %d", repo.gotLimit)
	}
}

// TestHandleAdminPostings_IncludesURLTitleAndSnippet verifies the
// vocabulary term-detail view's enrichment: each posting's URL, title, and
// a match excerpt (built from the fetched document's text), not just the
// raw doc_id/term_freq/doc_length triple.
func TestHandleAdminPostings_IncludesURLTitleAndSnippet(t *testing.T) {
	postings := []domain.PostingStats{{DocID: "doc-0", TermFreq: 3, DocLength: 10, DocFreq: 1}}
	docs := map[string]domain.Document{
		"doc-0": {ID: "doc-0", URL: "http://a", Title: "Katzen", Text: "Katzen sind toll und flauschig"},
	}
	repo := &fakeAdminRepo{postings: postings, postingsDocs: docs}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/postings?term=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Postings []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"postings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Postings) != 1 {
		t.Fatalf("expected 1 posting, got %+v", resp.Postings)
	}
	p := resp.Postings[0]
	if p.URL != "http://a" || p.Title != "Katzen" || !strings.Contains(p.Snippet, "<mark>") {
		t.Errorf("expected URL/title/highlighted snippet, got %+v", p)
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

// TestHandleAdminSearch_SurfacesScoreBreakdown verifies the per-result
// diagnostic fields the result-detail subpage depends on (normalized
// scores, per-term BM25 breakdown, and the tuning parameters used) all
// reach the wire response -- not just the three original score fields.
func TestHandleAdminSearch_SurfacesScoreBreakdown(t *testing.T) {
	results := []domain.HybridResult{{
		DocID: "doc-0", URL: "http://a", Title: "A",
		BM25Score: 1.2, NormBM25: 0.8, SemanticSim: 0.5,
		PageRank: 0.002, NormalizedPageRank: 0.4, FinalScore: 0.9,
		BM25Terms: []domain.TermScore{{Term: "cats", TermFreq: 3, DocFreq: 10, DocLength: 200, Score: 1.2}},
		Alpha:     0.6, K1: 1.2, B: 0.75, PageRankWeight: 0.1,
	}}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{results: results})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=cats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp []struct {
		NormBM25           float64 `json:"norm_bm25"`
		PageRank           float64 `json:"pagerank"`
		NormalizedPageRank float64 `json:"normalized_pagerank"`
		Alpha              float64 `json:"alpha"`
		K1                 float64 `json:"k1"`
		B                  float64 `json:"b"`
		PageRankWeight     float64 `json:"pagerank_weight"`
		BM25Terms          []struct {
			Term      string  `json:"term"`
			TermFreq  int     `json:"term_freq"`
			DocFreq   int     `json:"doc_freq"`
			DocLength int     `json:"doc_length"`
			Score     float64 `json:"score"`
		} `json:"bm25_terms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp))
	}
	r := resp[0]
	if r.NormBM25 != 0.8 || r.PageRank != 0.002 || r.NormalizedPageRank != 0.4 ||
		r.Alpha != 0.6 || r.K1 != 1.2 || r.B != 0.75 || r.PageRankWeight != 0.1 {
		t.Errorf("unexpected score-breakdown fields: %+v", r)
	}
	if len(r.BM25Terms) != 1 || r.BM25Terms[0].Term != "cats" || r.BM25Terms[0].TermFreq != 3 ||
		r.BM25Terms[0].DocFreq != 10 || r.BM25Terms[0].DocLength != 200 || r.BM25Terms[0].Score != 1.2 {
		t.Errorf("unexpected bm25_terms: %+v", r.BM25Terms)
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
	for _, path := range []string{"/admin/documents", "/admin/crawl", "/admin/jobs", "/admin/settings", "/admin/search", "/admin/search/result"} {
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
	for _, path := range []string{"/admin/documents", "/admin/crawl", "/admin/jobs", "/admin/settings", "/admin/search", "/admin/search/result"} {
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

// waitForDeletedCount polls repo.DeletedIDs() until it reaches want entries
// (handleAdminDeleteDomainDocuments' background goroutine runs
// asynchronously, detached from the request that queued it) or fails the
// test if it never does.
func waitForDeletedCount(t *testing.T, repo *fakeAdminRepo, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(repo.DeletedIDs()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d deletions, got %d: %v", want, len(repo.DeletedIDs()), repo.DeletedIDs())
}

// TestHandleAdminDeleteDomainDocuments_Success proves the core behavior:
// the request returns immediately with the queued count, and every
// document in the domain is deleted via a background goroutine that
// outlives the request itself.
func TestHandleAdminDeleteDomainDocuments_Success(t *testing.T) {
	repo := &fakeAdminRepo{docs: []domain.IndexedDocument{
		{ID: "doc-1"}, {ID: "doc-2"}, {ID: "doc-3"},
	}}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Queued != 3 {
		t.Errorf("expected queued=3, got %d", resp.Queued)
	}
	if repo.gotHost != "example.com" {
		t.Errorf("expected the domain filter passed to ListDocuments, got %q", repo.gotHost)
	}

	waitForDeletedCount(t, repo, 3)
	got := repo.DeletedIDs()
	want := map[string]bool{"doc-1": true, "doc-2": true, "doc-3": true}
	if len(got) != 3 {
		t.Fatalf("expected all 3 documents deleted, got %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected deleted ID %q", id)
		}
	}
}

// TestHandleAdminDeleteDomainDocuments_ContinuesPastOneFailure proves a
// single failed delete doesn't stop the rest of the background batch.
func TestHandleAdminDeleteDomainDocuments_ContinuesPastOneFailure(t *testing.T) {
	repo := &erroringOnFirstDeleteRepo{
		fakeAdminRepo: &fakeAdminRepo{docs: []domain.IndexedDocument{{ID: "doc-1"}, {ID: "doc-2"}}},
	}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	waitForDeletedCount(t, repo.fakeAdminRepo, 2)
}

// erroringOnFirstDeleteRepo makes only the first DeleteDocument call fail,
// so a test can prove the batch continues past it.
type erroringOnFirstDeleteRepo struct {
	*fakeAdminRepo
	failedOnce bool
}

func (r *erroringOnFirstDeleteRepo) DeleteDocument(ctx context.Context, id string) error {
	_ = r.fakeAdminRepo.DeleteDocument(ctx, id)
	if !r.failedOnce {
		r.failedOnce = true
		return errors.New("boom")
	}
	return nil
}

// syncBuffer is a bytes.Buffer safe for one goroutine to write to (via
// log.SetOutput) while another concurrently reads -- a plain bytes.Buffer
// isn't safe for that, and the log line under test here is written by a
// background goroutine the test itself doesn't otherwise synchronize with.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// TestHandleAdminDeleteDomainDocuments_LogsQuoteTheDomainParam proves the
// ?domain= value written to the log on a delete failure is quoted/escaped
// (%q), not written out raw (%s) -- otherwise an embedded CR/LF would let
// a caller forge what looks like a separate, fake log line.
func TestHandleAdminDeleteDomainDocuments_LogsQuoteTheDomainParam(t *testing.T) {
	repo := &erroringOnFirstDeleteRepo{
		fakeAdminRepo: &fakeAdminRepo{docs: []domain.IndexedDocument{{ID: "doc-1"}}},
	}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})

	logBuf := &syncBuffer{}
	origOutput := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(logBuf)
	log.SetFlags(0)
	defer func() { log.SetOutput(origOutput); log.SetFlags(origFlags) }()

	maliciousDomain := "evil.example\n2026-09-15T00:00:00 FAKE-ADMIN-LOGIN user=root"
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?"+url.Values{"domain": {maliciousDomain}}.Encode(), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	waitForDeletedCount(t, repo.fakeAdminRepo, 1)

	// The background goroutine logs asynchronously -- poll briefly for it.
	deadline := time.Now().Add(time.Second)
	for logBuf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	logged := logBuf.String()
	if strings.Contains(logged, "\nFAKE-ADMIN-LOGIN") || strings.Contains(logged, "\n2026-09-15T00:00:00") {
		t.Errorf("expected the embedded newline to be escaped, not written raw, got: %q", logged)
	}
	if !strings.Contains(logged, `\n2026-09-15T00:00:00 FAKE-ADMIN-LOGIN`) {
		t.Errorf("expected the log line to contain the quoted/escaped domain value, got: %q", logged)
	}
}

func TestHandleAdminDeleteDomainDocuments_EmptyDomain(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a missing domain, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDomainDocuments_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Admin: &fakeAdminRepo{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=example.com", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDomainDocuments_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteDomainDocuments_ListErrorReturns500(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("db unavailable")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=example.com", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// TestHandleAdminDeleteDomainDocuments_NoDocumentsQueuesNothing proves an
// empty (or already-empty) domain is a harmless no-op, not an error.
func TestHandleAdminDeleteDomainDocuments_NoDocumentsQueuesNothing(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/documents?domain=empty.example", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Queued int `json:"queued"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Queued != 0 {
		t.Errorf("expected queued=0, got %d", resp.Queued)
	}
}

// TestHandleAdminDeleteDomainDocuments_GetStillUsesListHandler proves the
// two overlapping route registrations for "/admin/api/documents" (a
// method-less GET-only handler, and this DELETE-specific one) route by
// method rather than one shadowing the other.
func TestHandleAdminDeleteDomainDocuments_GetStillUsesListHandler(t *testing.T) {
	repo := &fakeAdminRepo{docs: []domain.IndexedDocument{{ID: "doc-1", URL: "http://a"}}}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/documents", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected GET to still reach the list handler (200), got %d: %s", rec.Code, rec.Body.String())
	}
	var docs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "doc-1" {
		t.Errorf("expected the document list, got %+v", docs)
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

// TestHandleAdminSettings_DefaultRendererFieldRoundTrips mirrors
// TestHandleAdminSettings_FuzzyFieldsRoundTrip for the new
// default_renderer Tuning page knob: GET reports whatever's currently
// set, and a POST updates it.
func TestHandleAdminSettings_DefaultRendererFieldRoundTrips(t *testing.T) {
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{DefaultRenderer: domain.RendererChromium})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, domain.NewTuningSettings(0.5, 1.2, 0.75), opSettings)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Operational struct {
			DefaultRenderer string `json:"default_renderer"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.DefaultRenderer != domain.RendererChromium {
		t.Errorf("expected GET to report default_renderer=chromium, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"default_renderer": "firefox",
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); ov.DefaultRenderer != domain.RendererFirefox {
		t.Errorf("expected default_renderer=firefox to be applied, got %q", ov.DefaultRenderer)
	}
}

// TestHandleAdminSettings_LinkScopeFieldRoundTrips mirrors
// TestHandleAdminSettings_FuzzyFieldsRoundTrip for the new link_scope
// Tuning page knob: GET reports whatever's currently set, and a POST
// updates it.
func TestHandleAdminSettings_LinkScopeFieldRoundTrips(t *testing.T) {
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{LinkScope: domain.LinkScopeHost})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, domain.NewTuningSettings(0.5, 1.2, 0.75), opSettings)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Operational struct {
			LinkScope string `json:"link_scope"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.LinkScope != domain.LinkScopeHost {
		t.Errorf("expected GET to report link_scope=host, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"link_scope": "any",
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); ov.LinkScope != domain.LinkScopeAny {
		t.Errorf("expected link_scope=any to be applied, got %q", ov.LinkScope)
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

// TestHandleAdminSettings_MaxRetainedCrawlJobsFieldRoundTrips mirrors
// TestHandleAdminSettings_ANNSearchEnabledFieldRoundTrips for the new
// max_retained_crawl_jobs knob: GET reports whatever's currently set, and
// a POST updates it.
func TestHandleAdminSettings_MaxRetainedCrawlJobsFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MaxRetainedCrawlJobs: 200})
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
			MaxRetainedCrawlJobs int `json:"max_retained_crawl_jobs"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.MaxRetainedCrawlJobs != 200 {
		t.Errorf("expected GET to report max_retained_crawl_jobs=200, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"max_retained_crawl_jobs": 1000,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if ov := opSettings.Get(); ov.MaxRetainedCrawlJobs != 1000 {
		t.Errorf("expected max_retained_crawl_jobs=1000 to be applied, got %+v", ov)
	}

	var postResp struct {
		Operational struct {
			MaxRetainedCrawlJobs int `json:"max_retained_crawl_jobs"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.MaxRetainedCrawlJobs != 1000 {
		t.Errorf("expected the POST response to echo back max_retained_crawl_jobs=1000, got %+v", postResp.Operational)
	}
}

// TestHandleAdminSettings_MaxDocumentVersionsFieldRoundTrips mirrors
// TestHandleAdminSettings_MaxRetainedCrawlJobsFieldRoundTrips for the
// max_document_versions knob: GET reports whatever's currently set, and a
// POST updates it.
func TestHandleAdminSettings_MaxDocumentVersionsFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MaxDocumentVersions: 5})
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
			MaxDocumentVersions int `json:"max_document_versions"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.MaxDocumentVersions != 5 {
		t.Errorf("expected GET to report max_document_versions=5, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"max_document_versions": 20,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if ov := opSettings.Get(); ov.MaxDocumentVersions != 20 {
		t.Errorf("expected max_document_versions=20 to be applied, got %+v", ov)
	}

	var postResp struct {
		Operational struct {
			MaxDocumentVersions int `json:"max_document_versions"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.MaxDocumentVersions != 20 {
		t.Errorf("expected the POST response to echo back max_document_versions=20, got %+v", postResp.Operational)
	}
}

// TestHandleAdminSettings_TitleWeightFieldRoundTrips mirrors
// TestHandleAdminSettings_MaxDocumentVersionsFieldRoundTrips for the
// title_weight knob: GET reports whatever's currently set, and a POST
// updates it.
func TestHandleAdminSettings_TitleWeightFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{TitleWeight: 2})
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
			TitleWeight int `json:"title_weight"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.TitleWeight != 2 {
		t.Errorf("expected GET to report title_weight=2, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"title_weight": 4,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if ov := opSettings.Get(); ov.TitleWeight != 4 {
		t.Errorf("expected title_weight=4 to be applied, got %+v", ov)
	}

	var postResp struct {
		Operational struct {
			TitleWeight int `json:"title_weight"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.TitleWeight != 4 {
		t.Errorf("expected the POST response to echo back title_weight=4, got %+v", postResp.Operational)
	}
}

// TestHandleAdminSettings_EmbeddingFieldsRoundTrip mirrors
// TestHandleAdminSettings_TitleWeightFieldRoundTrips for the new
// embedding-provider knobs: GET reports the non-secret fields as currently
// set, and a POST updates all of them (including the API key).
func TestHandleAdminSettings_EmbeddingFieldsRoundTrip(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		EmbeddingProvider:       domain.EmbeddingProviderHash,
		EmbeddingHTTPBaseURL:    "http://localhost:11434/v1",
		EmbeddingHTTPModel:      "nomic-embed-text",
		EmbeddingHTTPDimensions: 768,
	})
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, Settings: settings, OpSettings: opSettings,
		NewEmbedder: stubNewEmbedder(nil),
	})

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var getResp struct {
		Operational struct {
			EmbeddingProvider       string `json:"embedding_provider"`
			EmbeddingHTTPBaseURL    string `json:"embedding_http_base_url"`
			EmbeddingHTTPModel      string `json:"embedding_http_model"`
			EmbeddingHTTPDimensions int    `json:"embedding_http_dimensions"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.EmbeddingProvider != domain.EmbeddingProviderHash ||
		getResp.Operational.EmbeddingHTTPBaseURL != "http://localhost:11434/v1" ||
		getResp.Operational.EmbeddingHTTPModel != "nomic-embed-text" ||
		getResp.Operational.EmbeddingHTTPDimensions != 768 {
		t.Errorf("unexpected GET response: %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_provider": "http", "embedding_http_base_url": "https://api.example.com/v1",
			"embedding_http_model": "text-embedding-3-small", "embedding_http_dimensions": 1536,
			"embedding_http_api_key": "sk-new-key",
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
	if ov.EmbeddingProvider != domain.EmbeddingProviderHTTP || ov.EmbeddingHTTPBaseURL != "https://api.example.com/v1" ||
		ov.EmbeddingHTTPModel != "text-embedding-3-small" || ov.EmbeddingHTTPDimensions != 1536 ||
		ov.EmbeddingHTTPAPIKey != "sk-new-key" {
		t.Errorf("expected embedding settings to be applied, got %+v", ov)
	}
}

// TestHandleAdminSettings_EmbeddingAPIKeyNeverInGETResponse proves a
// configured EmbeddingHTTPAPIKey never appears in a GET response body, only
// a boolean indicating one is set -- the same "never echo a real credential
// back" treatment as ScheduledCrawl's Cookie/BasicAuthPass, applied more
// strictly here per this field's own requirement.
func TestHandleAdminSettings_EmbeddingAPIKeyNeverInGETResponse(t *testing.T) {
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		EmbeddingProvider: domain.EmbeddingProviderHTTP, EmbeddingHTTPAPIKey: "sk-super-secret",
	})
	h, cookie := adminAuthedHandlerWithSettings(t, &fakeAdminRepo{}, &fakeDebugSearch{}, domain.NewTuningSettings(0.5, 1.2, 0.75), opSettings)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-super-secret") {
		t.Errorf("expected the GET response to never contain the configured API key, got: %s", rec.Body.String())
	}
	var resp struct {
		Operational struct {
			EmbeddingHTTPAPIKey    string `json:"embedding_http_api_key"`
			EmbeddingHTTPAPIKeySet bool   `json:"embedding_http_api_key_set"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if resp.Operational.EmbeddingHTTPAPIKey != "" {
		t.Errorf("expected embedding_http_api_key to be blank in the GET response, got %q", resp.Operational.EmbeddingHTTPAPIKey)
	}
	if !resp.Operational.EmbeddingHTTPAPIKeySet {
		t.Error("expected embedding_http_api_key_set to report true when a key is configured")
	}
}

// TestHandleAdminSettings_BlankEmbeddingAPIKeyPreservesExisting proves a
// settings save that doesn't touch the API key field (the normal case,
// since the form never shows the real value) doesn't wipe out whatever key
// is already configured.
func TestHandleAdminSettings_BlankEmbeddingAPIKeyPreservesExisting(t *testing.T) {
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		EmbeddingProvider: domain.EmbeddingProviderHTTP, EmbeddingHTTPAPIKey: "sk-keep-me",
	})
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, Settings: domain.NewTuningSettings(0.5, 1.2, 0.75), OpSettings: opSettings,
		NewEmbedder: stubNewEmbedder(nil),
	})

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_provider": "http",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ov := opSettings.Get(); ov.EmbeddingHTTPAPIKey != "sk-keep-me" {
		t.Errorf("expected the existing API key to survive a save that left it blank, got %q", ov.EmbeddingHTTPAPIKey)
	}
}

// postEmbeddingSettings POSTs a minimal valid settings body with
// embedding_provider set to provider against h, returning the decoded
// embedding_test_error field alongside the raw status code.
func postEmbeddingSettings(t *testing.T, h *restapi.Handler, cookie *http.Cookie, provider string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_provider": provider,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp struct {
		EmbeddingTestError string `json:"embedding_test_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return rec.Code, resp.EmbeddingTestError
}

// TestHandleAdminSettings_EmbeddingConnectivityTestReportsSuccess proves a
// save with the HTTP provider configured probes it via NewEmbedder and
// reports no error when that probe succeeds.
func TestHandleAdminSettings_EmbeddingConnectivityTestReportsSuccess(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: domain.NewTuningSettings(0.5, 1.2, 0.75), OpSettings: domain.DefaultOperationalSettings(),
		NewEmbedder: stubNewEmbedder(nil),
	})
	code, testErr := postEmbeddingSettings(t, h, cookie, domain.EmbeddingProviderHTTP)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no embedding_test_error on a successful probe, got %q", testErr)
	}
}

// TestHandleAdminSettings_EmbeddingConnectivityTestReportsFailure proves a
// save still succeeds (200, settings persisted) even when the HTTP
// provider's connectivity probe fails, but surfaces the probe's error in
// embedding_test_error so the admin sees it immediately instead of only
// discovering it on the next real search.
func TestHandleAdminSettings_EmbeddingConnectivityTestReportsFailure(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: domain.NewTuningSettings(0.5, 1.2, 0.75), OpSettings: domain.DefaultOperationalSettings(),
		NewEmbedder: stubNewEmbedder(errors.New("connection refused")),
	})
	code, testErr := postEmbeddingSettings(t, h, cookie, domain.EmbeddingProviderHTTP)
	if code != http.StatusOK {
		t.Fatalf("expected 200 even when the connectivity probe fails, got %d", code)
	}
	if !strings.Contains(testErr, "connection refused") {
		t.Errorf("expected embedding_test_error to surface the probe failure, got %q", testErr)
	}
}

// TestHandleAdminSettings_EmbeddingConnectivityTestSkippedForHashProvider
// proves the probe never runs (NewEmbedder never called, no
// embedding_test_error) when the saved settings configure the hash
// provider -- it can't fail this way, so there's nothing to test.
func TestHandleAdminSettings_EmbeddingConnectivityTestSkippedForHashProvider(t *testing.T) {
	called := false
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: domain.NewTuningSettings(0.5, 1.2, 0.75), OpSettings: domain.DefaultOperationalSettings(),
		NewEmbedder: func(domain.OperationalSettingsValues) ports.EmbeddingProvider {
			called = true
			return fakeEmbeddingProvider{}
		},
	})
	code, testErr := postEmbeddingSettings(t, h, cookie, domain.EmbeddingProviderHash)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no embedding_test_error for the hash provider, got %q", testErr)
	}
	if called {
		t.Error("expected NewEmbedder to never be called for the hash provider")
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
		NewEmbedder: stubNewEmbedder(nil),
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

// testSettingsEncryptionKey is a syntactically valid 32-byte
// settingscrypto key for tests -- its value doesn't matter beyond being
// well-formed hex of the right length.
const testSettingsEncryptionKey = "00000000000000000000000000000000000000000000000000000000000000ab"

// TestHandleAdminSettings_PostEncryptsEmbeddingAPIKeyAtRest proves that
// when SettingsEncryptionKey is configured, the embedding API key is
// persisted encrypted (never the plaintext, anywhere in the stored value)
// while the in-memory opSettings a handler actually uses keeps the real
// plaintext -- see Handler.encryptedOperationalValues' doc comment for why
// only the persisted copy is touched.
func TestHandleAdminSettings_PostEncryptsEmbeddingAPIKeyAtRest(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	key, err := settingscrypto.ParseKey(testSettingsEncryptionKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: settings, OpSettings: opSettings, SettingsStore: repo,
		DBDriver: "sqlite", AdminUser: testAdminUser, AdminPass: testAdminPass,
		SettingsEncryptionKey: key, NewEmbedder: stubNewEmbedder(nil),
	})
	loginBody, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5,
			"min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1,
			"crawl_delay_ms": 1, "max_response_kb": 1,
			"embedding_provider": "http", "embedding_http_api_key": "sk-super-secret",
			"embedding_http_base_url": "http://localhost/v1", "embedding_http_model": "m",
			"embedding_http_dimensions": 8,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rawOp, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyOperational)
	if err != nil || !found {
		t.Fatalf("expected the operational setting to be persisted, found=%v err=%v", found, err)
	}
	if strings.Contains(rawOp, "sk-super-secret") {
		t.Errorf("expected the persisted value to never contain the plaintext API key, got: %s", rawOp)
	}
	var storedOp domain.OperationalSettingsValues
	if err := json.Unmarshal([]byte(rawOp), &storedOp); err != nil {
		t.Fatalf("failed to decode persisted operational settings: %v", err)
	}
	if !strings.HasPrefix(storedOp.EmbeddingHTTPAPIKey, "enc:v1:") {
		t.Errorf("expected the persisted key to carry settingscrypto's enc:v1: prefix, got %q", storedOp.EmbeddingHTTPAPIKey)
	}
	dec, err := settingscrypto.Decrypt(key, storedOp.EmbeddingHTTPAPIKey)
	if err != nil {
		t.Fatalf("unexpected error decrypting the persisted value: %v", err)
	}
	if dec != "sk-super-secret" {
		t.Errorf("expected the persisted value to decrypt back to the real key, got %q", dec)
	}

	// The in-memory settings this same process would use to build its own
	// embedder still hold the real plaintext -- only the persisted copy
	// was encrypted.
	if opSettings.Get().EmbeddingHTTPAPIKey != "sk-super-secret" {
		t.Errorf("expected the in-memory settings to keep the plaintext key, got %q", opSettings.Get().EmbeddingHTTPAPIKey)
	}
}

// TestHandleAdminSettings_PostFallsBackToPlaintextOnEncryptionError proves
// a save still succeeds (persisting the plaintext key, exactly as it
// would with no key configured) if settingscrypto.Encrypt itself ever
// errors -- forced here via a malformed key that bypasses
// settingscrypto.ParseKey's own validation, which is only enforced at
// startup (see cmd/*/main.go), not by Handler itself.
func TestHandleAdminSettings_PostFallsBackToPlaintextOnEncryptionError(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		Settings: settings, OpSettings: opSettings, SettingsStore: repo,
		DBDriver: "sqlite", AdminUser: testAdminUser, AdminPass: testAdminPass,
		SettingsEncryptionKey: []byte("too-short-for-aes"), NewEmbedder: stubNewEmbedder(nil),
	})
	loginBody, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5,
			"min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1,
			"crawl_delay_ms": 1, "max_response_kb": 1,
			"embedding_provider": "http", "embedding_http_api_key": "sk-plain-key",
			"embedding_http_base_url": "http://localhost/v1", "embedding_http_model": "m",
			"embedding_http_dimensions": 8,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rawOp, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyOperational)
	if err != nil || !found {
		t.Fatalf("expected the operational setting to be persisted, found=%v err=%v", found, err)
	}
	if !strings.Contains(rawOp, "sk-plain-key") {
		t.Errorf("expected the fallback-to-plaintext behavior on an encryption error, got: %s", rawOp)
	}
}

// TestHandleAdminSettings_PostWithoutEncryptionKeyStoresPlaintext proves
// the opt-in, non-breaking default: with no SettingsEncryptionKey
// configured, the embedding API key is persisted exactly as before this
// feature existed.
func TestHandleAdminSettings_PostWithoutEncryptionKeyStoresPlaintext(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	h, cookie := adminAuthedHandlerWithSettingsStore(t, settings, opSettings, nil, repo)

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5,
			"min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1,
			"crawl_delay_ms": 1, "max_response_kb": 1,
			"embedding_provider": "http", "embedding_http_api_key": "sk-plain-key",
			"embedding_http_base_url": "http://localhost/v1", "embedding_http_model": "m",
			"embedding_http_dimensions": 8,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rawOp, found, err := repo.GetSetting(context.Background(), ports.SettingsKeyOperational)
	if err != nil || !found {
		t.Fatalf("expected the operational setting to be persisted, found=%v err=%v", found, err)
	}
	if !strings.Contains(rawOp, "sk-plain-key") {
		t.Errorf("expected the persisted value to contain the plaintext key with no encryption key configured, got: %s", rawOp)
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

// erroringSettingsStore always fails to save, letting a test exercise
// persistSetting's "SaveSetting failed" log-and-continue branch.
type erroringSettingsStore struct{}

func (erroringSettingsStore) SaveSetting(context.Context, string, string) error {
	return errors.New("db unavailable")
}
func (erroringSettingsStore) GetSetting(context.Context, string) (string, bool, error) {
	return "", false, nil
}

// TestHandleAdminSettings_PostSucceedsDespiteSettingsStoreSaveError proves
// persistSetting's SaveSetting-error branch is logged and skipped, not
// fatal: the in-process settings are still applied and the request still
// succeeds even though this process's edit can't reach the shared store
// for other processes to pick up.
func TestHandleAdminSettings_PostSucceedsDespiteSettingsStoreSaveError(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	h, cookie := adminAuthedHandlerWithSettingsStore(t, settings, domain.DefaultOperationalSettings(), nil, erroringSettingsStore{})
	body, _ := json.Marshal(map[string]interface{}{
		"tuning":      map[string]float64{"alpha": 0.9, "k1": 2.0, "b": 0.2},
		"operational": map[string]interface{}{"fetch_timeout_seconds": 3, "user_agent": "x", "default_max_pages": 5, "min_text_length": 1, "default_top_k": 1, "session_ttl_hours": 1, "crawl_delay_ms": 1, "max_response_kb": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 despite the settings store failing to save, got %d: %s", rec.Code, rec.Body.String())
	}
	if alpha, _, _ := settings.Get(); alpha != 0.9 {
		t.Errorf("expected the in-process tuning settings applied regardless, got alpha=%v", alpha)
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

// fakePageRankRepo mirrors application package's own test fake -- a
// minimal ports.PageRankRepository the admin recompute handler tests can
// inject errors into independently of fakeAdminRepo.
type fakePageRankRepo struct {
	graph        map[string][]string
	linkGraphErr error
	updated      map[string]float64
	updateErr    error
}

func (f *fakePageRankRepo) LinkGraph(context.Context) (map[string][]string, error) {
	if f.linkGraphErr != nil {
		return nil, f.linkGraphErr
	}
	return f.graph, nil
}

func (f *fakePageRankRepo) UpdatePageRanks(_ context.Context, scores map[string]float64) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = scores
	return nil
}

// adminAuthedHandlerWithPageRank mirrors adminAuthedHandlerWithOverrides,
// adding the PageRank dependency the other helpers don't carry.
func adminAuthedHandlerWithPageRank(t *testing.T, admin ports.AdminRepository, pageRank ports.PageRankRepository, settings *domain.TuningSettings, opSettings *domain.OperationalSettings) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: admin, PageRank: pageRank, Settings: settings, OpSettings: opSettings, DBDriver: "pgx",
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

func TestHandleAdminPageRankPage_GetServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/pagerank", nil)
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

func TestHandleAdminPageRankPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/pagerank", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
}

func TestHandleAdminPageRank_Success(t *testing.T) {
	repo := &fakeAdminRepo{totalDocs: 5, pageRankMin: 0.1, pageRankMax: 0.9, pageRankAvg: 0.5}
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	settings.SetPageRankWeight(0.3)
	opSettings := domain.DefaultOperationalSettings()
	h, cookie := adminAuthedHandlerWithPageRank(t, repo, nil, settings, opSettings)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		TotalDocs                int     `json:"total_docs"`
		MinPageRank              float64 `json:"min_pagerank"`
		MaxPageRank              float64 `json:"max_pagerank"`
		AvgPageRank              float64 `json:"avg_pagerank"`
		Damping                  float64 `json:"damping"`
		MaxIterations            int     `json:"max_iterations"`
		Epsilon                  float64 `json:"epsilon"`
		PageRankWeight           float64 `json:"pagerank_weight"`
		RecomputeIntervalMinutes int     `json:"recompute_interval_minutes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.TotalDocs != 5 || resp.MinPageRank != 0.1 || resp.MaxPageRank != 0.9 || resp.AvgPageRank != 0.5 {
		t.Errorf("unexpected distribution in response: %+v", resp)
	}
	if resp.Damping != domain.PageRankDamping || resp.MaxIterations != domain.PageRankMaxIterations || resp.Epsilon != domain.PageRankEpsilon {
		t.Errorf("expected the domain package's own algorithm constants echoed back, got %+v", resp)
	}
	if resp.PageRankWeight != 0.3 {
		t.Errorf("expected pagerank_weight=0.3 from tuning settings, got %v", resp.PageRankWeight)
	}
	if resp.RecomputeIntervalMinutes != opSettings.Get().PageRankRecomputeIntervalMinutes {
		t.Errorf("expected the operational settings' recompute interval, got %d", resp.RecomputeIntervalMinutes)
	}
}

func TestHandleAdminPageRank_NoTuningSettingsConfigured(t *testing.T) {
	repo := &fakeAdminRepo{}
	h, cookie := adminAuthedHandlerWithPageRank(t, repo, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even without tuning settings configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminPageRank_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin repo isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminPageRank_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{err: errors.New("boom")}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminPageRank_PageRankDistributionError(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{pageRankErr: errors.New("boom")}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when PageRankDistribution fails (even though CorpusStats succeeded), got %d", rec.Code)
	}
}

func TestHandleAdminPageRank_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminPageRankRecompute_Success(t *testing.T) {
	prRepo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}, "b": {"a"}}}
	adminRepo := &fakeAdminRepo{pageRankMin: 0.2, pageRankMax: 0.8, pageRankAvg: 0.5}
	h, cookie := adminAuthedHandlerWithPageRank(t, adminRepo, prRepo, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Documents   int     `json:"documents"`
		Iterations  int     `json:"iterations"`
		FinalDelta  float64 `json:"final_delta"`
		DurationMS  int64   `json:"duration_ms"`
		MinPageRank float64 `json:"min_pagerank"`
		MaxPageRank float64 `json:"max_pagerank"`
		AvgPageRank float64 `json:"avg_pagerank"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Documents != 2 {
		t.Errorf("expected 2 documents scored, got %d", resp.Documents)
	}
	if resp.Iterations <= 0 {
		t.Errorf("expected a positive iteration count, got %d", resp.Iterations)
	}
	if resp.DurationMS < 0 {
		t.Errorf("expected a non-negative duration, got %d", resp.DurationMS)
	}
	if resp.MinPageRank != 0.2 || resp.MaxPageRank != 0.8 || resp.AvgPageRank != 0.5 {
		t.Errorf("expected the post-recompute distribution from the admin repo, got %+v", resp)
	}
	if len(prRepo.updated) != 2 {
		t.Errorf("expected UpdatePageRanks called with both nodes' scores, got %+v", prRepo.updated)
	}
}

func TestHandleAdminPageRankRecompute_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when pagerank isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminPageRankRecompute_LinkGraphError(t *testing.T) {
	prRepo := &fakePageRankRepo{linkGraphErr: errors.New("boom")}
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{}, prRepo, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminPageRankRecompute_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{}, &fakePageRankRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// adminAuthedHandlerWithPageRankAndSettingsStore adds a real SettingsStore
// (see newSettingsStoreTestRepo) to adminAuthedHandlerWithPageRank's setup,
// for the tests below that check domain.PageRankStatus actually persists
// and round-trips through GET /admin/api/pagerank.
func adminAuthedHandlerWithPageRankAndSettingsStore(t *testing.T, pageRank ports.PageRankRepository, store ports.SettingsStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, PageRank: pageRank, SettingsStore: store,
		DBDriver: "pgx", AdminUser: testAdminUser, AdminPass: testAdminPass,
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

// TestHandleAdminPageRankRecompute_PersistsStatusForGetToRead proves the
// two handlers are actually wired together through the settings store --
// POST /recompute's result is what a subsequent GET /pagerank reports,
// not just what the POST response itself said.
func TestHandleAdminPageRankRecompute_PersistsStatusForGetToRead(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	prRepo := &fakePageRankRepo{graph: map[string][]string{"a": {"b"}, "b": {"a"}}}
	h, cookie := adminAuthedHandlerWithPageRankAndSettingsStore(t, prRepo, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var resp struct {
		RecomputeInProgress     bool    `json:"recompute_in_progress"`
		LastRecomputedAt        *string `json:"last_recomputed_at"`
		LastRecomputeDocuments  int     `json:"last_recompute_documents"`
		LastRecomputeIterations int     `json:"last_recompute_iterations"`
		LastRecomputeDurationMs int64   `json:"last_recompute_duration_ms"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.RecomputeInProgress {
		t.Error("expected recompute_in_progress false once the recompute finished")
	}
	if resp.LastRecomputedAt == nil {
		t.Error("expected last_recomputed_at to be set")
	}
	if resp.LastRecomputeDocuments != 2 {
		t.Errorf("expected last_recompute_documents=2, got %d", resp.LastRecomputeDocuments)
	}
	if resp.LastRecomputeIterations <= 0 {
		t.Errorf("expected a positive last_recompute_iterations, got %d", resp.LastRecomputeIterations)
	}
	if resp.LastRecomputeDurationMs < 0 {
		t.Errorf("expected a non-negative last_recompute_duration_ms, got %d", resp.LastRecomputeDurationMs)
	}
}

// TestHandleAdminPageRankRecompute_EmptyGraphReportsRealZeroes guards
// against a real bug: recomputing over an empty link graph legitimately
// scores 0 documents in 0 iterations, and an earlier version of
// adminPageRankResponse used `omitempty` on those int fields -- which
// silently dropped the real zero the same way a genuinely-missing value
// would, so the page showed "undefined" instead of 0.
func TestHandleAdminPageRankRecompute_EmptyGraphReportsRealZeroes(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	prRepo := &fakePageRankRepo{graph: map[string][]string{}}
	h, cookie := adminAuthedHandlerWithPageRankAndSettingsStore(t, prRepo, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/pagerank/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	var resp map[string]interface{}
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if _, ok := resp["last_recompute_documents"]; !ok {
		t.Error("expected last_recompute_documents present (as 0), not omitted, for an empty-graph run")
	}
	if _, ok := resp["last_recompute_iterations"]; !ok {
		t.Error("expected last_recompute_iterations present (as 0), not omitted, for an empty-graph run")
	}
	if _, ok := resp["last_recompute_duration_ms"]; !ok {
		t.Error("expected last_recompute_duration_ms present (as 0), not omitted, for an empty-graph run")
	}
	if resp["last_recompute_documents"] != float64(0) {
		t.Errorf("expected last_recompute_documents=0, got %+v", resp["last_recompute_documents"])
	}
}

// TestHandleAdminPageRank_NoStatusYetOmitsRecomputeFields proves a process
// that's never recomputed (or has no SettingsStore configured) reports
// recompute_in_progress=false and no last_recomputed_at, rather than a
// misleading zero-value timestamp.
func TestHandleAdminPageRank_NoStatusYetOmitsRecomputeFields(t *testing.T) {
	h, cookie := adminAuthedHandlerWithPageRank(t, &fakeAdminRepo{}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/pagerank", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if _, ok := resp["last_recomputed_at"]; ok {
		t.Errorf("expected last_recomputed_at omitted when nothing has recomputed yet, got %+v", resp)
	}
	if resp["recompute_in_progress"] != false {
		t.Errorf("expected recompute_in_progress=false, got %+v", resp["recompute_in_progress"])
	}
}

func TestHandleAdminDatabasePage_GetServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/database", nil)
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

func TestHandleAdminDatabasePage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/database", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
}

func TestHandleAdminDatabase_Success(t *testing.T) {
	repo := &fakeAdminRepo{
		tableRowCounts: map[string]int64{"documents": 3, "postings": 12},
		poolStats:      sql.DBStats{MaxOpenConnections: 25, OpenConnections: 2, InUse: 1, Idle: 1, WaitCount: 4, WaitDuration: 5 * time.Millisecond},
	}
	h, cookie := adminAuthedHandler(t, repo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/database", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Driver string `json:"driver"`
		Pool   struct {
			MaxOpenConnections int   `json:"max_open_connections"`
			OpenConnections    int   `json:"open_connections"`
			InUse              int   `json:"in_use"`
			Idle               int   `json:"idle"`
			WaitCount          int64 `json:"wait_count"`
			WaitDurationMS     int64 `json:"wait_duration_ms"`
		} `json:"pool"`
		TableRows map[string]int64 `json:"table_rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Driver != "pgx" {
		t.Errorf("expected driver echoed back, got %q", resp.Driver)
	}
	if resp.Pool.MaxOpenConnections != 25 || resp.Pool.OpenConnections != 2 || resp.Pool.InUse != 1 || resp.Pool.Idle != 1 || resp.Pool.WaitCount != 4 || resp.Pool.WaitDurationMS != 5 {
		t.Errorf("unexpected pool stats: %+v", resp.Pool)
	}
	if resp.TableRows["documents"] != 3 || resp.TableRows["postings"] != 12 {
		t.Errorf("expected table row counts passed through, got %+v", resp.TableRows)
	}
}

func TestHandleAdminDatabase_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/database", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin repo isn't configured, got %d", rec.Code)
	}
}

func TestHandleAdminDatabase_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{err: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/database", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDatabase_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// fakeEmbeddingRepo is a minimal ports.EmbeddingRepository for exercising
// the embeddings-recompute admin handlers without a real DB. UpdateEmbedding
// is called from handleAdminEmbeddingsRecomputeStart's background goroutine
// while a test's own polling goroutine reads UpdatedCount, so writes go
// through mu like fakeAdminRepo's deletedIDs does.
type fakeEmbeddingRepo struct {
	ids       []string
	docs      map[string]domain.Document
	allIDsErr error

	mu      sync.Mutex
	updated map[string][]float32
}

func (r *fakeEmbeddingRepo) AllDocumentIDs(context.Context) ([]string, error) {
	if r.allIDsErr != nil {
		return nil, r.allIDsErr
	}
	return r.ids, nil
}

func (r *fakeEmbeddingRepo) DocumentsByIDs(_ context.Context, ids []string) (map[string]domain.Document, error) {
	out := make(map[string]domain.Document)
	for _, id := range ids {
		if doc, ok := r.docs[id]; ok {
			out[id] = doc
		}
	}
	return out, nil
}

func (r *fakeEmbeddingRepo) UpdateEmbedding(_ context.Context, id string, vec []float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updated == nil {
		r.updated = make(map[string][]float32)
	}
	r.updated[id] = vec
	return nil
}

// UpdatedCount returns how many documents UpdateEmbedding has been called
// for so far, safe to call concurrently with UpdateEmbedding itself.
func (r *fakeEmbeddingRepo) UpdatedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updated)
}

// adminAuthedHandlerWithEmbedding mirrors adminAuthedHandlerWithPageRank for
// the embeddings-recompute dependencies (EmbeddingRepo/Embedder/SettingsStore).
func adminAuthedHandlerWithEmbedding(t *testing.T, embeddingRepo ports.EmbeddingRepository, embedder ports.EmbeddingProvider, settingsStore ports.SettingsStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, EmbeddingRepo: embeddingRepo, Embedder: embedder, SettingsStore: settingsStore,
		DBDriver: "pgx", AdminUser: testAdminUser, AdminPass: testAdminPass,
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

// waitForEmbeddingRecomputeDone polls store until
// application.LoadEmbeddingRecomputeStatus reports a finished run --
// handleAdminEmbeddingsRecomputeStart's background goroutine runs
// asynchronously, detached from the request that queued it, exactly like
// handleAdminDeleteDomainDocuments' (see waitForDeletedCount above) -- or
// fails the test if it never does.
func waitForEmbeddingRecomputeDone(t *testing.T, store ports.SettingsStore) domain.EmbeddingRecomputeStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := application.LoadEmbeddingRecomputeStatus(context.Background(), store)
		if !status.InProgress && !status.LastRunAt.IsZero() {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the embedding recompute to finish")
	return domain.EmbeddingRecomputeStatus{}
}

func TestHandleAdminEmbeddingsRecomputeStatus_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when embedding recompute isn't configured, got %d", rec.Code)
	}
}

// TestHandleAdminEmbeddingsRecompute_MethodNotAllowed proves a third method
// (neither of the two -- GET for status, POST for start -- separately
// registered for this one path) is rejected. GET and POST are each their
// own registered pattern for /admin/api/embeddings/recompute (see
// handler.go), so net/http's own mux already 405s anything else without
// ever reaching either handler's requireMethod check.
func TestHandleAdminEmbeddingsRecompute_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithEmbedding(t, &fakeEmbeddingRepo{}, fakeEmbeddingProvider{}, nil)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingsRecomputeStatus_NoRunYetReportsZeroValues(t *testing.T) {
	repo := &fakeEmbeddingRepo{}
	adminRepo := &fakeAdminRepo{totalDocs: 42}
	h := restapi.New(restapi.Config{
		Admin: adminRepo, EmbeddingRepo: repo, Embedder: fakeEmbeddingProvider{},
		DBDriver: "pgx", AdminUser: testAdminUser, AdminPass: testAdminPass,
	})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		TotalDocs  int     `json:"total_docs"`
		InProgress bool    `json:"in_progress"`
		LastRunAt  *string `json:"last_run_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.TotalDocs != 42 {
		t.Errorf("expected total_docs=42, got %d", resp.TotalDocs)
	}
	if resp.InProgress {
		t.Error("expected in_progress=false for a process that's never recomputed")
	}
	if resp.LastRunAt != nil {
		t.Errorf("expected no last_run_at for a process that's never recomputed, got %v", *resp.LastRunAt)
	}
}

func TestHandleAdminEmbeddingsRecomputeStart_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when embedding recompute isn't configured, got %d", rec.Code)
	}
}

// TestHandleAdminEmbeddingsRecomputeStart_Success proves the request
// returns immediately (202) and every document is recomputed via a
// background goroutine that outlives the request itself, with the result
// persisted for GET /admin/api/embeddings/recompute to read back.
func TestHandleAdminEmbeddingsRecomputeStart_Success(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeEmbeddingRepo{
		ids: []string{"a", "b"},
		docs: map[string]domain.Document{
			"a": {ID: "a", Text: "hello"},
			"b": {ID: "b", Text: "world"},
		},
	}
	h, cookie := adminAuthedHandlerWithEmbedding(t, repo, fakeEmbeddingProvider{}, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	status := waitForEmbeddingRecomputeDone(t, store)
	if status.Documents != 2 || status.Failed != 0 {
		t.Errorf("expected 2 documents recomputed, 0 failed, got %+v", status)
	}
	if repo.UpdatedCount() != 2 {
		t.Errorf("expected both documents' embeddings written, got %d", repo.UpdatedCount())
	}
}

// TestHandleAdminEmbeddingsRecomputeStart_AlreadyInProgress proves a second
// trigger while one is already running is rejected (409) rather than
// starting a redundant concurrent run -- the status is seeded directly
// into the store rather than relying on a real in-flight goroutine, so the
// test doesn't race against how fast the fake embedder finishes.
func TestHandleAdminEmbeddingsRecomputeStart_AlreadyInProgress(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	inProgress, _ := json.Marshal(domain.EmbeddingRecomputeStatus{InProgress: true})
	if err := store.SaveSetting(context.Background(), ports.SettingsKeyEmbeddingRecomputeStatus, string(inProgress)); err != nil {
		t.Fatalf("seeding in-progress status: %v", err)
	}
	h, cookie := adminAuthedHandlerWithEmbedding(t, &fakeEmbeddingRepo{}, fakeEmbeddingProvider{}, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 when a recompute is already in progress, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminEmbeddingsRecomputeStart_JobErrorIsLoggedNotFatal proves a
// background job error (e.g. AllDocumentIDs failing) is logged rather than
// crashing the detached goroutine, and still clears in_progress back to
// false -- see handleAdminEmbeddingsRecomputeStart's log.Printf branch.
func TestHandleAdminEmbeddingsRecomputeStart_JobErrorIsLoggedNotFatal(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeEmbeddingRepo{allIDsErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithEmbedding(t, repo, fakeEmbeddingProvider{}, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Can't use waitForEmbeddingRecomputeDone here: on this error path
	// LastRunAt is deliberately never set (see
	// RunEmbeddingRecomputeJobWithStatus), so that helper's condition
	// would never be satisfied. Instead wait for the status key to exist
	// (proving the goroutine's first, in-progress=true save already ran)
	// and then read back false -- checking "found" rules out the
	// zero-value default (also InProgress=false) racing this check before
	// the goroutine has done anything at all.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, found, err := store.GetSetting(context.Background(), ports.SettingsKeyEmbeddingRecomputeStatus)
		if err == nil && found {
			var status domain.EmbeddingRecomputeStatus
			if err := json.Unmarshal([]byte(raw), &status); err == nil && !status.InProgress {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for in_progress to clear after a job error")
}

// TestHandleAdminEmbeddingsRecompute_PersistsStatusForGetToRead mirrors
// TestHandleAdminPageRankRecompute_PersistsStatusForGetToRead: proves the
// two handlers are actually wired together through the settings store.
func TestHandleAdminEmbeddingsRecompute_PersistsStatusForGetToRead(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeEmbeddingRepo{ids: []string{"a"}, docs: map[string]domain.Document{"a": {ID: "a", Text: "hello"}}}
	h, cookie := adminAuthedHandlerWithEmbedding(t, repo, fakeEmbeddingProvider{}, store)

	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/recompute", nil)
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", postRec.Code, postRec.Body.String())
	}
	waitForEmbeddingRecomputeDone(t, store)

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/recompute", nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var resp struct {
		InProgress bool    `json:"in_progress"`
		LastRunAt  *string `json:"last_run_at"`
		Documents  int     `json:"documents"`
		Failed     int     `json:"failed"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.InProgress {
		t.Error("expected in_progress=false once the recompute finished")
	}
	if resp.LastRunAt == nil {
		t.Error("expected last_run_at to be set")
	}
	if resp.Documents != 1 || resp.Failed != 0 {
		t.Errorf("expected documents=1, failed=0, got %+v", resp)
	}
}
