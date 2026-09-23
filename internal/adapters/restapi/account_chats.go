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
// History reuses domain.ChatMessage's JSON shape directly, so syncing a tab
// is a straight round trip of what index.js already holds in memory.
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
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
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

// handleAccountUpdateChat replaces a pinned chat's Title/AgentID/History --
// used both for a plain rename and for resyncing history after each turn.
func (h *Handler) handleAccountUpdateChat(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chats != nil, accountChatsFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
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

// handleAccountDeleteChat unpins one of the session's own chats -- cascades
// to delete every file attached to it (uploaded_files' FK on chats).
func (h *Handler) handleAccountDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chats != nil, accountChatsFeatureName) {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
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
