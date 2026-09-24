package restapi

import (
	"context"
	"errors"
	"net/http"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxChatMessages bounds how many turns a single POST /chat body may
// carry, so a malicious client can't force an arbitrarily large call to
// the upstream model.
//
// maxChatMessageContentLength bounds only a ChatRoleUser message's own
// length (see validateChatMessages) -- the length a client actually
// AUTHORS. It deliberately does NOT apply to ChatRoleAssistant messages:
// those are the client faithfully re-sending the server's own prior
// answer as context, since POST /chat is stateless per call (chat_id
// only scopes the file-access token -- see chatRequest.ChatID's own doc
// comment; it never loads persisted history server-side). A real
// incident: one answer with a large code sample exceeded the old,
// uniformly-applied 4000-char cap, so the very next turn -- which had to
// resend that answer as history -- was rejected outright with "message
// too long" before the server ever attempted a completion call, let
// alone the tool call the user had actually asked for. maxChatMessages
// above remains the real bound on total request size/cost; a
// deliberately larger value here just keeps the user-authored cap
// closer to what a real single turn (e.g. a long pasted question) can
// legitimately need.
const (
	maxChatMessages             = 50
	maxChatMessageContentLength = 32000
)

type chatRequest struct {
	Messages []domain.ChatMessage `json:"messages"`
	// WebSearch, when present, overrides the admin default for this
	// question: it gates GatedByWebSearch MCP servers, not a direct search.
	// Omitted (nil) falls back to domain.ChatEndpoint.WebSearchEnabled.
	WebSearch *bool `json:"web_search,omitempty"`
	// AgentID, when non-empty, overrides domain.ChatEndpoint.DefaultAgentID
	// for this question; empty means use the endpoint's own default.
	AgentID string `json:"agent_id,omitempty"`
	// ChatID, when non-empty, names the pinned PersistedChat this turn
	// belongs to -- fileAccessTokenFor scopes the turn's file-access token
	// to it. Ignored unless it's actually one of this user's own pinned chats.
	ChatID string `json:"chat_id,omitempty"`
}

type chatResponse struct {
	Answer string `json:"answer"`
	// ContextTrimmed is true when older messages were dropped server-side
	// to fit the token budget -- omitted (reads false) when nothing was trimmed.
	ContextTrimmed bool `json:"context_trimmed,omitempty"`
	// ToolResults carries one entry per tool call the model made this turn
	// -- omitted when no MCP servers are configured or none was called.
	ToolResults []toolCallResultResponse `json:"tool_results,omitempty"`
	// TokenUsage is this turn's estimated context breakdown -- always
	// present, so the chat UI can show it for every answer.
	TokenUsage chatTokenUsageResponse `json:"token_usage"`
}

// chatTokenUsageResponse mirrors application.TokenUsage's field
// names/types/order exactly, so a plain type conversion works in handleChat.
type chatTokenUsageResponse struct {
	GlobalPromptTokens int `json:"global_prompt_tokens"`
	UserPromptTokens   int `json:"user_prompt_tokens"`
	AgentPromptTokens  int `json:"agent_prompt_tokens"`
	ToolPromptTokens   int `json:"tool_prompt_tokens"`
	HistoryTokens      int `json:"history_tokens"`
	MaxContextTokens   int `json:"max_context_tokens,omitempty"`
}

// toolCallResultResponse is the wire shape of one domain.ToolCallResult.
type toolCallResultResponse struct {
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Err       string `json:"err,omitempty"`
}

// toToolCallResultResponses' empty-input case returns a zero-length
// (non-nil) slice, which is fine: omitempty treats it as empty either way.
func toToolCallResultResponses(results []domain.ToolCallResult) []toolCallResultResponse {
	return mapSlice(results, func(r domain.ToolCallResult) toolCallResultResponse {
		return toolCallResultResponse{ToolName: r.ToolName, Arguments: r.Arguments, Output: r.Output, Err: r.Err}
	})
}

// userCustomPromptFor resolves the session's custom chat prompt for
// injection into ChatOptions -- empty for no session, unconfigured
// h.users, or any lookup error. Best-effort and silent by design: a
// personalization nicety should never fail an otherwise-working turn.
func (h *Handler) userCustomPromptFor(r *http.Request) string {
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" || h.users == nil {
		return ""
	}
	u, err := h.users.GetUser(r.Context(), userID)
	if err != nil {
		return ""
	}
	return u.CustomPrompt
}

