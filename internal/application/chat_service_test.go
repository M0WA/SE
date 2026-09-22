package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

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

// fakeMCPServerStore is a minimal ports.MCPServerStore fake: a fixed list
// of servers plus an error to return instead of it (ListMCPServers only --
// Create/Update/Delete are never exercised by ChatService.Chat).
type fakeMCPServerStore struct {
	servers []domain.MCPServer
	err     error
}

func (f *fakeMCPServerStore) ListMCPServers(ctx context.Context) ([]domain.MCPServer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.servers, nil
}
func (f *fakeMCPServerStore) CreateMCPServer(ctx context.Context, s domain.MCPServer) error {
	return nil
}
func (f *fakeMCPServerStore) UpdateMCPServer(ctx context.Context, s domain.MCPServer) error {
	return nil
}
func (f *fakeMCPServerStore) DeleteMCPServer(ctx context.Context, id string) error { return nil }

// fakeUserMCPServerStore is a minimal ports.UserMCPServerStore fake -- same
// reasoning as fakeMCPServerStore's own doc comment above, keyed by userID.
type fakeUserMCPServerStore struct {
	byUser map[string][]domain.MCPServer
	err    error
}

func (f *fakeUserMCPServerStore) ListUserMCPServers(ctx context.Context, userID string) ([]domain.MCPServer, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byUser[userID], nil
}
func (f *fakeUserMCPServerStore) CreateUserMCPServer(ctx context.Context, userID string, s domain.MCPServer) error {
	return nil
}
func (f *fakeUserMCPServerStore) UpdateUserMCPServer(ctx context.Context, userID string, s domain.MCPServer) error {
	return nil
}
func (f *fakeUserMCPServerStore) DeleteUserMCPServer(ctx context.Context, userID string, id string) error {
	return nil
}

// fakeAgentStore is a minimal ports.AgentStore fake -- a small local copy,
// same reasoning as fakeMCPServerStore's own doc comment above.
type fakeAgentStore struct {
	agents []domain.Agent
	err    error
}

func (f *fakeAgentStore) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}
func (f *fakeAgentStore) CreateAgent(ctx context.Context, a domain.Agent) error { return nil }
func (f *fakeAgentStore) UpdateAgent(ctx context.Context, a domain.Agent) error { return nil }
func (f *fakeAgentStore) DeleteAgent(ctx context.Context, id string) error      { return nil }

// mcpCall records one CallTool invocation against a fakeMCPSession.
type mcpCall struct {
	name          string
	argumentsJSON string
}

// fakeMCPSession is a minimal ports.MCPSession fake: outputs/errs keyed by
// tool name, recording every call it receives (and whether Close was
// called) so a test can assert on both.
type fakeMCPSession struct {
	outputs map[string]string
	errs    map[string]string
	calls   []mcpCall
	closed  bool
}

func (s *fakeMCPSession) CallTool(ctx context.Context, name, argumentsJSON string) (string, error) {
	s.calls = append(s.calls, mcpCall{name: name, argumentsJSON: argumentsJSON})
	if s.errs != nil {
		if e, ok := s.errs[name]; ok {
			return "", errors.New(e)
		}
	}
	return s.outputs[name], nil
}

func (s *fakeMCPSession) Close() { s.closed = true }

// fakeMCPToolProvider is a minimal ports.MCPToolProvider fake: Open always
// returns the fixed tools list and session configured on it (creating a
// zero-value session lazily if none was set), recording the servers/env it
// was last called with and how many times it was called -- so a test can
// assert Open was never invoked at all when every server was gated
// inactive (see ChatService.Chat, which only calls Open when
// len(activeServers) > 0).
type fakeMCPToolProvider struct {
	tools         []domain.MCPTool
	session       *fakeMCPSession
	openedServers []domain.MCPServer
	openedEnv     map[string]string
	openCount     int
}

func (p *fakeMCPToolProvider) Open(ctx context.Context, servers []domain.MCPServer, env map[string]string) (ports.MCPSession, []domain.MCPTool) {
	p.openCount++
	p.openedServers = servers
	p.openedEnv = env
	if p.session == nil {
		p.session = &fakeMCPSession{}
	}
	return p.session, p.tools
}

// mcpTool builds a domain.MCPTool with a bare, valid InputSchema -- the
// common case for a test that doesn't care about the schema's own shape.
func mcpTool(name, description string) domain.MCPTool {
	return domain.MCPTool{Name: name, Description: description, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
}

// argsJSON builds a single-key JSON object as a raw string -- the shape a
// real model emits as a tool call's Arguments.
func argsJSON(key, val string) string {
	b, _ := json.Marshal(map[string]string{key: val})
	return string(b)
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
	svc := NewChatService(&fakeChatEndpointStore{}, &fakeChatCompleter{}, nil, nil, nil, nil)
	_, err := svc.Chat(context.Background(), nil, ChatOptions{})
	if err == nil {
		t.Fatal("expected error for empty history, got nil")
	}
}

func TestChatService_NotConfigured(t *testing.T) {
	wantErr := errors.New("boom")
	endpoints := &fakeChatEndpointStore{getErr: wantErr}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped/equal sentinel error, got %v", err)
	}
}

