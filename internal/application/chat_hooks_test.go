package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"searchengine/internal/domain"
)

// fakeHookScriptRunner is a minimal ports.HookScriptRunner fake recording
// every call (script name + args verbatim, proving no shell interpolation
// ever mangles them) and returning canned output/error keyed by script
// name.
type fakeHookScriptRunner struct {
	calls   []fakeHookCall
	outputs map[string]string
	errs    map[string]error
}

type fakeHookCall struct {
	script string
	args   []string
	env    map[string]string
}

func (f *fakeHookScriptRunner) RunHookScript(ctx context.Context, scriptName string, args []string, env map[string]string) (string, error) {
	f.calls = append(f.calls, fakeHookCall{script: scriptName, args: append([]string(nil), args...), env: env})
	if err, ok := f.errs[scriptName]; ok {
		return "", err
	}
	return f.outputs[scriptName], nil
}

// fakeChatHookStore is a minimal ports.ChatHookStore fake -- only
// ListChatHooks is exercised by ChatService.Chat, so Create/Update/Delete
// are no-ops.
type fakeChatHookStore struct {
	hooks []domain.ChatHook
	err   error
}

func (f *fakeChatHookStore) ListChatHooks(ctx context.Context) ([]domain.ChatHook, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.hooks, nil
}
func (f *fakeChatHookStore) CreateChatHook(ctx context.Context, h domain.ChatHook) error { return nil }
func (f *fakeChatHookStore) UpdateChatHook(ctx context.Context, h domain.ChatHook) error { return nil }
func (f *fakeChatHookStore) DeleteChatHook(ctx context.Context, id string) error         { return nil }

