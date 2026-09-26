package restapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleVisionMode_RequireMethodBranch calls handleVisionMode
// directly (bypassing the mux, where a non-GET /vision/api/mode falls
// through to the "/" catch-all instead -- see vision_mode_test.go) to
// cover its own defensive method check, same reasoning as
// chat_internal_test.go's TestHandleChat_RequireMethodBranch.
func TestHandleVisionMode_RequireMethodBranch(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/vision/api/mode", nil)
	rec := httptest.NewRecorder()
	h.handleVisionMode(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleVisionModeSwitch_RequireMethodBranch(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/vision/api/mode", nil)
	rec := httptest.NewRecorder()
	h.handleVisionModeSwitch(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleVisionHeartbeat_RequireMethodBranch(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/vision/api/heartbeat", nil)
	rec := httptest.NewRecorder()
	h.handleVisionHeartbeat(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