func TestChatService_NotConfigured_ErrIsPreserved(t *testing.T) {
	endpoints := &fakeChatEndpointStore{getErr: ports.ErrChatEndpointNotConfigured}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected errors.Is to match ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_DisabledEndpoint(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: false}}
	svc := NewChatService(endpoints, &fakeChatCompleter{}, nil, nil, nil, nil)

	_, err := svc.Chat(context.Background(), []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}, ChatOptions{})
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Fatalf("expected ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestChatService_MaxContextTokens_Zero_NoTrimming(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 0}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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

func TestChatService_MaxContextTokens_KeepsToolPromptSystemMessageIntact(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, MaxContextTokens: 50}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "web_search.sh", Enabled: true, Prompt: strings.Repeat("s", 60)},
	}}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

	history := []domain.ChatMessage{
		{Role: domain.ChatRoleUser, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleAssistant, Content: strings.Repeat("old", 30)},
		{Role: domain.ChatRoleUser, Content: "newest question"},
	}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) < 2 {
		t.Fatalf("expected at least the tool prompt system message plus the newest message, got %v", completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem {
		t.Fatalf("expected the tool prompt system message to survive trimming as the first message, got role %q", completer.calledWith[0].Role)
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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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

func TestChatService_MCPNil_ToolResultsEmpty(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolResults) != 0 {
		t.Fatalf("expected no tool results when mcpServers/mcpTools are nil, got %v", result.ToolResults)
	}
}

// TestChatService_MCPServersConfigured_ToolCallPopulatesToolResults proves
// ChatService.Chat wires mcpServers/mcpTools end to end: a tool call the
// completer returns, naming a discovered tool, produces a populated
// ChatResult.ToolResults.
func TestChatService_MCPServersConfigured_ToolCallPopulatesToolResults(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %v", result.ToolResults)
	}
	r := result.ToolResults[0]
	if r.ToolName != "web_search" || r.ToolCallID != "call_1" || r.Arguments != argsJSON("query", "golang release notes") || r.Output != "top result" {
		t.Fatalf("unexpected tool result: %+v", r)
	}
	// The tools list offered on every call should describe this tool.
	if len(completer.allTools[0]) != 1 || completer.allTools[0][0].Name != "web_search" {
		t.Fatalf("expected the discovered tool offered as a tool, got %v", completer.allTools[0])
	}
	if !provider.session.closed {
		t.Error("expected the MCP session closed at the end of the turn")
	}
}

// TestChatService_ToolFires_FeedsResultsBackForFinalAnswer proves a tool
// call triggers a follow-up completion call (exactly one here, since the
// follow-up's own answer doesn't itself request a tool call -- see
// TestChatService_ToolRetries_SecondToolCallAlsoProcessed for the
// multi-round case), whose messages are the original ones plus the
// tool-call assistant turn plus one domain.ChatRoleTool message correlated
// by ToolCallID, and that the RETURNED answer is the follow-up's own
// content, not the tool-call request itself.
func TestChatService_ToolFires_FeedsResultsBackForFinalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	firstMsg := toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes"))
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		firstMsg,
		plainMessage("Go 1.26 was just released with several performance improvements."),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": `{"results":["go 1.26 release notes"]}`}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what's new in the latest go release?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "Go 1.26 was just released with several performance improvements." {
		t.Fatalf("expected the follow-up completion's own answer returned, got %q", result.Answer)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Output != `{"results":["go 1.26 release notes"]}` {
		t.Fatalf("expected the tool result still surfaced for the UI, got %+v", result.ToolResults)
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
		t.Fatalf("expected a tool-role message carrying the tool's result, correlated by ToolCallID, got %+v", followUp[2])
	}
}

// TestChatService_ToolRetries_SecondToolCallAlsoProcessed is the regression
// test for a real, reported failure: a model whose first fetch/search comes
// back empty or blocked reasonably tries again with a SECOND tool call --
// that second tool call used to be left completely unprocessed (the old
// "exactly one follow-up round" limit), leaving a dangling, unanswered
// tool call as the whole turn's Answer instead of a real response. Both
// rounds' tool results should be accumulated into the final ChatResult.
func TestChatService_ToolRetries_SecondToolCallAlsoProcessed(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_fetch", argsJSON("url", "https://example.com/blocked")),
		toolCallMessage("call_2", "web_fetch", argsJSON("url", "https://example.com/mirror")),
		plainMessage("The mirror page says hello world."),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_fetch", "Fetch a URL.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_fetch": "<empty/blocked page>"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "The mirror page says hello world." {
		t.Fatalf("expected the SECOND follow-up's real answer returned, not a dangling tool call, got %q", result.Answer)
	}
	if len(result.ToolResults) != 2 {
		t.Fatalf("expected both rounds' tool results accumulated, got %+v", result.ToolResults)
	}
	if len(completer.allCalls) != 3 {
		t.Fatalf("expected 3 completion calls (initial + 2 follow-ups), got %d", len(completer.allCalls))
	}
	if len(provider.session.calls) != 2 {
		t.Fatalf("expected the tool called once per round (2 total), got %d", len(provider.session.calls))
	}
	if !strings.Contains(provider.session.calls[0].argumentsJSON, "https://example.com/blocked") ||
		!strings.Contains(provider.session.calls[1].argumentsJSON, "https://example.com/mirror") {
		t.Fatalf("expected each round's own argument passed through, got %+v", provider.session.calls)
	}
}

// TestChatService_ToolRetries_CappedAtMaxFollowUpRounds proves the retry
// loop is bounded: a model that keeps invoking a tool in every response
// stops being fed back after maxHookFollowUpRounds rounds, rather than
// looping forever. The completer fake here always returns the same
// unsatisfied tool call -- including on the force-final-answer fallback
// call below (see TestChatService_ForceFinalAnswer_* for the case where
// that call actually produces real prose) -- so the end result is still an
// empty answer, just reached via one extra completion call than the loop
// alone accounts for.
func TestChatService_ToolRetries_CappedAtMaxFollowUpRounds(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{response: toolCallMessage("call", "web_fetch", argsJSON("url", "https://example.com/never-satisfied"))}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_fetch", "Fetch a URL.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_fetch": "<empty/blocked page>"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
	if len(result.ToolResults) != maxHookFollowUpRounds {
		t.Fatalf("expected exactly maxHookFollowUpRounds (%d) tool results accumulated, got %d", maxHookFollowUpRounds, len(result.ToolResults))
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
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "some results"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "some results"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_fetch", "Fetch a URL.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_fetch": "<empty/blocked page>"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected the force-final completion error to be swallowed, got %v", err)
	}
	if result.Answer != "" {
		t.Fatalf("expected an empty answer when the force-final call itself errors, got %q", result.Answer)
	}
}

