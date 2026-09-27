package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
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
	mux.Handle("/gpu/comfy/", requireToken(token, newComfyProxy(c.comfyReadyURL).ServeHTTP))
	mux.HandleFunc("POST /gpu/api/generate", requireToken(token, c.handleGenerate))
	mux.HandleFunc("GET /gpu/api/generate/{id}", requireToken(token, c.handleGenerateResult))
	mux.HandleFunc("GET /gpu/api/view", requireToken(token, newComfyViewProxy(c.comfyReadyURL)))
	mux.HandleFunc("/healthz", c.handleHealthz)
	return mux
}

// comfyProxyPrefix is stripped from every request path before it's
// forwarded to ComfyUI -- e.g. /gpu/comfy/queue becomes /queue.
const comfyProxyPrefix = "/gpu/comfy"

// comfyOrigin parses comfyReadyURL (e.g.
// "http://127.0.0.1:8188/system_stats") down to just its scheme+host --
// shared by newComfyProxy/newComfyViewProxy and
// Controller.Generate/GenerateResult, all of which need to reach one of
// ComfyUI's other endpoints (/prompt, /history, /view) at that same
// origin.
func comfyOrigin(comfyReadyURL string) (*url.URL, error) {
	parsed, err := url.Parse(comfyReadyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid comfy ready URL %q", comfyReadyURL)
	}
	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
}

// newComfyProxy reverse-proxies every request under /gpu/comfy/ straight
// through to ComfyUI's own local web UI -- letting an authenticated
// caller (searchengine's own admin-server, itself gating this behind an
// admin session -- see internal/adapters/restapi's own comfy proxy
// handler) reach ComfyUI directly for inspecting or debugging a running
// workflow by hand, even though ComfyUI itself only ever binds 127.0.0.1
// on this host and is never otherwise reachable off it. Derives
// ComfyUI's origin from comfyReadyURL (already configured for the
// readiness poll) rather than a separate env var -- same host:port, just
// a different path. Standard library httputil.ReverseProxy transparently
// handles the WebSocket upgrade ComfyUI's own UI uses for live queue/
// progress updates, so no extra code is needed for that here.
func newComfyProxy(comfyReadyURL string) http.Handler {
	origin, err := comfyOrigin(comfyReadyURL)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "comfy proxy misconfigured", http.StatusBadGateway)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(origin)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, comfyProxyPrefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
	}
	return proxy
}

// comfyViewQueryParams is the exact, whitelisted set of query parameters
// forwarded to ComfyUI's own GET /view -- unlike newComfyProxy (a raw,
// admin-only passthrough of ComfyUI's entire API/UI surface), this is
// deliberately narrow: read-only access to one already-generated output
// file, safe enough to also expose (via internal/adapters/restapi's own
// /vision/api/result) to any signed-in regular user viewing their own
// generation result, not just an admin.
var comfyViewQueryParams = []string{"filename", "subfolder", "type", "preview"}

// newComfyViewProxy streams ComfyUI's own GET /view response (an
// already-rendered image/video/audio file) straight through, copying
// only comfyViewQueryParams from the incoming request -- never the full
// query string or path, so this can't be turned into a general-purpose
// proxy the way /gpu/comfy/ deliberately is.
func newComfyViewProxy(comfyReadyURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin, err := comfyOrigin(comfyReadyURL)
		if err != nil {
			http.Error(w, "comfy proxy misconfigured", http.StatusBadGateway)
			return
		}
		q := url.Values{}
		for _, key := range comfyViewQueryParams {
			if v := r.URL.Query().Get(key); v != "" {
				q.Set(key, v)
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, origin.String()+"/view?"+q.Encode(), nil)
		if err != nil {
			http.Error(w, "building request", http.StatusInternalServerError)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "could not reach ComfyUI", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
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

// generateRequest is POST /gpu/api/generate's body -- the only thing a
// caller selects is the text prompt; every other workflow parameter
// (resolution, duration, model, negative prompt) comes from the fixed
// embedded template (see generate.go's own doc comment).
type generateRequest struct {
	Prompt string `json:"prompt"`
}

type generateResponse struct {
	PromptID string `json:"prompt_id"`
}

func (c *Controller) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	promptID, err := c.Generate(r.Context(), req.Prompt)
	switch {
	case errors.Is(err, errEmptyPrompt):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errNotInVisionMode):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		writeJSON(w, http.StatusAccepted, generateResponse{PromptID: promptID})
	}
}

// generateResultResponse's Status is one of "pending" (still queued or
// running), "done" (ViewURL points at the finished video via
// GET /gpu/api/view), or "failed" (Error explains why).
type generateResultResponse struct {
	Status  string `json:"status"`
	ViewURL string `json:"view_url,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (c *Controller) handleGenerateResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing prompt id", http.StatusBadRequest)
		return
	}
	status, file, err := c.GenerateResult(r.Context(), id)
	if err != nil && status == "" {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp := generateResultResponse{Status: status}
	if err != nil {
		resp.Error = err.Error()
	}
	if status == "done" {
		q := url.Values{"filename": {file.Filename}, "subfolder": {file.Subfolder}, "type": {file.Type}}
		resp.ViewURL = "/gpu/api/view?" + q.Encode()
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
