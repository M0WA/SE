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

// ChatSource is one search result surfaced to the user as supporting
// evidence for a RAG-augmented chat answer.
type ChatSource struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// WebSearchResult is one result from a live web search (see
// ports.WebSearcher), narrowed to just what a chat turn needs to fold into
// its context -- distinct from ChatSource (a citation already surfaced to
// the user) and from SearchResult (this instance's own indexed corpus,
// which carries ranking fields a live web search has no equivalent of).
type WebSearchResult struct {
	Title   string
	URL     string
	Snippet string
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
	// RAGEnabled turns on retrieval-augmented generation: the latest user
	// message is used as a search query, and matching results are folded
	// into the conversation as system context before the completion call.
	RAGEnabled bool
	// RAGResultCount bounds how many search results are retrieved when
	// RAGEnabled -- see DefaultChatRAGResultCount/Min/MaxChatRAGResultCount.
	RAGResultCount int
	// MaxContextTokens bounds how many tokens' worth of conversation
	// (RAG context plus message history) ChatService.Chat will send to the
	// model, approximated by character count -- see that package's
	// trimToBudget. Older messages are dropped first, oldest to newest,
	// always keeping the most recent user message. 0 disables trimming
	// (the full history is sent as-is), same convention as
	// EmbeddingHTTPEndpoint.ChunkSizeTokens.
	MaxContextTokens int
	// WebSearchEnabled turns on live web search (via a self-hosted SearXNG
	// instance, see ports.WebSearcher) as additional chat context --
	// independent of RAGEnabled (this instance's own indexed corpus): a
	// turn can draw on either, both, or neither, and their results are
	// folded into the same system context message together.
	WebSearchEnabled bool
	// WebSearchBaseURL is the SearXNG instance's base URL, e.g.
	// http://127.0.0.1:8888 -- ports.WebSearcher GETs
	// <WebSearchBaseURL>/search?q=...&format=json.
	WebSearchBaseURL string
	// WebSearchResultCount bounds how many web results are fetched when
	// WebSearchEnabled -- see DefaultChatWebSearchResultCount/Min/Max.
	WebSearchResultCount int
	// SystemPrompt, when non-empty, is injected as a leading system-role
	// domain.ChatMessage ahead of the rest of the conversation on every
	// turn (see chat_service.go's Chat) -- before the RAG/web-search
	// context system message, if any, so an admin-authored persona/
	// instruction always takes precedence. Invisible in the rendered chat
	// UI, same as the RAG/web-search context message already is (the
	// public chat UI only ever renders user/assistant roles). Always
	// preserved by trimToBudget's context trimming, never dropped even
	// when the conversation is trimmed to fit MaxContextTokens.
	SystemPrompt string
	UpdatedAt    time.Time
}

// DefaultChatRAGResultCount/MinChatRAGResultCount/MaxChatRAGResultCount
// bound ChatEndpoint.RAGResultCount, same self-healing convention as every
// other admin-tunable numeric setting in this repo (see e.g.
// OperationalSettingsValues.Set's clamping for FuzzyMaxEditDistance).
const (
	DefaultChatRAGResultCount = 5
	MinChatRAGResultCount     = 1
	MaxChatRAGResultCount     = 20
)

// DefaultChatWebSearchResultCount/Min/MaxChatWebSearchResultCount bound
// ChatEndpoint.WebSearchResultCount, the same self-healing convention as
// RAGResultCount above.
const (
	DefaultChatWebSearchResultCount = 5
	MinChatWebSearchResultCount     = 1
	MaxChatWebSearchResultCount     = 20
)

// Clamp self-heals RAGResultCount/WebSearchResultCount into their own
// [Min,Max] ranges, substituting each default for a non-positive value the
// same way ContentDedupSimHashMaxDistance's own Set-time clamping does for
// an admin-supplied zero/negative.
func (e *ChatEndpoint) Clamp() {
	if e.RAGResultCount <= 0 {
		e.RAGResultCount = DefaultChatRAGResultCount
	} else if e.RAGResultCount > MaxChatRAGResultCount {
		e.RAGResultCount = MaxChatRAGResultCount
	}
	if e.WebSearchResultCount <= 0 {
		e.WebSearchResultCount = DefaultChatWebSearchResultCount
	} else if e.WebSearchResultCount > MaxChatWebSearchResultCount {
		e.WebSearchResultCount = MaxChatWebSearchResultCount
	}
}
