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

// fakeChatVisionStore is a minimal ports.ChatVisionStore fake, same shape
// as fakeChatEndpointStore.
type fakeChatVisionStore struct {
	settings domain.ChatVisionSettings
	getErr   error
	setErr   error
}

func (f *fakeChatVisionStore) GetChatVisionSettings(ctx context.Context) (domain.ChatVisionSettings, error) {
	if f.getErr != nil {
		return domain.ChatVisionSettings{}, f.getErr
	}
	return f.settings, nil
}

func (f *fakeChatVisionStore) SetChatVisionSettings(ctx context.Context, v domain.ChatVisionSettings) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.settings = v
	return nil
}

// chatVisionResp mirrors admin.go's unexported chatVisionResponse wire shape.
type chatVisionResp struct {
	SimilarityEnabled    bool      `json:"similarity_enabled"`
	SimilarityProviderID string    `json:"similarity_provider_id"`
	CaptionEnabled       bool      `json:"caption_enabled"`
	CaptionBaseURL       string    `json:"caption_base_url"`
	HasCaptionAPIKey     bool      `json:"has_caption_api_key"`
	CaptionModel         string    `json:"caption_model"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func adminAuthedHandlerWithChatVision(t *testing.T, store ports.ChatVisionStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{
		Admin: &fakeAdminRepo{}, Debug: &fakeDebugSearch{}, ChatVision: store,
	})
}

func getChatVision(t *testing.T, h *restapi.Handler, cookie *http.Cookie) (int, chatVisionResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-vision", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	var resp chatVisionResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding get response: %v", err)
		}
	}
	return rec.Code, resp
}

func patchChatVision(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-vision", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	return rec
}

func TestHandleAdminChatVision_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandler(t, &fakeAdminRepo{}, &fakeDebugSearch{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/chat-vision", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminChatVision_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithChatVision(t, &fakeChatVisionStore{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/chat-vision", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAdminChatVision_GetDefaultsWhenNothingSaved proves a GET never
// fails just because nothing's been saved yet.
func TestHandleAdminChatVision_GetDefaultsWhenNothingSaved(t *testing.T) {
	store := &fakeChatVisionStore{getErr: ports.ErrChatVisionSettingsNotConfigured}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	code, resp := getChatVision(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if resp.SimilarityEnabled || resp.CaptionEnabled || resp.HasCaptionAPIKey {
		t.Errorf("expected zero-ish defaults, got %+v", resp)
	}
}

func TestHandleAdminChatVision_GetSavedValueMasksKey(t *testing.T) {
	h, cookie := adminAuthedHandlerWithChatVision(t, &fakeChatVisionStore{})
	patchChatVision(t, h, cookie, map[string]interface{}{
		"similarity_enabled": true, "similarity_provider_id": "h200_gte_qwen2",
		"caption_enabled": true, "caption_base_url": "http://x/v1", "caption_api_key": "sk-secret", "caption_model": "vl-chat",
	})

	code, resp := getChatVision(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !resp.SimilarityEnabled || resp.SimilarityProviderID != "h200_gte_qwen2" {
		t.Errorf("expected similarity fields, got %+v", resp)
	}
	if !resp.CaptionEnabled || resp.CaptionBaseURL != "http://x/v1" || resp.CaptionModel != "vl-chat" || !resp.HasCaptionAPIKey {
		t.Errorf("expected caption fields, got %+v", resp)
	}
}

func TestHandleAdminChatVision_GetStoreError(t *testing.T) {
	store := &fakeChatVisionStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	code, _ := getChatVision(t, h, cookie)
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

// TestHandleAdminChatVision_PatchCreatesNewConfig proves a PATCH with no
// prior saved config creates one, never echoing the API key back.
func TestHandleAdminChatVision_PatchCreatesNewConfig(t *testing.T) {
	store := &fakeChatVisionStore{getErr: ports.ErrChatVisionSettingsNotConfigured}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	rec := patchChatVision(t, h, cookie, map[string]interface{}{
		"similarity_enabled": true, "similarity_provider_id": "h200_gte_qwen2",
		"caption_enabled": true, "caption_base_url": "http://x/v1", "caption_api_key": "sk-test", "caption_model": "vl-chat",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Errorf("expected the response to never contain the API key, got: %s", rec.Body.String())
	}
	var resp chatVisionResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.SimilarityEnabled || resp.SimilarityProviderID != "h200_gte_qwen2" || !resp.HasCaptionAPIKey {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt set, got zero value")
	}
	if store.settings.CaptionAPIKey == "" {
		t.Errorf("expected a stored API key, got empty")
	}
}

// TestHandleAdminChatVision_PatchBlankAPIKeyPreservesExisting mirrors
// TestHandleAdminChatEndpoint_PatchBlankAPIKeyPreservesExisting's "blank
// means unchanged" convention.
func TestHandleAdminChatVision_PatchBlankAPIKeyPreservesExisting(t *testing.T) {
	store := &fakeChatVisionStore{}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	patchChatVision(t, h, cookie, map[string]interface{}{
		"caption_base_url": "http://x/v1", "caption_api_key": "sk-keep-me", "caption_model": "vl-chat",
	})

	rec := patchChatVision(t, h, cookie, map[string]interface{}{
		"caption_base_url": "http://x/v1", "caption_model": "vl-chat-2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.settings.CaptionAPIKey != "sk-keep-me" {
		t.Errorf("expected the existing API key to survive a PATCH that left it blank, got %q", store.settings.CaptionAPIKey)
	}
	if store.settings.CaptionModel != "vl-chat-2" {
		t.Errorf("expected model updated, got %q", store.settings.CaptionModel)
	}
}

// TestHandleAdminChatVision_PatchClearAPIKeyRemovesIt proves
// clear_caption_api_key is the explicit way to actually remove a
// configured key.
func TestHandleAdminChatVision_PatchClearAPIKeyRemovesIt(t *testing.T) {
	store := &fakeChatVisionStore{}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	patchChatVision(t, h, cookie, map[string]interface{}{
		"caption_base_url": "http://x/v1", "caption_api_key": "sk-remove-me",
	})

	rec := patchChatVision(t, h, cookie, map[string]interface{}{
		"caption_base_url": "http://x/v1", "clear_caption_api_key": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.settings.CaptionAPIKey != "" {
		t.Errorf("expected clear_caption_api_key to remove the stored key, got %q", store.settings.CaptionAPIKey)
	}
}

func TestHandleAdminChatVision_PatchInvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerWithChatVision(t, &fakeChatVisionStore{})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/chat-vision", strings.NewReader("not json"))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminChatVision_PatchLookupErrorPropagates(t *testing.T) {
	store := &fakeChatVisionStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	rec := patchChatVision(t, h, cookie, map[string]interface{}{"caption_base_url": "http://x/v1"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminChatVision_PatchStoreError(t *testing.T) {
	store := &fakeChatVisionStore{getErr: ports.ErrChatVisionSettingsNotConfigured, setErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithChatVision(t, store)
	rec := patchChatVision(t, h, cookie, map[string]interface{}{"caption_base_url": "http://x/v1"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