// TestChatService_ToolFollowUpCompletionErrors_FallsBackToOriginalAnswer
// proves a failed follow-up completion is best-effort, same convention as
// every other augmentation source in ChatService.Chat: the turn still
// succeeds, falling back to the empty Content of the tool-call message that
// triggered the (now-failed) follow-up round.
func TestChatService_ToolFollowUpCompletionErrors_FallsBackToOriginalAnswer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &erroringOnSecondCallCompleter{first: toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes"))}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "results"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
// satisfies a tool call and keeps retrying) and every call after that fails
// -- used to prove the force-final-answer fallback (see ChatService.Chat)
// is itself best-effort: a failure there must not fail the whole turn.
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

// TestChatService_NoToolCall_OnlyOneCompletionCall proves the follow-up
// round never fires when the model didn't request a tool call -- the
// common case shouldn't cost a second completion call.
func TestChatService_NoToolCall_OnlyOneCompletionCall(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "a plain answer with no tool call"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{tools: []domain.MCPTool{mcpTool("web_search", "Search the web.")}}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
// bounds each result's own Output, independent of any single MCP tool's own
// (possibly much larger) output size -- several long results in one turn
// could otherwise still add up to a very large follow-up prompt.
func TestToolResultMessages_TruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", maxHookOutputCharsForModel+500)
	got := toolResultMessages([]domain.ToolCallResult{{ToolName: "web_search", ToolCallID: "call_1", Output: long}})
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
// tool call's Err reaches the model instead of a blank Output, so it can
// tell the user the tool call didn't work rather than guessing.
func TestToolResultMessages_IncludesErrorInsteadOfOutput(t *testing.T) {
	got := toolResultMessages([]domain.ToolCallResult{{ToolName: "web_search", ToolCallID: "call_1", Err: "tool timed out"}})
	if len(got) != 1 || !strings.Contains(got[0].Content, "tool timed out") {
		t.Errorf("expected the error text included, got %v", got)
	}
}

// TestToolResultMessages_CorrelatesByToolCallID proves each result's
// message carries the matching ToolCallID and domain.ChatRoleTool role --
// the invariant native tool-calling requires between an assistant's
// tool_calls and their answering messages.
func TestToolResultMessages_CorrelatesByToolCallID(t *testing.T) {
	results := []domain.ToolCallResult{
		{ToolName: "a", ToolCallID: "call_1", Output: "out-a"},
		{ToolName: "b", ToolCallID: "call_2", Output: "out-b"},
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

// TestRunToolCalls_NilSession_FailsEveryCall proves runToolCalls degrades
// gracefully when session is nil (no MCP servers active this turn but the
// model still returned a tool call, e.g. against a stale/cached tools list)
// -- every call gets its own Err instead of panicking on a nil dereference,
// preserving the one-result-per-input-call invariant toolResultMessages
// depends on.
func TestRunToolCalls_NilSession_FailsEveryCall(t *testing.T) {
	calls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: argsJSON("query", "x")}}
	got := runToolCalls(context.Background(), nil, calls)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Err == "" {
		t.Fatalf("expected a non-empty Err for a nil session, got %+v", got[0])
	}
	if got[0].ToolName != "web_search" || got[0].ToolCallID != "call_1" {
		t.Fatalf("expected ToolName/ToolCallID still populated from the input call, got %+v", got[0])
	}
}

// TestChatService_ToolCallErrors_SurfacedAsResultErr proves a CallTool
// error (a real MCP server returning a failure, not just the higher-level
// "unknown tool" case runToolCalls' nil-session branch covers) surfaces on
// the corresponding ToolCallResult.Err rather than failing the turn.
func TestChatService_ToolCallErrors_SurfacedAsResultErr(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_fetch", argsJSON("url", "https://example.com/unreachable")),
		plainMessage("could not fetch that page"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_fetch", "Fetch a URL.")},
		session: &fakeMCPSession{errs: map[string]string{"web_fetch": "connection refused"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what does example.com say?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Err != "connection refused" {
		t.Fatalf("expected the CallTool error surfaced on the result, got %+v", result.ToolResults)
	}
	if result.ToolResults[0].Output != "" {
		t.Fatalf("expected empty Output on a failed call, got %q", result.ToolResults[0].Output)
	}
}

// TestToolDefsFrom_BuildsOneEntryPerTool proves the tools list offered to
// the model mirrors each discovered tool's Name/Description/InputSchema.
func TestToolDefsFrom_BuildsOneEntryPerTool(t *testing.T) {
	tools := []domain.MCPTool{
		mcpTool("web_search", "Search the web."),
		mcpTool("web_fetch", "Fetch a URL."),
	}
	defs := toolDefsFrom(tools)
	if len(defs) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(defs))
	}
	if defs[0].Name != "web_search" || defs[0].Description != "Search the web." {
		t.Errorf("unexpected first tool: %+v", defs[0])
	}
	if defs[1].Name != "web_fetch" || defs[1].Description != "Fetch a URL." {
		t.Errorf("unexpected second tool: %+v", defs[1])
	}
}

// TestToolDefsFrom_NoTools_ReturnsNil proves an empty tools list returns
// nil, not an empty slice -- so httpchat's own toWireTools correctly omits
// the request's "tools" field entirely.
func TestToolDefsFrom_NoTools_ReturnsNil(t *testing.T) {
	if got := toolDefsFrom(nil); got != nil {
		t.Fatalf("expected nil for no tools, got %v", got)
	}
}

// TestToolDefsFrom_EmptyInputSchema_DefaultsToBareObjectSchema proves a
// tool with no InputSchema set still produces a valid (if empty)
// JSON-schema object, never an empty/invalid Parameters value in the
// request sent to the model.
func TestToolDefsFrom_EmptyInputSchema_DefaultsToBareObjectSchema(t *testing.T) {
	tools := toolDefsFrom([]domain.MCPTool{{Name: "bare"}})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if string(tools[0].Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("expected a default bare object schema, got %q", tools[0].Parameters)
	}
}

// TestChatService_MCPServersListError_Swallowed proves a ListMCPServers
// error is best-effort, same convention as the web-search error elsewhere
// in this file: it never fails the whole chat turn.
func TestChatService_MCPServersListError_Swallowed(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{err: errors.New("db down")}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("expected mcp server list error to be swallowed, got %v", err)
	}
	if len(result.ToolResults) != 0 {
		t.Fatalf("expected no tool results when ListMCPServers errors, got %v", result.ToolResults)
	}
}

// TestChatService_GatedServer_InactiveWhenWebSearchOff proves a
// GatedByWebSearch server contributes neither its Prompt nor any tool offer
// when the effective web-search toggle is off -- even though Enabled is
// true -- and that Open is never even called (no wasted connection) since
// ChatService.Chat filters gating before calling it.
func TestChatService_GatedServer_InactiveWhenWebSearchOff(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{answer: "no need to search"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, m := range completer.calledWith {
		if m.Role == domain.ChatRoleSystem && strings.Contains(m.Content, "You can search the web.") {
			t.Fatalf("expected the gated server's Prompt not injected when web search is off, got %v", completer.calledWith)
		}
	}
	if len(completer.calledTools) != 0 {
		t.Fatalf("expected the gated server's tool not offered when web search is off, got %v", completer.calledTools)
	}
	if len(result.ToolResults) != 0 {
		t.Fatalf("expected no tool results when web search is off, got %v", result.ToolResults)
	}
	if provider.openCount != 0 {
		t.Fatalf("expected Open never called when every server is gated inactive, got %d calls", provider.openCount)
	}
}

// TestChatService_GatedServer_ActiveWhenWebSearchOn proves the same server
// as above IS active -- Prompt injected, its tool offered, and it runs
// normally -- once the effective web-search toggle (endpoint default here)
// is on.
func TestChatService_GatedServer_ActiveWhenWebSearchOn(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
		t.Fatalf("expected the gated server's Prompt injected as its own system message when web search is on, got %v", completer.allCalls[0])
	}
	if len(completer.allTools[0]) != 1 || completer.allTools[0][0].Name != "web_search" {
		t.Fatalf("expected the gated server's tool offered when web search is on, got %v", completer.allTools[0])
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Output != "top result" {
		t.Fatalf("expected the gated server's tool to run when web search is on, got %v", result.ToolResults)
	}
	if len(provider.session.calls) != 1 {
		t.Fatalf("expected exactly 1 tool call, got %d", len(provider.session.calls))
	}
}

// TestChatService_GatedServer_PerQuestionOverrideActivates proves the
// per-question ChatOptions.WebSearch override (not just the endpoint
// default) is what actually governs gating.
func TestChatService_GatedServer_PerQuestionOverrideActivates(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	on := true
	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{WebSearch: &on})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolResults) != 1 {
		t.Fatalf("expected the gated server active via the per-question override, got %v", result.ToolResults)
	}
}

// TestChatService_UngatedServer_ActiveRegardlessOfWebSearch proves a
// GatedByWebSearch=false server's Prompt is injected and its tool runs
// regardless of the web-search toggle's value -- today's existing
// behavior, unaffected by adding gating for other servers.
func TestChatService_UngatedServer_ActiveRegardlessOfWebSearch(t *testing.T) {
	for _, webSearchEnabled := range []bool{false, true} {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: webSearchEnabled}}
		completer := &fakeChatCompleter{responses: []domain.ChatMessage{
			toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
			plainMessage("done"),
		}}
		servers := &fakeMCPServerStore{servers: []domain.MCPServer{
			{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "You can search the web.", GatedByWebSearch: false},
		}}
		provider := &fakeMCPToolProvider{
			tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
			session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
		}
		svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
			t.Fatalf("expected the ungated server's Prompt injected regardless of webSearchEnabled=%v, got %v", webSearchEnabled, completer.allCalls[0])
		}
		if len(result.ToolResults) != 1 {
			t.Fatalf("expected the ungated server's tool to run regardless of webSearchEnabled=%v, got %v", webSearchEnabled, result.ToolResults)
		}
	}
}

