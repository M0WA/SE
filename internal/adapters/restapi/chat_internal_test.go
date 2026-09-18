package restapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleChat_RequireMethodBranch calls handleChat directly (bypassing
// the mux) to cover its own requireMethod(w, r, http.MethodPost) check --
// through the real RoutesSearch() mux, "POST /chat" is the only pattern
// registered for that path, so a non-POST request never reaches this
// handler at all (it falls through to the "/" catch-all instead; see
// chat_test.go's TestHandleChat_MethodNotAllowed for that externally
// observable behavior). This proves handleChat's own defensive check does
// the right thing if it's ever reached some other way.
func TestHandleChat_RequireMethodBranch(t *testing.T) {
	h := &Handler{chat: nil}
	req := httptest.NewRequest(http.MethodGet, "/chat", nil)
	rec := httptest.NewRecorder()
	h.handleChat(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
