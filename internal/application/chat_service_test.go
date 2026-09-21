package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

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

// fakeChatCompleter is a minimal ports.ChatCompleter fake recording the
// messages it was called with, so a test can assert whether/what search
// context got prepended. calledWith is the LAST call's messages (every
// pre-existing test only ever expects one call); allCalls records every
// call in order, for tests exercising ChatService.Chat's hook follow-up
// round, which calls Complete a second time. When answers is non-empty,
// each successive call returns the next entry (clamped to the last once
// exhausted) instead of the single fixed answer -- letting a test give a
// different answer to the follow-up call than the first.
type fakeChatCompleter struct {
	answer      string
	answers     []string
	err         error
	calledWith  []domain.ChatMessage
	allCalls    [][]domain.ChatMessage
	calledEndpt domain.ChatEndpoint
	wasCalled   bool
	callCount   int
}

func (f *fakeChatCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage) (string, error) {
	f.wasCalled = true
	f.calledEndpt = endpoint
	f.calledWith = messages
	f.allCalls = append(f.allCalls, messages)
	idx := f.callCount
	f.callCount++
	if f.err != nil {
		return "", f.err
	}
	if len(f.answers) > 0 {
		if idx >= len(f.answers) {
			idx = len(f.answers) - 1
		}
		return f.answers[idx], nil
	}
	return f.answer, nil
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
	if len(completer.calledWith) != 2 || completer.calledWith[0] != history[1] || completer.calledWith[1] != history[2] {
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
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: strings.Repeat("s", 60)},
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
	if completer.calledWith[1] != history[0] {
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
	if len(completer.calledWith) != 1 || completer.calledWith[0] != history[0] {
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
	if got[0] != messages[0] || got[1] != messages[1] {
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

// TestChatService_HooksConfigured_MatchingAnswerPopulatesHookResults proves
// ChatService.Chat wires hooks/hookRunner end to end: a hook whose pattern
// matches the completer's returned answer produces a populated
// ChatResult.HookResults.
func TestChatService_HooksConfigured_MatchingAnswerPopulatesHookResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answers: []string{"Sure, SEARCH[golang release notes] coming up", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
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
	if result.HookResults[0].HookName != "web_search" || result.HookResults[0].Input != "golang release notes" || result.HookResults[0].Output != "top result" {
		t.Fatalf("unexpected hook result: %+v", result.HookResults[0])
	}
}

// TestChatService_HookFires_FeedsResultsBackForFinalAnswer proves a hook
// match triggers a follow-up completion call (exactly one here, since the
// follow-up's own answer doesn't itself match a hook pattern -- see
// TestChatService_HookRetries_SecondToolCallAlsoProcessed for the
// multi-round case), whose messages are the original ones plus the
// tool-call assistant turn plus a system message carrying the hook's
// results, and that the RETURNED answer is the follow-up's own answer, not
// the bare tool-call text the model first emitted.
func TestChatService_HookFires_FeedsResultsBackForFinalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answers: []string{
		"SEARCH[golang release notes]",
		"Go 1.26 was just released with several performance improvements.",
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
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
		t.Fatalf("expected 3 messages in the follow-up call (history + tool-call turn + results), got %d: %+v", len(followUp), followUp)
	}
	if followUp[0] != history[0] {
		t.Fatalf("expected the original history preserved first, got %+v", followUp[0])
	}
	if followUp[1].Role != domain.ChatRoleAssistant || followUp[1].Content != "SEARCH[golang release notes]" {
		t.Fatalf("expected the model's own tool-call text re-sent as an assistant turn, got %+v", followUp[1])
	}
	if followUp[2].Role != domain.ChatRoleSystem || !strings.Contains(followUp[2].Content, `{"results":["go 1.26 release notes"]}`) {
		t.Fatalf("expected a system message carrying the hook's results, got %+v", followUp[2])
	}
}

// TestChatService_HookRetries_SecondToolCallAlsoProcessed is the regression
// test for a real, reported failure: a model whose first fetch/search comes
// back empty or blocked reasonably tries again in its own follow-up answer
// (e.g. a second <web_fetch> for a different URL) -- that second tool call
// used to be left completely unprocessed (the old "exactly one follow-up
// round" limit), leaving a dangling, unanswered tool-call tag as the whole
// turn's Answer instead of a real response. Both rounds' hook results
// should be accumulated into the final ChatResult.
func TestChatService_HookRetries_SecondToolCallAlsoProcessed(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answers: []string{
		"FETCH[https://example.com/blocked]",
		"That page looks blocked, let me try another one. FETCH[https://example.com/mirror]",
		"The mirror page says hello world.",
	}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Pattern: `FETCH\[(.+?)\]`, Script: "web_fetch.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_fetch.sh": "<empty/blocked page>"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "The mirror page says hello world." {
		t.Fatalf("expected the SECOND follow-up's real answer returned, not a dangling tool-call tag, got %q", result.Answer)
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
		t.Fatalf("expected each round's own capture group passed through, got %+v", runner.calls)
	}
}

// TestChatService_HookRetries_CappedAtMaxFollowUpRounds proves the retry
// loop is bounded: a model that keeps invoking a hook in every answer stops
// being fed back after maxHookFollowUpRounds rounds, rather than looping
// forever -- the last completion's own answer, once the cap is hit, is
// still a bare, still-unsatisfied tool-call tag, which stripHookCallTags
// then removes entirely (see its own tests), so the user sees an empty
// answer rather than a dangling tag.
func TestChatService_HookRetries_CappedAtMaxFollowUpRounds(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "FETCH[https://example.com/never-satisfied]"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Pattern: `FETCH\[(.+?)\]`, Script: "web_fetch.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_fetch.sh": "<empty/blocked page>"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(completer.allCalls) != maxHookFollowUpRounds+1 {
		t.Fatalf("expected exactly maxHookFollowUpRounds+1 (%d) completion calls, got %d", maxHookFollowUpRounds+1, len(completer.allCalls))
	}
	if len(result.HookResults) != maxHookFollowUpRounds {
		t.Fatalf("expected exactly maxHookFollowUpRounds (%d) hook results accumulated, got %d", maxHookFollowUpRounds, len(result.HookResults))
	}
	if result.Answer != "" {
		t.Fatalf("expected the still-unsatisfied tool-call tag stripped once the cap is hit, got %q", result.Answer)
	}
}

// TestChatService_HookFollowUpCompletionErrors_FallsBackToOriginalAnswer
// proves a failed follow-up completion is best-effort, same convention as
// every other augmentation source in ChatService.Chat: the turn still
// succeeds, falling back to the model's original answer -- but since that
// fallback answer is nothing but the bare tool-call tag itself, and
// stripHookCallTags strips exactly that, the user-visible result is empty
// rather than a dangling "<web_search>..." tag (see
// TestStripHookCallTags_LeavesSurroundingProseIntact for the case where
// the tag is only PART of the answer).
func TestChatService_HookFollowUpCompletionErrors_FallsBackToOriginalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &erroringOnSecondCallCompleter{firstAnswer: "SEARCH[golang release notes]"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "results"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected the follow-up completion error to be swallowed, got %v", err)
	}
	if result.Answer != "" {
		t.Fatalf("expected the bare tool-call tag stripped from the fallback answer, got %q", result.Answer)
	}
}

// TestStripHookCallTags_LeavesSurroundingProseIntact is the regression test
// for a real, reported failure: a follow-up answer that echoes a NEW tool
// call alongside real prose (e.g. "Let me try another source.
// <web_fetch>https://...</web_fetch>") must have only the tag itself
// removed, keeping the prose the user actually reads.
func TestStripHookCallTags_LeavesSurroundingProseIntact(t *testing.T) {
	hooks := []domain.ChatHook{
		{ID: "1", Name: "web_fetch", Pattern: `<web_fetch>([^<]+)</web_fetch>`, Enabled: true},
	}
	got := stripHookCallTags("Let me try another source.\n\n<web_fetch>https://example.com</web_fetch>", hooks)
	if got != "Let me try another source." {
		t.Errorf("expected only the tag stripped, prose kept, got %q", got)
	}
}

// TestStripHookCallTags_OnlyStripsActiveHookPatterns proves a hook not in
// the passed-in list (e.g. one gated off this turn) never has its pattern
// stripped from the answer -- only text matching an active hook's own
// Pattern is ever touched.
func TestStripHookCallTags_OnlyStripsActiveHookPatterns(t *testing.T) {
	hooks := []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `<web_search>([^<]+)</web_search>`, Enabled: true},
	}
	got := stripHookCallTags("See <web_fetch>https://example.com</web_fetch> for details.", hooks)
	if got != "See <web_fetch>https://example.com</web_fetch> for details." {
		t.Errorf("expected an inactive hook's pattern left untouched, got %q", got)
	}
}

