package restapi

import (
	"errors"
	"net/http"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// gpuModeStatusResponse is the wire shape GET/POST /vision/api/mode both
// return -- mirrors cmd/gpu-control's own Status type (see that
// package's doc comment), just re-declared here since restapi never
// imports a cmd/* package directly.
type gpuModeStatusResponse struct {
	Mode       string    `json:"mode"`
	Target     string    `json:"target,omitempty"`
	InProgress bool      `json:"in_progress"`
	Since      time.Time `json:"since,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

func toGPUModeStatusResponse(st domain.GPUModeStatus) gpuModeStatusResponse {
	return gpuModeStatusResponse{
		Mode: string(st.Mode), Target: string(st.Target), InProgress: st.InProgress,
		Since: st.Since, ExpiresAt: st.ExpiresAt, Detail: st.Detail,
	}
}

// handleVisionMode is GET /vision/api/mode -- a live (uncached) read, so
// the panel's own initial load and each poll iteration always reflects
// cmd/gpu-control's current answer. Absent entirely (404) when the
// feature isn't enabled -- see application.GPUModeService.Status's own
// doc comment -- so the public chat page's toggle can treat a 404 here
// as "Vision doesn't exist on this deployment," never a broken feature.
func (h *Handler) handleVisionMode(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	st, enabled, err := h.gpuModeService.Status(r.Context())
	if !enabled && err == nil {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, toGPUModeStatusResponse(st))
}

// visionModeSwitchRequest is POST /vision/api/mode's body -- the only
// thing a caller ever selects is which of the two known modes to move
// to, validated against domain.GPUModeChat/GPUModeVision below, never an
// arbitrary string forwarded to cmd/gpu-control.
type visionModeSwitchRequest struct {
	Mode string `json:"mode"`
}

// handleVisionModeSwitch is POST /vision/api/mode -- requires a signed-in
// session (see RoutesSearch's requireAuthAPI wrapping), and additionally
// gates through application.GPUModeService's own dwell/per-account rate
// limit on top of that. Always responds 200 with the resulting status
// (InProgress distinguishes "already there" from "switch under way," so
// the frontend doesn't need to interpret an HTTP status code for that)
// except: 404 when the feature isn't enabled, 429 when the
// dwell/rate-limit gate itself rejects the request, and 409 (still
// carrying a status body) when cmd/gpu-control reports it's already mid-
// switch toward a different target.
func (h *Handler) handleVisionModeSwitch(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	req, ok := decodeJSON[visionModeSwitchRequest](w, r)
	if !ok {
		return
	}
	var target domain.GPUMode
	switch req.Mode {
	case string(domain.GPUModeChat):
		target = domain.GPUModeChat
	case string(domain.GPUModeVision):
		target = domain.GPUModeVision
	default:
		http.Error(w, `mode must be "chat" or "vision"`, http.StatusBadRequest)
		return
	}

	_, userID, _ := h.sessionRoleFor(r)
	st, err := h.gpuModeService.Switch(r.Context(), userID, target)
	switch {
	case errors.Is(err, application.ErrGPUModeNotEnabled):
		http.NotFound(w, r)
	case errors.Is(err, application.ErrGPUModeSwitchTooSoon), errors.Is(err, application.ErrGPUModeRateLimited):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, ports.ErrGPUModeSwitchConflict):
		writeJSON(w, http.StatusConflict, toGPUModeStatusResponse(st))
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusOK, toGPUModeStatusResponse(st))
	}
}

// handleVisionHeartbeat is POST /vision/api/heartbeat -- resets
// cmd/gpu-control's own idle-revert timer while a Vision-mode panel
// stays open. No dwell/rate-limit gating (see GPUModeService.Heartbeat's
// own doc comment): a heartbeat can't itself thrash the GPU.
func (h *Handler) handleVisionHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	err := h.gpuModeService.Heartbeat(r.Context())
	switch {
	case errors.Is(err, application.ErrGPUModeNotEnabled):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