// userAgentForMCPFetch reads the admin-configured User-Agent (same
// *domain.OperationalSettings crawls use) so mcp-web's "web_fetch" sends it
// instead of its own default. Nil-safe, like every h.<dependency> here.
func (h *Handler) userAgentForMCPFetch() string {
	if h.opSettings == nil {
		return ""
	}
	return h.opSettings.Get().UserAgent
}

// fileAccessTokenFor mints a short-lived bearer token for userID, letting
// cmd/mcp-files call back as this turn's user -- empty if userID is empty
// or h.files/h.fileTokens aren't configured. chatID is baked in only once
// verified via userOwnsChat; an unowned/nonexistent chatID is silently
// dropped rather than trusted, since mcp-files trusts a token's chatID unconditionally.
func (h *Handler) fileAccessTokenFor(ctx context.Context, userID, chatID string) string {
	if userID == "" || h.fileTokens == nil {
		return ""
	}
	if chatID != "" && !h.userOwnsChat(ctx, userID, chatID) {
		chatID = ""
	}
	return h.fileTokens.issue(userID, chatID)
}

// validateChatMessages enforces handleChat's constraints: bounded message
// count, a ChatRoleUser message's own bounded length (see
// maxChatMessageContentLength's own doc comment for why a
// ChatRoleAssistant message is exempt), only ChatRoleUser/ChatRoleAssistant,
// no tool-call fields.
func validateChatMessages(messages []domain.ChatMessage) error {
	if len(messages) == 0 {
		return errors.New("messages must not be empty")
	}
	if len(messages) > maxChatMessages {
		return errors.New("too many messages")
	}
	for _, m := range messages {
		if m.Role == domain.ChatRoleUser && len(m.Content) > maxChatMessageContentLength {
			return errors.New("message too long")
		}
		if m.Role != domain.ChatRoleUser && m.Role != domain.ChatRoleAssistant {
			return errors.New("invalid role")
		}
		if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			return errors.New("tool_calls/tool_call_id are not client-settable")
		}
	}
	return nil
}

// handleChat answers one chat turn against the admin-configured chat
// endpoint (h.chat). A client-supplied message may only claim
// ChatRoleUser/ChatRoleAssistant -- RoleSystem/RoleTool are reserved for
// server-injected context, never client-settable, and ToolCalls/ToolCallID
// are populated only by ChatService itself. See validateChatMessages.
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
	if err := validateChatMessages(req.Messages); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, userID, _ := h.sessionRoleFor(r)
	result, err := h.chat.Chat(r.Context(), req.Messages, application.ChatOptions{
		WebSearch: req.WebSearch, UserCustomPrompt: h.userCustomPromptFor(r),
		UserAgent: h.userAgentForMCPFetch(), AgentID: req.AgentID,
		UserID: userID, FileAccessToken: h.fileAccessTokenFor(r.Context(), userID, req.ChatID),
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
		ToolResults: toToolCallResultResponses(result.ToolResults),
		TokenUsage:  chatTokenUsageResponse(result.TokenUsage),
	})
}

// publicAgentResponse is the minimal, browser-facing shape of a
// domain.Agent -- narrower than admin.go's agentResponse (no
// mcp_server_ids/enabled): just enough to populate a dropdown.
type publicAgentResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleChatAgents lists every enabled agent for the chat page's picker --
// unlike /admin/api/agents, reachable by any signed-in session, not just
// admin. Nil-safe: no agents configured just means an empty list, never an error.
func (h *Handler) handleChatAgents(w http.ResponseWriter, r *http.Request) {
	if !requireGetOrHead(w, r) {
		return
	}
	out := []publicAgentResponse{}
	if h.agents != nil {
		if agents, err := h.agents.ListAgents(r.Context()); err == nil {
			for _, a := range agents {
				if a.Enabled {
					out = append(out, publicAgentResponse{ID: a.ID, Name: a.Name, Description: a.Description})
				}
			}
		}
	}
	if r.Method == http.MethodHead {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