// TestStripHookCallTags_InvalidPatternSkipped proves a hook whose Pattern
// fails to compile is skipped rather than panicking or failing the turn --
// same defensive convention runChatHooks itself uses.
func TestStripHookCallTags_InvalidPatternSkipped(t *testing.T) {
	hooks := []domain.ChatHook{{ID: "1", Name: "broken", Pattern: `(unclosed`, Enabled: true}}
	got := stripHookCallTags("plain answer", hooks)
	if got != "plain answer" {
		t.Errorf("expected the answer unchanged when the hook's pattern fails to compile, got %q", got)
	}
}

// erroringOnSecondCallCompleter is a ports.ChatCompleter fake whose first
// call succeeds and every later call fails -- fakeChatCompleter's own
// answers-by-index doesn't model a call FAILING partway through, so this is
// a small dedicated fake for that one scenario.
type erroringOnSecondCallCompleter struct {
	firstAnswer string
	calls       int
}

func (f *erroringOnSecondCallCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage) (string, error) {
	f.calls++
	if f.calls == 1 {
		return f.firstAnswer, nil
	}
	return "", errors.New("upstream unavailable")
}

// TestChatService_NoHookMatch_OnlyOneCompletionCall proves the follow-up
// round never fires when nothing matched -- the common case shouldn't cost
// a second completion call.
func TestChatService_NoHookMatch_OnlyOneCompletionCall(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "a plain answer with no tool call"}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
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
		t.Fatalf("expected exactly 1 completion call when no hook matched, got %d", len(completer.allCalls))
	}
}

