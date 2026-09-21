package restapi

import (
	"errors"
	"net/http"

	"searchengine/internal/application"
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
	// WebSearch, when present, overrides the admin-configured default for
	// this question only: it decides whether GatedByWebSearch chat hooks
	// (e.g. a "web_search"/"web_fetch" hook) are active, not whether a
	// search is performed directly -- omitted (nil) falls back to
	// domain.ChatEndpoint.WebSearchEnabled, letting the chat UI's
	// per-question toggle decide instead of a fixed global setting.
	WebSearch *bool `json:"web_search,omitempty"`
}

type chatResponse struct {
	Answer string `json:"answer"`
	// ContextTrimmed is true when one or more of the conversation's older
	// messages were dropped server-side to fit the endpoint's configured
	// token budget before this answer was generated -- omitted (so it reads
	// as false) on the common case where nothing was trimmed.
	ContextTrimmed bool `json:"context_trimmed,omitempty"`
	// HookResults carries one entry per tool call the model made this turn
	// (see application.ChatResult.HookResults) -- omitted entirely on the
	// common case of no configured hooks or no tool call made.
	HookResults []chatHookResultResponse `json:"hook_results,omitempty"`
	// TokenUsage is this turn's estimated context breakdown (see
	// application.TokenUsage) -- always present, since every turn sends at
	// least a history message, letting the chat UI show a token-usage
	// diagram for every answer, not just ones with hooks.
	TokenUsage chatTokenUsageResponse `json:"token_usage"`
}

// chatTokenUsageResponse is the wire shape of application.TokenUsage -- kept
// with identical field names/types/order so a plain type conversion
// (chatTokenUsageResponse(result.TokenUsage), see handleChat) works; struct
// tags don't affect convertibility, only field shape does, so this must stay
// in lockstep with application.TokenUsage's own field list.
type chatTokenUsageResponse struct {
	GlobalPromptTokens int `json:"global_prompt_tokens"`
	UserPromptTokens   int `json:"user_prompt_tokens"`
	HookPromptTokens   int `json:"hook_prompt_tokens"`
	HistoryTokens      int `json:"history_tokens"`
	MaxContextTokens   int `json:"max_context_tokens,omitempty"`
}

// chatHookResultResponse is the wire shape of one domain.ChatHookResult.
type chatHookResultResponse struct {
	HookName string `json:"hook_name"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	Err      string `json:"err,omitempty"`
}

// toChatHookResultResponses' empty-input case naturally returns a
// zero-length (non-nil) slice, which is fine: HookResults' own
// "omitempty" tag omits it from the JSON response either way, since
// encoding/json's omitempty treats a zero-length slice as empty
// regardless of nil-ness.
func toChatHookResultResponses(results []domain.ChatHookResult) []chatHookResultResponse {
	return mapSlice(results, func(r domain.ChatHookResult) chatHookResultResponse {
		return chatHookResultResponse{HookName: r.HookName, Input: r.Input, Output: r.Output, Err: r.Err}
	})
}

// userCustomPromptFor resolves the current session's per-user custom chat
// prompt (domain.User.CustomPrompt), for injection into ChatOptions --
// empty whenever there's nothing to inject: an admin session (no
// associated domain.User row at all), h.users not configured, or a lookup
// error/empty CustomPrompt. Every failure here is best-effort and silent by
// design -- a per-user prompt is a nice-to-have personalization, never
// something that should fail an otherwise-working chat turn. Uses the
// single sessionRoleFor lookup already available rather than querying
// h.sessions.ValidSession a second time.
func (h *Handler) userCustomPromptFor(r *http.Request) string {
	role, userID, ok := h.sessionRoleFor(r)
	if !ok || role != domain.RoleUser || userID == "" || h.users == nil {
		return ""
	}
	u, err := h.users.GetUser(r.Context(), userID)
	if err != nil {
		return ""
	}
	return u.CustomPrompt
}

// handleChat answers one chat turn against the search-server-only,
// admin-configured chat endpoint (h.chat) -- see application.ChatService's
// doc comment for the web-search-grounding behavior this delegates to. A
// client-supplied message may only claim domain.ChatRoleUser or
// domain.ChatRoleAssistant -- domain.ChatRoleSystem/ChatRoleTool are
// reserved for server-injected context and tool results, never something a
// client can inject to try to override the system prompt or fake a tool
// call's outcome. A client-supplied message is also never allowed to carry
// ToolCalls/ToolCallID -- those are populated only by ChatService itself
// from a real model response/tool execution.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !requireConfigured(w, h.chat != nil, "chat") {
		return
	}
	req, ok := decodeJSON[chatRequest](w, r)
	if !ok {
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
		if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			http.Error(w, "tool_calls/tool_call_id are not client-settable", http.StatusBadRequest)
			return
		}
	}

	result, err := h.chat.Chat(r.Context(), req.Messages, application.ChatOptions{
		WebSearch: req.WebSearch, UserCustomPrompt: h.userCustomPromptFor(r),
	})
	if errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, chatResponse{
		Answer: result.Answer, ContextTrimmed: result.ContextTrimmed,
		HookResults: toChatHookResultResponses(result.HookResults),
		TokenUsage:  chatTokenUsageResponse(result.TokenUsage),
	})
}
