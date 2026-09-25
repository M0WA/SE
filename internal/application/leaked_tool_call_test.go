package application

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
)

var writeFileTool = domain.ToolDef{Name: "write_file"}

var errBoom = errors.New("boom")

func TestDetectLeakedToolCall_NoTagPresent(t *testing.T) {
	if detectLeakedToolCall("just a normal answer", []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for plain content with no tag")
	}
}

func TestDetectLeakedToolCall_ValidBlockMatchingOfferedTool(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"name": "write_file", "arguments": {"filename": "a.txt"}}` + "\n" + `</tool_call>`
	if !detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected detection for a valid block naming an offered tool")
	}
}

func TestDetectLeakedToolCall_NoToolsOffered(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"name": "write_file", "arguments": {}}` + "\n" + `</tool_call>`
	if detectLeakedToolCall(content, nil) {
		t.Error("expected no detection when no tools were offered at all")
	}
}

func TestDetectLeakedToolCall_NameNotOffered(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"name": "delete_everything", "arguments": {}}` + "\n" + `</tool_call>`
	if detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for a name that wasn't offered this turn")
	}
}

func TestDetectLeakedToolCall_MalformedJSON(t *testing.T) {
	content := "<tool_call>\nnot valid json\n</tool_call>"
	if detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for malformed JSON")
	}
}

func TestDetectLeakedToolCall_MissingName(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"arguments": {"filename": "a.txt"}}` + "\n" + `</tool_call>`
	if detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for a block with no name")
	}
}

func TestDetectLeakedToolCall_UnclosedButValidJSON(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"name": "write_file", "arguments": {}}`
	if !detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected detection for valid JSON even without a closing tag")
	}
}

func TestDetectLeakedToolCall_UnclosedAndUnparseable(t *testing.T) {
	content := `<tool_call>` + "\n" + `{"name": "write_file", "arguments": {"filename": "a`
	if detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for an unclosed, unparseable block")
	}
}

func TestDetectLeakedToolCall_InsideCodeFence(t *testing.T) {
	content := "For example:\n```\n<tool_call>\n" + `{"name": "write_file", "arguments": {}}` + "\n</tool_call>\n```"
	if detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected no detection for a block inside a code fence")
	}
}

func TestDetectLeakedToolCall_AfterAClosedCodeFence(t *testing.T) {
	content := "Some code:\n```\nfmt.Println(1)\n```\n<tool_call>\n" +
		`{"name": "write_file", "arguments": {}}` + "\n</tool_call>"
	if !detectLeakedToolCall(content, []domain.ToolDef{writeFileTool}) {
		t.Error("expected detection after an earlier, already-closed code fence")
	}
}

// fakeLeakDetectionCompleter is a minimal ports.ChatCompleter fake for
// unit-testing completeDetectingLeakedToolCalls directly, distinct from
// chat_service_test.go's fakeChatCompleter since it needs its own simple
// per-call response queue plus an error trigger keyed to a specific call
// index (the retry call, not the first).
type fakeLeakDetectionCompleter struct {
	responses []domain.ChatMessage
	errAt     int // -1 means never
	calls     [][]domain.ChatMessage
	callCount int
}

func (f *fakeLeakDetectionCompleter) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error) {
	f.calls = append(f.calls, messages)
	idx := f.callCount
	f.callCount++
	if idx == f.errAt {
		return domain.ChatMessage{}, errBoom
	}
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	return f.responses[idx], nil
}

func TestCompleteDetectingLeakedToolCalls_NoLeakReturnsFirstResponseUnchanged(t *testing.T) {
	svc := &ChatService{completer: &fakeLeakDetectionCompleter{
		responses: []domain.ChatMessage{plainMessage("a normal answer")},
		errAt:     -1,
	}}
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	msg, gotMessages, err := svc.completeDetectingLeakedToolCalls(context.Background(), domain.ChatEndpoint{}, messages, []domain.ToolDef{writeFileTool})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content != "a normal answer" {
		t.Errorf("expected the plain answer unchanged, got %q", msg.Content)
	}
	if len(gotMessages) != len(messages) {
		t.Errorf("expected message history unchanged when no leak detected, got %d messages", len(gotMessages))
	}
}

func TestCompleteDetectingLeakedToolCalls_LeakDetectedRetriesWithNudge(t *testing.T) {
	leaked := plainMessage(`<tool_call>` + "\n" + `{"name": "write_file", "arguments": {"filename": "a.txt"}}` + "\n" + `</tool_call>`)
	real := toolCallMessage("call_1", "write_file", argsJSON("filename", "a.txt"))
	completer := &fakeLeakDetectionCompleter{responses: []domain.ChatMessage{leaked, real}, errAt: -1}
	svc := &ChatService{completer: completer}

	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "write a file"}}
	msg, gotMessages, err := svc.completeDetectingLeakedToolCalls(context.Background(), domain.ChatEndpoint{}, messages, []domain.ToolDef{writeFileTool})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "write_file" {
		t.Fatalf("expected the retry's real ToolCall returned, got %+v", msg)
	}
	if completer.callCount != 2 {
		t.Fatalf("expected exactly 2 completer calls (original + one retry), got %d", completer.callCount)
	}
	if len(gotMessages) != len(messages)+2 {
		t.Fatalf("expected history extended by the leaked message + nudge, got %d messages", len(gotMessages))
	}
	nudgeMsg := gotMessages[len(gotMessages)-1]
	if nudgeMsg.Role != domain.ChatRoleSystem || nudgeMsg.Content != leakedToolCallNudge {
		t.Fatalf("expected the last history message to be the nudge, got %+v", nudgeMsg)
	}
}

