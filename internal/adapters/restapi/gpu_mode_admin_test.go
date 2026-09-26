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
	"time"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeGPUModeStore is a minimal ports.GPUModeStore fake, same shape as
// fakeChatVisionStore.
type fakeGPUModeStore struct {
	settings domain.GPUModeSettings
	getErr   error
	setErr   error
}

func (f *fakeGPUModeStore) GetGPUModeSettings(ctx context.Context) (domain.GPUModeSettings, error) {
	if f.getErr != nil {
		return domain.GPUModeSettings{}, f.getErr
	}
	return f.settings, nil
}

func (f *fakeGPUModeStore) SetGPUModeSettings(ctx context.Context, v domain.GPUModeSettings) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.settings = v
	return nil
}

// gpuModeResp mirrors admin.go's unexported gpuModeSettingsResponse wire shape.
type gpuModeResp struct {
	Enabled              bool      `json:"enabled"`
	ControlBaseURL       string    `json:"control_base_url"`
	HasControlAPIKey     bool      `json:"has_control_api_key"`
	SwitchTimeoutSeconds int       `json:"switch_timeout_seconds"`
	IdleRevertMinutes    int       `json:"idle_revert_minutes"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func adminAuthedHandlerWithGPUMode(t *testing.T, store ports.GPUModeStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, GPUMode: store,
	})
}

func getGPUMode(t *testing.T, h *restapi.Handler, cookie *http.Cookie) (int, gpuModeResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/gpu-mode", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp gpuModeResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding get response: %v", err)
		}
	}
	return rec.Code, resp
}

func patchGPUMode(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/gpu-mode", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

func TestHandleAdminGPUMode_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/gpu-mode", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminGPUMode_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithGPUMode(t, &fakeGPUModeStore{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/gpu-mode", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminGPUMode_GetDefaultsWhenNothingSaved proves a GET never
// fails just because nothing's been saved yet -- and defaults to Enabled
// false, the master kill switch's own documented default.
func TestHandleAdminGPUMode_GetDefaultsWhenNothingSaved(t *testing.T) {
	store := &fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	code, resp := getGPUMode(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.Enabled || resp.HasControlAPIKey {
		t.Errorf("expected zero-ish defaults (Enabled false), got %+v", resp)
	}
}

func TestHandleAdminGPUMode_GetSavedValueMasksKey(t *testing.T) {
	h, cookie := adminAuthedHandlerWithGPUMode(t, &fakeGPUModeStore{})
	patchGPUMode(t, h, cookie, map[string]interface{}{
		"enabled": true, "control_base_url": "http://10.7.226.11:8002",
		"control_api_key": "sk-secret", "switch_timeout_seconds": 300, "idle_revert_minutes": 15,
	})

	code, resp := getGPUMode(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !resp.Enabled || resp.ControlBaseURL != "http://10.7.226.11:8002" || !resp.HasControlAPIKey {
		t.Errorf("expected control fields, got %+v", resp)
	}
	if resp.SwitchTimeoutSeconds != 300 || resp.IdleRevertMinutes != 15 {
		t.Errorf("expected timeout/revert fields, got %+v", resp)
	}
}

func TestHandleAdminGPUMode_GetStoreError(t *testing.T) {
	store := &fakeGPUModeStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	code, _ := getGPUMode(t, h, cookie)
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

// TestHandleAdminGPUMode_PatchCreatesNewConfig proves a PATCH with no prior
// saved config creates one, never echoing the API key back.
func TestHandleAdminGPUMode_PatchCreatesNewConfig(t *testing.T) {
	store := &fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	rec := patchGPUMode(t, h, cookie, map[string]interface{}{
		"enabled": true, "control_base_url": "http://10.7.226.11:8002",
		"control_api_key": "sk-test", "switch_timeout_seconds": 300, "idle_revert_minutes": 15,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Errorf("expected the response to never contain the API key, got: %s", rec.Body.String())
	}
	var resp gpuModeResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.Enabled || resp.ControlBaseURL != "http://10.7.226.11:8002" || !resp.HasControlAPIKey {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt set, got zero value")
	}
	if store.settings.ControlAPIKey == "" {
		t.Errorf("expected a stored API key, got empty")
	}
}

// TestHandleAdminGPUMode_PatchBlankAPIKeyPreservesExisting mirrors
// TestHandleAdminChatVision_PatchBlankAPIKeyPreservesExisting's "blank
// means unchanged" convention.
func TestHandleAdminGPUMode_PatchBlankAPIKeyPreservesExisting(t *testing.T) {
	store := &fakeGPUModeStore{}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "control_api_key": "sk-keep-me",
	})

	rec := patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "switch_timeout_seconds": 600,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.settings.ControlAPIKey != "sk-keep-me" {
		t.Errorf("expected the existing API key to survive a PATCH that left it blank, got %q", store.settings.ControlAPIKey)
	}
	if store.settings.SwitchTimeoutSeconds != 600 {
		t.Errorf("expected switch_timeout_seconds updated, got %d", store.settings.SwitchTimeoutSeconds)
	}
}

// TestHandleAdminGPUMode_PatchClearAPIKeyRemovesIt proves
// clear_control_api_key is the explicit way to actually remove a
// configured key.
func TestHandleAdminGPUMode_PatchClearAPIKeyRemovesIt(t *testing.T) {
	store := &fakeGPUModeStore{}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "control_api_key": "sk-remove-me",
	})

	rec := patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "clear_control_api_key": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.settings.ControlAPIKey != "" {
		t.Errorf("expected clear_control_api_key to remove the stored key, got %q", store.settings.ControlAPIKey)
	}
}

func TestHandleAdminGPUMode_PatchInvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerWithGPUMode(t, &fakeGPUModeStore{})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/gpu-mode", strings.NewReader("not json"))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminGPUMode_PatchLookupErrorPropagates(t *testing.T) {
	store := &fakeGPUModeStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	rec := patchGPUMode(t, h, cookie, map[string]interface{}{"control_base_url": "http://10.7.226.11:8002"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminGPUMode_PatchStoreError(t *testing.T) {
	store := &fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured, setErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithGPUMode(t, store)
	rec := patchGPUMode(t, h, cookie, map[string]interface{}{"control_base_url": "http://10.7.226.11:8002"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminGPUMode_PatchNegativeSwitchTimeoutSecondsRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithGPUMode(t, &fakeGPUModeStore{})
	rec := patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "switch_timeout_seconds": -1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminGPUMode_PatchNegativeIdleRevertMinutesRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithGPUMode(t, &fakeGPUModeStore{})
	rec := patchGPUMode(t, h, cookie, map[string]interface{}{
		"control_base_url": "http://10.7.226.11:8002", "idle_revert_minutes": -1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
