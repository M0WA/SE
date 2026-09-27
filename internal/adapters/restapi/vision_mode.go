package restapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// visionAspectRatios lists exactly the values cmd/gpu-control's own
// generate.go accepts for its aspect_ratio override (itself matching
// ComfyUI's ResolutionSelector node's enum) -- duplicated here (this
// package deliberately never imports cmd/gpu-control, which runs as its
// own separate binary on a separate host) so a bad value gets a quick,
// friendly 400 without a network round trip, same as the existing
// empty-prompt check below.
var visionAspectRatios = map[string]bool{
	"1:1 (Square)":               true,
	"2:3 (Portrait Photo)":       true,
	"3:2 (Photo)":                true,
	"3:4 (Portrait Standard)":    true,
	"4:3 (Standard)":             true,
	"9:16 (Portrait Widescreen)": true,
	"16:9 (Widescreen)":          true,
	"21:9 (Ultrawide)":           true,
}

// visionMinDurationSeconds/visionMaxDurationSeconds mirror
// cmd/gpu-control/generate.go's own minDurationSeconds/
// maxDurationSeconds bounds.
const (
	visionMinDurationSeconds = 3
	visionMaxDurationSeconds = 10
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

// visionGenerateRequest is POST /vision/api/generate's body. ChatID, when
// given, is a signed-in account's own already-pinned chat (same
// convention as /chat's own chat_id) -- once the job finishes, the
// resulting video is saved into that chat's files, so it shows up in
// the account's existing file list rather than only being reachable
// through a job id a browser tab happened to still hold. Left empty
// (e.g. an unpinned/session-only tab), that save step is simply
// skipped -- same "files require a pinned chat" rule as everywhere else
// files are involved. AspectRatio/DurationSeconds are both optional --
// left unset, cmd/gpu-control falls back to its own defaults (matching
// this feature's original fixed values) -- but when given, are
// validated against the exact same whitelist/bounds cmd/gpu-control
// itself enforces, so a bad value fails fast here instead of after a
// network round trip.
type visionGenerateRequest struct {
	Prompt          string `json:"prompt"`
	ChatID          string `json:"chat_id,omitempty"`
	AspectRatio     string `json:"aspect_ratio,omitempty"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
}

type visionGenerateResponse struct {
	JobID string `json:"job_id"`
}

// handleVisionGenerate is POST /vision/api/generate -- submits a new
// text-to-video generation job. Only meaningful while the shared GPU is
// already in Vision mode; the frontend only ever shows the Generate
// button then, but this still handles the race of someone switching back
// to Chat in between by surfacing cmd/gpu-control's own 409 as a 409
// here too, rather than a confusing 502.
func (h *Handler) handleVisionGenerate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	req, ok := decodeJSON[visionGenerateRequest](w, r)
	if !ok {
		return
	}
	if req.Prompt == "" {
		http.Error(w, "prompt must not be empty", http.StatusBadRequest)
		return
	}
	if req.AspectRatio != "" && !visionAspectRatios[req.AspectRatio] {
		http.Error(w, "aspect_ratio must be one of the supported values", http.StatusBadRequest)
		return
	}
	if req.DurationSeconds != 0 && (req.DurationSeconds < visionMinDurationSeconds || req.DurationSeconds > visionMaxDurationSeconds) {
		http.Error(w, fmt.Sprintf("duration_seconds must be between %d and %d", visionMinDurationSeconds, visionMaxDurationSeconds), http.StatusBadRequest)
		return
	}
	_, userID, _ := h.sessionRoleFor(r)
	jobID, err := h.gpuModeService.Generate(r.Context(), userID, req.ChatID, req.Prompt, req.AspectRatio, req.DurationSeconds)
	switch {
	case errors.Is(err, application.ErrGPUModeNotEnabled):
		http.NotFound(w, r)
	case errors.Is(err, ports.ErrGPUGenerateNotInVisionMode):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusAccepted, visionGenerateResponse{JobID: jobID})
	}
}

// visionResultResponse mirrors application.GPUModeService.GenerateResult's
// own domain.GPUGenerateResult shape. FileID, once set, names the
// account file (GET /account/api/files/{id}) the finished video was
// saved into -- see GPUModeService.saveGeneratedFileOnce.
type visionResultResponse struct {
	Status  string `json:"status"`
	ViewURL string `json:"view_url,omitempty"`
	Error   string `json:"error,omitempty"`
	FileID  string `json:"file_id,omitempty"`
}

// handleVisionResult is GET /vision/api/result?job_id=... -- polls a
// previously submitted generation job. ViewURL, when present, is always
// this same handler's own package's /vision/api/asset (never
// cmd/gpu-control's URL directly), so the browser never needs to reach
// anything but search-server.
func (h *Handler) handleVisionResult(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		http.Error(w, "job_id is required", http.StatusBadRequest)
		return
	}
	result, err := h.gpuModeService.GenerateResult(r.Context(), jobID)
	switch {
	case errors.Is(err, application.ErrGPUModeNotEnabled):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		resp := visionResultResponse{Status: result.Status, Error: result.Error, FileID: result.FileID}
		if result.ViewURL != "" {
			resp.ViewURL = "/vision/api/asset?job_id=" + url.QueryEscape(jobID)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleVisionAsset is GET /vision/api/asset?job_id=... -- re-fetches
// job_id's current result (cheap: cmd/gpu-control's own /history lookup)
// to get its view_url, then streams cmd/gpu-control's own /gpu/api/view
// bytes straight through. Re-fetching rather than trusting a client-
// supplied view_url means a signed-in user can never ask this endpoint
// to fetch an arbitrary ComfyUI-side path -- only whatever job_id's own
// already-computed result names.
func (h *Handler) handleVisionAsset(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if h.gpuModeService == nil {
		http.NotFound(w, r)
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		http.Error(w, "job_id is required", http.StatusBadRequest)
		return
	}
	result, err := h.gpuModeService.GenerateResult(r.Context(), jobID)
	if errors.Is(err, application.ErrGPUModeNotEnabled) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if result.Status != "done" || result.ViewURL == "" {
		http.Error(w, "generation is not finished", http.StatusNotFound)
		return
	}
	contentType, body, err := h.gpuModeService.ViewAsset(r.Context(), result.ViewURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer body.Close()
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	_, _ = io.Copy(w, body)
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