// singleStringParams is the {"type":"object","properties":{name:{"type":
// "string"}},"required":[name]} shape every test hook's Parameters uses --
// see domain.ChatHook.Parameters's "exactly one property" convention.
func singleStringParams(propertyName string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"` + propertyName + `":{"type":"string"}},"required":["` + propertyName + `"]}`)
}

func argsJSON(propertyName, value string) string {
	b, _ := json.Marshal(map[string]string{propertyName: value})
	return string(b)
}

func TestRunToolCalls_MatchingCall_RunsScriptWithArgumentValue(t *testing.T) {
	hooks := []domain.ChatHook{{ID: "1", Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "search results"}}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: argsJSON("query", "golang release notes")}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	r := results[0]
	if r.HookName != "web_search" || r.ToolCallID != "call_1" || r.Input != "golang release notes" || r.Output != "search results" || r.Err != "" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 script call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.script != "web_search.sh" {
		t.Fatalf("expected script %q, got %q", "web_search.sh", call.script)
	}
	if len(call.args) != 1 || call.args[0] != "golang release notes" {
		t.Fatalf("expected the argument value verbatim as the sole argv value, got %v", call.args)
	}
}

func TestRunToolCalls_UnknownToolName_ProducesErrResultNoScriptCall(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "unknown_tool", Arguments: `{}`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 {
		t.Fatalf("expected 1 result (one per tool call, even unresolved), got %v", results)
	}
	if results[0].Err == "" {
		t.Fatalf("expected Err set for an unknown tool name, got %+v", results[0])
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_MultipleCalls_EachProducesAResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "echo", Parameters: singleStringParams("value"), Script: "echo.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"echo.sh": "ok"}}
	toolCalls := []domain.ToolCall{
		{ID: "call_1", Name: "echo", Arguments: argsJSON("value", "a")},
		{ID: "call_2", Name: "echo", Arguments: argsJSON("value", "b")},
		{ID: "call_3", Name: "echo", Arguments: argsJSON("value", "c")},
	}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 3 {
		t.Fatalf("expected 3 results (one per call), got %d: %v", len(results), results)
	}
	for _, r := range results {
		if r.HookName != "echo" || r.Output != "ok" || r.Err != "" {
			t.Fatalf("unexpected result: %+v", r)
		}
	}
	if len(runner.calls) != 3 {
		t.Fatalf("expected 3 script calls, got %d", len(runner.calls))
	}
	want := []string{"a", "b", "c"}
	for i, call := range runner.calls {
		if call.args[0] != want[i] {
			t.Fatalf("call %d: expected arg %q, got %q", i, want[i], call.args[0])
		}
	}
}

// TestRunToolCalls_CallsBeyondCap_StillGetAResultButNoScriptRuns proves
// runToolCalls always returns exactly one ChatHookResult per input
// ToolCall (required so toolResultMessages can answer every tool_call_id
// the model emitted), but a call past maxHookMatchesPerTurn never actually
// runs its script.
func TestRunToolCalls_CallsBeyondCap_StillGetAResultButNoScriptRuns(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "echo", Parameters: singleStringParams("n"), Script: "echo.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"echo.sh": "ok"}}

	toolCalls := make([]domain.ToolCall, maxHookMatchesPerTurn+3)
	for i := range toolCalls {
		toolCalls[i] = domain.ToolCall{ID: "call", Name: "echo", Arguments: argsJSON("n", "x")}
	}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != len(toolCalls) {
		t.Fatalf("expected one result per input tool call (%d), got %d", len(toolCalls), len(results))
	}
	if len(runner.calls) != maxHookMatchesPerTurn {
		t.Fatalf("expected script calls capped at %d, got %d", maxHookMatchesPerTurn, len(runner.calls))
	}
	for i, r := range results {
		if i < maxHookMatchesPerTurn {
			if r.Err != "" {
				t.Fatalf("call %d: expected no error under the cap, got %q", i, r.Err)
			}
		} else if r.Err == "" {
			t.Fatalf("call %d: expected an error past the cap, got none", i)
		}
	}
}

func TestRunToolCalls_ScriptError_ProducesResultWithErrSetAndOutputEmpty(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "flaky", Parameters: singleStringParams("arg"), Script: "flaky.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{errs: map[string]error{"flaky.sh": errors.New("script timed out")}}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "flaky", Arguments: argsJSON("arg", "argument")}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 {
		t.Fatalf("expected 1 result even on script error, got %v", results)
	}
	if results[0].Output != "" {
		t.Fatalf("expected empty Output on script error, got %q", results[0].Output)
	}
	if results[0].Err != "script timed out" {
		t.Fatalf("expected Err set to the script error, got %q", results[0].Err)
	}
}

func TestRunToolCalls_DisabledHook_ProducesErrResultNoScriptCall(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "off", Parameters: singleStringParams("x"), Script: "x.sh", Enabled: false}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "off", Arguments: argsJSON("x", "y")}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected a disabled hook's call to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_InvalidParameters_ProducesErrResultNoScriptCall(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "bad", Parameters: json.RawMessage(`not valid json`), Script: "bad.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "bad", Arguments: `{}`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected a hook with invalid Parameters to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_WrongParameterPropertyCount_ProducesErrResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "bad-params", Parameters: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`), Script: "x.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "bad-params", Arguments: `{"a":"1","b":"2"}`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected a hook whose parameters has != 1 property to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_ArgumentsMissingDeclaredProperty_ProducesErrResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: `{"wrong_property":"x"}`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected arguments missing the declared property to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_MalformedArgumentsJSON_ProducesErrResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: `not json`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected malformed Arguments JSON to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_ArgumentPropertyNotAString_ProducesErrResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: `{"query":42}`}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, nil)

	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected a non-string argument value to produce an Err result, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunToolCalls_NoToolCalls_NoResults(t *testing.T) {
	results := runToolCalls(context.Background(), nil, &fakeHookScriptRunner{}, nil, nil)
	if len(results) != 0 {
		t.Fatalf("expected no results with no tool calls, got %v", results)
	}
}

// TestRunToolCalls_EnvForwardedUnchangedToRunHookScript proves runToolCalls
// passes its env parameter through to every RunHookScript call verbatim --
// it does not need to interpret env itself, just forward it.
func TestRunToolCalls_EnvForwardedUnchangedToRunHookScript(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Parameters: singleStringParams("query"), Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "results"}}
	env := map[string]string{"WEB_SEARCH_BASE_URL": "http://searxng.example:8888"}
	toolCalls := []domain.ToolCall{{ID: "call_1", Name: "web_search", Arguments: argsJSON("query", "golang")}}

	results := runToolCalls(context.Background(), hooks, runner, toolCalls, env)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %v", results)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 script call, got %d", len(runner.calls))
	}
	if got := runner.calls[0].env; len(got) != 1 || got["WEB_SEARCH_BASE_URL"] != "http://searxng.example:8888" {
		t.Fatalf("expected env forwarded unchanged to RunHookScript, got %v", got)
	}
}
