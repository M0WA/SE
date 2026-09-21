package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// msgEqual compares two domain.ChatMessage values -- ChatMessage now
// carries a []ToolCall slice field, making it non-comparable with the plain
// == operator every test in this file used to use.
func msgEqual(a, b domain.ChatMessage) bool {
	return reflect.DeepEqual(a, b)
}

// fakeChatEndpointStore is a minimal ports.ChatEndpointStore fake: a
// single stored endpoint plus an error to return instead, mirroring the
// port's Get/Set-on-one-row shape (no need for a map of many, unlike
// EmbeddingEndpointStore).
type fakeChatEndpointStore struct {
	endpoint domain.ChatEndpoint
	getErr   error
}

func (f *fakeChatEndpointStore) GetChatEndpoint(ctx context.Context) (domain.ChatEndpoint, error) {
	if f.getErr != nil {
		return domain.ChatEndpoint{}, f.getErr
	}
	return f.endpoint, nil
}

func (f *fakeChatEndpointStore) SetChatEndpoint(ctx context.Context, e domain.ChatEndpoint) error {
	f.endpoint = e
	return nil
}

// toolCallMessage builds an assistant domain.ChatMessage carrying a single
// ToolCall -- the shape a real OpenAI-compatible endpoint returns when the
// model chooses to invoke a tool instead of answering directly (empty
// Content).
func toolCallMessage(id, name, argumentsJSON string) domain.ChatMessage {
	return domain.ChatMessage{Role: domain.ChatRoleAssistant, ToolCalls: []domain.ToolCall{{ID: id, Name: name, Arguments: argumentsJSON}}}
}

// plainMessage builds an ordinary assistant answer with no tool call.
func plainMessage(content string) domain.ChatMessage {
	return domain.ChatMessage{Role: domain.ChatRoleAssistant, Content: content}
}

// fakeChatCompleter is a minimal ports.ChatCompleter fake recording the
// messages and tools it was called with, so a test can assert whether/what
// got prepended or offered. calledWith/calledTools are the LAST call's
// values (every test that only expects one call uses these); allCalls/
// allTools record every call in order, for tests exercising ChatService.
// Chat's tool follow-up round, which calls Complete more than once. When
// responses is non-empty, each successive call returns the next entry
// (clamped to the last once exhausted) instead of the single fixed
// response -- letting a test give a different response to a follow-up call
// than the first. answer is a convenience for the common case of a single
// plain-text response with no tool call.
type fakeChatCompleter struct {
	answer      string
	response    domain.ChatMessage
	responses   []domain.ChatMessage
	err         error
	calledWith  []domain.ChatMessage
	calledTools []domain.ToolDef
	allCalls    [][]domain.ChatMessage
	allTools    [][]domain.ToolDef
	calledEndpt domain.ChatEndpoint
	wasCalled   bool
	callCount   int
}

func (f *fakeChatCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error) {
	f.wasCalled = true
	f.calledEndpt = endpoint
	f.calledWith = messages
	f.calledTools = tools
	f.allCalls = append(f.allCalls, messages)
	f.allTools = append(f.allTools, tools)
	idx := f.callCount
	f.callCount++
	if f.err != nil {
		return domain.ChatMessage{}, f.err
	}
	if len(f.responses) > 0 {
		if idx >= len(f.responses) {
			idx = len(f.responses) - 1
		}
		return f.responses[idx], nil
	}
	if f.response.Content != "" || len(f.response.ToolCalls) > 0 {
		return f.response, nil
	}
	return plainMessage(f.answer), nil
}

func TestChatService_EmptyHistory(t *testing.T) {
	svc := NewChatService(&fakeChatEndpointStore{}, &fakeChatCompleter{}, nil, nil)
	_, err := svc.Chat(context.Background(), nil, ChatOptions{})
	if err == nil {
		t.Fatal("expected error for empty history, got nil")
	}
}

func TestChatService_NotConfigured(t *testing.T) {
	wantErr := errors.New("boom")
	endpoints := &fakeChatEndpointStore{getErr: wantErr}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped/equal sentinel error, got %v", err)
	}
}

func TestChatService_NotConfigured_ErrIsPreserved(t *testing.T) {
	endpoints := &fakeChatEndpointStore{getErr: ports.ErrChatEndpointNotConfigured}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected errors.Is to match ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_DisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: false}}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_MaxContextTokens_Zero_NoTrimming(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 0}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("x", 100)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("y", 100)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("z", 100)},
	}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != len(history) {
		t.Fatalf("expected no trimming with MaxContextTokens=0, got %d messages", len(completer.calledWith))
	}
	if result.ContextTrimmed {
		t.Error("expected ContextTrimmed=false when MaxContextTokens=0 disables trimming")
	}
}

func TestChatService_MaxContextTokens_TrimsOldestMessages(t *testing.T) {
	// Budget fits only the newest message (30 chars ~= 10 tokens) -- each
	// older 90-char (~30-token) message would blow past 15.
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 15}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 90)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("b", 90)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("c", 30)},
	}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 || completer.calledWith[0].Content != history[2].Content {
		t.Fatalf("expected only the most recent message kept, got %v", completer.calledWith)
	}
	if !result.ContextTrimmed {
		t.Error("expected ContextTrimmed=true when older messages were dropped")
	}
}

func TestChatService_MaxContextTokens_KeepsAsManyRecentMessagesAsFit(t *testing.T) {
	// Budget fits the newest (10 tokens) plus the one before it (30 tokens)
	// but not the oldest (another 30 tokens): 10+30=40 <= 40 < 70.
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 40}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 90)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("b", 90)},
		{Role: domain.ChatRoleUser, Content: strings.Repeat("c", 30)},
	}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 2 || !msgEqual(completer.calledWith[0], history[1]) || !msgEqual(completer.calledWith[1], history[2]) {
		t.Fatalf("expected the two newest messages kept, got %v", completer.calledWith)
	}
}