// TestChatService_TwoActiveServersWithPrompts_TwoSeparateLeadingSystemMessages
// proves two active servers, each with a non-empty Prompt, produce two
// separate leading system messages (not concatenated into one), in list
// order, after endpoint.SystemPrompt and before the conversation history.
func TestChatService_TwoActiveServersWithPrompts_TwoSeparateLeadingSystemMessages(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.",
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "first", Transport: "stdio", Command: "a", Enabled: true, Prompt: "First server prompt."},
		{ID: "2", Name: "second", Transport: "stdio", Command: "b", Enabled: true, Prompt: "Second server prompt."},
	}}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 4 {
		t.Fatalf("expected endpoint prompt + 2 server prompts + 1 history message, got %d: %v", len(completer.calledWith), completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem || completer.calledWith[0].Content != "You are a pirate." {
		t.Fatalf("expected the endpoint's own SystemPrompt first, got %+v", completer.calledWith[0])
	}
	if completer.calledWith[1].Role != domain.ChatRoleSystem || completer.calledWith[1].Content != "First server prompt." {
		t.Fatalf("expected the first server's own separate system message second, got %+v", completer.calledWith[1])
	}
	if completer.calledWith[2].Role != domain.ChatRoleSystem || completer.calledWith[2].Content != "Second server prompt." {
		t.Fatalf("expected the second server's own separate system message third, got %+v", completer.calledWith[2])
	}
	if !msgEqual(completer.calledWith[3], history[0]) {
		t.Fatalf("expected original history preserved last, got %v", completer.calledWith[3])
	}
}

// TestChatService_UserCustomPrompt_InjectedBetweenGlobalPromptAndToolPrompts
// proves opts.UserCustomPrompt is injected as its own leading system
// message, positioned after the endpoint's own SystemPrompt and before any
// active server's own Prompt -- order: global endpoint prompt -> personal
// user prompt -> server prompts.
func TestChatService_UserCustomPrompt_InjectedBetweenGlobalPromptAndToolPrompts(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.",
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "first", Transport: "stdio", Command: "a", Enabled: true, Prompt: "First server prompt."},
	}}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{UserCustomPrompt: "Always answer in haiku."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 4 {
		t.Fatalf("expected endpoint prompt + user prompt + server prompt + 1 history message, got %d: %v", len(completer.calledWith), completer.calledWith)
	}
	if completer.calledWith[0].Role != domain.ChatRoleSystem || completer.calledWith[0].Content != "You are a pirate." {
		t.Fatalf("expected the endpoint's own SystemPrompt first, got %+v", completer.calledWith[0])
	}
	if completer.calledWith[1].Role != domain.ChatRoleSystem || completer.calledWith[1].Content != "Always answer in haiku." {
		t.Fatalf("expected the user's own custom prompt second (after global, before servers), got %+v", completer.calledWith[1])
	}
	if completer.calledWith[2].Role != domain.ChatRoleSystem || completer.calledWith[2].Content != "First server prompt." {
		t.Fatalf("expected the server's own prompt third, got %+v", completer.calledWith[2])
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
// convention as SystemPrompt/server prompts.
func TestChatService_EmptyUserCustomPrompt_NoLeadingPromptMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate."}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

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

// TestChatService_Agent_InjectsSystemPromptBetweenUserPromptAndServerPrompts
// proves the active agent's own SystemPrompt (resolved from
// endpoint.DefaultAgentID) is injected as its own leading system message,
// positioned after the user's own custom prompt and before any MCP server
// prompt, and that TokenUsage.AgentPromptTokens reflects it.
func TestChatService_Agent_InjectsSystemPromptBetweenUserPromptAndServerPrompts(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{
		Enabled: true, SystemPrompt: "You are a pirate.", DefaultAgentID: "researcher",
	}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "first", Transport: "stdio", Command: "a", Enabled: true, Prompt: "First server prompt."},
	}}
	agents := &fakeAgentStore{agents: []domain.Agent{
		{ID: "researcher", Name: "Researcher", SystemPrompt: "Dig up sources.", MCPServerIDs: []string{"1"}, Enabled: true},
	}}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, agents, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{UserCustomPrompt: "Always answer in haiku."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 5 {
		t.Fatalf("expected endpoint + user + agent + server prompts + 1 history message, got %d: %v", len(completer.calledWith), completer.calledWith)
	}
	if completer.calledWith[1].Content != "Always answer in haiku." {
		t.Fatalf("expected the user's own custom prompt second, got %+v", completer.calledWith[1])
	}
	if completer.calledWith[2].Role != domain.ChatRoleSystem || completer.calledWith[2].Content != "Dig up sources." {
		t.Fatalf("expected the agent's own SystemPrompt third (after user prompt, before server prompts), got %+v", completer.calledWith[2])
	}
	if completer.calledWith[3].Content != "First server prompt." {
		t.Fatalf("expected the server's own prompt fourth, got %+v", completer.calledWith[3])
	}
	if result.TokenUsage.AgentPromptTokens <= 0 {
		t.Errorf("expected a nonzero AgentPromptTokens, got %+v", result.TokenUsage)
	}
}

