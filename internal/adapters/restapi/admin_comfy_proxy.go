package restapi

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"searchengine/internal/ports"
)

// adminComfyProxyPrefix is stripped from every request path (replaced
// with cmd/gpu-control's own comfy passthrough prefix) before the
// request is forwarded -- e.g. /admin/comfy/queue becomes
// /gpu/comfy/queue on the control service.
const adminComfyProxyPrefix = "/admin/comfy"

// handleAdminComfyProxy reverse-proxies every request under
// /admin/comfy/ through to cmd/gpu-control's own /gpu/comfy/ passthrough
// (which itself proxies to ComfyUI's local web UI) -- lets an
// authenticated admin open ComfyUI directly (e.g. to inspect or debug a
// running workflow) even though ComfyUI itself only ever binds 127.0.0.1
// on the GPU host, never reachable from outside it. Gated by
// requireAdminAuthPage at the route registration (see handler.go), same
// as every other /admin/* page -- running a workflow here bypasses the
// switch-timeout/dwell/rate-limit safety GPUModeService enforces for the
// public toggle, so this is deliberately an admin-only maintenance path,
// never linked to a regular signed-in user.
//
// Built fresh per request rather than cached at startup, so a changed
// admin-configured ControlBaseURL/ControlAPIKey takes effect immediately
// without a restart -- acceptable here since this is a low-frequency,
// human-interactive debugging path, not a hot request path.
func (h *Handler) handleAdminComfyProxy(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.gpuMode != nil, "gpu mode settings") {
		return
	}
	cfg, err := h.gpuMode.GetGPUModeSettings(r.Context())
	if errors.Is(err, ports.ErrGPUModeSettingsNotConfigured) {
		http.Error(w, "gpu mode is not enabled", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "loading gpu mode settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !cfg.Enabled {
		http.Error(w, "gpu mode is not enabled", http.StatusNotFound)
		return
	}
	target, err := url.Parse(strings.TrimRight(cfg.ControlBaseURL, "/"))
	if err != nil || target.Scheme == "" || target.Host == "" {
		http.Error(w, "gpu mode control endpoint is not configured", http.StatusBadGateway)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.URL.Path = "/gpu/comfy" + strings.TrimPrefix(req.URL.Path, adminComfyProxyPrefix)
		req.Header.Set("X-Internal-Token", cfg.ControlAPIKey)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("admin comfy proxy: %v", err)
		http.Error(w, "could not reach the GPU control service", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
