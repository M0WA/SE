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
	UpdatedAt      time.Time
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

// Clamp self-heals RAGResultCount into [MinChatRAGResultCount,
// MaxChatRAGResultCount], substituting DefaultChatRAGResultCount for a
// non-positive value the same way ContentDedupSimHashMaxDistance's own
// Set-time clamping does for an admin-supplied zero/negative.
func (e *ChatEndpoint) Clamp() {
	if e.RAGResultCount <= 0 {
		e.RAGResultCount = DefaultChatRAGResultCount
	} else if e.RAGResultCount > MaxChatRAGResultCount {
		e.RAGResultCount = MaxChatRAGResultCount
	}
}
