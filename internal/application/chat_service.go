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
	// hooks and hookRunner are both nil-safe (see Chat): a deployment that
	// hasn't wired regex-triggered chat hooks yet simply gets an empty
	// ChatResult.HookResults every turn, same convention as s.webSearch's
	// own nil check.
	hooks      ports.ChatHookStore
	hookRunner ports.HookScriptRunner
}

// NewChatService wires a ChatService from its six collaborators: the
// endpoint config store, the client that actually talks to the configured
// OpenAI-compatible endpoint, the existing hybrid search service used for
// RAG context, a live web searcher used for web-search context, and the
// store/runner pair behind regex-triggered chat hooks (see chat_hooks.go).
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, search ports.SearchService, webSearch ports.WebSearcher, hooks ports.ChatHookStore, hookRunner ports.HookScriptRunner) *ChatService {
	return &ChatService{endpoints: endpoints, completer: completer, search: search, webSearch: webSearch, hooks: hooks, hookRunner: hookRunner}
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
	// HookResults is one entry per regex-triggered chat hook match against
	// Answer this turn (see chat_hooks.go's runChatHooks), in no particular
	// order beyond match order -- empty whenever s.hooks is nil or no
	// enabled hook's pattern matched.
	HookResults []domain.ChatHookResult
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

	// List ALL hooks early, before building the messages sent to the first
	// completion call -- a hook's own Prompt (see below) needs to reach the
	// model before it can decide to invoke that hook at all, so this can't
	// wait until after an answer comes back. activeHooks is reused for the
	// runChatHooks call after the first answer, so ListChatHooks is never
	// called twice in one turn. A ListChatHooks error is best-effort, same
	// convention as the RAG/web-search errors below: it just leaves
	// activeHooks empty rather than failing the turn.
	var activeHooks []domain.ChatHook
	if s.hooks != nil {
		if all, err := s.hooks.ListChatHooks(ctx); err == nil {
			for _, h := range all {
				if h.Enabled && (!h.GatedByWebSearch || useWebSearch) {
					activeHooks = append(activeHooks, h)
				}
			}
		}
	}

	messages := history
	var sources []domain.ChatSource
	var ctxBlock strings.Builder
	if useRAG || useWebSearch {
		if lastUser, ok := lastUserMessage(history); ok {
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
		}
	}

	// Leading system messages, in order: (1) the persistent per-endpoint
	// system prompt, unconditional, when set; (2) each activeHooks entry's
	// own non-empty Prompt, in list order, each its OWN separate system
	// message (not concatenated into one blob) -- so a hook's invocation
	// syntax reaches the model before the first completion call, letting it
	// decide whether to invoke that hook at all; (3) the RAG/web-search
	// context message, if any. Building this as one ordered slice (rather
	// than prepending piecemeal) keeps that order obvious and gives
	// trimToBudget a single well-defined run of leading system-role
	// messages to keep intact.
	var leading []domain.ChatMessage
	if endpoint.SystemPrompt != "" {
		leading = append(leading, domain.ChatMessage{Role: domain.ChatRoleSystem, Content: endpoint.SystemPrompt})
	}
	for _, h := range activeHooks {
		if h.Prompt != "" {
			leading = append(leading, domain.ChatMessage{Role: domain.ChatRoleSystem, Content: h.Prompt})
		}
	}
	if ctxBlock.Len() > 0 {
		leading = append(leading, domain.ChatMessage{
			Role:    domain.ChatRoleSystem,
			Content: "Use the following search results to answer the user's question. Cite the sources you use by URL.\n\n" + ctxBlock.String(),
		})
	}
	if len(leading) > 0 {
		withLeading := make([]domain.ChatMessage, 0, len(leading)+len(messages))
		withLeading = append(withLeading, leading...)
		withLeading = append(withLeading, messages...)
		messages = withLeading
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

	// A tool call's own raw text (e.g. "<web_search>golang release
	// notes</web_search>") is never the final answer a user sees: whenever a
	// hook fires, its own tool-call turn plus the hook's results are fed
	// back to the model in a follow-up completion, so it reads and responds
	// to what the tool actually found rather than the caller seeing the bare
	// invocation syntax. This repeats up to maxHookFollowUpRounds times --
	// not just once -- because a model that reasonably decides to retry
	// (e.g. a fetch came back with an empty/blocked page, so it tries
	// another URL or another search) makes ANOTHER tool call in that
	// follow-up answer; capping this at exactly one round used to leave that
	// second tool call completely unprocessed, landing in the user's face as
	// a dangling, unanswered tool-call tag instead of a real answer. Every
	// round's hookResults are accumulated into the final ChatResult, so the
	// UI's folded transparency panel shows every attempt, not just the last.
	// Reuses the SAME activeHooks list computed above every round --
	// ListChatHooks is never called again mid-turn. env carries only
	// ADMIN-CONFIGURED endpoint config (never anything derived from the
	// model's own answer or a capture group) -- see ports.HookScriptRunner's
	// doc comment for why this doesn't reopen runChatHooks's security
	// surface.
	var hookResults []domain.ChatHookResult
	currentMessages := messages
	for round := 0; round < maxHookFollowUpRounds && len(activeHooks) > 0; round++ {
		env := map[string]string{"WEB_SEARCH_BASE_URL": endpoint.WebSearchBaseURL}
		roundResults := runChatHooks(ctx, activeHooks, s.hookRunner, answer, env)
		if len(roundResults) == 0 {
			break
		}
		hookResults = append(hookResults, roundResults...)

		followUp := make([]domain.ChatMessage, 0, len(currentMessages)+2)
		followUp = append(followUp, currentMessages...)
		followUp = append(followUp, domain.ChatMessage{Role: domain.ChatRoleAssistant, Content: answer})
		followUp = append(followUp, domain.ChatMessage{Role: domain.ChatRoleSystem, Content: formatHookResultsForModel(roundResults)})

		finalAnswer, err := s.completer.Complete(ctx, endpoint, followUp)
		if err != nil {
			// Best-effort, same convention as every other augmentation
			// source in this method: keep the current answer (which may
			// still be a bare tool call) rather than failing the turn.
			break
		}
		answer = finalAnswer
		currentMessages = followUp
	}

	return ChatResult{Answer: answer, Sources: sources, ContextTrimmed: contextTrimmed, HookResults: hookResults}, nil
}

// maxHookFollowUpRounds bounds how many times ChatService.Chat will feed a
// hook's results back to the model and ask again -- each round costs one
// more completion call and (via maxHookMatchesPerTurn, chat_hooks.go) up to
// maxHookMatchesPerTurn more script executions, so this is a real cost
// bound, not just a correctness one. 2 is enough for the common
// "one tool call, maybe one retry" pattern without letting a model stuck
// repeatedly retrying run up an unbounded number of completions.
const maxHookFollowUpRounds = 2

// maxHookOutputCharsForModel bounds how much of each hook result's own
// Output formatHookResultsForModel feeds back into the follow-up completion
// call -- hookrunner.Runner already caps a single script's stdout at 64KB,
// but up to maxHookMatchesPerTurn (chat_hooks.go) of those could still add
// up to a very large follow-up prompt; this is a second, tighter cap
// specifically on what actually reaches the model, same "cap and note"
// convention as hookrunner's own truncation.
const maxHookOutputCharsForModel = 8000

// formatHookResultsForModel renders every hookResults entry as a labeled
// block instructing the model to answer from them, for the follow-up
// completion call in Chat -- a failed hook's Err is included instead of its
// (empty) Output, so the model can say it couldn't search rather than being
// left to guess why a tool call produced nothing.
func formatHookResultsForModel(results []domain.ChatHookResult) string {
	var b strings.Builder
	b.WriteString("Tool results for the tool call you just made -- read them and answer the user's original question; do not just repeat or describe the tool call itself.\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "[%s]\n", r.HookName)
		if r.Err != "" {
			fmt.Fprintf(&b, "error: %s\n\n", r.Err)
			continue
		}
		fmt.Fprintf(&b, "%s\n\n", truncateForModel(r.Output))
	}
	return b.String()
}

func truncateForModel(s string) string {
	if len(s) <= maxHookOutputCharsForModel {
		return s
	}
	return s[:maxHookOutputCharsForModel] + fmt.Sprintf("... [truncated, %d bytes total]", len(s))
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

// trimToBudget drops the oldest messages in messages -- keeping every
// leading system-role message intact (there can now be several: the
// persistent per-endpoint SystemPrompt, then one per active chat hook's own
// Prompt, then the RAG/web-search context message, see ChatService.Chat),
// and always keeping at least the single most recent message even if it
// alone exceeds budget, since trimming it away would leave nothing left to
// answer -- until the estimated token count fits within maxTokens.
func trimToBudget(messages []domain.ChatMessage, maxTokens int) []domain.ChatMessage {
	if estimateTokens(messages) <= maxTokens {
		return messages
	}

	leadingSystem := 0
	for leadingSystem < len(messages) && messages[leadingSystem].Role == domain.ChatRoleSystem {
		leadingSystem++
	}
	system, rest := messages[:leadingSystem], messages[leadingSystem:]
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
