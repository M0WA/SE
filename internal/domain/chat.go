package domain

import (
	"encoding/json"
	"time"
)

// Chat roles, matching the OpenAI-compatible chat-completions message role
// values. ChatRoleTool is only ever set by ChatService itself; a
// client-submitted message claiming it is rejected, same as ChatRoleSystem.
const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"
	ChatRoleSystem    = "system"
	ChatRoleTool      = "tool"
)

// ChatMessage is one turn in a chat conversation. ToolCalls is set only on
// an assistant message that invoked tools (Content is then typically
// empty). ToolCallID is set only on a ChatRoleTool message answering one
// specific ToolCall.ID from the preceding assistant message.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one tool invocation the model requested via native
// tool-calling. ID is opaque, used only to correlate the answering
// ChatMessage{Role: ChatRoleTool}.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object text, exactly as the model emitted it
}

// ToolDef describes one callable tool, mirroring the OpenAI-compatible
// "tools" field's function shape. Parameters is a JSON-schema object.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCallResult is the outcome of running one tool call this turn,
// surfaced on ChatResult so the caller can decide how to show it.
type ToolCallResult struct {
	ToolName   string // may repeat across servers, but unique among tools offered this turn
	ToolCallID string // correlates back to the ToolCall.ID it answers
	Arguments  string // model-supplied raw JSON arguments, verbatim
	Output     string // the tool's raw result content, as text
	Err        string // non-empty on failure/timeout/unresolved; Output is empty then
}

// ChatEndpoint is the single admin-configured chat-completions backend --
// unlike the embedding endpoints (a list of many blended providers), chat
// only ever has one active configuration at a time.
type ChatEndpoint struct {
	BaseURL string
	// APIKey is opaque at this layer -- encryption/decryption happens in
	// restapi, this struct just carries whatever string it's given.
	APIKey string
	Model  string
	// Enabled gates whether ChatService.Chat calls this endpoint at all.
	Enabled bool
	// MaxContextTokens bounds conversation size sent to the model,
	// approximated by character count (see trimToBudget). Oldest messages
	// are dropped first, always keeping the most recent user message. 0
	// disables trimming, same convention as ChunkSizeTokens.
	MaxContextTokens int
	// WebSearchEnabled is the "Web" toggle's default: when on, every
	// MCPServer with GatedByWebSearch=true becomes active for the turn (its
	// tools offered, its Prompt injected) instead of this layer searching itself.
	WebSearchEnabled bool
	// WebSearchBaseURL is the self-hosted SearXNG instance's base URL,
	// passed as WEB_SEARCH_BASE_URL to every active stdio MCPServer process
	// so mcp-web knows which instance to query.
	WebSearchBaseURL string
	// WebSearchResultCount, when positive, caps results per call from
	// mcp-web's "web_search" tool, passed as WEB_SEARCH_RESULT_COUNT the
	// same way WebSearchBaseURL is. Zero means no cap. Keep this generous
	// (50+): too small a cap can leave the model with only a couple of
	// same-domain results, and nothing to fall back to if that domain
	// blocks fetching (confirmed live: capped at 2, both AccuWeather, both
	// blocked -- the model never found a working source).
	WebSearchResultCount int
	// SystemPrompt, when non-empty, is injected as a leading system-role
	// message ahead of the conversation, before any MCP server's own
	// Prompt so it takes precedence. Invisible in the chat UI. Always
	// preserved by trimToBudget, never dropped.
	SystemPrompt string
	// DefaultAgentID names the Agent selected by default, overridable per
	// question by ChatOptions.AgentID. Empty means no agent specialization.
	DefaultAgentID string
	// CompletionTimeoutSeconds bounds how long a single completion call to
	// this endpoint is allowed to take (see httpchat.Client.Complete).
	// <= 0 falls back to httpchat's own default. One turn can chain
	// several completions (the tool-calling follow-up loop, plus the
	// leaked-tool-call recovery retries), so this bounds each individual
	// call, not the whole turn -- keep it comfortably under nginx's own
	// proxy_read_timeout (packaging/nginx/searchengine.conf) for the
	// FIRST call at least, since that one has no earlier partial response
	// to fall back on if it times out.
	CompletionTimeoutSeconds int
	UpdatedAt                time.Time
}

// ChatCompletionReserveFraction is the fraction of a model's advertised max
// context length reserved for the completion when auto-detecting
// MaxContextTokens -- the advertised max is prompt+completion combined, so
// reserving part of it leaves the model room to actually answer.
const ChatCompletionReserveFraction = 0.25

// AutoMaxContextTokens computes the prompt-only budget to store as
// MaxContextTokens from a model's raw advertised max context length,
// reserving ChatCompletionReserveFraction for the completion. Returns 0
// (trimming stays disabled) when modelMaxContextTokens isn't positive.
func AutoMaxContextTokens(modelMaxContextTokens int) int {
	if modelMaxContextTokens <= 0 {
		return 0
	}
	return modelMaxContextTokens - int(float64(modelMaxContextTokens)*ChatCompletionReserveFraction)
}
