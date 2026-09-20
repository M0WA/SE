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
	"reflect"
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

	aliasGroups      []domain.DocumentAliasGroup
	aliasGroupsTotal int
	aliasGroupsErr   error
	gotAliasLimit    int
	gotAliasOffset   int

	clearContentErr     error
	clearContentCalled  bool
	clearSettingsErr    error
	clearSettingsCalled bool

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

func (f *fakeAdminRepo) ListDocumentAliasGroups(_ context.Context, limit, offset int) ([]domain.DocumentAliasGroup, int, error) {
	f.gotAliasLimit = limit
	f.gotAliasOffset = offset
	if f.aliasGroupsErr != nil {
		return nil, 0, f.aliasGroupsErr
	}
	return f.aliasGroups, f.aliasGroupsTotal, nil
}
func (f *fakeAdminRepo) ClearContent(context.Context) error {
	f.clearContentCalled = true
	return f.clearContentErr
}
func (f *fakeAdminRepo) ClearSettings(context.Context) error {
	f.clearSettingsCalled = true
	return f.clearSettingsErr
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
// testEmbeddingConnectivity (the embedding endpoint connectivity-test
// probe) never makes a real network call from the test suite.
type fakeEmbeddingProvider struct {
	err error
}

func (f fakeEmbeddingProvider) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, f.err
}
func (f fakeEmbeddingProvider) Dimensions() int { return 1 }

// stubNewEmbedder returns a restapi.Config.NewEmbedder that always hands
// back a fakeEmbeddingProvider failing with err (nil for success),
// regardless of the candidate endpoint config it's given.
func stubNewEmbedder(err error) func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
	return func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
		return fakeEmbeddingProvider{err: err}
	}
}

// fakeEmbeddingProviderWithModels extends fakeEmbeddingProvider with
// ListModels, satisfying restapi's unexported modelLister interface via
// Go's structural typing -- used to exercise
// handleAdminEmbeddingsModels' success and ListModels-error paths.
// fakeEmbeddingProvider itself (no ListModels method) is what exercises
// its "provider doesn't support listing models" branch.
type fakeEmbeddingProviderWithModels struct {
	fakeEmbeddingProvider
	models    []string
	modelsErr error
}

func (f fakeEmbeddingProviderWithModels) ListModels(context.Context) ([]string, error) {
	return f.models, f.modelsErr
}

// stubNewEmbedderWithModels mirrors stubNewEmbedder, for a
// restapi.Config.NewEmbedder whose embedder also supports ListModels.
func stubNewEmbedderWithModels(models []string, modelsErr error) func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
	return func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
		return fakeEmbeddingProviderWithModels{models: models, modelsErr: modelsErr}
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

func TestHandleAdminSearch_SemanticParamParsesWeightedPairs(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen&semantic=hash:0.3,ionos:0.7", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	want := map[string]float64{"hash": 0.3, "ionos": 0.7}
	if !reflect.DeepEqual(fd.gotOpts.ProviderWeights, want) {
		t.Errorf("expected ProviderWeights %+v, got %+v", want, fd.gotOpts.ProviderWeights)
	}
}

func TestHandleAdminSearch_AbsentSemanticParamLeavesProviderWeightsNil(t *testing.T) {
	fd := &fakeDebugSearch{}
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, fd)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/search?q=katzen", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if fd.gotOpts.ProviderWeights != nil {
		t.Errorf("expected no ?semantic= param to leave ProviderWeights nil (admin default used), got %+v", fd.gotOpts.ProviderWeights)
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

// TestHandleAdminSettings_MaxConcurrentCrawlsFieldRoundTrips mirrors
// TestHandleAdminSettings_MaxRetainedCrawlJobsFieldRoundTrips for the
// max_concurrent_crawls knob: GET reports whatever's currently set, and a
// POST updates it.
func TestHandleAdminSettings_MaxConcurrentCrawlsFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MaxConcurrentCrawls: 3})
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
			MaxConcurrentCrawls int `json:"max_concurrent_crawls"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.MaxConcurrentCrawls != 3 {
		t.Errorf("expected GET to report max_concurrent_crawls=3, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"max_concurrent_crawls": 8,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}

	if ov := opSettings.Get(); ov.MaxConcurrentCrawls != 8 {
		t.Errorf("expected max_concurrent_crawls=8 to be applied, got %+v", ov)
	}

	var postResp struct {
		Operational struct {
			MaxConcurrentCrawls int `json:"max_concurrent_crawls"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decoding POST response: %v", err)
	}
	if postResp.Operational.MaxConcurrentCrawls != 8 {
		t.Errorf("expected the POST response to echo back max_concurrent_crawls=8, got %+v", postResp.Operational)
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

// TestHandleAdminSettings_EmbeddingTitleWeightFieldRoundTrips mirrors
// TestHandleAdminSettings_TitleWeightFieldRoundTrips for the title/body
// embedding blend weight.
func TestHandleAdminSettings_EmbeddingTitleWeightFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{EmbeddingTitleWeight: 0.2})
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
			EmbeddingTitleWeight float64 `json:"embedding_title_weight"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if getResp.Operational.EmbeddingTitleWeight != 0.2 {
		t.Errorf("expected GET to report embedding_title_weight=0.2, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_title_weight": 0.6,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); ov.EmbeddingTitleWeight != 0.6 {
		t.Errorf("expected embedding_title_weight=0.6 to be applied, got %+v", ov)
	}
}

// TestHandleAdminSettings_URLAliasWWWEnabledFieldRoundTrips proves
// url_alias_www_enabled round-trips through GET/POST like every other
// operational field, and that a false value is actually applied (not just
// left at Set's own default, since false is this field's zero value too --
// see domain.OperationalSettingsValues.URLAliasWWWEnabled).
func TestHandleAdminSettings_URLAliasWWWEnabledFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{URLAliasWWWEnabled: true})
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
			URLAliasWWWEnabled bool `json:"url_alias_www_enabled"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if !getResp.Operational.URLAliasWWWEnabled {
		t.Errorf("expected GET to report url_alias_www_enabled=true, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"url_alias_www_enabled": false,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); ov.URLAliasWWWEnabled {
		t.Errorf("expected url_alias_www_enabled=false to be applied, got %+v", ov)
	}
}

