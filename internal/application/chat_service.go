package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// ChatService orchestrates a chat turn: load the single admin-configured
// domain.ChatEndpoint, activate any admin-configured chat hooks (tools)
// whose gating the turn's effective web-search toggle satisfies, offer them
// to the model as native tool-calling functions, and run whichever ones the
// model actually invokes. Kept separate from
// hybridSearchService so chat's single-endpoint Get/Set config
// (ports.ChatEndpointStore) never gets confused with the multi-endpoint
// blended CRUD ports.EmbeddingEndpointStore uses.
type ChatService struct {
	endpoints ports.ChatEndpointStore
	completer ports.ChatCompleter
	// hooks and hookRunner are both nil-safe (see Chat): a deployment that
	// hasn't wired regex-triggered chat hooks yet simply gets an empty
	// ChatResult.HookResults every turn.
	hooks      ports.ChatHookStore
	hookRunner ports.HookScriptRunner
}

// NewChatService wires a ChatService from its four collaborators: the
// endpoint config store, the client that actually talks to the configured
// OpenAI-compatible endpoint, and the store/runner pair behind
// regex-triggered chat hooks (see chat_hooks.go).
func NewChatService(endpoints ports.ChatEndpointStore, completer ports.ChatCompleter, hooks ports.ChatHookStore, hookRunner ports.HookScriptRunner) *ChatService {
	return &ChatService{endpoints: endpoints, completer: completer, hooks: hooks, hookRunner: hookRunner}
}

// ChatOptions carries this turn's per-question overrides for
// ChatService.Chat -- a nil field falls back to the admin-configured
// endpoint default (domain.ChatEndpoint.WebSearchEnabled), a non-nil one
// decides for this question only, letting the chat UI's per-question
// toggle override a fixed global setting without changing it. See
// WebSearch's own doc comment for what the toggle actually does.
type ChatOptions struct {
	// WebSearch, when non-nil, decides for this question only whether every
	// ChatHook with GatedByWebSearch=true is active -- it does not itself
	// perform a search or fetch anything; it only decides which hooks the
	// model is offered, leaving the model to invoke them (e.g. a
	// "web_search" or "web_fetch" hook) if it chooses to.
	WebSearch *bool
	// UserCustomPrompt, when non-empty, is injected as its own leading
	// system message for this turn -- empty means no per-user prompt is
	// injected. Set by the HTTP handler layer (restapi.handleChat) from the
	// current session's associated domain.User.CustomPrompt, only when the
	// session is role=user; a role=admin session has no associated
	// domain.User row to draw this from, so it's always empty for one.
	UserCustomPrompt string
}

// ChatResult is one completed chat turn's answer.
type ChatResult struct {
	Answer string
	// ContextTrimmed reports whether trimToBudget actually dropped one or
	// more older messages to fit endpoint.MaxContextTokens for this turn --
	// the client sends its full running history on every call (the backend
	// keeps no session state), so without this flag a user has no way to
	// know the model answered without seeing the whole conversation.
	ContextTrimmed bool
	// HookResults is one entry per tool call the model made this turn (see
	// chat_hooks.go's runToolCalls), in call order -- empty whenever s.hooks
	// is nil or the model never invoked a tool.
	HookResults []domain.ChatHookResult
	// TokenUsage breaks down the estimated size of what was actually sent to
	// the model for this turn's first completion call -- lets the chat UI
	// show where a turn's context budget went (global prompt vs. active
	// hooks' own prompts vs. search context vs. conversation history)
	// instead of just a single opaque total.
	TokenUsage TokenUsage
}

// TokenUsage is one turn's leading-context token estimate, broken down by
// where each piece came from -- see estimateTokens for the (deliberately
// approximate, character-count-based) estimation method.
// GlobalPromptTokens/UserPromptTokens/HookPromptTokens are computed
// directly from the same pieces ChatService.Chat assembles into `leading`,
// so they're exact for what was actually sent (not re-derived from the
// final message list). HistoryTokens is measured after trimToBudget, so it
// reflects what actually made it into the request, not the client's full
// untrimmed history. MaxContextTokens echoes endpoint.MaxContextTokens (0
// means unbounded) so the UI can render usage against the configured
// budget, not just relative proportions.
type TokenUsage struct {
	GlobalPromptTokens int
	// UserPromptTokens is this turn's ChatOptions.UserCustomPrompt
	// contribution -- zero whenever UserCustomPrompt is empty (a role=admin
	// session, or a role=user session with no custom prompt set).
	UserPromptTokens int
	HookPromptTokens int
	HistoryTokens    int
	MaxContextTokens int
}