// TestChatService_Agent_PerQuestionOverridesEndpointDefault proves
// opts.AgentID overrides endpoint.DefaultAgentID for that turn only.
func TestChatService_Agent_PerQuestionOverridesEndpointDefault(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
	completer := &fakeChatCompleter{answer: "answer"}
	agents := &fakeAgentStore{agents: []domain.Agent{
		{ID: "researcher", SystemPrompt: "Dig up sources.", Enabled: true},
		{ID: "summarizer", SystemPrompt: "Be concise.", Enabled: true},
	}}
	svc := NewChatService(endpoints, completer, nil, nil, agents, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{AgentID: "summarizer"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 2 || completer.calledWith[0].Content != "Be concise." {
		t.Fatalf("expected the per-question agent override applied, got %v", completer.calledWith)
	}
}

// TestChatService_Agent_DisabledAgentIsIgnored proves a disabled agent
// contributes no leading message at all, same as if none were configured.
func TestChatService_Agent_DisabledAgentIsIgnored(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
	completer := &fakeChatCompleter{answer: "answer"}
	agents := &fakeAgentStore{agents: []domain.Agent{
		{ID: "researcher", SystemPrompt: "Dig up sources.", Enabled: false},
	}}
	svc := NewChatService(endpoints, completer, nil, nil, agents, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.calledWith) != 1 || !msgEqual(completer.calledWith[0], history[0]) {
		t.Fatalf("expected no leading message from a disabled agent, got %v", completer.calledWith)
	}
	if result.TokenUsage.AgentPromptTokens != 0 {
		t.Errorf("expected zero AgentPromptTokens for a disabled agent, got %+v", result.TokenUsage)
	}
}

// TestChatService_Agent_UnknownIDAndListErrorAreBestEffort proves an
// endpoint.DefaultAgentID that names no configured agent, and a
// ListAgents error, both resolve to "no agent active" rather than failing
// the turn -- same tolerance as ListMCPServers elsewhere in this file.
func TestChatService_Agent_UnknownIDAndListErrorAreBestEffort(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "missing"}}
		completer := &fakeChatCompleter{answer: "answer"}
		agents := &fakeAgentStore{agents: []domain.Agent{{ID: "researcher", SystemPrompt: "x", Enabled: true}}}
		svc := NewChatService(endpoints, completer, nil, nil, agents, nil)
		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 1 {
			t.Fatalf("expected no leading message for an unknown agent id, got %v", completer.calledWith)
		}
	})
	t.Run("ListAgents error", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
		completer := &fakeChatCompleter{answer: "answer"}
		agents := &fakeAgentStore{err: errors.New("db unavailable")}
		svc := NewChatService(endpoints, completer, nil, nil, agents, nil)
		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 1 {
			t.Fatalf("expected no leading message when ListAgents errors, got %v", completer.calledWith)
		}
	})
	t.Run("blank id and nil store", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
		completer := &fakeChatCompleter{answer: "answer"}
		svc := NewChatService(endpoints, completer, nil, nil, nil, nil)
		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 1 {
			t.Fatalf("expected no leading message with a blank id and nil agents store, got %v", completer.calledWith)
		}
	})
}