func TestChatService_MaxContextTokens_AlwaysKeepsNewestMessageEvenIfOversized(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 1}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: strings.Repeat("a", 300)}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 {
		t.Fatalf("expected the single oversized message kept regardless of budget, got %v", completer.calledWith)
	}
	if result.ContextTrimmed {
		t.Error("expected ContextTrimmed=false when nothing was actually dropped (only message kept regardless)")
	}
}

func TestChatService_MaxContextTokens_KeepsHookPromptSystemMessageIntact(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 50}}
	completer := &fakeChatCompleter{answer: "answer"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: strings.Repeat("s", 60)},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleUser, Content: "newest question"},
	}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) < 2 {
		t.Fatalf("expected at least the hook prompt system message plus the newest message, got %v", completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem {
		t.Fatalf("expected the hook prompt system message to survive trimming as the first message, got role %q", completer.calledWith[0].Role)
	}
	last := completer.calledWith[len(completer.calledWith)-1]
	if last.Content != "newest question" {
		t.Fatalf("expected the newest message to survive trimming, got %v", last)
	}
}

func TestEstimateTokens(t *testing.T) {
	messages := []domain.ChatMessage{{Content: "abcdef"}, {Content: "abc"}}
	if got, want := estimateTokens(messages), 3; got != want {
		t.Fatalf("estimateTokens() = %d, want %d", got, want)
	}
}

func TestTrimToBudget_UnderBudget_ReturnsUnchanged(t *testing.T) {
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	got := trimToBudget(messages, 1000)
	if len(got) != 1 {
		t.Fatalf("expected messages unchanged, got %v", got)
	}
}

func TestChatService_CompleterError(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{err: errors.New("upstream 500")}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	_, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err == nil {
		t.Fatal("expected error from completer to propagate")
	}
}

// TestChatService_SystemPromptAlone_SoleLeadingSystemMessage proves a
// persistent per-endpoint SystemPrompt is prepended as the only leading
// system message when no search context applies.
func TestChatService_SystemPromptAlone_SoleLeadingSystemMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate."}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 2 {
		t.Fatalf("expected persistent prompt + 1 history message, got %v", completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem || completer.calledWith[0].Content != "You are a pirate." {
		t.Fatalf("expected the leading message to be the persistent system prompt, got %+v", completer.calledWith[0])
	}
	if !msgEqual(completer.calledWith[1], history[0]) {
		t.Fatalf("expected original history preserved after the prompt, got %v", completer.calledWith[1])
	}
}

// TestChatService_EmptySystemPrompt_NoLeadingPromptMessage proves an empty
// SystemPrompt behaves exactly as before its addition -- no regression.
func TestChatService_EmptySystemPrompt_NoLeadingPromptMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: ""}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 || !msgEqual(completer.calledWith[0], history[0]) {
		t.Fatalf("expected plain history unchanged with an empty SystemPrompt, got %v", completer.calledWith)
	}
}

// TestTrimToBudget_TwoLeadingSystemMessages_KeepsBothAndNewest proves
// trimToBudget's fix: ALL leading system-role messages are kept intact,
// not just the first one, when the budget only fits them plus the newest
// message.
func TestTrimToBudget_TwoLeadingSystemMessages_KeepsBothAndNewest(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("p", 15)}, // persistent prompt, ~5 tokens
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("r", 15)}, // web-search context, ~5 tokens
		{Role: domain.ChatRoleUser, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleUser, Content: "newest question"},
	}
	// Budget fits both system messages (~10 tokens) plus the newest
	// message (~5 tokens) but nothing else.
	got := trimToBudget(messages, 15)

	if len(got) != 3 {
		t.Fatalf("expected both system messages plus the newest message kept, got %d messages: %v", len(got), got)
	}
	if !msgEqual(got[0], messages[0]) || !msgEqual(got[1], messages[1]) {
		t.Fatalf("expected both leading system messages preserved intact, got %v", got[:2])
	}
	if got[2].Content != "newest question" {
		t.Fatalf("expected the newest message kept, got %v", got[2])
	}
}

func TestChatService_HooksNil_HookResultsEmpty(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.HookResults) != 0 {
		t.Fatalf("expected no hook results when hooks is nil, got %v", result.HookResults)
	}
}

// TestChatService_HooksConfigured_ToolCallPopulatesHookResults proves
// ChatService.Chat wires hooks/hookRunner end to end: a tool call the
// completer returns, naming an active hook, produces a populated
// ChatResult.HookResults.
func TestChatService_HooksConfigured_ToolCallPopulatesHookResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.HookResults) != 1 {
		t.Fatalf("expected 1 hook result, got %v", result.HookResults)
	}
	r := result.HookResults[0]
	if r.HookName != "web_search" || r.ToolCallID != "call_1" || r.Input != "golang release notes" || r.Output != "top result" {
		t.Fatalf("unexpected hook result: %+v", r)
	}
	// The tools list offered on every call should describe this hook.
	if len(completer.allTools[0]) != 1 || completer.allTools[0][0].Name != "web_search" {
		t.Fatalf("expected the active hook offered as a tool, got %v", completer.allTools[0])
	}
}

