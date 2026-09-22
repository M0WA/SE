package restapi

import (
	"context"
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
	// this question only: it decides whether GatedByWebSearch MCP servers
	// (e.g. a "web_search"/"web_fetch" tool) are active, not whether a
	// search is performed directly -- omitted (nil) falls back to
	// domain.ChatEndpoint.WebSearchEnabled, letting the chat UI's
	// per-question toggle decide instead of a fixed global setting.
	WebSearch *bool `json:"web_search,omitempty"`
	// AgentID, when non-empty, overrides domain.ChatEndpoint.DefaultAgentID
	// for this question only -- see application.ChatOptions.AgentID's doc
	// comment. Empty (the default) means "use the endpoint's own default."
	AgentID string `json:"agent_id,omitempty"`
	// ChatID, when non-empty, names the PersistedChat this turn belongs to
	// (the active tab is pinned) -- see fileAccessTokenFor, which scopes
	// this turn's file-access token to it so cmd/mcp-files' list_files/
	// read_file/write_file only ever see this one chat's files. Ignored
	// (treated as unscoped) unless it's actually one of this user's own
	// pinned chats.
	ChatID string `json:"chat_id,omitempty"`
}

type chatResponse struct {
	Answer string `json:"answer"`
	// ContextTrimmed is true when one or more of the conversation's older
	// messages were dropped server-side to fit the endpoint's configured
	// token budget before this answer was generated -- omitted (so it reads
	// as false) on the common case where nothing was trimmed.
	ContextTrimmed bool `json:"context_trimmed,omitempty"`
	// ToolResults carries one entry per tool call the model made this turn
	// (see application.ChatResult.ToolResults) -- omitted entirely on the
	// common case of no configured MCP servers or no tool call made.
	ToolResults []toolCallResultResponse `json:"tool_results,omitempty"`
	// TokenUsage is this turn's estimated context breakdown (see
	// application.TokenUsage) -- always present, since every turn sends at
	// least a history message, letting the chat UI show a token-usage
	// diagram for every answer, not just ones with tool calls.
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

// toToolCallResultResponses' empty-input case naturally returns a
// zero-length (non-nil) slice, which is fine: ToolResults' own
// "omitempty" tag omits it from the JSON response either way, since
// encoding/json's omitempty treats a zero-length slice as empty
// regardless of nil-ness.
func toToolCallResultResponses(results []domain.ToolCallResult) []toolCallResultResponse {
	return mapSlice(results, func(r domain.ToolCallResult) toolCallResultResponse {
		return toolCallResultResponse{ToolName: r.ToolName, Arguments: r.Arguments, Output: r.Output, Err: r.Err}
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

// userAgentForMCPFetch reads the live-synced, admin-configured User-Agent
// (the same *domain.OperationalSettings crawls already use) so the
// first-party mcp-web server's "web_fetch" tool sends it instead of its own
// hardcoded default -- see application.ChatOptions.UserAgent. Nil-safe:
// h.opSettings is always wired by cmd/search's main, but this stays
// defensive the same way every other h.<dependency> use in this file is.
func (h *Handler) userAgentForMCPFetch() string {
	if h.opSettings == nil {
		return ""
	}
	return h.opSettings.Get().UserAgent
}

// fileAccessTokenFor mints a fresh, short-lived bearer token (see
// fileTokenStore) for userID, letting cmd/mcp-files call back into
// /account/api/files as this turn's own user -- empty whenever userID is
// empty (an admin session, or h.files/h.fileTokens not configured), same
// tolerance as userCustomPromptFor. chatID is baked into the token (see
// fileTokenStore.issue) only once verified as one of userID's own pinned
// chats (userOwnsChat) -- an unowned or nonexistent chatID is silently
// dropped (an unscoped token, same as no chat pinned at all) rather than
// trusted from the client as-is, since cmd/mcp-files' own file access
// later trusts the token's chatID unconditionally.
func (h *Handler) fileAccessTokenFor(ctx context.Context, userID, chatID string) string {
	if userID == "" || h.fileTokens == nil {
		return ""
	}
	if chatID != "" && !h.userOwnsChat(ctx, userID, chatID) {
		chatID = ""
	}
	return h.fileTokens.issue(userID, chatID)
}

// validateChatMessages checks that every client-supplied message obeys the
// constraints documented on handleChat: a bounded message count, a bounded
// per-message content length, only domain.ChatRoleUser/ChatRoleAssistant
// roles, and no client-settable tool-call fields. Returns the same error
// text handleChat previously reported inline for each violation.
func validateChatMessages(messages []domain.ChatMessage) error {
	if len(messages) == 0 {
		return errors.New("messages must not be empty")
	}
	if len(messages) > maxChatMessages {
		return errors.New("too many messages")
	}
	for _, m := range messages {
		if len(m.Content) > maxChatMessageContentLength {
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

// handleChat answers one chat turn against the search-server-only,
// admin-configured chat endpoint (h.chat) -- see application.ChatService's
// doc comment for the web-search-grounding behavior this delegates to. A
// client-supplied message may only claim domain.ChatRoleUser or
// domain.ChatRoleAssistant -- domain.ChatRoleSystem/ChatRoleTool are
// reserved for server-injected context and tool results, never something a
// client can inject to try to override the system prompt or fake a tool
// call's outcome. A client-supplied message is also never allowed to carry
// ToolCalls/ToolCallID -- those are populated only by ChatService itself
// from a real model response/tool execution. See validateChatMessages for
// the actual per-message checks.
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

	role, userID, _ := h.sessionRoleFor(r)
	if role != domain.RoleUser {
		userID = ""
	}
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
// domain.Agent -- deliberately narrower than admin.go's own agentResponse
// (no mcp_server_ids/enabled): a signed-in chat user picking an agent only
// needs enough to populate a dropdown, not the admin config surface.
type publicAgentResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleChatAgents lists every ENABLED agent for the chat page's own
// picker (see index.js) -- unlike /admin/api/agents, this is reachable by
// any signed-in session (role=admin or role=user), not just an admin one,
// since picking an agent to talk to is a chat-page action, not an admin
// one. Nil-safe like every other optional collaborator in this file: a
// deployment with no agents wired (or none configured yet) just gets an
// empty list, never an error -- there's nothing to fail over for a
// supplementary picker.
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