// Chat answers the conversation in history using the admin-configured chat
// endpoint. opts.WebSearch, when non-nil, decides for this question only
// whether GatedByWebSearch chat hooks are active, falling back to
// endpoint.WebSearchEnabled otherwise -- so an admin's default can still be
// overridden per question without changing it globally. This layer never
// performs a web search or fetch itself: it only decides which hooks the
// model is offered as native tools (their Name/Description/Parameters sent
// in the request's tools list, their Prompt injected) and leaves the model
// to invoke them, same as any other active hook.
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

	useWebSearch := endpoint.WebSearchEnabled
	if opts.WebSearch != nil {
		useWebSearch = *opts.WebSearch
	}

	// List ALL hooks early, before building the messages sent to the first
	// completion call -- a hook's own tool definition and Prompt (see below)
	// both need to reach the model before it can decide to invoke that hook
	// at all, so this can't wait until after an answer comes back.
	// activeHooks is reused for every runToolCalls call this turn, so
	// ListChatHooks is never called twice in one turn. A ListChatHooks error
	// is best-effort: it just leaves activeHooks empty rather than failing
	// the turn.
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

	// Leading system messages, in order: (1) the persistent per-endpoint
	// system prompt, unconditional, when set; (2) the calling user's own
	// personal custom prompt (opts.UserCustomPrompt), when set -- see
	// ChatOptions.UserCustomPrompt's doc comment for who sets this and why;
	// (3) each activeHooks entry's own non-empty Prompt, in list order,
	// each its OWN separate system message (not concatenated into one
	// blob) -- so a hook's invocation syntax reaches the model before the
	// first completion call, letting it decide whether to invoke that hook
	// at all. Building this as one ordered slice (rather than prepending
	// piecemeal) keeps that order obvious and gives trimToBudget a single
	// well-defined run of leading system-role messages to keep intact.
	tokenUsage := TokenUsage{MaxContextTokens: endpoint.MaxContextTokens}
	var leading []domain.ChatMessage
	if endpoint.SystemPrompt != "" {
		msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: expandPromptPlaceholders(endpoint.SystemPrompt)}
		leading = append(leading, msg)
		tokenUsage.GlobalPromptTokens = estimateTokens([]domain.ChatMessage{msg})
	}
	if opts.UserCustomPrompt != "" {
		msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: expandPromptPlaceholders(opts.UserCustomPrompt)}
		leading = append(leading, msg)
		tokenUsage.UserPromptTokens = estimateTokens([]domain.ChatMessage{msg})
	}
	for _, h := range activeHooks {
		if h.Prompt != "" {
			msg := domain.ChatMessage{Role: domain.ChatRoleSystem, Content: expandPromptPlaceholders(h.Prompt)}
			leading = append(leading, msg)
			tokenUsage.HookPromptTokens += estimateTokens([]domain.ChatMessage{msg})
		}
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
	// messages[len(leading):] is the actual conversation history sent to the
	// model this turn -- post-trim, since trimToBudget only ever drops
	// history messages, never the leading system messages just measured
	// above (see trimToBudget's own doc comment).
	tokenUsage.HistoryTokens = estimateTokens(messages[len(leading):])

	tools := toolDefsFrom(activeHooks)
	assistantMsg, err := s.completer.Complete(ctx, endpoint, messages, tools)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}

	// Whenever the model chooses to invoke one or more tools instead of
	// answering directly (assistantMsg.ToolCalls non-empty), run them and
	// feed the results back as domain.ChatRoleTool messages correlated by
	// ToolCallID, then ask again -- native tool-calling's own standard
	// multi-turn shape. This repeats up to maxHookFollowUpRounds times --
	// not just once -- because a model that reasonably decides to retry
	// (e.g. a fetch came back with an empty/blocked page, so it tries
	// another URL or another search) makes ANOTHER tool call in that
	// follow-up answer; capping this at exactly one round would leave that
	// second tool call completely unprocessed. Every round's hookResults are
	// accumulated into the final ChatResult, so the UI's folded transparency
	// panel shows every attempt, not just the last. Reuses the SAME
	// activeHooks/tools computed above every round -- ListChatHooks is never
	// called again mid-turn. env carries only ADMIN-CONFIGURED endpoint
	// config (never anything derived from the model's own output or a tool
	// call's argument) -- see ports.HookScriptRunner's doc comment for why
	// this doesn't reopen runToolCalls's security surface.
	var hookResults []domain.ChatHookResult
	currentMessages := messages
	env := map[string]string{"WEB_SEARCH_BASE_URL": endpoint.WebSearchBaseURL}
	for round := 0; round < maxHookFollowUpRounds && len(assistantMsg.ToolCalls) > 0; round++ {
		roundResults := runToolCalls(ctx, activeHooks, s.hookRunner, assistantMsg.ToolCalls, env)
		hookResults = append(hookResults, roundResults...)

		followUp := make([]domain.ChatMessage, 0, len(currentMessages)+1+len(roundResults))
		followUp = append(followUp, currentMessages...)
		followUp = append(followUp, assistantMsg)
		followUp = append(followUp, toolResultMessages(roundResults)...)

		nextMsg, err := s.completer.Complete(ctx, endpoint, followUp, tools)
		if err != nil {
			// Best-effort, same convention as every other augmentation
			// source in this method: keep the current assistantMsg (which
			// may still carry unresolved tool calls) rather than failing
			// the turn.
			break
		}
		assistantMsg = nextMsg
		currentMessages = followUp
	}

	answer := assistantMsg.Content

	// If the loop above ran out of rounds while the model was STILL trying
	// to invoke one more tool (assistantMsg.ToolCalls still non-empty --
	// per the wire convention, Content is typically empty on such a
	// message), or the model returned a genuinely empty answer despite
	// tools being available, answer is empty here. Left as-is, the user
	// would see a blank response with no explanation, even though every
	// result gathered so far (in currentMessages/hookResults) is still
	// right there. Force one last completion call with NO tools offered
	// (so the model can't request yet another one) instead of returning
	// nothing: the model already has everything it found, it just needs
	// telling plainly that no more tool calls are available and to answer
	// with what it has now. Best-effort like every other augmentation here
	// -- a failure just leaves answer empty, no worse than doing nothing.
	if answer == "" && len(tools) > 0 {
		forceFinal := make([]domain.ChatMessage, 0, len(currentMessages)+1)
		forceFinal = append(forceFinal, currentMessages...)
		forceFinal = append(forceFinal, domain.ChatMessage{
			Role:    domain.ChatRoleSystem,
			Content: "No more tool calls are available for this turn. Answer the user's question directly now, using only the information already gathered above.",
		})
		if finalMsg, err := s.completer.Complete(ctx, endpoint, forceFinal, nil); err == nil {
			answer = finalMsg.Content
		}
	}

	return ChatResult{Answer: answer, ContextTrimmed: contextTrimmed, HookResults: hookResults, TokenUsage: tokenUsage}, nil
}