// TestChatService_Agent_ScopesActiveMCPServersToAllowedList proves a
// non-empty domain.Agent.MCPServerIDs narrows the global MCP server
// catalog down to just that scope, verified via which servers the MCP
// session was opened with.
func TestChatService_Agent_ScopesActiveMCPServersToAllowedList(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "web", Name: "web", Transport: "stdio", Command: "a", Enabled: true},
		{ID: "datetime", Name: "datetime", Transport: "stdio", Command: "b", Enabled: true},
	}}
	agents := &fakeAgentStore{agents: []domain.Agent{
		{ID: "researcher", Enabled: true, MCPServerIDs: []string{"web"}},
	}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, servers, provider, agents, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 1 || provider.openedServers[0].ID != "web" {
		t.Fatalf("expected only the agent-scoped \"web\" server active, got %+v", provider.openedServers)
	}
}

// TestChatService_Agent_EmptyScopeOnActiveAgentAllowsNoGlobalServers proves
// there is no "unscoped" state: an ACTIVE agent with no MCPServerIDs set
// gets zero global servers, not every one -- see domain.Agent.MCPServerIDs'
// own doc comment. Contrast with
// TestChatService_NoAgentActive_AllowsEveryGlobalServer below, which proves
// the *different* case of no agent being selected at all still allows
// everything.
func TestChatService_Agent_EmptyScopeOnActiveAgentAllowsNoGlobalServers(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "web", Name: "web", Transport: "stdio", Command: "a", Enabled: true},
		{ID: "datetime", Name: "datetime", Transport: "stdio", Command: "b", Enabled: true},
	}}
	agents := &fakeAgentStore{agents: []domain.Agent{{ID: "researcher", Enabled: true}}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, servers, provider, agents, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 0 {
		t.Fatalf("expected no global servers active for an agent with an empty (unset) scope, got %+v", provider.openedServers)
	}
}

// TestChatService_NoAgentActive_AllowsEveryGlobalServer is the essential
// non-regression check for the "no unscoped state" change above: a turn
// with NO agent selected at all (opts.AgentID empty AND
// endpoint.DefaultAgentID empty) must still see every enabled global
// server, exactly as before -- domain.Agent.AllowsServer's own
// empty-means-block semantics must never apply when there's no agent in
// the picture to begin with.
func TestChatService_NoAgentActive_AllowsEveryGlobalServer(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "web", Name: "web", Transport: "stdio", Command: "a", Enabled: true},
		{ID: "datetime", Name: "datetime", Transport: "stdio", Command: "b", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 2 {
		t.Fatalf("expected both global servers active with no agent selected at all, got %+v", provider.openedServers)
	}
}

// TestChatService_UserMCPServers_MergedRegardlessOfAgentScope proves a
// caller's own personal MCP server is added even when an active agent
// scopes the global catalog to exclude it entirely -- domain.Agent's own
// MCPServerIDs never applies to a user's personal servers.
func TestChatService_UserMCPServers_MergedRegardlessOfAgentScope(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, DefaultAgentID: "researcher"}}
	completer := &fakeChatCompleter{answer: "answer"}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "web", Name: "web", Transport: "stdio", Command: "a", Enabled: true},
	}}
	agents := &fakeAgentStore{agents: []domain.Agent{
		{ID: "researcher", Enabled: true, MCPServerIDs: []string{"web"}},
	}}
	userServers := &fakeUserMCPServerStore{byUser: map[string][]domain.MCPServer{
		"alice": {{ID: "personal", Name: "personal", Transport: "http", BaseURL: "http://example.test", Enabled: true}},
	}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, servers, provider, agents, userServers)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{UserID: "alice"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 2 {
		t.Fatalf("expected the scoped global server plus the personal one, got %+v", provider.openedServers)
	}
	var gotPersonal bool
	for _, s := range provider.openedServers {
		if s.ID == "personal" {
			gotPersonal = true
		}
	}
	if !gotPersonal {
		t.Fatalf("expected the personal server active despite the agent's own scope excluding it, got %+v", provider.openedServers)
	}
}

