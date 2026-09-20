package domain

import "time"

// Chat roles, matching the OpenAI-compatible chat-completions message role
// values every configured chat backend is expected to accept.
const (
	ChatRoleUser      = "user"
	ChatRoleAssistant = "assistant"
	ChatRoleSystem    = "system"
)

// ChatMessage is one turn in a chat conversation, sent to/from a configured
// domain.ChatEndpoint via ports.ChatCompleter.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