// TestHandleAdminSettings_ContentDedupFieldsRoundTrip proves the 4
// content-dedup operational fields round-trip through GET/POST like every
// other operational field, and that content_dedup_enabled=false is actually
// applied (not just left at Set's own default, since false is this field's
// zero value too).
func TestHandleAdminSettings_ContentDedupFieldsRoundTrip(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{
		ContentDedupEnabled: true, ContentDedupMethod: domain.ContentDedupMethodSimHash,
		ContentDedupSimHashMaxDistance: 5, ContentDedupIntervalMinutes: 180,
	})
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
			ContentDedupEnabled            bool   `json:"content_dedup_enabled"`
			ContentDedupMethod             string `json:"content_dedup_method"`
			ContentDedupSimHashMaxDistance int    `json:"content_dedup_simhash_max_distance"`
			ContentDedupIntervalMinutes    int    `json:"content_dedup_interval_minutes"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if !getResp.Operational.ContentDedupEnabled || getResp.Operational.ContentDedupMethod != domain.ContentDedupMethodSimHash ||
		getResp.Operational.ContentDedupSimHashMaxDistance != 5 || getResp.Operational.ContentDedupIntervalMinutes != 180 {
		t.Errorf("expected GET to report the configured content-dedup fields, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"content_dedup_enabled": false, "content_dedup_method": "exact",
			"content_dedup_simhash_max_distance": 4, "content_dedup_interval_minutes": 90,
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
	if ov.ContentDedupEnabled || ov.ContentDedupMethod != domain.ContentDedupMethodExact ||
		ov.ContentDedupSimHashMaxDistance != 4 || ov.ContentDedupIntervalMinutes != 90 {
		t.Errorf("expected the posted content-dedup fields to be applied, got %+v", ov)
	}
}

// TestHandleAdminSettings_EmbeddingHashEnabledFieldRoundTrips proves
// EmbeddingHashEnabled round-trips through GET/POST like every other
// operational field, and that Set no longer forces it back to true (that
// self-healing moved to a live-endpoint-list-aware check -- see
// domain.ReconcileActiveProvider).
func TestHandleAdminSettings_EmbeddingHashEnabledFieldRoundTrips(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{EmbeddingHashEnabled: true})
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
			EmbeddingHashEnabled bool `json:"embedding_hash_enabled"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if !getResp.Operational.EmbeddingHashEnabled {
		t.Errorf("expected GET to report embedding_hash_enabled=true, got %+v", getResp.Operational)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_hash_enabled": false,
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); ov.EmbeddingHashEnabled {
		t.Errorf("expected embedding_hash_enabled=false to be applied, got %+v", ov)
	}
}

// TestHandleAdminSettings_EmbeddingSearchWeightsReconciledAgainstNonexistentEndpoint
// proves domain.ReconcileSearchWeights's self-healing is actually reachable
// through the HTTP API: posting an embedding_search_weights entry naming an
// endpoint that isn't configured (deleted, mistyped, or simply invented)
// comes back as {hash: 1}.
func TestHandleAdminSettings_EmbeddingSearchWeightsReconciledAgainstNonexistentEndpoint(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, Settings: settings, OpSettings: opSettings,
		EmbeddingEndpoints: repo, DBDriver: "sqlite",
	})

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_hash_enabled": true, "embedding_search_weights": map[string]float64{"some-deleted-endpoint": 1},
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); len(ov.EmbeddingSearchWeights) != 1 || ov.EmbeddingSearchWeights[domain.EmbeddingProviderHash] != 1 {
		t.Errorf("expected weights naming a nonexistent endpoint to self-heal to {hash: 1}, got %+v", ov.EmbeddingSearchWeights)
	}
}

// TestHandleAdminSettings_EmbeddingSearchWeightsReconciliationPreservesEnabledEndpoint
// proves the reconciliation doesn't clobber a genuinely valid, currently-
// enabled endpoint's weight.
func TestHandleAdminSettings_EmbeddingSearchWeightsReconciliationPreservesEnabledEndpoint(t *testing.T) {
	settings := domain.NewTuningSettings(0.5, 1.2, 0.75)
	opSettings := domain.DefaultOperationalSettings()
	repo := newSettingsStoreTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, Settings: settings, OpSettings: opSettings,
		EmbeddingEndpoints: repo, DBDriver: "sqlite",
	})

	body, _ := json.Marshal(map[string]interface{}{
		"tuning": map[string]float64{"alpha": 0.5, "k1": 1.2, "b": 0.75},
		"operational": map[string]interface{}{
			"fetch_timeout_seconds": 8, "default_max_pages": 20, "min_text_length": 50,
			"default_top_k": 10, "session_ttl_hours": 12, "crawl_delay_ms": 250, "max_response_kb": 5120,
			"embedding_search_weights": map[string]float64{"ionos": 1},
		},
	})
	postReq := httptest.NewRequest(http.MethodPost, "/admin/api/settings", bytes.NewReader(body))
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", postRec.Code, postRec.Body.String())
	}
	if ov := opSettings.Get(); len(ov.EmbeddingSearchWeights) != 1 || ov.EmbeddingSearchWeights["ionos"] != 1 {
		t.Errorf("expected the enabled endpoint's weight to stay active, got %+v", ov.EmbeddingSearchWeights)
	}
}

type adminEmbeddingModelsResp struct {
	Models []string `json:"models"`
	Error  string   `json:"error"`
}

func postEmbeddingModels(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, adminEmbeddingModelsResp) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/models", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp adminEmbeddingModelsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return rec.Code, resp
}

// TestHandleAdminEmbeddingsModels_AlwaysAvailable proves this endpoint has
// no "not configured" state -- unlike most admin endpoints, a Handler with
// no OpSettings/Admin/etc. configured at all still serves it, since probing
// a candidate config has no optional dependency to gate on (h.newEmbedder
// is always set by New).
func TestHandleAdminEmbeddingsModels_AlwaysAvailable(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/models", bytes.NewReader([]byte("{}")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 even with no optional dependencies configured, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingsModels_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: stubNewEmbedderWithModels(nil, nil),
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/models", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingsModels_InvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: stubNewEmbedderWithModels(nil, nil),
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/models", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminEmbeddingsModels_BlankBaseURLReturnsEmptyWithoutCalling
// proves the "only probe once the endpoint has actually been filled in"
// rule is enforced server-side too, not just left to the frontend.
func TestHandleAdminEmbeddingsModels_BlankBaseURLReturnsEmptyWithoutCalling(t *testing.T) {
	called := false
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			called = true
			return fakeEmbeddingProviderWithModels{}
		},
	})
	code, resp := postEmbeddingModels(t, h, cookie, map[string]interface{}{"base_url": ""})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(resp.Models) != 0 || resp.Error != "" {
		t.Errorf("expected an empty response for a blank base URL, got %+v", resp)
	}
	if called {
		t.Error("expected NewEmbedder to never be called for a blank base URL")
	}
}

func TestHandleAdminEmbeddingsModels_ProviderWithoutListModelsSupport(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: stubNewEmbedder(nil),
	})
	code, resp := postEmbeddingModels(t, h, cookie, map[string]interface{}{"base_url": "https://example.com/v1"})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(resp.Models) != 0 || resp.Error == "" {
		t.Errorf("expected an error explaining models aren't supported, got %+v", resp)
	}
}

func TestHandleAdminEmbeddingsModels_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: stubNewEmbedderWithModels([]string{"intfloat/e5-large-v2", "Qwen/Qwen3-VL-Embedding-8B"}, nil),
	})
	code, resp := postEmbeddingModels(t, h, cookie, map[string]interface{}{"base_url": "https://example.com/v1"})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(resp.Models) != 2 || resp.Models[0] != "intfloat/e5-large-v2" || resp.Models[1] != "Qwen/Qwen3-VL-Embedding-8B" {
		t.Errorf("unexpected models: %+v", resp)
	}
	if resp.Error != "" {
		t.Errorf("expected no error on success, got %q", resp.Error)
	}
}

func TestHandleAdminEmbeddingsModels_ListModelsErrorIsSoftFailure(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: stubNewEmbedderWithModels(nil, errors.New("401 unauthorized")),
	})
	code, resp := postEmbeddingModels(t, h, cookie, map[string]interface{}{"base_url": "https://example.com/v1"})
	if code != http.StatusOK {
		t.Fatalf("expected 200 (a soft failure, not a hard error), got %d", code)
	}
	if len(resp.Models) != 0 || resp.Error != "401 unauthorized" {
		t.Errorf("expected the ListModels error surfaced, got %+v", resp)
	}
}