// TestChatService_HookFires_FeedsResultsBackForFinalAnswer proves a tool
// call triggers a follow-up completion call (exactly one here, since the
// follow-up's own answer doesn't itself request a tool call -- see
// TestChatService_HookRetries_SecondToolCallAlsoProcessed for the
// multi-round case), whose messages are the original ones plus the
// tool-call assistant turn plus one domain.ChatRoleTool message correlated
// by ToolCallID, and that the RETURNED answer is the follow-up's own
// content, not the tool-call request itself.
func TestChatService_HookFires_FeedsResultsBackForFinalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	firstMsg := toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes"))
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		firstMsg,
		plainMessage("Go 1.26 was just released with several performance improvements."),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": `{"results":["go 1.26 release notes"]}`}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what's new in the latest go release?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "Go 1.26 was just released with several performance improvements." {
		t.Fatalf("expected the follow-up completion's own answer returned, got %q", result.Answer)
	}
	if len(result.HookResults) != 1 || result.HookResults[0].Output != `{"results":["go 1.26 release notes"]}` {
		t.Fatalf("expected the hook result still surfaced for the UI, got %+v", result.HookResults)
	}

	if len(completer.allCalls) != 2 {
		t.Fatalf("expected exactly 2 completion calls (initial + one follow-up), got %d", len(completer.allCalls))
	}
	followUp := completer.allCalls[1]
	if len(followUp) != 3 {
		t.Fatalf("expected 3 messages in the follow-up call (history + tool-call turn + tool result), got %d: %+v", len(followUp), followUp)
	}
	if !msgEqual(followUp[0], history[0]) {
		t.Fatalf("expected the original history preserved first, got %+v", followUp[0])
	}
	if followUp[1].Role != domain.ChatRoleAssistant || len(followUp[1].ToolCalls) != 1 || followUp[1].ToolCalls[0].Name != "web_search" {
		t.Fatalf("expected the model's own tool-call message re-sent as an assistant turn, got %+v", followUp[1])
	}
	if followUp[2].Role != domain.ChatRoleTool || followUp[2].ToolCallID != "call_1" || !strings.Contains(followUp[2].Content, `{"results":["go 1.26 release notes"]}`) {
		t.Fatalf("expected a tool-role message carrying the hook's result, correlated by ToolCallID, got %+v", followUp[2])
	}
}

// TestChatService_HookRetries_SecondToolCallAlsoProcessed is the regression
// test for a real, reported failure: a model whose first fetch/search comes
// back empty or blocked reasonably tries again with a SECOND tool call --
// that second tool call used to be left completely unprocessed (the old
// "exactly one follow-up round" limit), leaving a dangling, unanswered
// tool call as the whole turn's Answer instead of a real response. Both
// rounds' hook results should be accumulated into the final ChatResult.
func TestChatService_HookRetries_SecondToolCallAlsoProcessed(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_fetch", argsJSON("url", "https://example.com/blocked")),
		toolCallMessage("call_2", "web_fetch", argsJSON("url", "https://example.com/mirror")),
		plainMessage("The mirror page says hello world."),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Description: "Fetch a URL.", Parameters: singleStringParams("url"), Script: "web_fetch.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_fetch.sh": "<empty/blocked page>"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "The mirror page says hello world." {
		t.Fatalf("expected the SECOND follow-up's real answer returned, not a dangling tool call, got %q", result.Answer)
	}
	if len(result.HookResults) != 2 {
		t.Fatalf("expected both rounds' hook results accumulated, got %+v", result.HookResults)
	}
	if len(completer.allCalls) != 3 {
		t.Fatalf("expected 3 completion calls (initial + 2 follow-ups), got %d", len(completer.allCalls))
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected the script run once per round (2 total), got %d", len(runner.calls))
	}
	if runner.calls[0].args[0] != "https://example.com/blocked" || runner.calls[1].args[0] != "https://example.com/mirror" {
		t.Fatalf("expected each round's own argument passed through, got %+v", runner.calls)
	}
}

// TestChatService_HookRetries_CappedAtMaxFollowUpRounds proves the retry
// loop is bounded: a model that keeps invoking a tool in every response
// stops being fed back after maxHookFollowUpRounds rounds, rather than
// looping forever. The completer fake here always returns the same
// unsatisfied tool call -- including on the force-final-answer fallback
// call below (see TestChatService_ForceFinalAnswer_* for the case where
// that call actually produces real prose) -- so the end result is still an
// empty answer, just reached via one extra completion call than the loop
// alone accounts for.
func TestChatService_HookRetries_CappedAtMaxFollowUpRounds(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{response: toolCallMessage("call", "web_fetch", argsJSON("url", "https://example.com/never-satisfied"))}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Description: "Fetch a URL.", Parameters: singleStringParams("url"), Script: "web_fetch.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_fetch.sh": "<empty/blocked page>"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// maxHookFollowUpRounds loop iterations, plus the initial completion
	// before the loop, plus one force-final-answer call once the loop's
	// last response is still an unsatisfied tool call.
	wantCalls := maxHookFollowUpRounds + 2
	if len(completer.allCalls) != wantCalls {
		t.Fatalf("expected exactly %d completion calls, got %d", wantCalls, len(completer.allCalls))
	}
	if len(result.HookResults) != maxHookFollowUpRounds {
		t.Fatalf("expected exactly maxHookFollowUpRounds (%d) hook results accumulated, got %d", maxHookFollowUpRounds, len(result.HookResults))
	}
	if result.Answer != "" {
		t.Fatalf("expected an empty answer (the force-final call got the same unsatisfied tool call again), got %q", result.Answer)
	}
	// The force-final call must offer no tools, so the model can't request
	// yet another one.
	lastTools := completer.allTools[len(completer.allTools)-1]
	if len(lastTools) != 0 {
		t.Fatalf("expected the force-final call to offer no tools, got %v", lastTools)
	}
}

// TestChatService_ForceFinalAnswer_RescuesAnEmptyAnswerAfterCapIsHit is the
// direct regression test for a real, reported failure: a model whose last
// permitted follow-up (round maxHookFollowUpRounds-1) is STILL nothing but
// a tool call used to leave the user with a literally empty response. The
// force-final-answer fallback must issue one more, tool-free completion
// call and use ITS answer instead of returning nothing.
func TestChatService_ForceFinalAnswer_RescuesAnEmptyAnswerAfterCapIsHit(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	// maxHookFollowUpRounds+1 unsatisfied tool calls -- the initial response
	// PLUS every round's own follow-up must all be a tool call to actually
	// exhaust the loop without a real answer ending it early -- then a real
	// answer on the force-final call.
	responses := make([]domain.ChatMessage, 0, maxHookFollowUpRounds+2)
	for i := 0; i < maxHookFollowUpRounds+1; i++ {
		responses = append(responses, toolCallMessage("call", "web_search", argsJSON("query", "golang release notes")))
	}
	responses = append(responses, plainMessage("Go 1.26 was released in August 2026."))
	completer := &fakeChatCompleter{responses: responses}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "some results"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "when was the latest Go released?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "Go 1.26 was released in August 2026." {
		t.Fatalf("expected the force-final call's real answer, got %q", result.Answer)
	}
	wantCalls := maxHookFollowUpRounds + 2
	if len(completer.allCalls) != wantCalls {
		t.Fatalf("expected exactly %d completion calls (initial + %d rounds + 1 force-final), got %d", wantCalls, maxHookFollowUpRounds, len(completer.allCalls))
	}
	lastCall := completer.allCalls[len(completer.allCalls)-1]
	lastMsg := lastCall[len(lastCall)-1]
	if lastMsg.Role != domain.ChatRoleSystem || !strings.Contains(lastMsg.Content, "No more tool calls are available") {
		t.Fatalf("expected the force-final call's last message to be the no-more-tools system instruction, got %+v", lastMsg)
	}
}

