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

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeScheduledCrawlStore struct {
	schedules []domain.ScheduledCrawl
	listErr   error

	created   domain.ScheduledCrawl
	createErr error

	updated   domain.ScheduledCrawl
	updateErr error

	deletedID string
	deleteErr error
}

func (f *fakeScheduledCrawlStore) CreateScheduledCrawl(_ context.Context, s domain.ScheduledCrawl) error {
	f.created = s
	return f.createErr
}
func (f *fakeScheduledCrawlStore) ListScheduledCrawls(context.Context) ([]domain.ScheduledCrawl, error) {
	return f.schedules, f.listErr
}
func (f *fakeScheduledCrawlStore) UpdateScheduledCrawl(_ context.Context, s domain.ScheduledCrawl) error {
	f.updated = s
	return f.updateErr
}
func (f *fakeScheduledCrawlStore) DeleteScheduledCrawl(_ context.Context, id string) error {
	f.deletedID = id
	return f.deleteErr
}
func (f *fakeScheduledCrawlStore) DueScheduledCrawls(context.Context, time.Time) ([]domain.ScheduledCrawl, error) {
	return nil, nil
}
func (f *fakeScheduledCrawlStore) MarkScheduledCrawlRun(context.Context, string, time.Time, time.Time, bool, int) error {
	return nil
}

var _ ports.ScheduledCrawlStore = (*fakeScheduledCrawlStore)(nil)