// TestFormatHookResultsForModel_TruncatesLongOutput proves
// formatHookResultsForModel bounds each result's own Output, independent of
// hookrunner's own (much larger) 64KB cap -- several long results in one
// turn could otherwise still add up to a very large follow-up prompt.
func TestFormatHookResultsForModel_TruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", maxHookOutputCharsForModel+500)
	got := formatHookResultsForModel([]domain.ChatHookResult{{HookName: "web_search", Output: long}})
	if strings.Contains(got, long) {
		t.Error("expected the long output truncated, got it included verbatim")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected a truncation note, got %q", got)
	}
}

// TestFormatHookResultsForModel_IncludesErrorInsteadOfOutput proves a
// failed hook's Err reaches the model instead of a blank Output, so it can
// tell the user the tool call didn't work rather than guessing.
func TestFormatHookResultsForModel_IncludesErrorInsteadOfOutput(t *testing.T) {
	got := formatHookResultsForModel([]domain.ChatHookResult{{HookName: "web_search", Err: "script timed out"}})
	if !strings.Contains(got, "script timed out") {
		t.Errorf("expected the error text included, got %q", got)
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
// GatedByWebSearch hook contributes neither its Prompt nor an execution
// when the effective web-search toggle is off -- even though its pattern
// matches the answer and Enabled is true.
func TestChatService_GatedHook_InactiveWhenWebSearchOff(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
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
	if len(result.HookResults) != 0 || len(runner.calls) != 0 {
		t.Fatalf("expected the gated hook not to run when web search is off, got results=%v calls=%v", result.HookResults, runner.calls)
	}
}

// TestChatService_GatedHook_ActiveWhenWebSearchOn proves the same hook as
// above IS active -- Prompt injected and it runs normally -- once the
// effective web-search toggle (endpoint default here) is on.
func TestChatService_GatedHook_ActiveWhenWebSearchOn(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true}}
	completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, m := range completer.calledWith {
		if m.Role == domain.ChatRoleSystem && m.Content == "You can search the web." {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the gated hook's Prompt injected as its own system message when web search is on, got %v", completer.calledWith)
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
	completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
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
		completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
		hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
			{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: false},
		}}
		runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
		svc := NewChatService(endpoints, completer, hooks, runner)

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		result, err := svc.Chat(context.Background(), history, ChatOptions{})
		if err != nil {
			t.Fatalf("unexpected error (webSearchEnabled=%v): %v", webSearchEnabled, err)
		}
		found := false
		for _, m := range completer.calledWith {
			if m.Role == domain.ChatRoleSystem && m.Content == "You can search the web." {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected the ungated hook's Prompt injected regardless of webSearchEnabled=%v, got %v", webSearchEnabled, completer.calledWith)
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
		{ID: "1", Name: "first", Pattern: `A\((.+?)\)`, Script: "a.sh", Enabled: true, Prompt: "First hook prompt."},
		{ID: "2", Name: "second", Pattern: `B\((.+?)\)`, Script: "b.sh", Enabled: true, Prompt: "Second hook prompt."},
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
	if completer.calledWith[3] != history[0] {
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
		{ID: "1", Name: "first", Pattern: `A\((.+?)\)`, Script: "a.sh", Enabled: true, Prompt: "First hook prompt."},
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
	if completer.calledWith[3] != history[0] {
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
			{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "hook prompt", GatedByWebSearch: true},
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
	completer := &fakeChatCompleter{answers: []string{"SEARCH[news]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "Also today is %c."},
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
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: "Use SEARCH[term] to search."},
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
// active (Enabled, ungated, matching answer) -- it still runs normally.
func TestChatService_ActiveHookWithEmptyPrompt_NoExtraSystemMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true, Prompt: ""},
	}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "top result"}}
	svc := NewChatService(endpoints, completer, hooks, runner)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The hook matches and triggers a follow-up completion call, so
	// completer.calledWith (the LAST call) is the follow-up's messages --
	// check the FIRST call's messages instead for the leading-system-message
	// assertion this test cares about.
	firstCall := completer.allCalls[0]
	if len(firstCall) != 1 || firstCall[0] != history[0] {
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
	completer := &fakeChatCompleter{answers: []string{"SEARCH[golang release notes]", "done"}}
	hooks := &fakeChatHookStore{hooks: []domain.ChatHook{
		{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
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
	if got[0] != messages[0] || got[1] != messages[1] || got[2] != messages[2] {
		t.Fatalf("expected all three leading system messages preserved intact, got %v", got[:3])
	}
	if got[3].Content != "newest question" {
		t.Fatalf("expected the newest message kept, got %v", got[3])
	}
}