// TestChatService_ForceFinalAnswer_NotTriggeredWhenAnswerIsNonEmpty proves
// the force-final fallback is scoped exactly to the empty-answer case --
// a turn that ends with a real (non-tool-call) answer, whether or not it
// used the full round budget, never makes an extra completion call.
func TestChatService_ForceFinalAnswer_NotTriggeredWhenAnswerIsNonEmpty(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("Go 1.26 was released in August 2026."),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "some results"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "when was the latest Go released?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "Go 1.26 was released in August 2026." {
		t.Fatalf("expected the real second answer, got %q", result.Answer)
	}
	if len(completer.allCalls) != 2 {
		t.Fatalf("expected exactly 2 completion calls (initial + 1 follow-up), no force-final call, got %d", len(completer.allCalls))
	}
}

// TestChatService_ForceFinalAnswer_FailureLeavesAnswerEmpty proves the
// force-final call is best-effort like every other augmentation source in
// this method: if it errors too, the turn still succeeds (no error
// returned), just with an empty answer -- no worse than before this
// fallback existed.
func TestChatService_ForceFinalAnswer_FailureLeavesAnswerEmpty(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &erroringAfterNCallsCompleter{
		n:        maxHookFollowUpRounds + 1,
		response: toolCallMessage("call", "web_fetch", argsJSON("url", "https://example.com/never-satisfied")),
	}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Description: "Fetch a URL.", Parameters: singleStringParams("url"), Script: "web_fetch.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_fetch.sh": "<empty/blocked page>"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected the force-final completion error to be swallowed, got %v", err)
	}
	if result.Answer != "" {
		t.Fatalf("expected an empty answer when the force-final call itself errors, got %q", result.Answer)
	}
}

// TestChatService_HookFollowUpCompletionErrors_FallsBackToOriginalAnswer
// proves a failed follow-up completion is best-effort, same convention as
// every other augmentation source in ChatService.Chat: the turn still
// succeeds, falling back to the empty Content of the tool-call message that
// triggered the (now-failed) follow-up round.
func TestChatService_HookFollowUpCompletionErrors_FallsBackToOriginalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &erroringOnSecondCallCompleter{first: toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes"))}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "results"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected the follow-up completion error to be swallowed, got %v", err)
	}
	if result.Answer != "" {
		t.Fatalf("expected an empty answer (the tool-call message that triggered the failed follow-up has empty Content), got %q", result.Answer)
	}
}

// erroringOnSecondCallCompleter is a ports.ChatCompleter fake whose first
// call succeeds and every later call fails -- fakeChatCompleter's own
// responses-by-index doesn't model a call FAILING partway through, so this
// is a small dedicated fake for that one scenario.
type erroringOnSecondCallCompleter struct {
	first domain.ChatMessage
	calls int
}

func (f *erroringOnSecondCallCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return domain.ChatMessage{}, errors.New("upstream unavailable")
}

// erroringAfterNCallsCompleter is a ports.ChatCompleter fake whose first n
// calls succeed with the same response (modeling a model that never
// satisfies a hook and keeps retrying) and every call after that fails --
// used to prove the force-final-answer fallback (see ChatService.Chat) is
// itself best-effort: a failure there must not fail the whole turn.
type erroringAfterNCallsCompleter struct {
	n        int
	response domain.ChatMessage
	calls    int
}

func (f *erroringAfterNCallsCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error) {
	f.calls++
	if f.calls <= f.n {
		return f.response, nil
	}
	return domain.ChatMessage{}, errors.New("upstream unavailable")
}

// TestChatService_NoHookMatch_OnlyOneCompletionCall proves the follow-up
// round never fires when the model didn't request a tool call -- the
// common case shouldn't cost a second completion call.
func TestChatService_NoHookMatch_OnlyOneCompletionCall(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "a plain answer with no tool call"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "a plain answer with no tool call" {
		t.Fatalf("expected the original answer unchanged, got %q", result.Answer)
	}
	if len(completer.allCalls) != 1 {
		t.Fatalf("expected exactly 1 completion call when no tool was called, got %d", len(completer.allCalls))
	}
}

// TestToolResultMessages_TruncatesLongOutput proves toolResultMessages
// bounds each result's own Output, independent of hookrunner's own (much
// larger) 64KB cap -- several long results in one turn could otherwise
// still add up to a very large follow-up prompt.
func TestToolResultMessages_TruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", maxHookOutputCharsForModel+500)
	got := toolResultMessages([]domain.ChatHookResult{{HookName: "web_search", ToolCallID: "call_1", Output: long}})
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if strings.Contains(got[0].Content, long) {
		t.Error("expected the long output truncated, got it included verbatim")
	}
	if !strings.Contains(got[0].Content, "truncated") {
		t.Errorf("expected a truncation note, got %q", got[0].Content)
	}
}