func postEmbeddingTest(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, string) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/test", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return rec.Code, resp.Error
}

// TestHandleAdminEmbeddingsTest_AlwaysAvailable mirrors
// TestHandleAdminEmbeddingsModels_AlwaysAvailable -- this endpoint has no
// "not configured" state either. base_url is deliberately blank so this
// never attempts a real network call even with the real
// bootstrap.NewHTTPEmbedder (adminAuthedHandler sets no NewEmbedder stub).
func TestHandleAdminEmbeddingsTest_AlwaysAvailable(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{"base_url": ""})
	if code != http.StatusOK {
		t.Errorf("expected 200 even with no optional dependencies configured, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no error for a blank base URL, got %q", testErr)
	}
}

func TestHandleAdminEmbeddingsTest_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, NewEmbedder: stubNewEmbedder(nil),
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/test", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminEmbeddingsTest_Success proves a candidate config probes
// via NewEmbedder and reports no error when the probe succeeds.
func TestHandleAdminEmbeddingsTest_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, NewEmbedder: stubNewEmbedder(nil),
	})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{"base_url": "https://example.com/v1"})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no error on a successful probe, got %q", testErr)
	}
}

// TestHandleAdminEmbeddingsTest_Failure proves the probe's error is
// surfaced in the response (still 200 -- a failed test is a reported
// result, not a request error).
func TestHandleAdminEmbeddingsTest_Failure(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, NewEmbedder: stubNewEmbedder(errors.New("connection refused")),
	})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{"base_url": "https://example.com/v1"})
	if code != http.StatusOK {
		t.Fatalf("expected 200 even when the connectivity probe fails, got %d", code)
	}
	if !strings.Contains(testErr, "connection refused") {
		t.Errorf("expected the probe failure surfaced, got %q", testErr)
	}
}

func TestHandleAdminEmbeddingsTest_BlankBaseURLSkipsProbe(t *testing.T) {
	called := false
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{},
		NewEmbedder: func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			called = true
			return fakeEmbeddingProvider{}
		},
	})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{"base_url": ""})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no error for a blank base URL, got %q", testErr)
	}
	if called {
		t.Error("expected NewEmbedder to never be called for a blank base URL")
	}
}

// TestHandleAdminEmbeddingsTest_FallsBackToStoredAPIKeyByID proves
// resolveCandidateAPIKey's whole reason for existing: the endpoint edit
// page never re-populates the API key field with an already-saved
// endpoint's real value (see embeddingEndpointResponse), so testing it
// without retyping the key must still probe with the real stored key, not
// an empty one -- otherwise every saved endpoint would always fail "Test
// connection" with an auth error regardless of whether its actual stored
// credentials work.
func TestHandleAdminEmbeddingsTest_FallsBackToStoredAPIKeyByID(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", APIKey: "real-stored-key", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var gotAPIKey string
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		NewEmbedder: func(e domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			gotAPIKey = e.APIKey
			return fakeEmbeddingProvider{}
		},
	})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{
		"id": "ionos", "base_url": "https://example.com", "api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no error, got %q", testErr)
	}
	if gotAPIKey != "real-stored-key" {
		t.Errorf("expected the probe to use the real stored API key, got %q", gotAPIKey)
	}
}

// TestHandleAdminEmbeddingsTest_TypedAPIKeyOverridesStored proves a
// newly-typed key always wins over whatever's already stored -- an admin
// actively changing the key (not just re-testing the existing one) must
// probe with what they just typed.
func TestHandleAdminEmbeddingsTest_TypedAPIKeyOverridesStored(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", APIKey: "old-key", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var gotAPIKey string
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		NewEmbedder: func(e domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			gotAPIKey = e.APIKey
			return fakeEmbeddingProvider{}
		},
	})
	postEmbeddingTest(t, h, cookie, map[string]interface{}{
		"id": "ionos", "base_url": "https://example.com", "api_key": "newly-typed-key",
	})
	if gotAPIKey != "newly-typed-key" {
		t.Errorf("expected the newly typed key to win over the stored one, got %q", gotAPIKey)
	}
}

// TestHandleAdminEmbeddingsTest_UnknownIDFallsBackToBlankAPIKey proves an
// id naming an endpoint that no longer exists (deleted concurrently, or a
// stale page) degrades to the same blank-key behavior as a brand new
// endpoint, rather than erroring the request.
func TestHandleAdminEmbeddingsTest_UnknownIDFallsBackToBlankAPIKey(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	var gotAPIKey string
	gotAPIKeySet := false
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		NewEmbedder: func(e domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			gotAPIKey = e.APIKey
			gotAPIKeySet = true
			return fakeEmbeddingProvider{}
		},
	})
	code, testErr := postEmbeddingTest(t, h, cookie, map[string]interface{}{
		"id": "gone", "base_url": "https://example.com", "api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if testErr != "" {
		t.Errorf("expected no error, got %q", testErr)
	}
	if !gotAPIKeySet || gotAPIKey != "" {
		t.Errorf("expected a blank API key for an unknown id, got %q", gotAPIKey)
	}
}

// TestHandleAdminEmbeddingsModels_FallsBackToStoredAPIKeyByID mirrors
// TestHandleAdminEmbeddingsTest_FallsBackToStoredAPIKeyByID for the "List
// available models" probe, which needs the same fallback for the same
// reason.
func TestHandleAdminEmbeddingsModels_FallsBackToStoredAPIKeyByID(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", APIKey: "real-stored-key", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var gotAPIKey string
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		NewEmbedder: func(e domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider {
			gotAPIKey = e.APIKey
			return fakeEmbeddingProviderWithModels{}
		},
	})
	code, resp := postEmbeddingModels(t, h, cookie, map[string]interface{}{
		"id": "ionos", "base_url": "https://example.com", "api_key": "",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.Error != "" {
		t.Errorf("expected no error, got %q", resp.Error)
	}
	if gotAPIKey != "real-stored-key" {
		t.Errorf("expected the probe to use the real stored API key, got %q", gotAPIKey)
	}
}

// embeddingEndpointResp mirrors admin.go's unexported embeddingEndpointResponse
// wire shape, for decoding test responses.
type embeddingEndpointResp struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	BaseURL            string    `json:"base_url"`
	HasAPIKey          bool      `json:"has_api_key"`
	Model              string    `json:"model"`
	Dimensions         int       `json:"dimensions"`
	RateLimitPerSecond float64   `json:"rate_limit_per_second"`
	Enabled            bool      `json:"enabled"`
	ChunkSizeTokens    int       `json:"chunk_size_tokens"`
	TokenizeURL        string    `json:"tokenize_url"`
	CreatedAt          time.Time `json:"created_at"`
}

func adminAuthedHandlerWithEmbeddingEndpoints(t *testing.T, repo ports.EmbeddingEndpointStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		NewEmbedder: stubNewEmbedder(nil),
	})
}

func createTestEmbeddingEndpoint(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, embeddingEndpointResp) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/endpoints", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp embeddingEndpointResp
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding create response: %v", err)
		}
	}
	return rec.Code, resp
}

