package domain

import (
	"encoding/json"
	"time"
)

// Chat roles, matching the OpenAI-compatible chat-completions message role
// values every configured chat backend is expected to accept. ChatRoleTool
// is only ever set by ChatService itself (see chat_service.go's follow-up
// loop) -- a client-submitted message claiming this role is rejected the
// same way one claiming ChatRoleSystem already is (see restapi.handleChat).
const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"
	ChatRoleSystem    = "system"
	ChatRoleTool      = "tool"
)

// ChatMessage is one turn in a chat conversation, sent to/from a configured
// domain.ChatEndpoint via ports.ChatCompleter. ToolCalls is set only on an
// assistant message the model itself returned when it chose to invoke one
// or more tools (see ports.ChatCompleter's doc comment) -- Content is
// typically empty on such a message. ToolCallID is set only on a
// ChatRoleTool message answering one specific ToolCall.ID from the
// immediately preceding assistant message.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one tool invocation the model requested, as returned by an
// OpenAI-compatible chat-completions endpoint's native tool-calling support
// -- see ports.ChatCompleter. ID is opaque, assigned by the model/backend,
// and only ever used to correlate the ChatMessage{Role: ChatRoleTool} that
// answers it.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON object text, exactly as the model emitted it
}

// ToolDef describes one callable tool to a domain.ChatCompleter, mirroring
// the OpenAI-compatible "tools" request field's function shape. Parameters
// is a JSON-schema object (never nil when Complete is called with a
// non-empty tools list -- see chat_service.go's toolDefsFrom).
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
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
	// Enabled gates whether ChatService.Chat will actually call out to this
	// endpoint at all.
	Enabled bool
	// MaxContextTokens bounds how many tokens' worth of conversation
	// (leading system messages plus message history) ChatService.Chat will
	// send to the model, approximated by character count -- see that
	// package's trimToBudget. Older messages are dropped first, oldest to newest,
	// always keeping the most recent user message. 0 disables trimming
	// (the full history is sent as-is), same convention as
	// EmbeddingHTTPEndpoint.ChunkSizeTokens.
	MaxContextTokens int
	// WebSearchEnabled is the "Web" toggle's default: when on (as either
	// this admin-configured default or the per-question ChatOptions.WebSearch
	// override), every ChatHook with GatedByWebSearch=true becomes active for
	// the turn -- its Prompt is injected and its pattern is matched against
	// the answer, letting the model invoke it (e.g. a "web_search" or
	// "web_fetch" hook) rather than this layer performing a search itself.
	WebSearchEnabled bool
	// WebSearchBaseURL is the self-hosted SearXNG instance's base URL, e.g.
	// http://127.0.0.1:8888 -- passed to every active hook's script as the
	// WEB_SEARCH_BASE_URL environment variable (see
	// ports.HookScriptRunner), so a "web_search" hook script knows which
	// instance to query without the admin repeating the URL per hook.
	WebSearchBaseURL string
	// SystemPrompt, when non-empty, is injected as a leading system-role
	// domain.ChatMessage ahead of the rest of the conversation on every
	// turn (see chat_service.go's Chat) -- before any active hook's own
	// Prompt, if any, so an admin-authored persona/instruction always takes
	// precedence. Invisible in the rendered chat UI, same as a hook's Prompt
	// already is (the public chat UI only ever renders user/assistant
	// roles). Always preserved by trimToBudget's context trimming, never
	// dropped even when the conversation is trimmed to fit MaxContextTokens.
	SystemPrompt string
	UpdatedAt    time.Time
}

// ChatCompletionReserveFraction is the fraction of a model's own advertised
// maximum context length reserved for the completion (the model's own
// answer) when auto-detecting ChatEndpoint.MaxContextTokens from the model
// itself (see AutoMaxContextTokens and restapi.Handler's chat-endpoint PATCH
// handler) -- the detected max is a TOTAL budget (prompt + completion
// combined), so using the whole thing as the prompt-only trimming budget
// would leave the model no room to actually answer once a conversation
// grows close to the limit.
const ChatCompletionReserveFraction = 0.25

// AutoMaxContextTokens computes the prompt-only token budget to store as
// ChatEndpoint.MaxContextTokens from a model's own raw advertised maximum
// context length (as returned by e.g. ports.ChatCompleter's optional
// model-probing capability), reserving ChatCompletionReserveFraction of it
// for the completion. Returns 0 (meaning "no budget configured, trimming
// stays disabled") when modelMaxContextTokens isn't positive -- the caller
// couldn't detect one, same as leaving MaxContextTokens unset today.
func AutoMaxContextTokens(modelMaxContextTokens int) int {
	if modelMaxContextTokens <= 0 {
		return 0
	}
	return modelMaxContextTokens - int(float64(modelMaxContextTokens)*ChatCompletionReserveFraction)
}