// TestToolResultMessages_IncludesErrorInsteadOfOutput proves a failed
// hook's Err reaches the model instead of a blank Output, so it can tell
// the user the tool call didn't work rather than guessing.
func TestToolResultMessages_IncludesErrorInsteadOfOutput(t *testing.T) {
	got := toolResultMessages([]domain.ChatHookResult{{HookName: "web_search", ToolCallID: "call_1", Err: "script timed out"}})
	if len(got) != 1 || !strings.Contains(got[0].Content, "script timed out") {
		t.Errorf("expected the error text included, got %v", got)
	}
}

// TestToolResultMessages_CorrelatesByToolCallID proves each result's
// message carries the matching ToolCallID and domain.ChatRoleTool role --
// the invariant native tool-calling requires between an assistant's
// tool_calls and their answering messages.
func TestToolResultMessages_CorrelatesByToolCallID(t *testing.T) {
	results := []domain.ChatHookResult{
		{HookName: "a", ToolCallID: "call_1", Output: "out-a"},
		{HookName: "b", ToolCallID: "call_2", Output: "out-b"},
	}
	got := toolResultMessages(results)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	for i, m := range got {
		if m.Role != domain.ChatRoleTool {
			t.Errorf("message %d: expected role %q, got %q", i, domain.ChatRoleTool, m.Role)
		}
	}
	if got[0].ToolCallID != "call_1" || got[1].ToolCallID != "call_2" {
		t.Fatalf("expected ToolCallID correlated in order, got %+v", got)
	}
}

// TestToolDefsFrom_BuildsOneEntryPerHook proves the tools list offered to
// the model mirrors each active hook's Name/Description/Parameters.
func TestToolDefsFrom_BuildsOneEntryPerHook(t *testing.T) {
	hooks := []domain.ChatHook{
		{Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query")},
		{Name: "web_fetch", Description: "Fetch a URL.", Parameters: singleStringParams("url")},
	}
	tools := toolDefsFrom(hooks)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "web_search" || tools[0].Description != "Search the web." {
		t.Errorf("unexpected first tool: %+v", tools[0])
	}
	if tools[1].Name != "web_fetch" || tools[1].Description != "Fetch a URL." {
		t.Errorf("unexpected second tool: %+v", tools[1])
	}
}

// TestToolDefsFrom_NoHooks_ReturnsNil proves an empty hooks list returns
// nil, not an empty slice -- so httpchat's own toWireTools correctly omits
// the request's "tools" field entirely.
func TestToolDefsFrom_NoHooks_ReturnsNil(t *testing.T) {
	if got := toolDefsFrom(nil); got != nil {
		t.Fatalf("expected nil for no hooks, got %v", got)
	}
}

// TestToolDefsFrom_EmptyParameters_DefaultsToBareObjectSchema proves a hook
// with no Parameters set still produces a valid (if empty) JSON-schema
// object, never an empty/invalid Parameters value in the request sent to
// the model.
func TestToolDefsFrom_EmptyParameters_DefaultsToBareObjectSchema(t *testing.T) {
	tools := toolDefsFrom([]domain.ChatHook{{Name: "bare"}})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if string(tools[0].Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("expected a default bare object schema, got %q", tools[0].Parameters)
	}
}

// TestChatService_HooksListError_Swallowed proves a ListChatHooks error is
// best-effort, same convention as the web-search error elsewhere in
// this file: it never fails the whole chat turn.
func TestChatService_HooksListError_Swallowed(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	hooks := &fakeChatHookStore{err: errors.New("db down")}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected hook list error to be swallowed, got %v", err)
	}
	if len(result.HookResults) != 0 {
		t.Fatalf("expected no hook results when ListChatHooks errors, got %v", result.HookResults)
	}
}

// TestChatService_GatedHook_InactiveWhenWebSearchOff proves a
// GatedByWebSearch hook contributes neither its Prompt nor a tool offer
// when the effective web-search toggle is off -- even though Enabled is
// true.
func TestChatService_GatedHook_InactiveWhenWebSearchOff(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{answer: "no need to search"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range completer.calledWith {
		if m.Role == domain.ChatRoleSystem && strings.Contains(m.Content, "You can search the web.") {
			t.Fatalf("expected the gated hook's Prompt not injected when web search is off, got %v", completer.calledWith)
		}
	}
	if len(completer.calledTools) != 0 {
		t.Fatalf("expected the gated hook not offered as a tool when web search is off, got %v", completer.calledTools)
	}
	if len(result.HookResults) != 0 || len(runner.calls) != 0 {
		t.Fatalf("expected the gated hook not to run when web search is off, got results=%v calls=%v", result.HookResults, runner.calls)
	}
}

// TestChatService_GatedHook_ActiveWhenWebSearchOn proves the same hook as
// above IS active -- Prompt injected, offered as a tool, and runs normally
// -- once the effective web-search toggle (endpoint default here) is on.
func TestChatService_GatedHook_ActiveWhenWebSearchOn(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, m := range completer.allCalls[0] {
		if m.Role == domain.ChatRoleSystem && m.Content == "You can search the web." {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the gated hook's Prompt injected as its own system message when web search is on, got %v", completer.allCalls[0])
	}
	if len(completer.allTools[0]) != 1 || completer.allTools[0][0].Name != "web_search" {
		t.Fatalf("expected the gated hook offered as a tool when web search is on, got %v", completer.allTools[0])
	}
	if len(result.HookResults) != 1 || result.HookResults[0].Output != "top result" {
		t.Fatalf("expected the gated hook to run when web search is on, got %v", result.HookResults)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 script call, got %d", len(runner.calls))
	}
}

// TestChatService_GatedHook_PerQuestionOverrideActivates proves the
// per-question ChatOptions.WebSearch override (not just the endpoint
// default) is what actually governs gating.
func TestChatService_GatedHook_PerQuestionOverrideActivates(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	on := true
	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{WebSearch: &on})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.HookResults) != 1 {
		t.Fatalf("expected the gated hook active via the per-question override, got %v", result.HookResults)
	}
}

