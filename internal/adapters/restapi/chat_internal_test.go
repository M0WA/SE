package restapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleChat_RequireMethodBranch calls handleChat directly (bypassing
// the mux, where a non-POST /chat falls through to the "/" catch-all
// instead -- see chat_test.go) to cover its own defensive method check.
func TestHandleChat_RequireMethodBranch(t *testing.T) {
	h := &Handler{chat: nil}
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	h.handleChat(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