// toolDefsFrom builds the tools list offered to the model from this turn's
// active hooks. A hook whose Parameters is empty/invalid is still offered
// as-is (Complete doesn't validate Parameters, only runToolCalls does, when
// and if the model actually calls it) -- matching the old Pattern-based
// mechanism's own "skip at call time, not at offer time" tolerance for a
// pre-existing invalid row; an empty Parameters is defaulted to a bare
// no-properties object schema so the request never sends invalid JSON.
func toolDefsFrom(hooks []domain.ChatHook) []domain.ToolDef {
	if len(hooks) == 0 {
		return nil
	}
	out := make([]domain.ToolDef, len(hooks))
	for i, h := range hooks {
		params := h.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = domain.ToolDef{Name: h.Name, Description: h.Description, Parameters: params}
	}
	return out
}

// toolResultMessages builds one domain.ChatMessage{Role: ChatRoleTool} per
// result, correlated to the tool call it answers via ToolCallID -- native
// tool-calling expects exactly one such message per tool_call in the
// preceding assistant message (see runToolCalls, which always produces
// exactly one ChatHookResult per input ToolCall, even a skipped/failed one,
// specifically so this invariant holds). A failed/skipped call's message
// carries its Err text instead of Output, so the model can say it couldn't
// complete the tool call rather than being left to guess why nothing came
// back.
func toolResultMessages(results []domain.ChatHookResult) []domain.ChatMessage {
	out := make([]domain.ChatMessage, len(results))
	for i, r := range results {
		content := r.Output
		if r.Err != "" {
			content = "error: " + r.Err
		}
		out[i] = domain.ChatMessage{
			Role:       domain.ChatRoleTool,
			Content:    domain.TruncateWithNote(content, maxHookOutputCharsForModel),
			ToolCallID: r.ToolCallID,
		}
	}
	return out
}

