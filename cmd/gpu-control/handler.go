package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// requireToken gates a handler behind X-Internal-Token, constant-time
// compared against token -- mirrors internal/adapters/restapi's own
// requestHasSecretHeader idiom, reimplemented here rather than imported
// since this binary deliberately never depends on that package (it runs
// on a separate host, outside searchengine's own three-binary hexagonal
// core). Unlike crawl-server's own optional CrawlInternalToken, token is
// never empty here -- main() refuses to start otherwise -- so there is
// no "disabled when unset" fallback.
func requireToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Token")), []byte(token)) != 1 {
			http.Error(w, "invalid or missing internal token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// newMux builds this service's full route table. /healthz is
// deliberately unauthenticated (matching crawl-server's own
// RoutesCrawlInternal convention) -- everything else requires token.
func newMux(c *Controller, token string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gpu/api/mode", requireToken(token, c.handleGetMode))
	mux.HandleFunc("POST /gpu/api/mode", requireToken(token, c.handlePostMode))
	mux.HandleFunc("POST /gpu/api/heartbeat", requireToken(token, c.handleHeartbeat))
	mux.HandleFunc("/healthz", c.handleHealthz)
	return mux
}

func (c *Controller) handleGetMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, c.Status())
}

// modeRequest is POST /gpu/api/mode's body -- the only thing an HTTP
// caller ever selects is which of the two known modes to move to, never
// a unit name or command (see this package's own doc comment).
type modeRequest struct {
	Mode Mode `json:"mode"`
}

// modeErrorResponse is returned alongside a non-2xx Switch outcome
// (409 conflict) -- still reports the current status fields so a caller
// can show "already switching to vision" instead of a bare error string.
type modeErrorResponse struct {
	Error      string `json:"error"`
	Mode       Mode   `json:"mode"`
	Target     Mode   `json:"target,omitempty"`
	InProgress bool   `json:"in_progress"`
}

func (c *Controller) handlePostMode(w http.ResponseWriter, r *http.Request) {
	var req modeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Mode != ModeChat && req.Mode != ModeVision {
		http.Error(w, `mode must be "chat" or "vision"`, http.StatusBadRequest)
		return
	}
	st, code, err := c.Switch(req.Mode)
	if err != nil {
		writeJSON(w, code, modeErrorResponse{
			Error: err.Error(), Mode: st.Mode, Target: st.Target, InProgress: st.InProgress,
		})
		return
	}
	writeJSON(w, code, st)
}

func (c *Controller) handleHeartbeat(w http.ResponseWriter, _ *http.Request) {
	c.Heartbeat()
	w.WriteHeader(http.StatusNoContent)
}

func (c *Controller) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
