package restapi

import (
	"errors"
	"net/http"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const (
	accountChatsFeatureName = "chats"
	accountChatNotFound     = "chat not found"
)

// pinnedChatRequest is the wire shape POST/PATCH /account/api/chats accepts --
// History reuses domain.ChatMessage's own JSON shape directly (the exact
// array index.js's own tab.history already holds), so pinning or
// resyncing a tab is a straight round trip of what the client already
// has in memory, no reshaping needed on either side.
type pinnedChatRequest struct {
	Title   string               `json:"title"`
	AgentID string               `json:"agent_id"`
	History []domain.ChatMessage `json:"history"`
}

type pinnedChatResponse struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	AgentID   string               `json:"agent_id"`
	History   []domain.ChatMessage `json:"history"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt string               `json:"updated_at"`
}

func toPinnedChatResponse(c domain.PersistedChat) pinnedChatResponse {
	history := c.History
	if history == nil {
		history = []domain.ChatMessage{}
	}
	return pinnedChatResponse{
		ID: c.ID, Title: c.Title, AgentID: c.AgentID, History: history,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// handleAccountChats lists (GET) or pins a new chat (POST) for the calling
// session's own userID -- mirrors handleAccountMCPServers' shape closely.
func (h *Handler) handleAccountChats(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chats != nil, accountChatsFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		chats, err := h.chats.ListChats(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(chats, toPinnedChatResponse))
	case http.MethodPost:
		req, ok := decodeJSON[pinnedChatRequest](w, r)
		if !ok {
			return
		}
		if req.Title == "" {
			http.Error(w, "title must not be empty", http.StatusBadRequest)
			return
		}
		c, err := h.chats.CreateChat(r.Context(), domain.PersistedChat{
			OwnerUserID: userID, Title: req.Title, AgentID: req.AgentID, History: req.History,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toPinnedChatResponse(c))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAccountUpdateChat replaces one of the calling session's own pinned
// chats' Title/AgentID/History -- used both for a plain rename (title
// only changed) and for resyncing history after every turn.
func (h *Handler) handleAccountUpdateChat(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chats != nil, accountChatsFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	req, ok := decodeJSON[pinnedChatRequest](w, r)
	if !ok {
		return
	}
	if req.Title == "" {
		http.Error(w, "title must not be empty", http.StatusBadRequest)
		return
	}
	c := domain.PersistedChat{
		ID: r.PathValue("id"), OwnerUserID: userID,
		Title: req.Title, AgentID: req.AgentID, History: req.History,
	}
	err := h.chats.UpdateChat(r.Context(), c)
	respondOrNotFound(w, err, ports.ErrChatNotFound, accountChatNotFound, toPinnedChatResponse(c))
}

// handleAccountDeleteChat unpins (removes) one of the calling session's
// own chats -- cascades to delete every file attached to it (see the
// chats table's own foreign key from uploaded_files).
func (h *Handler) handleAccountDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chats != nil, accountChatsFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	err := h.chats.DeleteChat(r.Context(), userID, r.PathValue("id"))
	if errors.Is(err, ports.ErrChatNotFound) {
		http.Error(w, accountChatNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
