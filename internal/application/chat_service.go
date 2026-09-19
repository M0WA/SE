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
// domain.ChatEndpoint, optionally augment the conversation with context
// from one or both of two independent sources -- retrieval-augmented
// generation (RAG) against the existing search index, and a live web
// search via ports.WebSearcher -- then delegate the actual completion call
// to a ports.ChatCompleter. Kept separate from hybridSearchService so
// chat's single-endpoint Get/Set config (ports.ChatEndpointStore) never
// gets confused with the multi-endpoint blended CRUD
// ports.EmbeddingEndpointStore uses.
type ChatService struct {
	endpoints ports.ChatEndpointStore
	completer ports.ChatCompleter
	search    ports.SearchService
	webSearch ports.WebSearcher
}

// NewChatService wires a ChatService from its four collaborators: the
// endpoint config store, the client that actually talks to the configured
// OpenAI-compatible endpoint, the existing hybrid search service used for
// RAG context, and a live web searcher used for web-search context.
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, search ports.SearchService, webSearch ports.WebSearcher) *ChatService {
	return &ChatService{endpoints: endpoints, completer: completer, search: search, webSearch: webSearch}
}

// ChatOptions carries this turn's per-question overrides for
// ChatService.Chat -- a nil field falls back to the admin-configured
// endpoint default (domain.ChatEndpoint.RAGEnabled/WebSearchEnabled), a
// non-nil one decides for this question only, letting the chat UI's
// per-question toggles override a fixed global setting without changing
// it.
type ChatOptions struct {
	RAG       *bool
	WebSearch *bool
}

// ChatResult is one completed chat turn's answer, plus the search results
// (if any) whose content informed it -- surfaced separately from Answer so
// the UI can render them as clickable citations rather than parsing the
// answer text for them.
type ChatResult struct {
	Answer  string
	Sources []domain.ChatSource
	// ContextTrimmed reports whether trimToBudget actually dropped one or
	// more older messages to fit endpoint.MaxContextTokens for this turn --
	// the client sends its full running history on every call (the backend
	// keeps no session state), so without this flag a user has no way to
	// know the model answered without seeing the whole conversation.
	ContextTrimmed bool
}

// Chat answers the conversation in history using the admin-configured chat
// endpoint. Each of opts.RAG/opts.WebSearch, when non-nil, decides for this
// question only whether that source is used, falling back to
// endpoint.RAGEnabled/WebSearchEnabled otherwise -- so an admin's default
// can still be overridden per question without changing it globally. Both
// sources can apply at once: the last user message is used as the query
// against whichever of s.search (this instance's own index) and
// s.webSearch (a live web search) are enabled, and any results found from
// either are woven together into one system message ahead of the rest of
// history. A failure from either source at this stage is treated as
// best-effort (that source's results are simply omitted) rather than
// failing the whole call, since this context is an enhancement, not a
// requirement, of answering.
func (s *ChatService) Chat(ctx context.Context, history []domain.ChatMessage, opts ChatOptions) (ChatResult, error) {
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
	if opts.RAG != nil {
		useRAG = *opts.RAG
	}
	useWebSearch := endpoint.WebSearchEnabled
	if opts.WebSearch != nil {
		useWebSearch = *opts.WebSearch
	}

	messages := history
	var sources []domain.ChatSource
	if useRAG || useWebSearch {
		if lastUser, ok := lastUserMessage(history); ok {
			var ctxBlock strings.Builder
			if useRAG {
				results, err := s.search.Search(ctx, lastUser.Content, ports.SearchQuery{TopK: endpoint.RAGResultCount})
				if err == nil && len(results) > 0 {
					ctxBlock.WriteString("Indexed search results:\n\n")
					for _, r := range results {
						fmt.Fprintf(&ctxBlock, "Title: %s\nURL: %s\nSnippet: %s\n\n", r.Title, r.URL, r.Snippet)
						sources = append(sources, domain.ChatSource{URL: r.URL, Title: r.Title})
					}
				}
				// A search error is intentionally swallowed here: RAG
				// context is best-effort.
			}
			if useWebSearch && endpoint.WebSearchBaseURL != "" && s.webSearch != nil {
				webResults, err := s.webSearch.Search(ctx, endpoint.WebSearchBaseURL, lastUser.Content, endpoint.WebSearchResultCount)
				if err == nil && len(webResults) > 0 {
					ctxBlock.WriteString("Live web search results:\n\n")
					for _, r := range webResults {
						fmt.Fprintf(&ctxBlock, "Title: %s\nURL: %s\nSnippet: %s\n\n", r.Title, r.URL, r.Snippet)
						sources = append(sources, domain.ChatSource{URL: r.URL, Title: r.Title})
					}
				}
				// A web search error is likewise best-effort.
			}
			if ctxBlock.Len() > 0 {
				augmented := make([]domain.ChatMessage, 0, len(history)+1)
				augmented = append(augmented, domain.ChatMessage{
					Role:    domain.ChatRoleSystem,
					Content: "Use the following search results to answer the user's question. Cite the sources you use by URL.\n\n" + ctxBlock.String(),
				})
				augmented = append(augmented, history...)
				messages = augmented
			}
		}
	}

	contextTrimmed := false
	if endpoint.MaxContextTokens > 0 {
		before := len(messages)
		messages = trimToBudget(messages, endpoint.MaxContextTokens)
		contextTrimmed = len(messages) < before
	}

	answer, err := s.completer.Complete(ctx, endpoint, messages)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}
	return ChatResult{Answer: answer, Sources: sources, ContextTrimmed: contextTrimmed}, nil
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
