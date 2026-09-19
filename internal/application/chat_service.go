package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ChatService orchestrates a chat turn: load the single admin-configured
// domain.ChatEndpoint, optionally augment the conversation with a
// retrieval-augmented-generation (RAG) system message built from the
// existing search index, then delegate the actual completion call to a
// ports.ChatCompleter. Kept separate from hybridSearchService so chat's
// single-endpoint Get/Set config (ports.ChatEndpointStore) never gets
// confused with the multi-endpoint blended CRUD ports.EmbeddingEndpointStore
// uses.
type ChatService struct {
	endpoints ports.ChatEndpointStore
	completer ports.ChatCompleter
	search    ports.SearchService
}

// NewChatService wires a ChatService from its three collaborators: the
// endpoint config store, the client that actually talks to the configured
// OpenAI-compatible endpoint, and the existing hybrid search service used
// for RAG context.
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, search ports.SearchService) *ChatService {
	return &ChatService{endpoints: endpoints, completer: completer, search: search}
}

// ChatResult is one completed chat turn's answer, plus the search results
// (if any) whose content informed it -- surfaced separately from Answer so
// the UI can render them as clickable citations rather than parsing the
// answer text for them.
type ChatResult struct {
	Answer  string
	Sources []domain.ChatSource
}

// Chat answers the conversation in history using the admin-configured chat
// endpoint. Whether this turn is retrieval-augmented is decided by
// ragOverride when non-nil (the per-question toggle in the chat UI),
// falling back to endpoint.RAGEnabled otherwise -- so an admin's default
// can still be overridden per question without changing it globally. When
// RAG applies, the last user message is used as a search query against
// s.search, and any results found are woven in as a system message ahead
// of the rest of history -- a search error at this stage is treated as
// best-effort (the plain history is still sent, unaugmented) rather than
// failing the whole call, since RAG context is an enhancement, not a
// requirement, of answering.
func (s *ChatService) Chat(ctx context.Context, history []domain.ChatMessage, ragOverride *bool) (ChatResult, error) {
	if len(history) == 0 {
		return ChatResult{}, errors.New("chat: message history must not be empty")
	}

	endpoint, err := s.endpoints.GetChatEndpoint(ctx)
	if err != nil {
		return ChatResult{}, err
	}
	if !endpoint.Enabled {
		return ChatResult{}, ports.ErrChatEndpointNotConfigured
	}

	useRAG := endpoint.RAGEnabled
	if ragOverride != nil {
		useRAG = *ragOverride
	}

	messages := history
	var sources []domain.ChatSource
	if useRAG {
		if lastUser, ok := lastUserMessage(history); ok {
			results, err := s.search.Search(ctx, lastUser.Content, ports.SearchQuery{TopK: endpoint.RAGResultCount})
			if err == nil && len(results) > 0 {
				sources = make([]domain.ChatSource, 0, len(results))
				var ctxBlock strings.Builder
				ctxBlock.WriteString("Use the following search results to answer the user's question. Cite the sources you use by URL.\n\n")
				for _, r := range results {
					fmt.Fprintf(&ctxBlock, "Title: %s\nURL: %s\nSnippet: %s\n\n", r.Title, r.URL, r.Snippet)
					sources = append(sources, domain.ChatSource{URL: r.URL, Title: r.Title})
				}
				augmented := make([]domain.ChatMessage, 0, len(history)+1)
				augmented = append(augmented, domain.ChatMessage{Role: domain.ChatRoleSystem, Content: ctxBlock.String()})
				augmented = append(augmented, history...)
				messages = augmented
			}
			// A search error is intentionally swallowed here: RAG context
			// is best-effort, and the chat call still proceeds against the
			// plain history below.
		}
	}

	if endpoint.MaxContextTokens > 0 {
		messages = trimToBudget(messages, endpoint.MaxContextTokens)
	}

	answer, err := s.completer.Complete(ctx, endpoint, messages)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}
	return ChatResult{Answer: answer, Sources: sources}, nil
}

// approxCharsPerToken mirrors httpembed's own conservative token estimate
// (overestimates real token count, so trimming stops a little early rather
// than a little late, matching that package's identical reasoning for its
// own constant of the same value).
const approxCharsPerToken = 3

// estimateTokens sums messages' character-count-based token estimate (see
// approxCharsPerToken) -- exact tokenization isn't worth the complexity
// here, since trimToBudget drops a whole message at a time, which already
// has slack an exact tokenizer's extra precision wouldn't meaningfully
// improve.
func estimateTokens(messages []domain.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / approxCharsPerToken
}

// trimToBudget drops the oldest messages in messages -- keeping a leading
// RAG-injected system message intact if present, and always keeping at
// least the single most recent message even if it alone exceeds budget,
// since trimming it away would leave nothing left to answer -- until the
// estimated token count fits within maxTokens.
func trimToBudget(messages []domain.ChatMessage, maxTokens int) []domain.ChatMessage {
	if estimateTokens(messages) <= maxTokens {
		return messages
	}

	var system, rest []domain.ChatMessage
	if len(messages) > 0 && messages[0].Role == domain.ChatRoleSystem {
		system, rest = messages[:1], messages[1:]
	} else {
		rest = messages
	}
	budget := maxTokens - estimateTokens(system)

	// Walk backward from the newest message, keeping as many as fit --
	// the newest one is always kept regardless of budget (the `i != last`
	// guard skips its own size check).
	start := len(rest)
	used := 0
	for i := len(rest) - 1; i >= 0; i-- {
		t := estimateTokens(rest[i : i+1])
		if i != len(rest)-1 && used+t > budget {
			break
		}
		used += t
		start = i
	}

	kept := make([]domain.ChatMessage, 0, len(system)+len(rest)-start)
	kept = append(kept, system...)
	kept = append(kept, rest[start:]...)
	return kept
}

// lastUserMessage returns the last domain.ChatRoleUser message in history,
// so RAG always searches on the most recent thing the user actually asked
// rather than an earlier turn or an assistant/system message.
func lastUserMessage(history []domain.ChatMessage) (domain.ChatMessage, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == domain.ChatRoleUser {
			return history[i], true
		}
	}
	return domain.ChatMessage{}, false
}