// TestChatService_UngatedHook_ActiveRegardlessOfWebSearch proves a
// GatedByWebSearch=false hook's Prompt is injected and it runs regardless
// of the web-search toggle's value -- today's existing behavior,
// unaffected by adding gating for other hooks.
func TestChatService_UngatedHook_ActiveRegardlessOfWebSearch(t *testing.T) {
	for _, webSearchEnabled := range []bool{false, true} {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: webSearchEnabled}}
		completer := &fakeChatCompleter{responses: []domain.ChatMessage{
			toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
			plainMessage("done"),
		}}
		hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
			{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: false},
		}}
		runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
		svc := NewChatService(endpoints, completer, hooks, runner)

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		result, err := svc.Chat(context.Background(), history, ChatOptions{})
		if err != nil {
			t.Fatalf("unexpected error (webSearchEnabled=%v): %v", webSearchEnabled, err)
		}
		found := false
		for _, m := range completer.allCalls[0] {
			if m.Role == domain.ChatRoleSystem && m.Content == "You can search the web." {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected the ungated hook's Prompt injected regardless of webSearchEnabled=%v, got %v", webSearchEnabled, completer.allCalls[0])
		}
		if len(result.HookResults) != 1 {
			t.Fatalf("expected the ungated hook to run regardless of webSearchEnabled=%v, got %v", webSearchEnabled, result.HookResults)
		}
	}
}

// TestChatService_TwoActiveHooksWithPrompts_TwoSeparateLeadingSystemMessages
// proves two active hooks, each with a non-empty Prompt, produce two
// separate leading system messages (not concatenated into one), in list
// order, after endpoint.SystemPrompt and before the conversation history.
func TestChatService_TwoActiveHooksWithPrompts_TwoSeparateLeadingSystemMessages(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.",
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "first", Description: "First tool.", Parameters: singleStringParams("a"), Script: "a.sh", Enabled: true, Prompt: "First hook prompt."},
		{ID: "2", Name: "second", Description: "Second tool.", Parameters: singleStringParams("b"), Script: "b.sh", Enabled: true, Prompt: "Second hook prompt."},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 4 {
		t.Fatalf("expected endpoint prompt + 2 hook prompts + 1 history message, got %d: %v", len(completer.calledWith), completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem || completer.calledWith[0].Content != "You are a pirate." {
		t.Fatalf("expected the endpoint's own SystemPrompt first, got %+v", completer.calledWith[0])
	}
	if completer.calledWith[1].Role != domain.ChatRoleSystem || completer.calledWith[1].Content != "First hook prompt." {
		t.Fatalf("expected the first hook's own separate system message second, got %+v", completer.calledWith[1])
	}
	if completer.calledWith[2].Role != domain.ChatRoleSystem || completer.calledWith[2].Content != "Second hook prompt." {
		t.Fatalf("expected the second hook's own separate system message third, got %+v", completer.calledWith[2])
	}
	if !msgEqual(completer.calledWith[3], history[0]) {
		t.Fatalf("expected original history preserved last, got %v", completer.calledWith[3])
	}
}

// TestChatService_UserCustomPrompt_InjectedBetweenGlobalPromptAndHookPrompts
// proves opts.UserCustomPrompt is injected as its own leading system
// message, positioned after the endpoint's own SystemPrompt and before any
// active hook's own Prompt -- order: global endpoint prompt -> personal
// user prompt -> hook/tool-usage prompts.
func TestChatService_UserCustomPrompt_InjectedBetweenGlobalPromptAndHookPrompts(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.",
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "first", Description: "First tool.", Parameters: singleStringParams("a"), Script: "a.sh", Enabled: true, Prompt: "First hook prompt."},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{UserCustomPrompt: "Always answer in haiku."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 4 {
		t.Fatalf("expected endpoint prompt + user prompt + hook prompt + 1 history message, got %d: %v", len(completer.calledWith), completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem || completer.calledWith[0].Content != "You are a pirate." {
		t.Fatalf("expected the endpoint's own SystemPrompt first, got %+v", completer.calledWith[0])
	}
	if completer.calledWith[1].Role != domain.ChatRoleSystem || completer.calledWith[1].Content != "Always answer in haiku." {
		t.Fatalf("expected the user's own custom prompt second (after global, before hooks), got %+v", completer.calledWith[1])
	}
	if completer.calledWith[2].Role != domain.ChatRoleSystem || completer.calledWith[2].Content != "First hook prompt." {
		t.Fatalf("expected the hook's own prompt third, got %+v", completer.calledWith[2])
	}
	if !msgEqual(completer.calledWith[3], history[0]) {
		t.Fatalf("expected original history preserved last, got %v", completer.calledWith[3])
	}
	if result.TokenUsage.UserPromptTokens <= 0 {
		t.Errorf("expected a nonzero UserPromptTokens, got %+v", result.TokenUsage)
	}
}

// TestChatService_EmptyUserCustomPrompt_NoLeadingPromptMessage proves an
// empty opts.UserCustomPrompt (the common case: a role=admin session, or a
// role=user session with no custom prompt set) contributes no extra
// leading system message and no UserPromptTokens -- same empty-skip
// convention as SystemPrompt/hook prompts.
func TestChatService_EmptyUserCustomPrompt_NoLeadingPromptMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate."}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{UserCustomPrompt: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 2 {
		t.Fatalf("expected only the global prompt + history message, got %v", completer.calledWith)
	}
	if result.TokenUsage.UserPromptTokens != 0 {
		t.Errorf("expected zero UserPromptTokens when UserCustomPrompt is empty, got %+v", result.TokenUsage)
	}
}

// TestChatService_UserCustomPromptDatePlaceholder_Expanded proves a literal
// "%c" in opts.UserCustomPrompt is expanded the same way as the global
// SystemPrompt/hook Prompt.
func TestChatService_UserCustomPromptDatePlaceholder_Expanded(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{UserCustomPrompt: "Today is %c."}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) < 1 {
		t.Fatalf("expected at least 1 leading message, got %v", completer.calledWith)
	}
	now := strftime(time.Now().UTC(), "%a %b %e %H:%M")
	if strings.Contains(completer.calledWith[0].Content, "%c") || !strings.Contains(completer.calledWith[0].Content, now) {
		t.Errorf("expected the user prompt's %%c expanded to contain %q, got %q", now, completer.calledWith[0].Content)
	}
}