// TestChatService_UserMCPServers_StdioTransportRejected proves a stored
// "stdio" row in a user's own servers (however it got there -- restapi
// validation should already prevent it, this is the last line of defense)
// is never handed to mcpTools.Open -- a "stdio" server means real local
// command execution on the server host, a trust tier no regular user's own
// config may ever reach.
func TestChatService_UserMCPServers_StdioTransportRejected(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	userServers := &fakeUserMCPServerStore{byUser: map[string][]domain.MCPServer{
		"alice": {{ID: "sneaky", Name: "sneaky", Transport: "stdio", Command: "/bin/sh", Enabled: true}},
	}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, nil, provider, nil, userServers)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{UserID: "alice"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 0 {
		t.Fatalf("expected the stdio-transport personal server to never reach mcpTools.Open, got %+v", provider.openedServers)
	}
}

// TestChatService_UserMCPServers_EmptyUserIDOrNilStoreAddsNothing covers
// both best-effort fallback paths: no UserID set on ChatOptions (e.g. an
// admin session, which has no personal servers at all), and a deployment
// that hasn't wired userMCPServers.
func TestChatService_UserMCPServers_EmptyUserIDOrNilStoreAddsNothing(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	userServers := &fakeUserMCPServerStore{byUser: map[string][]domain.MCPServer{
		"alice": {{ID: "personal", Name: "personal", Transport: "http", BaseURL: "http://example.test", Enabled: true}},
	}}

	t.Run("empty UserID", func(t *testing.T) {
		provider := &fakeMCPToolProvider{}
		svc := NewChatService(endpoints, completer, nil, provider, nil, userServers)
		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(provider.openedServers) != 0 {
			t.Fatalf("expected no personal servers with an empty UserID, got %+v", provider.openedServers)
		}
	})

	t.Run("nil store", func(t *testing.T) {
		provider := &fakeMCPToolProvider{}
		svc := NewChatService(endpoints, completer, nil, provider, nil, nil)
		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{UserID: "alice"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(provider.openedServers) != 0 {
			t.Fatalf("expected no personal servers with a nil userMCPServers store, got %+v", provider.openedServers)
		}
	})
}

// TestChatService_UserMCPServers_ListErrorIsBestEffort proves a
// ListUserMCPServers error just leaves the personal-server list empty,
// never failing the turn -- same tolerance as the global ListMCPServers
// call.
func TestChatService_UserMCPServers_ListErrorIsBestEffort(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	userServers := &fakeUserMCPServerStore{err: errors.New("db exploded")}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, nil, provider, nil, userServers)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{UserID: "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "answer" {
		t.Fatalf("expected the turn to still succeed despite the list error, got %+v", result)
	}
}

// TestChatService_UserMCPServers_GatedByWebSearchHonored proves a personal
// server's own GatedByWebSearch flag is respected the same way a global
// server's is.
func TestChatService_UserMCPServers_GatedByWebSearchHonored(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchEnabled: false}}
	completer := &fakeChatCompleter{answer: "answer"}
	userServers := &fakeUserMCPServerStore{byUser: map[string][]domain.MCPServer{
		"alice": {{ID: "gated", Name: "gated", Transport: "http", BaseURL: "http://example.test", Enabled: true, GatedByWebSearch: true}},
	}}
	provider := &fakeMCPToolProvider{}
	svc := NewChatService(endpoints, completer, nil, provider, nil, userServers)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{UserID: "alice"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.openedServers) != 0 {
		t.Fatalf("expected the web-search-gated personal server inactive with web search off, got %+v", provider.openedServers)
	}
}

// TestChatService_EndpointSystemPrompt_InjectedWhenMCPServersInactiveOrNil
// is a regression check proving the endpoint's own SystemPrompt injection
// never depends on MCP server state -- neither when every server is
// inactive (gated server, web search off) nor when mcpServers/mcpTools are
// nil.
func TestChatService_EndpointSystemPrompt_InjectedWhenMCPServersInactiveOrNil(t *testing.T) {
	t.Run("servers all inactive", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate.", WebSearchEnabled: false}}
		completer := &fakeChatCompleter{answer: "answer"}
		servers := &fakeMCPServerStore{servers: []domain.MCPServer{
			{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "server prompt", GatedByWebSearch: true},
		}}
		svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 2 || completer.calledWith[0].Content != "You are a pirate." {
			t.Fatalf("expected the endpoint's SystemPrompt still injected with every server inactive, got %v", completer.calledWith)
		}
	})

	t.Run("mcp nil", func(t *testing.T) {
		endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, SystemPrompt: "You are a pirate."}}
		completer := &fakeChatCompleter{answer: "answer"}
		svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

		history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
		if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(completer.calledWith) != 2 || completer.calledWith[0].Content != "You are a pirate." {
			t.Fatalf("expected the endpoint's SystemPrompt still injected with mcpServers/mcpTools nil, got %v", completer.calledWith)
		}
	})
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
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: "Use the web_search tool when helpful."},
	}}
	svc := NewChatService(endpoints, completer, servers, &fakeMCPToolProvider{}, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "what is a?"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u := result.TokenUsage
	if u.GlobalPromptTokens != estimateTokens([]domain.ChatMessage{{Role: domain.ChatRoleSystem, Content: "You are a pirate."}}) {
		t.Errorf("expected GlobalPromptTokens to match the system prompt's own estimate, got %+v", u)
	}
	if u.ToolPromptTokens <= 0 {
		t.Errorf("expected a nonzero ToolPromptTokens for the one active server's prompt, got %+v", u)
	}
	if u.HistoryTokens != estimateTokens(history) {
		t.Errorf("expected HistoryTokens to match the (untrimmed, since nothing exceeded budget) history estimate, got %+v", u)
	}
	if u.MaxContextTokens != 10000 {
		t.Errorf("expected MaxContextTokens echoed from the endpoint config, got %+v", u)
	}
}

