package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeGPUModeController is a local copy of internal/application's own
// unexported test fake -- restapi_test can't reach it from a different
// package. fakeGPUModeStore itself is already defined in
// gpu_mode_admin_test.go (same package, shared here directly).
type fakeGPUModeController struct {
	status    domain.GPUModeStatus
	statusErr error
	switchErr error
	heartErr  error
}

func (f *fakeGPUModeController) Status(context.Context, domain.GPUModeSettings) (domain.GPUModeStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeGPUModeController) Switch(_ context.Context, _ domain.GPUModeSettings, target domain.GPUMode) (domain.GPUModeStatus, error) {
	if f.switchErr != nil {
		return f.status, f.switchErr
	}
	return domain.GPUModeStatus{Mode: f.status.Mode, Target: target, InProgress: true}, nil
}

func (f *fakeGPUModeController) Heartbeat(context.Context, domain.GPUModeSettings) error {
	return f.heartErr
}

// gpuModeAuthedHandler mirrors chatAuthedHandler, wiring GPUModeService
// (and, optionally, Chat) instead.
func gpuModeAuthedHandler(t *testing.T, svc *application.GPUModeService, chat *application.ChatService) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Search: &fakeSearch{}, Chat: chat, GPUModeService: svc,
		Users: testAdminUsersStore(),
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

func doVisionRequest(t *testing.T, h *restapi.Handler, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

func TestHandleVisionMode_NilServiceIs404(t *testing.T) {
	h, cookie := gpuModeAuthedHandler(t, nil, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/mode", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleVisionMode_MethodNotAllowed proves a non-GET/POST request
// never reaches handleVisionMode's own requireMethod check: since
// "/vision/api/mode" is registered only as "GET ..."/"POST ...", the mux
// falls through to the "/" catch-all (handleIndex), which 404s instead of
// the 405 requireMethod would give -- same reasoning as
// TestHandleChat_MethodNotAllowed.
func TestHandleVisionMode_MethodNotAllowed(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodDelete, "/vision/api/mode", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (falls through to the \"/\" catch-all), got %d", rec.Code)
	}
}

func TestHandleVisionMode_NotEnabledIs404(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/mode", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleVisionMode_StoreErrorIs502(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{getErr: errors.New("db down")}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/mode", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionMode_ControllerErrorIs502(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{statusErr: errors.New("unreachable")})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/mode", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionMode_Success(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/mode", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["mode"] != "chat" {
		t.Errorf("expected mode=chat, got %v", resp)
	}
}

func TestHandleVisionModeSwitch_NilServiceIs404(t *testing.T) {
	h, cookie := gpuModeAuthedHandler(t, nil, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "vision"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleVisionModeSwitch_InvalidJSON(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVisionModeSwitch_InvalidMode(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "sleep"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVisionModeSwitch_NotEnabledIs404(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "vision"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleVisionModeSwitch_ConflictIs409(t *testing.T) {
	controller := &fakeGPUModeController{
		status:    domain.GPUModeStatus{Mode: domain.GPUModeChat, Target: domain.GPUModeVision, InProgress: true},
		switchErr: fmt.Errorf("busy: %w", ports.ErrGPUModeSwitchConflict),
	}
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, controller)
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "chat"})
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionModeSwitch_ControllerErrorIs502(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{switchErr: errors.New("unreachable")})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "vision"})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionModeSwitch_Success(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "vision"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["target"] != "vision" || resp["in_progress"] != true {
		t.Errorf("expected an in-progress switch to vision, got %v", resp)
	}
}

func TestHandleVisionModeSwitch_RateLimitedIs429(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	// First switch succeeds; the second, immediately after, must be
	// blocked by the global dwell (see application.GPUModeService.Switch).
	doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "vision"})
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/mode", map[string]string{"mode": "chat"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionHeartbeat_NilServiceIs404(t *testing.T) {
	h, cookie := gpuModeAuthedHandler(t, nil, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/heartbeat", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleVisionHeartbeat_MethodNotAllowed mirrors
// TestHandleVisionMode_MethodNotAllowed's own reasoning -- "/vision/api/
// heartbeat" is registered only as "POST ...", so a GET falls through to
// the "/" catch-all (404), never reaching requireMethod's own 405.
func TestHandleVisionHeartbeat_MethodNotAllowed(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodGet, "/vision/api/heartbeat", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 (falls through to the \"/\" catch-all), got %d", rec.Code)
	}
}

func TestHandleVisionHeartbeat_NotEnabledIs404(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/heartbeat", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleVisionHeartbeat_ControllerErrorIs502(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{heartErr: errors.New("unreachable")})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/heartbeat", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionHeartbeat_Success(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	h, cookie := gpuModeAuthedHandler(t, svc, nil)
	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/vision/api/heartbeat", nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
}

// --- handleChat's own GPU-mode availability check ---

func TestHandleChat_GPUModeVisionMakesChatUnavailable(t *testing.T) {
	svc := application.NewGPUModeService(
		&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeVision, InProgress: false}},
	)
	// Populate the cache the same way cmd/search's own background poll
	// would, before any chat request arrives.
	svc.RefreshCache(context.Background())

	chatSvc := application.NewChatService(&fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}, &fakeChatCompleter{answer: "hi"}, nil, nil, nil, nil, application.VisionConfig{})
	h, cookie := gpuModeAuthedHandler(t, svc, chatSvc)

	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/chat", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["error"] != "gpu_mode_unavailable" || resp["mode"] != "vision" {
		t.Errorf("unexpected response body: %v", resp)
	}
}

func TestHandleChat_GPUModeChatAllowsChatThrough(t *testing.T) {
	svc := application.NewGPUModeService(
		&fakeGPUModeStore{settings: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}},
	)
	svc.RefreshCache(context.Background())

	chatSvc := application.NewChatService(&fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}, &fakeChatCompleter{answer: "hi there"}, nil, nil, nil, nil, application.VisionConfig{})
	h, cookie := gpuModeAuthedHandler(t, svc, chatSvc)

	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/chat", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChat_GPUModeDisabledAllowsChatThrough(t *testing.T) {
	svc := application.NewGPUModeService(&fakeGPUModeStore{getErr: ports.ErrGPUModeSettingsNotConfigured}, &fakeGPUModeController{})
	svc.RefreshCache(context.Background())

	chatSvc := application.NewChatService(&fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}, &fakeChatCompleter{answer: "hi there"}, nil, nil, nil, nil, application.VisionConfig{})
	h, cookie := gpuModeAuthedHandler(t, svc, chatSvc)

	rec := doVisionRequest(t, h, cookie, http.MethodPost, "/chat", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChat_NilGPUModeServiceAllowsChatThrough(t *testing.T) {
	chatSvc := application.NewChatService(&fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}, &fakeChatCompleter{answer: "hi there"}, nil, nil, nil, nil, application.VisionConfig{})
	h, cookie := chatAuthedHandler(t, chatSvc)

	rec := postChat(t, h, cookie, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