// TestChatService_EndpointSystemPrompt_InjectedWhenHooksInactiveOrNil is a
// regression check proving the endpoint's own SystemPrompt injection never
// depends on hook state -- neither when every hook is inactive (gated hook,
// web search off) nor when s.hooks is nil.
func TestChatService_EndpointSystemPrompt_InjectedWhenHooksInactiveOrNil(t *testing.T) {
	t.Run("hooks all inactive", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate.", WebSearchEnabled: false}}
		completer := &fakeChatCompleter{answer: "answer"}
		hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
			{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "hook prompt", GatedByWebSearch: true},
		}}
		svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 2 || completer.calledWith[0].Content != "You are a pirate." {
			t.Fatalf("expected the endpoint's SystemPrompt still injected with every hook inactive, got %v", completer.calledWith)
		}
	})

	t.Run("hooks nil", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate."}}
		completer := &fakeChatCompleter{answer: "answer"}
		svc := NewChatService(endpoints, completer, nil, nil)

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 2 || completer.calledWith[0].Content != "You are a pirate." {
			t.Fatalf("expected the endpoint's SystemPrompt still injected with s.hooks nil, got %v", completer.calledWith)
		}
	})
}

// TestChatService_SystemPromptDatePlaceholder_Expanded proves a literal "%c"
// in either the endpoint's global SystemPrompt or an active hook's own
// Prompt is replaced with the current date and time before reaching the
// model -- lets an admin anchor "assume this may be outdated" language to a
// concrete timestamp without re-saving the setting every day. Compared at
// minute granularity (dropping seconds) so this doesn't flake on a rare
// second-boundary race between the call and this assertion.
func TestChatService_SystemPromptDatePlaceholder_Expanded(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "Today is %c."}}
	completer := &fakeChatCompleter{answer: "done, no need to search"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "Also today is %c."},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) < 2 {
		t.Fatalf("expected at least 2 leading messages, got %v", completer.calledWith)
	}
	now := strftime(time.Now().UTC(), "%a %b %e %H:%M")
	if strings.Contains(completer.calledWith[0].Content, "%c") || !strings.Contains(completer.calledWith[0].Content, now) {
		t.Errorf("expected the global prompt's %%c expanded to contain %q, got %q", now, completer.calledWith[0].Content)
	}
	if strings.Contains(completer.calledWith[1].Content, "%c") || !strings.Contains(completer.calledWith[1].Content, now) {
		t.Errorf("expected the hook prompt's %%c expanded to contain %q, got %q", now, completer.calledWith[1].Content)
	}
}

// TestStrftime_C proves %c matches real strftime(3)'s own ctime-style
// composite ("%a %b %e %H:%M:%S %Y"), not a bespoke Go-layout-derived
// format -- September 21, 2026 is a Monday.
func TestStrftime_C(t *testing.T) {
	tm := time.Date(2026, time.September, 21, 4, 22, 25, 0, time.UTC)
	if got, want := strftime(tm, "%c"), "Mon Sep 21 04:22:25 2026"; got != want {
		t.Errorf("strftime(%%c) = %q, want %q", got, want)
	}
}

// TestStrftime_IndividualDirectives spot-checks every directive strftime
// supports against a fixed, known instant.
func TestStrftime_IndividualDirectives(t *testing.T) {
	tm := time.Date(2026, time.September, 21, 4, 5, 6, 0, time.UTC) // a Monday
	cases := map[string]string{
		"%Y": "2026", "%y": "26", "%m": "09", "%d": "21", "%e": "21",
		"%H": "04", "%I": "04", "%M": "05", "%S": "06",
		"%A": "Monday", "%a": "Mon", "%B": "September", "%b": "September"[:3], "%h": "September"[:3],
		"%p": "AM", "%j": "264", "%Z": "UTC", "%%": "%",
	}
	for format, want := range cases {
		if got := strftime(tm, format); got != want {
			t.Errorf("strftime(%q) = %q, want %q", format, got, want)
		}
	}
}

// TestStrftime_SingleDigitDayPadsWithSpaceNotZero proves %e (unlike %d)
// space-pads a single-digit day, matching POSIX strftime exactly.
func TestStrftime_SingleDigitDayPadsWithSpaceNotZero(t *testing.T) {
	tm := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	if got, want := strftime(tm, "%e"), " 1"; got != want {
		t.Errorf("strftime(%%e) = %q, want %q", got, want)
	}
	if got, want := strftime(tm, "%d"), "01"; got != want {
		t.Errorf("strftime(%%d) = %q, want %q", got, want)
	}
}

// TestStrftime_NoonAndMidnightAreTwelveHour proves %I (12-hour clock)
// renders both midnight and noon as 12, not 0, matching POSIX strftime.
func TestStrftime_NoonAndMidnightAreTwelveHour(t *testing.T) {
	midnight := time.Date(2026, time.September, 21, 0, 30, 0, 0, time.UTC)
	noon := time.Date(2026, time.September, 21, 12, 30, 0, 0, time.UTC)
	if got := strftime(midnight, "%I %p"); got != "12 AM" {
		t.Errorf("strftime(midnight) = %q, want %q", got, "12 AM")
	}
	if got := strftime(noon, "%I %p"); got != "12 PM" {
		t.Errorf("strftime(noon) = %q, want %q", got, "12 PM")
	}
}

// TestStrftime_UnrecognizedDirectiveLeftVerbatim proves an unsupported
// %<letter> is kept as-is (e.g. "%q") rather than silently dropped, and a
// trailing lone "%" at the end of the format string is kept too.
func TestStrftime_UnrecognizedDirectiveLeftVerbatim(t *testing.T) {
	tm := time.Date(2026, time.September, 21, 4, 5, 6, 0, time.UTC)
	if got, want := strftime(tm, "50%q done"), "50%q done"; got != want {
		t.Errorf("strftime with an unknown directive = %q, want %q", got, want)
	}
	if got, want := strftime(tm, "100%"), "100%"; got != want {
		t.Errorf("strftime with a trailing bare %% = %q, want %q", got, want)
	}
}