func TestCompleteDetectingLeakedToolCalls_LeakDetectedButModelDeclinesOnRetry(t *testing.T) {
	leaked := plainMessage(`<tool_call>` + "\n" + `{"name": "write_file", "arguments": {}}` + "\n" + `</tool_call>`)
	declined := plainMessage("Never mind, I wasn't actually trying to call anything.")
	completer := &fakeLeakDetectionCompleter{responses: []domain.ChatMessage{leaked, declined}, errAt: -1}
	svc := &ChatService{completer: completer}

	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	msg, _, err := svc.completeDetectingLeakedToolCalls(context.Background(), domain.ChatEndpoint{}, messages, []domain.ToolDef{writeFileTool})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content != "Never mind, I wasn't actually trying to call anything." || len(msg.ToolCalls) != 0 {
		t.Fatalf("expected the model's declined-normally answer, got %+v", msg)
	}
}

func TestCompleteDetectingLeakedToolCalls_FirstCallErrors(t *testing.T) {
	completer := &fakeLeakDetectionCompleter{responses: nil, errAt: 0}
	svc := &ChatService{completer: completer}
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	_, gotMessages, err := svc.completeDetectingLeakedToolCalls(context.Background(), domain.ChatEndpoint{}, messages, []domain.ToolDef{writeFileTool})
	if err == nil {
		t.Fatal("expected the first call's error to propagate")
	}
	if len(gotMessages) != len(messages) {
		t.Errorf("expected original messages returned unchanged on error, got %d", len(gotMessages))
	}
}

func TestCompleteDetectingLeakedToolCalls_RetryCallErrorsIsBestEffort(t *testing.T) {
	leaked := plainMessage(`<tool_call>` + "\n" + `{"name": "write_file", "arguments": {}}` + "\n" + `</tool_call>`)
	completer := &fakeLeakDetectionCompleter{responses: []domain.ChatMessage{leaked}, errAt: 1}
	svc := &ChatService{completer: completer}
	messages := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	msg, gotMessages, err := svc.completeDetectingLeakedToolCalls(context.Background(), domain.ChatEndpoint{}, messages, []domain.ToolDef{writeFileTool})
	if err != nil {
		t.Fatalf("expected a failed retry to be best-effort, not propagate an error, got %v", err)
	}
	if msg.Content != leaked.Content {
		t.Errorf("expected the original leaked-looking message kept when the retry itself errors, got %+v", msg)
	}
	if len(gotMessages) != len(messages) {
		t.Errorf("expected original messages kept when the retry errors, got %d", len(gotMessages))
	}
}

// TestChatService_LeakedToolCallOnFirstResponseNudgedIntoRealCall proves
// the full ChatService.Chat wiring: a first response shaped like a leaked
// tool call (the confirmed-live vLLM/hermes bug) gets nudged into a real
// tool_calls response, which then runs exactly like any other genuine
// tool call -- populating ToolResults and driving a normal follow-up.
func TestChatService_LeakedToolCallOnFirstResponseNudgedIntoRealCall(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	leaked := plainMessage(`<tool_call>` + "\n" +
		`{"name": "web_search", "arguments": {"query": "golang release notes"}}` + "\n" + `</tool_call>`)
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		leaked,
		toolCallMessage("call_1", "web_search", argsJSON("query", "golang release notes")),
		plainMessage("Go 1.26 was just released."),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{"web_search": "top result"}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil, VisionConfig{Settings: nil, InternalAPIKey: ""})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "search for golang release notes"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "Go 1.26 was just released." {
		t.Errorf("expected the real follow-up's answer, got %q", result.Answer)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].ToolName != "web_search" {
		t.Fatalf("expected 1 real web_search tool result, got %v", result.ToolResults)
	}
	if completer.callCount != 3 {
		t.Fatalf("expected 3 completer calls (leaked attempt, nudged retry, post-tool follow-up), got %d", completer.callCount)
	}
}

// TestChatService_LeakedToolCallDeclinedByModelAnswersNormally proves a
// model that, once nudged, does NOT actually make a real call (e.g. it
// was only echoing/quoting text, never genuinely attempting one) simply
// answers normally -- no tool ever runs, and no error results.
func TestChatService_LeakedToolCallDeclinedByModelAnswersNormally(t *testing.T) {
	endpoints := &fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}}
	leaked := plainMessage(`<tool_call>` + "\n" +
		`{"name": "web_search", "arguments": {"query": "x"}}` + "\n" + `</tool_call>`)
	completer := &fakeChatCompleter{responses: []domain.ChatMessage{
		leaked,
		plainMessage("Sorry, I wasn't actually trying to search for anything."),
	}}
	servers := &fakeMCPServerStore{servers: []domain.MCPServer{
		{ID: "1", Name: "web", Transport: "stdio", Command: "mcp-web", Enabled: true},
	}}
	provider := &fakeMCPToolProvider{
		tools:   []domain.MCPTool{mcpTool("web_search", "Search the web.")},
		session: &fakeMCPSession{outputs: map[string]string{}},
	}
	svc := NewChatService(endpoints, completer, servers, provider, nil, nil, VisionConfig{Settings: nil, InternalAPIKey: ""})

	history := []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}}
	result, err := svc.Chat(context.Background(), history, ChatOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "Sorry, I wasn't actually trying to search for anything." {
		t.Errorf("expected the declined-normally answer, got %q", result.Answer)
	}
	if len(result.ToolResults) != 0 {
		t.Errorf("expected no tool results when the model declines on retry, got %v", result.ToolResults)
	}
	if completer.callCount != 2 {
		t.Fatalf("expected exactly 2 completer calls (leaked attempt + nudged retry, no tool round), got %d", completer.callCount)
	}
}