// TestChatService_TokenUsage_ZeroWhenNothingConfigured proves every field is
// simply zero when there's no system prompt, no active servers, and no
// search context -- only the history itself contributes.
func TestChatService_TokenUsage_ZeroWhenNothingConfigured(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{answer: "answer"}
	svc := NewChatService(endpoints, completer, nil, nil, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u := result.TokenUsage
	if u.GlobalPromptTokens != 0 || u.ToolPromptTokens != 0 || u.MaxContextTokens != 0 {
		t.Errorf("expected every configured-piece field to be zero, got %+v", u)
	}
	if u.HistoryTokens != estimateTokens(history) {
		t.Errorf("expected HistoryTokens to still reflect the conversation itself, got %+v", u)
	}
}

// TestChatService_ActiveServerWithEmptyPrompt_NoExtraSystemMessage proves an
// empty Prompt contributes no extra system message even when its server is
// active (Enabled, ungated, offering a tool) -- it still runs normally.
func TestChatService_ActiveServerWithEmptyPrompt_NoExtraSystemMessage(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true, Prompt: ""},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

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
		t.Fatalf("expected no extra system message for an empty-Prompt server, got %v", firstCall)
	}
	if len(result.ToolResults) != 1 {
		t.Fatalf("expected the empty-Prompt server's tool to still run normally, got %v", result.ToolResults)
	}
}

// TestChatService_MCPEnv_CarriesEndpointWebSearchBaseURL proves the env map
// passed into Open (and thus available to every spawned stdio server)
// contains WEB_SEARCH_BASE_URL matching endpoint.WebSearchBaseURL.
func TestChatService_MCPEnv_CarriesEndpointWebSearchBaseURL(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchBaseURL: "http://searxng.example:8888"}}
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.openCount != 1 {
		t.Fatalf("expected exactly 1 Open call, got %d", provider.openCount)
	}
	if got := provider.openedEnv["WEB_SEARCH_BASE_URL"]; got != "http://searxng.example:8888" {
		t.Fatalf("expected env[WEB_SEARCH_BASE_URL] = %q, got %q (env=%v)", "http://searxng.example:8888", got, provider.openedEnv)
	}
}

// mcpEnvTestServers/mcpEnvTestCompleter/mcpEnvTestServerStore/
// mcpEnvTestProvider factor out the fixture shared by the four
// WEB_SEARCH_RESULT_COUNT/WEB_FETCH_USER_AGENT env tests below, mirroring
// TestChatService_MCPEnv_CarriesEndpointWebSearchBaseURL's own setup.
func mcpEnvTestFixtures() (*fakeChatCompleter, *fakeMCPServerStore, *fakeMCPToolProvider) {
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("done"),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	return completer, servers, provider
}

// TestChatService_MCPEnv_CarriesWebSearchResultCountWhenPositive proves a
// positive domain.ChatEndpoint.WebSearchResultCount reaches Open's env map
// as WEB_SEARCH_RESULT_COUNT.
func TestChatService_MCPEnv_CarriesWebSearchResultCountWhenPositive(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchResultCount: 5}}
	completer, servers, provider := mcpEnvTestFixtures()
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := provider.openedEnv["WEB_SEARCH_RESULT_COUNT"]; got != "5" {
		t.Fatalf("expected env[WEB_SEARCH_RESULT_COUNT] = %q, got %q (env=%v)", "5", got, provider.openedEnv)
	}
}

// TestChatService_MCPEnv_OmitsWebSearchResultCountWhenZero proves the
// "no cap" default (0) leaves WEB_SEARCH_RESULT_COUNT out of the env map
// entirely, rather than sending a literal "0" a spawned server would have
// to know how to interpret.
func TestChatService_MCPEnv_OmitsWebSearchResultCountWhenZero(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true, WebSearchResultCount: 0}}
	completer, servers, provider := mcpEnvTestFixtures()
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := provider.openedEnv["WEB_SEARCH_RESULT_COUNT"]; ok {
		t.Fatalf("expected WEB_SEARCH_RESULT_COUNT omitted for 0, got env=%v", provider.openedEnv)
	}
}

// TestChatService_MCPEnv_CarriesUserAgentWhenSet proves a non-empty
// ChatOptions.UserAgent reaches Open's env map as WEB_FETCH_USER_AGENT.
func TestChatService_MCPEnv_CarriesUserAgentWhenSet(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer, servers, provider := mcpEnvTestFixtures()
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{UserAgent: "custom-agent/1.0"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := provider.openedEnv["WEB_FETCH_USER_AGENT"]; got != "custom-agent/1.0" {
		t.Fatalf("expected env[WEB_FETCH_USER_AGENT] = %q, got %q (env=%v)", "custom-agent/1.0", got, provider.openedEnv)
	}
}

// TestChatService_MCPEnv_OmitsUserAgentWhenEmpty proves an empty
// ChatOptions.UserAgent (the zero value, e.g. h.opSettings unwired) leaves
// WEB_FETCH_USER_AGENT out of the env map entirely, so mcp-web falls back
// to its own built-in default rather than receiving an empty override.
func TestChatService_MCPEnv_OmitsUserAgentWhenEmpty(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	completer, servers, provider := mcpEnvTestFixtures()
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil)

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	if _, err := svc.Chat(context.Background(), history, ChatOptions{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := provider.openedEnv["WEB_FETCH_USER_AGENT"]; ok {
		t.Fatalf("expected WEB_FETCH_USER_AGENT omitted when empty, got env=%v", provider.openedEnv)
	}
}

// TestTrimToBudget_ThreeLeadingSystemMessages_KeepsAllIntact extends the
// two-message case above to three -- endpoint prompt + 2 server prompts --
// proving trimToBudget's generic "walk every leading system-role message"
// logic keeps all of them, not just the first two.
func TestTrimToBudget_ThreeLeadingSystemMessages_KeepsAllIntact(t *testing.T) {
	messages := []domain.ChatMessage{
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("p", 15)}, // endpoint prompt, ~5 tokens
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("h", 15)}, // server 1 prompt, ~5 tokens
		{Role: domain.ChatRoleSystem, Content: strings.Repeat("i", 15)}, // server 2 prompt, ~5 tokens
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