func TestHandleAdminEmbeddingEndpoints_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/endpoints", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when embedding endpoints aren't configured, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingEndpoints_MethodNotAllowed(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/embeddings/endpoints", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingEndpoints_CreateInvalidJSON(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/endpoints", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminEmbeddingEndpoints_CreateValidation(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)

	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing name", map[string]interface{}{"base_url": "https://example.com", "dimensions": 4}},
		{"missing base_url", map[string]interface{}{"name": "x", "dimensions": 4}},
		{"zero dimensions", map[string]interface{}{"name": "x", "base_url": "https://example.com", "dimensions": 0}},
		{"negative dimensions", map[string]interface{}{"name": "x", "base_url": "https://example.com", "dimensions": -1}},
		{"negative rate limit", map[string]interface{}{"name": "x", "base_url": "https://example.com", "dimensions": 4, "rate_limit_per_second": -1}},
		{"negative chunk size", map[string]interface{}{"name": "x", "base_url": "https://example.com", "dimensions": 4, "chunk_size_tokens": -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/admin/api/embeddings/endpoints", bytes.NewReader(data))
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			h.RoutesAdmin().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHandleAdminEmbeddingEndpoints_CreateThenList proves a created
// endpoint's ID is minted from its name, it never echoes the API key back,
// and it shows up in a subsequent list.
func TestHandleAdminEmbeddingEndpoints_CreateThenList(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)

	code, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS bge-m3", "base_url": "https://example.com/v1", "api_key": "sk-test",
		"model": "BAAI/bge-m3", "dimensions": 1024, "rate_limit_per_second": 5, "enabled": true,
		"chunk_size_tokens": 6000, "tokenize_url": "http://localhost:8000/tokenize",
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if created.ID != "ionos_bge_m3" {
		t.Errorf("expected the ID minted from the name, got %q", created.ID)
	}
	if !created.HasAPIKey {
		t.Errorf("expected has_api_key=true, got %+v", created)
	}
	if created.ChunkSizeTokens != 6000 || created.TokenizeURL != "http://localhost:8000/tokenize" {
		t.Errorf("expected chunk_size_tokens/tokenize_url round-tripped in the create response, got %+v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/endpoints", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRec.Code)
	}
	if strings.Contains(listRec.Body.String(), "sk-test") {
		t.Errorf("expected the list response to never contain the API key, got: %s", listRec.Body.String())
	}
	var list []embeddingEndpointResp
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ionos_bge_m3" {
		t.Errorf("expected the created endpoint listed, got %+v", list)
	}
}

// TestHandleAdminEmbeddingEndpoints_CreateDedupesIDOnNameCollision proves
// two endpoints created with the same name get distinct IDs, per
// domain.NewEmbeddingEndpointID's dedupe rule.
func TestHandleAdminEmbeddingEndpoints_CreateDedupesIDOnNameCollision(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)

	_, first := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "Ollama", "base_url": "https://a.example", "dimensions": 4,
	})
	_, second := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "Ollama", "base_url": "https://b.example", "dimensions": 8,
	})
	if first.ID == second.ID {
		t.Errorf("expected distinct IDs for two endpoints named the same, got both %q", first.ID)
	}
}

func TestHandleAdminGetEmbeddingEndpoint_Success(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "dimensions": 4,
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/endpoints/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got embeddingEndpointResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("expected the created endpoint, got %+v", got)
	}
}

func TestHandleAdminGetEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/endpoints/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminGetEmbeddingEndpoint_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/embeddings/endpoints/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func patchEmbeddingEndpoint(t *testing.T, h *restapi.Handler, cookie *http.Cookie, id string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/embeddings/endpoints/"+id, bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

// TestHandleAdminUpdateEmbeddingEndpoint_ReplacesEditableFields proves a
// PATCH replaces every editable field (not the ID).
func TestHandleAdminUpdateEmbeddingEndpoint_ReplacesEditableFields(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://old.example/v1", "model": "old-model", "dimensions": 4, "enabled": true,
	})

	rec := patchEmbeddingEndpoint(t, h, cookie, created.ID, map[string]interface{}{
		"name": "Renamed", "base_url": "https://new.example/v1", "model": "new-model",
		"dimensions": 8, "rate_limit_per_second": 3, "enabled": false,
		"chunk_size_tokens": 512, "tokenize_url": "http://localhost:8000/tokenize",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Renamed" || got.BaseURL != "https://new.example/v1" || got.Model != "new-model" ||
		got.Dimensions != 8 || got.RateLimitPerSecond != 3 || got.Enabled ||
		got.ChunkSizeTokens != 512 || got.TokenizeURL != "http://localhost:8000/tokenize" {
		t.Errorf("expected every editable field replaced, got %+v", got)
	}
}

// TestHandleAdminUpdateEmbeddingEndpoint_BlankAPIKeyPreservesExisting proves
// a PATCH that leaves api_key blank (the normal case, since the edit form
// never shows the real value) doesn't wipe out whatever key is already
// configured.
func TestHandleAdminUpdateEmbeddingEndpoint_BlankAPIKeyPreservesExisting(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "api_key": "sk-keep-me", "dimensions": 4,
	})

	rec := patchEmbeddingEndpoint(t, h, cookie, created.ID, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "dimensions": 4,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey != "sk-keep-me" {
		t.Errorf("expected the existing API key to survive a PATCH that left it blank, got %q", got.APIKey)
	}
}

// TestHandleAdminUpdateEmbeddingEndpoint_ClearAPIKeyRemovesIt proves
// clear_api_key is the explicit way to actually remove a configured key.
func TestHandleAdminUpdateEmbeddingEndpoint_ClearAPIKeyRemovesIt(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "api_key": "sk-remove-me", "dimensions": 4,
	})

	rec := patchEmbeddingEndpoint(t, h, cookie, created.ID, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "dimensions": 4, "clear_api_key": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey != "" {
		t.Errorf("expected clear_api_key to remove the stored key, got %q", got.APIKey)
	}
}

func TestHandleAdminUpdateEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	rec := patchEmbeddingEndpoint(t, h, cookie, "missing", map[string]interface{}{
		"name": "x", "base_url": "https://example.com", "dimensions": 4,
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateEmbeddingEndpoint_InvalidJSON(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/embeddings/endpoints/anything", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateEmbeddingEndpoint_ValidationFailureBeforeLookup(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	rec := patchEmbeddingEndpoint(t, h, cookie, "missing", map[string]interface{}{"name": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteEmbeddingEndpoint_Success(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "dimensions": 4,
	})

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/embeddings/endpoints/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID); !errors.Is(err, ports.ErrEmbeddingEndpointNotFound) {
		t.Errorf("expected the endpoint gone after delete, got err=%v", err)
	}
}

func TestHandleAdminDeleteEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/embeddings/endpoints/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteEmbeddingEndpoint_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/embeddings/endpoints/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestHandleAdminEmbeddingEndpoints_APIKeyEncryptedAtRest proves that when
// SettingsEncryptionKey is configured, an endpoint's API key is persisted
// encrypted (never the plaintext, anywhere in the stored row) -- mirroring
// this codebase's existing settingscrypto precedent.
func TestHandleAdminEmbeddingEndpoints_APIKeyEncryptedAtRest(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	key, err := settingscrypto.ParseKey(testSettingsEncryptionKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, EmbeddingEndpoints: repo,
		SettingsEncryptionKey: key, NewEmbedder: stubNewEmbedder(nil),
	})

	code, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "api_key": "sk-super-secret", "dimensions": 4,
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}

	stored, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.APIKey == "sk-super-secret" {
		t.Error("expected the stored API key to be encrypted, not plaintext")
	}
	if !strings.HasPrefix(stored.APIKey, "enc:v1:") {
		t.Errorf("expected the stored key to carry settingscrypto's enc:v1: prefix, got %q", stored.APIKey)
	}
	dec, err := settingscrypto.Decrypt(key, stored.APIKey)
	if err != nil {
		t.Fatalf("unexpected error decrypting the stored value: %v", err)
	}
	if dec != "sk-super-secret" {
		t.Errorf("expected the stored value to decrypt back to the real key, got %q", dec)
	}
}

