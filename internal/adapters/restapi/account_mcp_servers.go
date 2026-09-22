package restapi

import (
	"net/http"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const (
	accountMCPServersFeatureName  = "mcp servers"
	accountMCPServersAuthRequired = "authentication required"
	accountMCPServerNotFound      = "mcp server not found"
)

// validateUserMCPServerRequest is validateMCPServerRequest's self-service
// counterpart -- a regular user may only ever configure "http" transport
// (never "stdio", which grants real local command execution on the server
// host -- see ports.UserMCPServerStore's own doc comment for why this trust
// tier must stay admin-only). Reuses mcpServerRequest/mcpServerResponse
// (admin.go) as-is: same field set, this is just a stricter gate on what a
// self-service caller may submit.
func validateUserMCPServerRequest(w http.ResponseWriter, req mcpServerRequest) bool {
	if req.Name == "" {
		http.Error(w, "name must not be empty", http.StatusBadRequest)
		return false
	}
	if req.Transport != "http" {
		http.Error(w, `transport must be "http"`, http.StatusBadRequest)
		return false
	}
	if req.BaseURL == "" {
		http.Error(w, "base_url must not be empty", http.StatusBadRequest)
		return false
	}
	return true
}

// handleAccountMCPServers lists (GET) or creates (POST) the CALLING
// session's own MCP servers -- there is no ID in the URL for the list/create
// route, and every operation is scoped to the session's own userID, the
// same "no way to see or act on another user's row" discipline
// handleAccount already applies to the account itself. Mirrors
// handleAdminMCPServers' shape closely.
func (h *Handler) handleAccountMCPServers(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.userMCPServers != nil, accountMCPServersFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, accountMCPServersAuthRequired, http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		servers, err := h.userMCPServers.ListUserMCPServers(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(servers, toMCPServerResponse))
	case http.MethodPost:
		req, ok := decodeJSON[mcpServerRequest](w, r)
		if !ok {
			return
		}
		if !validateUserMCPServerRequest(w, req) {
			return
		}
		existing, err := h.userMCPServers.ListUserMCPServers(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existingIDs := existingIDSet(existing, func(s domain.MCPServer) string { return s.ID })
		s := domain.MCPServer{
			ID: domain.NewMCPServerID(req.Name, existingIDs), Name: req.Name,
			Transport: "http", BaseURL: req.BaseURL,
			APIKey: h.encryptAPIKey(req.APIKey), Enabled: req.Enabled,
			Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
		}
		if err := h.userMCPServers.CreateUserMCPServer(r.Context(), userID, s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toMCPServerResponse(s))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAccountGetMCPServer returns one of the caller's own servers by ID.
// Like ports.MCPServerStore, ports.UserMCPServerStore has no single-row get
// -- this scans ListUserMCPServers, same tolerance as
// handleAdminGetMCPServer (a self-service user's own server list is at
// least as small as an admin's global one).
func (h *Handler) handleAccountGetMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.userMCPServers != nil, accountMCPServersFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, accountMCPServersAuthRequired, http.StatusUnauthorized)
		return
	}
	servers, err := h.userMCPServers.ListUserMCPServers(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	for _, s := range servers {
		if s.ID == id {
			writeJSON(w, http.StatusOK, toMCPServerResponse(s))
			return
		}
	}
	http.Error(w, accountMCPServerNotFound, http.StatusNotFound)
}

// handleAccountUpdateMCPServer replaces the caller's own server's editable
// fields. APIKey is the one exception to "PATCH is a full replace" -- see
// embeddingEndpointRequest.ClearAPIKey's doc comment for why. ID and
// Transport are never editable once created (Transport is always "http" for
// a self-service row, enforced at creation).
func (h *Handler) handleAccountUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.userMCPServers != nil, accountMCPServersFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, accountMCPServersAuthRequired, http.StatusUnauthorized)
		return
	}
	req, ok := decodeJSON[mcpServerRequest](w, r)
	if !ok {
		return
	}
	if !validateUserMCPServerRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	servers, err := h.userMCPServers.ListUserMCPServers(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var existingAPIKey string
	found := false
	for _, s := range servers {
		if s.ID == id {
			existingAPIKey = s.APIKey
			found = true
			break
		}
	}
	if !found {
		http.Error(w, accountMCPServerNotFound, http.StatusNotFound)
		return
	}
	apiKey := h.resolveUpdatedAPIKey(existingAPIKey, req.APIKey, req.ClearAPIKey)
	s := domain.MCPServer{
		ID: id, Name: req.Name, Transport: "http", BaseURL: req.BaseURL,
		APIKey: apiKey, Enabled: req.Enabled,
		Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
	}
	err = h.userMCPServers.UpdateUserMCPServer(r.Context(), userID, s)
	respondOrNotFound(w, err, ports.ErrUserMCPServerNotFound, accountMCPServerNotFound, toMCPServerResponse(s))
}

func (h *Handler) handleAccountDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.userMCPServers != nil, accountMCPServersFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, accountMCPServersAuthRequired, http.StatusUnauthorized)
		return
	}
	err := h.userMCPServers.DeleteUserMCPServer(r.Context(), userID, r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrUserMCPServerNotFound, accountMCPServerNotFound, map[string]bool{"ok": true})
}

func (h *Handler) handleAccountMCPServersPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", accountMCPServersHTML)
}

func (h *Handler) handleAccountMCPServersJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", accountMCPServersJS)
}

func (h *Handler) handleAccountMCPServerPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", accountMCPServerHTML)
}

func (h *Handler) handleAccountMCPServerJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", accountMCPServerJS)
}