// TestStrftime_LiteralTextAndEscapesPassThrough proves plain text, %%, %n,
// and %t all behave as POSIX strftime specifies.
func TestStrftime_LiteralTextAndEscapesPassThrough(t *testing.T) {
	tm := time.Date(2026, time.September, 21, 4, 5, 6, 0, time.UTC)
	if got, want := strftime(tm, "100%% done"), "100% done"; got != want {
		t.Errorf("strftime(%%%%) = %q, want %q", got, want)
	}
	if got, want := strftime(tm, "a%nb%tc"), "a\nb\tc"; got != want {
		t.Errorf("strftime(%%n/%%t) = %q, want %q", got, want)
	}
}

// TestChatService_TokenUsage_AttributesEachPieceCorrectly proves
// ChatResult.TokenUsage counts each leading-message category against the
// right field (not lumped into one total) and that HistoryTokens reflects
// what was actually sent post-trim, not the client's full untrimmed
// history.
func TestChatService_TokenUsage_AttributesEachPieceCorrectly(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.", MaxContextTokens: 10000,
	}}
	completer := &fakeChatCompleter{answer: "plain answer"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: "Use the web_search tool when helpful."},
	}}
	svc := NewChatService(endpoints, completer, hooks, &fakeHookScriptRunner{})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what is a?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u := result.TokenUsage
	if u.GlobalPromptTokens != estimateTokens([]domain.ChatMessage{{Role: domain.ChatRoleSystem, Content: "You are a pirate."}}) {
		t.Errorf("expected GlobalPromptTokens to match the system prompt's own estimate, got %+v", u)
	}
	if u.HookPromptTokens <= 0 {
		t.Errorf("expected a nonzero HookPromptTokens for the one active hook's prompt, got %+v", u)
	}
	if u.HistoryTokens != estimateTokens(history) {
		t.Errorf("expected HistoryTokens to match the (untrimmed, since nothing exceeded budget) history estimate, got %+v", u)
	}
	if u.MaxContextTokens != 10000 {
		t.Errorf("expected MaxContextTokens echoed from the endpoint config, got %+v", u)
	}
}

// TestChatService_TokenUsage_ZeroWhenNothingConfigured proves every field is
// simply zero when there's no system prompt, no active hooks, and no
// search context -- only the history itself contributes.
func TestChatService_TokenUsage_ZeroWhenNothingConfigured(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u := result.TokenUsage
	if u.GlobalPromptTokens != 0 || u.HookPromptTokens != 0 || u.MaxContextTokens != 0 {
		t.Errorf("expected every configured-piece field to be zero, got %+v", u)
	}
	if u.HistoryTokens != estimateTokens(history) {
		t.Errorf("expected HistoryTokens to still reflect the conversation itself, got %+v", u)
	}
}

// TestChatService_ActiveHookWithEmptyPrompt_NoExtraSystemMessage proves an
// empty Prompt contributes no extra system message even when its hook is
// active (Enabled, ungated, offered as a tool) -- it still runs normally.
func TestChatService_ActiveHookWithEmptyPrompt_NoExtraSystemMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true, Prompt: ""},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The tool call triggers a follow-up completion call, so
	// completer.calledWith (the LAST call) is the follow-up's messages --
	// check the FIRST call's messages instead for the leading-system-message
	// assertion this test cares about.
	firstCall := completer.allCalls[0]
	if len(firstCall) != 1 || !msgEqual(firstCall[0], history[0]) {
		t.Fatalf("expected no extra system message for an empty-Prompt hook, got %v", firstCall)
	}
	if len(result.HookResults) != 1 {
		t.Fatalf("expected the empty-Prompt hook to still run normally, got %v", result.HookResults)
	}
}

// TestChatService_HookEnv_CarriesEndpointWebSearchBaseURL proves the env
// map passed into a hook execution contains WEB_SEARCH_BASE_URL matching
// endpoint.WebSearchBaseURL.
func TestChatService_HookEnv_CarriesEndpointWebSearchBaseURL(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchBaseURL: "http://searxng.example:8888"}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Description: "Search the web.", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 script call, got %d", len(runner.calls))
	}
	if got := runner.calls[0].env["WEB_SEARCH_BASE_URL"]; got != "http://searxng.example:8888" {
		t.Fatalf("expected env[WEB_SEARCH_BASE_URL] = %q, got %q (env=%v)", "http://searxng.example:8888", got, runner.calls[0].env)
	}
}

// TestTrimToBudget_ThreeLeadingSystemMessages_KeepsAllIntact extends the
// two-message case above to three -- endpoint prompt + 2 hook prompts --
// proving trimToBudget's generic "walk every leading system-role message"
// logic keeps all of them, not just the first two.
func TestTrimToBudget_ThreeLeadingSystemMessages_KeepsAllIntact(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("p", 15)}, // endpoint prompt, ~5 tokens
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("h", 15)}, // hook 1 prompt, ~5 tokens
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("i", 15)}, // hook 2 prompt, ~5 tokens
		{Role: domain.ChatRoleUser, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleUser, Content: "newest question"},
	}
	// Budget fits all three system messages (~15 tokens) plus the newest
	// message (~5 tokens) but nothing else.
	got := trimToBudget(messages, 20)

	if len(got) != 4 {
		t.Fatalf("expected all three system messages plus the newest message kept, got %d messages: %v", len(got), got)
	}
	if !msgEqual(got[0], messages[0]) || !msgEqual(got[1], messages[1]) || !msgEqual(got[2], messages[2]) {
		t.Fatalf("expected all three leading system messages preserved intact, got %v", got[:3])
	}
	if got[3].Content != "newest question" {
		t.Fatalf("expected the newest message kept, got %v", got[3])
	}
}
