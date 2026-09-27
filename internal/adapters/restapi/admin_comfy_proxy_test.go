package restapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestHandleAdminComfyProxy_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerFromConfig(t, restapi.Config{Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}})
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminComfyProxy_NotEnabledReturns404(t *testing.T) {
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: false, ControlBaseURL: "http://10.7.226.11:8002"}}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminComfyProxy_NeverSavedReturns404(t *testing.T) {
	store := &fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminComfyProxy_StoreErrorReturns500(t *testing.T) {
	store := &fakeGPUModeStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminComfyProxy_MisconfiguredBaseURLReturnsBadGateway(t *testing.T) {
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true, ControlBaseURL: "not a url"}}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminComfyProxy_ForwardsWithTokenAndStrippedPrefix(t *testing.T) {
	var gotPath, gotToken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("comfy-ui-body"))
	}))
	defer upstream.Close()

	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{
		Enabled: true, ControlBaseURL: upstream.URL, ControlAPIKey: "shh-token",
	}}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "comfy-ui-body" {
		t.Fatalf("expected the upstream body proxied through, got %q", rec.Body.String())
	}
	if gotPath != "/gpu/comfy/queue" {
		t.Fatalf("expected /admin/comfy to be rewritten to /gpu/comfy, upstream saw %q", gotPath)
	}
	if gotToken != "shh-token" {
		t.Fatalf("expected the control API key forwarded as X-Internal-Token, got %q", gotToken)
	}
}

// TestHandleAdminComfyProxy_RegularUserAllowed proves this route is
// deliberately not admin-only: any authenticated signed-in user (role
// "user", not "admin") reaches it too -- see handleAdminComfyProxy's own
// doc comment for why.
func TestHandleAdminComfyProxy_RegularUserAllowed(t *testing.T) {
	var gotToken string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Internal-Token")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("comfy-ui-body"))
	}))
	defer upstream.Close()

	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{
		Enabled: true, ControlBaseURL: upstream.URL, ControlAPIKey: "shh-token",
	}}
	h := restapi.New(restapi.Config{Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, GPUMode: store, Sessions: fakeUserRoleSessionStore{}})
	cookie := &http.Cookie{Name: "se_session", Value: "user-test-token"}

	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a regular signed-in user, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotToken != "shh-token" {
		t.Fatalf("expected the control API key forwarded as X-Internal-Token, got %q", gotToken)
	}
}

func TestHandleAdminComfyProxy_UnauthenticatedRedirectsToLogin(t *testing.T) {
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true, ControlBaseURL: "http://10.7.226.11:8002"}}
	h, _ := adminAuthedHandlerWithGPUMode(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/comfy/queue", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a 303 redirect to /login, got %d: %s", rec.Code, rec.Body.String())
	}
}