// TestHandleAdminEmbeddingEndpoints_APIKeyPlaintextWithoutEncryptionKey
// proves the opt-in, non-breaking default: with no SettingsEncryptionKey
// configured, the API key is stored exactly as submitted.
func TestHandleAdminEmbeddingEndpoints_APIKeyPlaintextWithoutEncryptionKey(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithEmbeddingEndpoints(t, repo)

	_, created := createTestEmbeddingEndpoint(t, h, cookie, map[string]interface{}{
		"name": "IONOS", "base_url": "https://example.com/v1", "api_key": "sk-plain-key", "dimensions": 4,
	})
	stored, err := repo.GetEmbeddingEndpoint(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.APIKey != "sk-plain-key" {
		t.Errorf("expected the plaintext key stored with no encryption key configured, got %q", stored.APIKey)
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
	bootstrap.SyncSettings(syncCtx, repo, otherProcessSettings, nil, nil, nil, nil)
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

func TestHandleAdminEmbeddingsPage_GetServesPage(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/embeddings", nil)
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

func TestHandleAdminEmbeddingsPage_Unauthenticated_Redirects(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/embeddings", nil)
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

func TestHandleAdminClearContent_Success(t *testing.T) {
	adminRepo := &fakeAdminRepo{}
	h, cookie := adminAuthedHandler(t, adminRepo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-content", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !adminRepo.clearContentCalled {
		t.Error("expected ClearContent called")
	}
}

func TestHandleAdminClearContent_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-content", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminClearContent_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{clearContentErr: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-content", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminClearContent_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/database/clear-content", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminClearSettings_Success proves both the DB clear and the
// immediate in-memory reset of this process's own tuning/operational/
// overrides settings happen on success.
func TestHandleAdminClearSettings_Success(t *testing.T) {
	adminRepo := &fakeAdminRepo{}
	h, cookie := adminAuthedHandler(t, adminRepo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !adminRepo.clearSettingsCalled {
		t.Error("expected ClearSettings called")
	}
}

func TestHandleAdminClearSettings_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminClearSettings_ServiceError(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{clearSettingsErr: errors.New("boom")}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/database/clear-settings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminClearSettings_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/database/clear-settings", nil)
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

func (r *fakeEmbeddingRepo) UpdateEmbedding(_ context.Context, id string, embeddings map[string][]float32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updated == nil {
		r.updated = make(map[string][]float32)
	}
	r.updated[id] = embeddings[domain.EmbeddingProviderHash]
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
		Admin: &fakeAdminRepo{}, EmbeddingRepo: embeddingRepo,
		Embedders:     map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: embedder},
		SettingsStore: settingsStore,
		DBDriver:      "pgx", AdminUser: testAdminUser, AdminPass: testAdminPass,
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
		Admin: adminRepo, EmbeddingRepo: repo,
		Embedders: map[string]ports.EmbeddingProvider{domain.EmbeddingProviderHash: fakeEmbeddingProvider{}},
		DBDriver:  "pgx", AdminUser: testAdminUser, AdminPass: testAdminPass,
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

// fakeContentDedupRepo backs the content-dedup admin handler tests --
// AllDocumentFingerprints/MergeDocuments are the two ports.
// ContentDedupRepository methods application.RunContentDedupJob calls.
type fakeContentDedupRepo struct {
	fingerprints    []domain.DocumentFingerprint
	fingerprintsErr error
	mergeErr        error
	lockBusy        bool

	mu     sync.Mutex
	merges []struct {
		canonicalID string
		loserIDs    []string
		reason      string
	}
}

func (r *fakeContentDedupRepo) TryAcquireContentDedupLock(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.lockBusy, nil
}

func (r *fakeContentDedupRepo) ReleaseContentDedupLock(context.Context) error {
	return nil
}

func (r *fakeContentDedupRepo) AllDocumentFingerprints(context.Context) ([]domain.DocumentFingerprint, error) {
	if r.fingerprintsErr != nil {
		return nil, r.fingerprintsErr
	}
	return r.fingerprints, nil
}

func (r *fakeContentDedupRepo) MergeDocuments(_ context.Context, canonicalID string, loserIDs []string, reason string) error {
	if r.mergeErr != nil {
		return r.mergeErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.merges = append(r.merges, struct {
		canonicalID string
		loserIDs    []string
		reason      string
	}{canonicalID, loserIDs, reason})
	return nil
}

func (r *fakeContentDedupRepo) mergeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.merges)
}

func adminAuthedHandlerWithContentDedup(t *testing.T, repo ports.ContentDedupRepository, opSettings *domain.OperationalSettings, settingsStore ports.SettingsStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Admin: &fakeAdminRepo{}, ContentDedupRepo: repo,
		OpSettings: opSettings, SettingsStore: settingsStore,
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

func waitForContentDedupDone(t *testing.T, store ports.SettingsStore) domain.ContentDedupStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := application.LoadContentDedupStatus(context.Background(), store)
		if !status.InProgress && !status.LastRunAt.IsZero() {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the content dedup recompute to finish")
	return domain.ContentDedupStatus{}
}

func TestHandleAdminContentDedupStatus_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/content-dedup", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when content dedup isn't configured, got %d", rec.Code)
	}
}

// TestHandleAdminContentDedup_MethodNotAllowed proves a third method
// (neither GET for status nor POST for start, separately registered for
// this one path -- see handler.go) is rejected by net/http's own mux before
// ever reaching either handler's requireMethod check.
func TestHandleAdminContentDedup_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithContentDedup(t, &fakeContentDedupRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/content-dedup", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminContentDedupStatus_NoRunYetReportsZeroValues(t *testing.T) {
	h, cookie := adminAuthedHandlerWithContentDedup(t, &fakeContentDedupRepo{}, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/content-dedup", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		InProgress bool    `json:"in_progress"`
		LastRunAt  *string `json:"last_run_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.InProgress {
		t.Error("expected in_progress=false for a process that's never recomputed")
	}
	if resp.LastRunAt != nil {
		t.Errorf("expected no last_run_at for a process that's never recomputed, got %v", *resp.LastRunAt)
	}
}

func TestHandleAdminContentDedupRecomputeStart_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when content dedup isn't configured, got %d", rec.Code)
	}
}

// TestHandleAdminContentDedupRecomputeStart_Success proves the request
// returns immediately (202) and the job runs via a background goroutine
// that outlives the request itself, with the result persisted for GET
// /admin/api/content-dedup to read back.
func TestHandleAdminContentDedupRecomputeStart_Success(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeContentDedupRepo{
		fingerprints: []domain.DocumentFingerprint{
			{ID: "a", URL: "https://example.com/a", Host: "example.com", ContentHash: "h1"},
			{ID: "b", URL: "https://mirror.example/a", Host: "mirror.example", ContentHash: "h1"},
		},
	}
	opSettings := domain.NewOperationalSettings(domain.OperationalSettingsValues{ContentDedupMethod: domain.ContentDedupMethodExact})
	h, cookie := adminAuthedHandlerWithContentDedup(t, repo, opSettings, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	status := waitForContentDedupDone(t, store)
	if status.GroupsFound != 1 || status.DocumentsMerged != 1 {
		t.Errorf("expected 1 group merged (1 document removed), got %+v", status)
	}
	if repo.mergeCount() != 1 {
		t.Errorf("expected exactly one MergeDocuments call, got %d", repo.mergeCount())
	}
}

// TestHandleAdminContentDedupRecomputeStart_AlreadyInProgress proves a
// second trigger while one is already running is rejected (409) rather
// than starting a redundant concurrent run.
func TestHandleAdminContentDedupRecomputeStart_AlreadyInProgress(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	inProgress, _ := json.Marshal(domain.ContentDedupStatus{InProgress: true})
	if err := store.SaveSetting(context.Background(), ports.SettingsKeyContentDedupStatus, string(inProgress)); err != nil {
		t.Fatalf("seeding in-progress status: %v", err)
	}
	h, cookie := adminAuthedHandlerWithContentDedup(t, &fakeContentDedupRepo{}, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 when a recompute is already in progress, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminContentDedupRecomputeStart_LosesLockRaceToAnotherProcess
// covers the gap the fast-path InProgress check above can't close: the
// status flag it inspects synchronously said "not running" (nothing
// seeded here), but by the time the spawned goroutine actually calls
// RunContentDedupJobWithStatus, cmd/crawl's own scheduler has already
// taken the real lock -- see ports.ErrContentDedupAlreadyRunning's doc
// comment. Still 202 (the response was already decided before the race
// could even happen); the real assertion is that the job never touches
// fingerprints/merges or the persisted status once it loses that race.
func TestHandleAdminContentDedupRecomputeStart_LosesLockRaceToAnotherProcess(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeContentDedupRepo{lockBusy: true}
	h, cookie := adminAuthedHandlerWithContentDedup(t, repo, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// No positive "it's done" signal exists to poll for here (that's the
	// whole point -- losing the lock race means nothing ever runs), so
	// give the detached goroutine a brief, generous window to have acted
	// if it were going to, then assert it didn't.
	time.Sleep(100 * time.Millisecond)
	if repo.mergeCount() > 0 {
		t.Error("expected no merges to be attempted after losing the lock race")
	}
	if _, found, err := store.GetSetting(context.Background(), ports.SettingsKeyContentDedupStatus); err != nil || found {
		t.Errorf("expected no status write when the lock race is lost, found=%v err=%v", found, err)
	}
}

// TestHandleAdminContentDedupRecomputeStart_JobErrorIsLoggedNotFatal proves
// a background job error (e.g. AllDocumentFingerprints failing) is logged
// rather than crashing the detached goroutine, and still clears
// in_progress back to false.
func TestHandleAdminContentDedupRecomputeStart_JobErrorIsLoggedNotFatal(t *testing.T) {
	store := newSettingsStoreTestRepo(t)
	repo := &fakeContentDedupRepo{fingerprintsErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithContentDedup(t, repo, nil, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/recompute", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	// Can't use waitForContentDedupDone here: on this error path LastRunAt
	// is deliberately never set (see RunContentDedupJobWithStatus), so
	// that helper's condition would never be satisfied -- same reasoning
	// as TestHandleAdminEmbeddingsRecomputeStart_JobErrorIsLoggedNotFatal.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, found, err := store.GetSetting(context.Background(), ports.SettingsKeyContentDedupStatus)
		if err == nil && found {
			var status domain.ContentDedupStatus
			if err := json.Unmarshal([]byte(raw), &status); err == nil && !status.InProgress {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for in_progress to clear after a job error")
}

func TestHandleAdminContentDedupAliasGroups_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, nil, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/content-dedup/alias-groups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin diagnostics aren't configured, got %d", rec.Code)
	}
}

func TestHandleAdminContentDedupAliasGroups_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/content-dedup/alias-groups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminContentDedupAliasGroups_Success proves the endpoint lists
// merged groups (the "what actually got merged" transparency listing) and
// forwards limit/offset to the repository for pagination.
func TestHandleAdminContentDedupAliasGroups_Success(t *testing.T) {
	adminRepo := &fakeAdminRepo{
		aliasGroups: []domain.DocumentAliasGroup{
			{CanonicalID: "doc-1", CanonicalURL: "https://example.com/a", Aliases: []domain.DocumentAlias{
				{URL: "https://www.example.com/a", Reason: domain.DocumentAliasReasonCanonicalTag},
			}},
		},
		aliasGroupsTotal: 7,
	}
	h, cookie := adminAuthedHandler(t, adminRepo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/content-dedup/alias-groups?limit=5&offset=10", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if adminRepo.gotAliasLimit != 5 || adminRepo.gotAliasOffset != 10 {
		t.Errorf("expected limit=5 offset=10 forwarded, got limit=%d offset=%d", adminRepo.gotAliasLimit, adminRepo.gotAliasOffset)
	}
	var resp struct {
		Total  int `json:"total"`
		Groups []struct {
			CanonicalID  string `json:"canonical_id"`
			CanonicalURL string `json:"canonical_url"`
			Aliases      []struct {
				URL    string `json:"url"`
				Reason string `json:"reason"`
			} `json:"aliases"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Total != 7 || len(resp.Groups) != 1 || resp.Groups[0].CanonicalID != "doc-1" || len(resp.Groups[0].Aliases) != 1 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.Groups[0].Aliases[0].Reason != "canonical_tag" {
		t.Errorf("expected the alias's own reason surfaced, got %+v", resp.Groups[0].Aliases[0])
	}
}

func TestHandleAdminContentDedupAliasGroups_ServiceError(t *testing.T) {
	adminRepo := &fakeAdminRepo{aliasGroupsErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandler(t, adminRepo, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/content-dedup/alias-groups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// chatEndpointResp mirrors admin.go's unexported chatEndpointResponse wire
// shape, for decoding test responses.
type chatEndpointResp struct {
	BaseURL              string    `json:"base_url"`
	HasAPIKey            bool      `json:"has_api_key"`
	Model                string    `json:"model"`
	Enabled              bool      `json:"enabled"`
	MaxContextTokens     int       `json:"max_context_tokens"`
	WebSearchEnabled     bool      `json:"web_search_enabled"`
	WebSearchBaseURL     string    `json:"web_search_base_url"`
	WebSearchResultCount int       `json:"web_search_result_count"`
	SystemPrompt         string    `json:"system_prompt"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func adminAuthedHandlerWithChatEndpoints(t *testing.T, store ports.ChatEndpointStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, ChatEndpoints: store,
	})
}

func getChatEndpoint(t *testing.T, h *restapi.Handler, cookie *http.Cookie) (int, chatEndpointResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-endpoint", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp chatEndpointResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding get response: %v", err)
		}
	}
	return rec.Code, resp
}

func patchChatEndpoint(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-endpoint", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

func TestHandleAdminChatEndpoint_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-endpoint", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminChatEndpoint_MethodNotAllowed(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-endpoint", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminChatEndpoint_GetDefaultsWhenNothingSaved proves a GET
// never fails just because nothing's been saved yet -- same spirit as
// /admin/api/settings always succeeding.
func TestHandleAdminChatEndpoint_GetDefaultsWhenNothingSaved(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	code, resp := getChatEndpoint(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.WebSearchEnabled || resp.WebSearchResultCount != domain.DefaultChatWebSearchResultCount {
		t.Errorf("expected web search off by default (needs a base URL configured) with a default result count, got %+v", resp)
	}
	if resp.HasAPIKey || resp.BaseURL != "" || resp.Model != "" || resp.Enabled || resp.WebSearchBaseURL != "" {
		t.Errorf("expected zero-ish defaults otherwise, got %+v", resp)
	}
}

func TestHandleAdminChatEndpoint_GetSavedValueMasksKey(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "api_key": "sk-secret", "model": "gpt-x", "enabled": true,
	})

	code, resp := getChatEndpoint(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !resp.HasAPIKey || resp.BaseURL != "https://example.com/v1" || resp.Model != "gpt-x" || !resp.Enabled {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestHandleAdminChatEndpoint_GetStoreError(t *testing.T) {
	store := &fakeChatEndpointStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, store)
	code, _ := getChatEndpoint(t, h, cookie)
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

// TestHandleAdminChatEndpoint_PatchCreatesNewConfig proves a PATCH with no
// prior saved config creates one, never echoes the API key back, and
// clamps web_search_result_count via domain.ChatEndpoint.Clamp.
func TestHandleAdminChatEndpoint_PatchCreatesNewConfig(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "api_key": "sk-test", "model": "gpt-x",
		"enabled": true, "max_context_tokens": 6000,
		"web_search_enabled": true, "web_search_base_url": "http://127.0.0.1:8888", "web_search_result_count": 999,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Errorf("expected the response to never contain the API key, got: %s", rec.Body.String())
	}
	var resp chatEndpointResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.HasAPIKey || resp.BaseURL != "https://example.com/v1" || resp.Model != "gpt-x" ||
		!resp.Enabled {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.MaxContextTokens != 6000 {
		t.Errorf("expected max_context_tokens round tripped, got %d", resp.MaxContextTokens)
	}
	if !resp.WebSearchEnabled || resp.WebSearchBaseURL != "http://127.0.0.1:8888" {
		t.Errorf("expected web search fields round tripped, got %+v", resp)
	}
	if resp.WebSearchResultCount != domain.MaxChatWebSearchResultCount {
		t.Errorf("expected web_search_result_count clamped to the max, got %d", resp.WebSearchResultCount)
	}
	if resp.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt set, got zero value")
	}

	got, err := repo.GetChatEndpoint(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey == "" {
		t.Errorf("expected a stored API key, got empty")
	}
}

// TestHandleAdminChatEndpoint_PatchBlankAPIKeyPreservesExisting mirrors
// TestHandleAdminUpdateEmbeddingEndpoint_BlankAPIKeyPreservesExisting's same
// "blank api_key on update means unchanged" convention.
func TestHandleAdminChatEndpoint_PatchBlankAPIKeyPreservesExisting(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "api_key": "sk-keep-me", "model": "gpt-x",
	})

	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "model": "gpt-x-2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := repo.GetChatEndpoint(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey != "sk-keep-me" {
		t.Errorf("expected the existing API key to survive a PATCH that left it blank, got %q", got.APIKey)
	}
	if got.Model != "gpt-x-2" {
		t.Errorf("expected model updated, got %q", got.Model)
	}
}

// TestHandleAdminChatEndpoint_PatchClearAPIKeyRemovesIt proves
// clear_api_key is the explicit way to actually remove a configured key.
func TestHandleAdminChatEndpoint_PatchClearAPIKeyRemovesIt(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "api_key": "sk-remove-me",
	})

	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "clear_api_key": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := repo.GetChatEndpoint(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.APIKey != "" {
		t.Errorf("expected clear_api_key to remove the stored key, got %q", got.APIKey)
	}
}

// TestHandleAdminChatEndpoint_PatchNegativeMaxContextTokensRejected mirrors
// TestHandleAdminEmbeddingEndpoints_CreateValidation's negative-chunk-size
// case for the same reason: MaxContextTokens shares ChunkSizeTokens' "0
// disables, negative is invalid" convention.
func TestHandleAdminChatEndpoint_PatchNegativeMaxContextTokensRejected(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "model": "gpt-x", "max_context_tokens": -1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminChatEndpoint_PatchInvalidJSON(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-endpoint", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminChatEndpoint_PatchLookupErrorPropagates proves a
// PATCH's own preserve-the-existing-key lookup surfaces a real store
// error (anything other than ports.ErrChatEndpointNotConfigured) as 500,
// rather than silently treating it as "no prior key."
func TestHandleAdminChatEndpoint_PatchLookupErrorPropagates(t *testing.T) {
	store := &fakeChatEndpointStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, store)
	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{"base_url": "https://example.com"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminChatEndpoint_PatchStoreError(t *testing.T) {
	store := &fakeChatEndpointStore{getErr: ports.ErrChatEndpointNotConfigured, setErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, store)
	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{"base_url": "https://example.com"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminChatEndpoint_PatchSystemPromptRoundTrips proves
// system_prompt round-trips through PATCH and a subsequent GET, and that a
// PATCH omitting it clears it back to "" (unlike api_key, it has no
// preserve-when-blank special case -- every PATCH is a full replace of it).
func TestHandleAdminChatEndpoint_PatchSystemPromptRoundTrips(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatEndpoints(t, repo)

	rec := patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "model": "gpt-x", "system_prompt": "You are a helpful librarian.",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp chatEndpointResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.SystemPrompt != "You are a helpful librarian." {
		t.Errorf("expected system_prompt round tripped in the PATCH response, got %q", resp.SystemPrompt)
	}

	code, got := getChatEndpoint(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if got.SystemPrompt != "You are a helpful librarian." {
		t.Errorf("expected system_prompt round tripped in a subsequent GET, got %q", got.SystemPrompt)
	}

	rec = patchChatEndpoint(t, h, cookie, map[string]interface{}{
		"base_url": "https://example.com/v1", "model": "gpt-x",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.SystemPrompt != "" {
		t.Errorf("expected a PATCH omitting system_prompt to clear it, got %q", resp.SystemPrompt)
	}
}

// chatHookResp mirrors admin.go's unexported chatHookResponse wire shape,
// for decoding test responses.
type chatHookResp struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Pattern          string `json:"pattern"`
	Script           string `json:"script"`
	Enabled          bool   `json:"enabled"`
	Prompt           string `json:"prompt"`
	GatedByWebSearch bool   `json:"gated_by_web_search"`
}

func adminAuthedHandlerWithChatHooks(t *testing.T, store ports.ChatHookStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, ChatHooks: store,
	})
}

func createTestChatHook(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, chatHookResp) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/chat-hooks", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp chatHookResp
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding create response: %v", err)
		}
	}
	return rec.Code, resp
}

func patchChatHook(t *testing.T, h *restapi.Handler, cookie *http.Cookie, id string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-hooks/"+id, bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

func TestHandleAdminChatHooks_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when chat hooks aren't configured, got %d", rec.Code)
	}
}

func TestHandleAdminChatHooks_MethodNotAllowed(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-hooks", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminChatHooks_CreateInvalidJSON(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/chat-hooks", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminChatHooks_CreateValidation covers every
// validateChatHookRequest rejection branch: empty name, empty script, a
// pattern that fails to compile, and a pattern with the wrong capture
// group count (zero or more than one) -- see runChatHooks's security
// doc comment for why exactly one capture group is enforced here.
func TestHandleAdminChatHooks_CreateValidation(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)

	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing name", map[string]interface{}{"pattern": `SEARCH\((.+)\)`, "script": "search.sh"}},
		{"missing script", map[string]interface{}{"name": "web_search", "pattern": `SEARCH\((.+)\)`}},
		{"invalid regex", map[string]interface{}{"name": "web_search", "pattern": `SEARCH\((.+`, "script": "search.sh"}},
		{"no capture groups", map[string]interface{}{"name": "web_search", "pattern": `SEARCH`, "script": "search.sh"}},
		{"two capture groups", map[string]interface{}{"name": "web_search", "pattern": `SEARCH\((.+)\)-(.+)`, "script": "search.sh"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/admin/api/chat-hooks", bytes.NewReader(data))
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			h.RoutesAdmin().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHandleAdminChatHooks_CreateThenList proves a created hook's ID is
// minted from its name and it shows up in a subsequent list.
func TestHandleAdminChatHooks_CreateThenList(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)

	code, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "Web Search", "pattern": `SEARCH\((.+)\)`, "script": "search.sh", "enabled": true,
		"prompt": "To search the web, output SEARCH(query).", "gated_by_web_search": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if created.ID != "web_search" {
		t.Errorf("expected the ID minted from the name, got %q", created.ID)
	}
	if created.Pattern != `SEARCH\((.+)\)` || created.Script != "search.sh" || !created.Enabled {
		t.Errorf("expected every field round tripped in the create response, got %+v", created)
	}
	if created.Prompt != "To search the web, output SEARCH(query)." || !created.GatedByWebSearch {
		t.Errorf("expected prompt and gated_by_web_search round tripped in the create response, got %+v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRec.Code)
	}
	var list []chatHookResp
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].ID != "web_search" {
		t.Errorf("expected the created hook listed, got %+v", list)
	}
}

// TestHandleAdminChatHooks_CreateDedupesIDOnNameCollision proves two hooks
// created with the same name get distinct IDs, per domain.NewChatHookID's
// dedupe rule.
func TestHandleAdminChatHooks_CreateDedupesIDOnNameCollision(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)

	_, first := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	_, second := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `B\((.+)\)`, "script": "b.sh",
	})
	if first.ID == second.ID {
		t.Errorf("expected distinct IDs for two hooks named the same, got both %q", first.ID)
	}
}

func TestHandleAdminChatHooks_ListError(t *testing.T) {
	store := &fakeChatHookStore{listErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminChatHooks_CreateListErrorPropagates(t *testing.T) {
	store := &fakeChatHookStore{listErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)
	code, _ := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

func TestHandleAdminChatHooks_CreateStoreError(t *testing.T) {
	store := &fakeChatHookStore{createErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)
	code, _ := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

func TestHandleAdminGetChatHook_Success(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	_, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got chatHookResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("expected the created hook, got %+v", got)
	}
}

func TestHandleAdminGetChatHook_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminGetChatHook_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminGetChatHook_ListError(t *testing.T) {
	store := &fakeChatHookStore{listErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// TestHandleAdminUpdateChatHook_ReplacesEditableFields proves a PATCH
// replaces every editable field (not the ID).
func TestHandleAdminUpdateChatHook_ReplacesEditableFields(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	_, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh", "enabled": true,
		"prompt": "original prompt", "gated_by_web_search": true,
	})

	rec := patchChatHook(t, h, cookie, created.ID, map[string]interface{}{
		"name": "renamed", "pattern": `B\((.+)\)`, "script": "b.sh", "enabled": false,
		"prompt": "renamed prompt", "gated_by_web_search": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp chatHookResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.ID != created.ID {
		t.Errorf("expected ID unchanged by PATCH, got %q, was %q", resp.ID, created.ID)
	}
	if resp.Name != "renamed" || resp.Pattern != `B\((.+)\)` || resp.Script != "b.sh" || resp.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", resp)
	}
	if resp.Prompt != "renamed prompt" || resp.GatedByWebSearch {
		t.Errorf("expected prompt and gated_by_web_search replaced, got %+v", resp)
	}
}

// TestHandleAdminChatHooks_PromptAndGatedByWebSearchRoundTrip proves the new
// prompt/gated_by_web_search fields flow through create, get, and update
// unchanged -- backed by fakeChatHookStore (not the real sqlrepo-backed
// newSettingsStoreTestRepo helper) so this test does not depend on the
// sqlrepo chat_hooks migration for these two columns landing.
func TestHandleAdminChatHooks_PromptAndGatedByWebSearchRoundTrip(t *testing.T) {
	store := &fakeChatHookStore{}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)

	code, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
		"prompt": "hook prompt", "gated_by_web_search": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if created.Prompt != "hook prompt" || !created.GatedByWebSearch {
		t.Errorf("expected prompt and gated_by_web_search in the create response, got %+v", created)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/"+created.ID, nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	var got chatHookResp
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding get response: %v", err)
	}
	if got.Prompt != "hook prompt" || !got.GatedByWebSearch {
		t.Errorf("expected prompt and gated_by_web_search in the get response, got %+v", got)
	}

	rec := patchChatHook(t, h, cookie, created.ID, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
		"prompt": "updated prompt", "gated_by_web_search": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated chatHookResp
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	if updated.Prompt != "updated prompt" || updated.GatedByWebSearch {
		t.Errorf("expected prompt and gated_by_web_search replaced by PATCH, got %+v", updated)
	}
}

func TestHandleAdminUpdateChatHook_InvalidJSON(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-hooks/anything", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateChatHook_InvalidPattern(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	_, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	rec := patchChatHook(t, h, cookie, created.ID, map[string]interface{}{
		"name": "hook", "pattern": `no groups here`, "script": "a.sh",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUpdateChatHook_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	rec := patchChatHook(t, h, cookie, "missing", map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUpdateChatHook_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	rec := patchChatHook(t, h, cookie, "anything", map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateChatHook_StoreError(t *testing.T) {
	store := &fakeChatHookStore{updateErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithChatHooks(t, store)
	rec := patchChatHook(t, h, cookie, "anything", map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDeleteChatHook_Success(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	_, created := createTestChatHook(t, h, cookie, map[string]interface{}{
		"name": "hook", "pattern": `A\((.+)\)`, "script": "a.sh",
	})

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-hooks/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/admin/api/chat-hooks/"+created.ID, nil)
	getReq.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("expected the deleted hook to 404 afterward, got %d", getRec.Code)
	}
}

func TestHandleAdminDeleteChatHook_NotFound(t *testing.T) {
	repo := newSettingsStoreTestRepo(t)
	h, cookie := adminAuthedHandlerWithChatHooks(t, repo)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-hooks/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteChatHook_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-hooks/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
