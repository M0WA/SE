package restapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxChatMessages/maxChatMessageContentLength bound a POST /chat request
// body, so an unbounded or malicious client can't force an arbitrarily
// large (or many, or huge) call out to the configured upstream chat model.
const (
	maxChatMessages             = 50
	maxChatMessageContentLength = 4000
)

type chatRequest struct {
	Messages []domain.ChatMessage `json:"messages"`
	// RAG, when present, overrides the admin-configured default for this
	// question only -- omitted (nil) falls back to
	// domain.ChatEndpoint.RAGEnabled, letting the chat UI's per-question
	// toggle decide instead of a fixed global setting.
	RAG *bool `json:"rag,omitempty"`
}

type chatResponse struct {
	Answer  string              `json:"answer"`
	Sources []domain.ChatSource `json:"sources,omitempty"`
}

// handleChat answers one chat turn against the search-server-only,
// admin-configured chat endpoint (h.chat) -- see application.ChatService's
// doc comment for the retrieval-augmented-generation behavior this
// delegates to. A client-supplied message is never allowed to claim
// domain.ChatRoleSystem: that role is reserved for server-injected RAG
// context, never something a client can inject to try to override the
// system prompt.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !requireConfigured(w, h.chat != nil, "chat") {
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages must not be empty", http.StatusBadRequest)
		return
	}
	if len(req.Messages) > maxChatMessages {
		http.Error(w, "too many messages", http.StatusBadRequest)
		return
	}
	for _, m := range req.Messages {
		if len(m.Content) > maxChatMessageContentLength {
			http.Error(w, "message too long", http.StatusBadRequest)
			return
		}
		if m.Role != domain.ChatRoleUser && m.Role != domain.ChatRoleAssistant {
			http.Error(w, "invalid role", http.StatusBadRequest)
			return
		}
	}

	result, err := h.chat.Chat(r.Context(), req.Messages, req.RAG)
	if errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, chatResponse{Answer: result.Answer, Sources: result.Sources})
}