// promptDatePlaceholder, when present in the global system prompt or a
// hook's own Prompt, is replaced with the current UTC date and time,
// rendered via strftime's own %c conversion (see strftime below) -- lets
// an admin write a prompt like "the current time is %c" so the model has a
// concrete anchor for judging whether cached/trained-in information could
// be stale, without needing to re-save the setting every day.
const promptDatePlaceholder = "%c"

func expandPromptPlaceholders(prompt string) string {
	if !strings.Contains(prompt, promptDatePlaceholder) {
		return prompt
	}
	return strings.ReplaceAll(prompt, promptDatePlaceholder, strftime(time.Now().UTC(), "%c"))
}

// strftime formats t using a subset of POSIX strftime(3)'s own conversion
// specifiers -- not a bespoke, Go-reference-layout-derived format of our
// own invention, so %c in a prompt means exactly what %c means anywhere
// else (a shell script, a C program, `date +%c`). Covers the directives a
// date-anchoring prompt placeholder plausibly needs, not the full POSIX
// set; an unrecognized %<letter> is left in the output verbatim (e.g. "%q"
// stays "%q") rather than silently dropped, and a lone trailing "%" is
// kept as-is. %c itself is composed from the others, matching the C
// locale's own conventional ctime(3)/asctime(3)-style rendering (e.g. "Sun
// Sep 21 04:22:35 2026") -- deliberately no time zone abbreviation, same
// as real strftime's %c; a prompt wanting one can write "%c %Z" itself.
func strftime(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i == len(format)-1 {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			fmt.Fprintf(&b, "%d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'A':
			b.WriteString(t.Weekday().String())
		case 'a':
			b.WriteString(t.Weekday().String()[:3])
		case 'B':
			b.WriteString(t.Month().String())
		case 'b', 'h':
			b.WriteString(t.Month().String()[:3])
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		case 'c':
			b.WriteString(strftime(t, "%a %b %e %H:%M:%S %Y"))
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// maxHookFollowUpRounds bounds how many times ChatService.Chat will run a
// round of tool calls and feed the results back to the model for another
// completion -- each round costs one more completion call and (via
// maxHookMatchesPerTurn, chat_hooks.go) up to maxHookMatchesPerTurn more
// script executions, so this is a real cost bound, not just a correctness
// one. 4 covers web_search's own suggested Prompt text
// (packaging/chat-hooks/README.md): one search, then fetching the 3 most
// relevant results to cross-verify -- 4 tool calls total, executed by
// processing the search (round 0), fetch 1 (round 1), fetch 2 (round 2),
// and fetch 3 (round 3). Keep this in sync with that prompt text if either
// changes: a smaller value here than what the prompt asks for silently
// drops the model's last permitted call (see the post-loop
// force-final-answer fallback below, which exists specifically to catch a
// model that still tries ONE more call than this allows, whatever the
// reason).
const maxHookFollowUpRounds = 4

// maxHookOutputCharsForModel bounds how much of each hook result's own
// Output toolResultMessages feeds back into the follow-up completion call
// -- hookrunner.Runner already caps a single script's stdout at 64KB, but
// up to maxHookMatchesPerTurn (chat_hooks.go) of those could still add up
// to a very large follow-up prompt; this is a second, tighter cap
// specifically on what actually reaches the model, same "cap and note"
// convention as hookrunner's own truncation.
const maxHookOutputCharsForModel = 8000

// estimateTokens sums messages' character-count-based token estimate (see
// domain.ApproxCharsPerToken) -- exact tokenization isn't worth the
// complexity here, since trimToBudget drops a whole message at a time,
// which already has slack an exact tokenizer's extra precision wouldn't
// meaningfully improve.
func estimateTokens(messages []domain.ChatMessage) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
	}
	return chars / domain.ApproxCharsPerToken
}

// trimToBudget drops the oldest messages in messages -- keeping every
// leading system-role message intact (there can now be several: the
// persistent per-endpoint SystemPrompt, then one per active chat hook's own
// Prompt, then the search-context message, see ChatService.Chat), and
// always keeping at least the single most recent message even if it
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