func adminAuthedHandlerWithSchedules(t *testing.T, schedules ports.ScheduledCrawlStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		ScheduledCrawls: schedules, DBDriver: "pgx",
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

func TestHandleAdminSchedules_GetListsSchedules(t *testing.T) {
	lastRun := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nextRun := lastRun.Add(30 * time.Minute)
	store := &fakeScheduledCrawlStore{schedules: []domain.ScheduledCrawl{
		{ID: "sched-1", SeedURLs: []string{"http://a"}, MaxPages: 10, IntervalMinutes: 30, Enabled: true, LastRunAt: &lastRun, NextRunAt: nextRun},
	}}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/schedules", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out []struct {
		ID              string   `json:"id"`
		SeedURLs        []string `json:"seed_urls"`
		IntervalMinutes int      `json:"interval_minutes"`
		Enabled         bool     `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(out) != 1 || out[0].ID != "sched-1" || out[0].IntervalMinutes != 30 || !out[0].Enabled {
		t.Errorf("unexpected schedules response: %+v", out)
	}
}

func TestHandleAdminSchedules_GetServiceError(t *testing.T) {
	store := &fakeScheduledCrawlStore{listErr: errors.New("boom")}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/schedules", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/schedules", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{ScheduledCrawls: &fakeScheduledCrawlStore{}, AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/schedules", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_PostCreatesRecurringCrawl(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a.example"}, "max_pages": 15,
		"interval_minutes": 20, "respect_robots": true, "use_sitemap": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.ID == "" {
		t.Error("expected a generated ID")
	}
	if len(store.created.SeedURLs) != 1 || store.created.SeedURLs[0] != "http://a.example" || store.created.MaxPages != 15 {
		t.Errorf("unexpected created schedule: %+v", store.created)
	}
	if !store.created.RespectRobots || !store.created.UseSitemap {
		t.Errorf("expected options to pass through, got %+v", store.created)
	}
	if !store.created.Enabled {
		t.Error("expected a freshly created entry to be enabled")
	}
	if !store.created.Recurring || store.created.IntervalMinutes != 20 {
		t.Errorf("expected a recurring entry with interval_minutes 20, got %+v", store.created)
	}
	wantNext := store.created.CreatedAt.Add(20 * time.Minute)
	if store.created.NextRunAt.Sub(wantNext).Abs() > time.Second {
		t.Errorf("expected NextRunAt ~%v, got %v", wantNext, store.created.NextRunAt)
	}
}

// TestHandleAdminSchedules_PostCreatesOneOffCrawl proves a non-recurring
// entry (the "just crawl this once" case -- see domain.ScheduledCrawl's doc
// comment) needs no interval and is due immediately, not interval_minutes
// from now.
func TestHandleAdminSchedules_PostCreatesOneOffCrawl(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a.example"}, "max_pages": 15,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.Recurring {
		t.Error("expected a non-recurring entry")
	}
	if !store.created.Enabled {
		t.Error("expected a freshly created entry to be enabled (so the scheduler's next tick actually runs it)")
	}
	if store.created.NextRunAt.Sub(store.created.CreatedAt).Abs() > time.Second {
		t.Errorf("expected a non-recurring entry to be due immediately, got NextRunAt=%v CreatedAt=%v", store.created.NextRunAt, store.created.CreatedAt)
	}
}

// TestHandleAdminSchedules_PostReusesExistingScheduleForSameDomain guards
// against a real duplication bug: submitting the crawl form again for a
// domain that already has a schedule must update that schedule in place,
// not insert a second one racing it for the same site.
func TestHandleAdminSchedules_PostReusesExistingScheduleForSameDomain(t *testing.T) {
	store := &fakeScheduledCrawlStore{schedules: []domain.ScheduledCrawl{
		{ID: "sched-1", SeedURLs: []string{"http://a.example/old-path"}, MaxPages: 10, Enabled: false},
	}}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a.example/new-path"}, "max_pages": 30, "interval_minutes": 45,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (updated existing), got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.ID != "" {
		t.Errorf("expected no new schedule created, got %+v", store.created)
	}
	if store.updated.ID != "sched-1" {
		t.Errorf("expected existing schedule sched-1 to be updated, got %+v", store.updated)
	}
	if store.updated.MaxPages != 30 || store.updated.SeedURLs[0] != "http://a.example/new-path" {
		t.Errorf("expected the existing schedule's options replaced, got %+v", store.updated)
	}
	if !store.updated.Enabled {
		t.Error("expected re-submitting a crawl to re-enable its schedule")
	}
}

func TestHandleAdminSchedules_PostDifferentDomainCreatesNewSchedule(t *testing.T) {
	store := &fakeScheduledCrawlStore{schedules: []domain.ScheduledCrawl{
		{ID: "sched-1", SeedURLs: []string{"http://a.example"}, MaxPages: 10},
	}}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://b.example"}, "max_pages": 10,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 (new domain, new schedule), got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.ID == "" || store.created.SeedURLs[0] != "http://b.example" {
		t.Errorf("expected a new schedule for the new domain, got %+v", store.created)
	}
}

func TestHandleAdminSchedules_PostDomainLookupServiceError(t *testing.T) {
	store := &fakeScheduledCrawlStore{listErr: errors.New("boom")}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a.example"}, "max_pages": 10,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_PostEmptySeedURLs(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{"interval_minutes": 20})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminSchedules_PostNoIntervalMeansOneOff proves omitting
// interval_minutes entirely (the zero value) is a perfectly valid one-off
// crawl -- there's no separate "recurring" flag to set, and no validation
// error for leaving the interval blank.
func TestHandleAdminSchedules_PostNoIntervalMeansOneOff(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.Recurring {
		t.Error("expected a blank interval to mean one-off, not recurring")
	}
}

func TestHandleAdminSchedules_PostNegativeIntervalRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "interval_minutes": -5})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_PostNegativeMaxRunsRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "interval_minutes": 30, "max_runs": -1})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminSchedules_PostMaxRunsPassesThrough proves an optional
// MaxRuns cap on a recurring crawl is stored, and 0 (the default, omitted
// here) means unlimited elsewhere.
func TestHandleAdminSchedules_PostMaxRunsPassesThrough(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "interval_minutes": 30, "max_runs": 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.MaxRuns != 5 {
		t.Errorf("expected MaxRuns 5, got %d", store.created.MaxRuns)
	}
}

// TestHandleAdminSchedules_PostRendererPassesThrough proves an explicit
// per-crawl renderer override is stored as-is.
func TestHandleAdminSchedules_PostRendererPassesThrough(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "renderer": "chromium",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.Renderer != "chromium" {
		t.Errorf("expected renderer=chromium, got %q", store.created.Renderer)
	}
}

// TestHandleAdminSchedules_PostBlankRendererMeansInherit proves omitting
// renderer entirely (the zero value) is valid -- it means "inherit the
// Tuning page's global default," not an error.
func TestHandleAdminSchedules_PostBlankRendererMeansInherit(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.Renderer != "" {
		t.Errorf("expected an empty Renderer to mean inherit, got %q", store.created.Renderer)
	}
}

func TestHandleAdminSchedules_PostInvalidRendererRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "renderer": "internet-explorer",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAdminSchedules_PostLinkScopePassesThrough proves an explicit
// per-crawl link_scope override is stored as-is.
func TestHandleAdminSchedules_PostLinkScopePassesThrough(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "link_scope": "host",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.LinkScope != "host" {
		t.Errorf("expected link_scope=host, got %q", store.created.LinkScope)
	}
}

// TestHandleAdminSchedules_PostBlankLinkScopeMeansInherit proves omitting
// link_scope entirely (the zero value) is valid -- it means "inherit the
// Tuning page's global default," not an error.
func TestHandleAdminSchedules_PostBlankLinkScopeMeansInherit(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.created.LinkScope != "" {
		t.Errorf("expected an empty LinkScope to mean inherit, got %q", store.created.LinkScope)
	}
}

// TestHandleAdminSchedules_PostAllowBlockDomainsAndFollowIndexedPassThrough
// proves the new per-crawl allow/block domain lists and FollowIndexedDomains
// are stored as-is, mirroring the existing link_scope/renderer pass-through
// tests.
func TestHandleAdminSchedules_PostAllowBlockDomainsAndFollowIndexedPassThrough(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls":              []string{"http://a"},
		"allowed_domains":        []string{"allowed.example"},
		"blocked_domains":        []string{"blocked.example"},
		"follow_indexed_domains": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.created.AllowedDomains) != 1 || store.created.AllowedDomains[0] != "allowed.example" {
		t.Errorf("expected allowed_domains to pass through, got %+v", store.created.AllowedDomains)
	}
	if len(store.created.BlockedDomains) != 1 || store.created.BlockedDomains[0] != "blocked.example" {
		t.Errorf("expected blocked_domains to pass through, got %+v", store.created.BlockedDomains)
	}
	if !store.created.FollowIndexedDomains {
		t.Error("expected follow_indexed_domains=true to pass through")
	}
}

// TestHandleAdminSchedules_PostAllowBlockDomainsDefaultToEmpty proves
// omitting the new fields entirely is valid -- no allow/block list and
// FollowIndexedDomains false, not an error.
func TestHandleAdminSchedules_PostAllowBlockDomainsDefaultToEmpty(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.created.AllowedDomains) != 0 || len(store.created.BlockedDomains) != 0 || store.created.FollowIndexedDomains {
		t.Errorf("expected empty allow/block lists and FollowIndexedDomains false by default, got %+v", store.created)
	}
}

func TestHandleAdminSchedules_PostInvalidLinkScopeRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "link_scope": "planet",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_PostInvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader([]byte("not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_PostStoreErrorReturns500(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{createErr: errors.New("db unavailable")})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "interval_minutes": 20})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/schedules", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when the store fails to create a schedule, got %d", rec.Code)
	}
}

func TestHandleAdminSchedules_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/schedules", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateSchedule_Success(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"seed_urls": []string{"http://a"}, "max_pages": 5,
		"interval_minutes": 10, "enabled": false,
	})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/schedules/sched-1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.updated.ID != "sched-1" || store.updated.MaxPages != 5 || store.updated.IntervalMinutes != 10 || store.updated.Enabled {
		t.Errorf("unexpected updated schedule: %+v", store.updated)
	}
}

func TestHandleAdminUpdateSchedule_NotFound(t *testing.T) {
	store := &fakeScheduledCrawlStore{updateErr: ports.ErrScheduledCrawlNotFound}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}, "interval_minutes": 10})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/schedules/missing", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateSchedule_InvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/schedules/sched-1", bytes.NewReader([]byte("not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateSchedule_EmptySeedURLs(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{"interval_minutes": 10})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/schedules/sched-1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateSchedule_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, nil)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/schedules/sched-1", bytes.NewReader([]byte("{}")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteSchedule_Success(t *testing.T) {
	store := &fakeScheduledCrawlStore{}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/schedules/sched-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.deletedID != "sched-1" {
		t.Errorf("expected sched-1 to be deleted, got %q", store.deletedID)
	}
}

func TestHandleAdminDeleteSchedule_NotFound(t *testing.T) {
	store := &fakeScheduledCrawlStore{deleteErr: ports.ErrScheduledCrawlNotFound}
	h, cookie := adminAuthedHandlerWithSchedules(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/schedules/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteSchedule_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, nil)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/schedules/sched-1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
