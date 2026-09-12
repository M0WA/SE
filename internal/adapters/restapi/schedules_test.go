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
func (f *fakeScheduledCrawlStore) MarkScheduledCrawlRun(context.Context, string, time.Time, time.Time) error {
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

func TestHandleAdminSchedulesPage_RequiresAuth(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/admin/schedules", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", rec.Code)
	}
}

func TestHandleAdminSchedulesPage_ServesWhenAuthenticated(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin/schedules", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
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

func TestHandleAdminSchedules_PostCreatesSchedule(t *testing.T) {
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
		t.Error("expected a freshly created schedule to be enabled")
	}
	if store.created.IntervalMinutes != 20 {
		t.Errorf("expected interval_minutes 20, got %d", store.created.IntervalMinutes)
	}
	wantNext := store.created.CreatedAt.Add(20 * time.Minute)
	if store.created.NextRunAt.Sub(wantNext).Abs() > time.Second {
		t.Errorf("expected NextRunAt ~%v, got %v", wantNext, store.created.NextRunAt)
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

func TestHandleAdminSchedules_PostZeroInterval(t *testing.T) {
	h, cookie := adminAuthedHandlerWithSchedules(t, &fakeScheduledCrawlStore{})
	body, _ := json.Marshal(map[string]interface{}{"seed_urls": []string{"http://a"}})
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
