package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestRunChatHooks_MatchingPattern_RunsScriptWithCaptureGroupArgs(t *testing.T) {
	hooks := []domain.ChatHook{{ID: "1", Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "search results"}}

	results := runChatHooks(context.Background(), hooks, runner, "let me check SEARCH[golang release notes] for you", nil)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d: %v", len(results), results)
	}
	if results[0].HookName != "web_search" || results[0].Input != "golang release notes" || results[0].Output != "search results" || results[0].Err != "" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly 1 script call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.script != "web_search.sh" {
		t.Fatalf("expected script %q, got %q", "web_search.sh", call.script)
	}
	if len(call.args) != 1 || call.args[0] != "golang release notes" {
		t.Fatalf("expected the capture group verbatim as the sole argv value, got %v", call.args)
	}
}

func TestRunChatHooks_NoMatch_NoResults(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}

	results := runChatHooks(context.Background(), hooks, runner, "nothing to see here", nil)

	if len(results) != 0 {
		t.Fatalf("expected no results, got %v", results)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no script calls, got %v", runner.calls)
	}
}

func TestRunChatHooks_MultipleMatches_EachProducesAResult(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "echo", Pattern: `X\((\w+)\)`, Script: "echo.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"echo.sh": "ok"}}

	results := runChatHooks(context.Background(), hooks, runner, "X(a) then X(b) then X(c)", nil)

	if len(results) != 3 {
		t.Fatalf("expected 3 results (one per match), got %d: %v", len(results), results)
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

func TestRunChatHooks_MatchesCappedAtMaxHookMatchesPerTurn(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "echo", Pattern: `X\((\d+)\)`, Script: "echo.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"echo.sh": "ok"}}

	var sb strings.Builder
	for i := 0; i < maxHookMatchesPerTurn+3; i++ {
		fmt.Fprintf(&sb, "X(%d) ", i)
	}

	results := runChatHooks(context.Background(), hooks, runner, sb.String(), nil)

	if len(results) != maxHookMatchesPerTurn {
		t.Fatalf("expected results capped at %d, got %d", maxHookMatchesPerTurn, len(results))
	}
	if len(runner.calls) != maxHookMatchesPerTurn {
		t.Fatalf("expected script calls capped at %d, got %d", maxHookMatchesPerTurn, len(runner.calls))
	}
}

// TestRunChatHooks_MultipleHooks_CapAppliesAcrossHooks proves the cap is a
// combined budget across every hook, not one budget per hook -- once the
// first hook's matches fill it, a second hook's own matching pattern is
// never even reached.
func TestRunChatHooks_MultipleHooks_CapAppliesAcrossHooks(t *testing.T) {
	hooks := []domain.ChatHook{
		{Name: "first", Pattern: `A\((\d+)\)`, Script: "a.sh", Enabled: true},
		{Name: "second", Pattern: `B\((\d+)\)`, Script: "b.sh", Enabled: true},
	}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"a.sh": "a", "b.sh": "b"}}
	answer := "A(1) A(2) A(3) A(4) A(5) B(6) B(7)"

	results := runChatHooks(context.Background(), hooks, runner, answer, nil)

	if len(results) != maxHookMatchesPerTurn {
		t.Fatalf("expected cap of %d across hooks combined, got %d", maxHookMatchesPerTurn, len(results))
	}
	for _, r := range results {
		if r.HookName != "first" {
			t.Fatalf("expected only the first hook's matches to count before the cap was hit, got %+v", r)
		}
	}
}

func TestRunChatHooks_ScriptError_ProducesResultWithErrSetAndOutputEmpty(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "flaky", Pattern: `RUN\((.+?)\)`, Script: "flaky.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{errs: map[string]error{"flaky.sh": errors.New("script timed out")}}

	results := runChatHooks(context.Background(), hooks, runner, "RUN(argument)", nil)

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

func TestRunChatHooks_DisabledHook_NeverMatched(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "off", Pattern: `X\((.+?)\)`, Script: "x.sh", Enabled: false}}
	runner := &fakeHookScriptRunner{}

	results := runChatHooks(context.Background(), hooks, runner, "X(would match if enabled)", nil)

	if len(results) != 0 || len(runner.calls) != 0 {
		t.Fatalf("expected a disabled hook never matched, got results=%v calls=%v", results, runner.calls)
	}
}

func TestRunChatHooks_InvalidRegex_SkippedWithoutPanic(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "bad", Pattern: `(unterminated`, Script: "bad.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}

	results := runChatHooks(context.Background(), hooks, runner, "anything at all", nil)

	if len(results) != 0 || len(runner.calls) != 0 {
		t.Fatalf("expected an uncompilable hook skipped, got results=%v calls=%v", results, runner.calls)
	}
}

func TestRunChatHooks_WrongCaptureGroupCount_Skipped(t *testing.T) {
	hooks := []domain.ChatHook{{Name: "bad-groups", Pattern: `(A)(B)`, Script: "x.sh", Enabled: true}}
	runner := &fakeHookScriptRunner{}

	results := runChatHooks(context.Background(), hooks, runner, "AB", nil)

	if len(results) != 0 || len(runner.calls) != 0 {
		t.Fatalf("expected a hook whose pattern has != 1 capture group skipped, got results=%v calls=%v", results, runner.calls)
	}
}

func TestRunChatHooks_NoHooks_NoResults(t *testing.T) {
	results := runChatHooks(context.Background(), nil, &fakeHookScriptRunner{}, "anything", nil)
	if len(results) != 0 {
		t.Fatalf("expected no results with no hooks configured, got %v", results)
	}
}

// TestRunChatHooks_EnvForwardedUnchangedToRunHookScript proves runChatHooks
// passes its env parameter through to every RunHookScript call verbatim --
// it does not need to interpret env itself, just forward it.
func TestRunChatHooks_EnvForwardedUnchangedToRunHookScript(t *testing.T) {
	hooks := []domain.ChatHook{
		{Name: "web_search", Pattern: `SEARCH\[(.+?)\]`, Script: "web_search.sh", Enabled: true},
	}
	runner := &fakeHookScriptRunner{outputs: map[string]string{"web_search.sh": "results"}}
	env := map[string]string{"WEB_SEARCH_BASE_URL": "http://searxng.example:8888"}

	results := runChatHooks(context.Background(), hooks, runner, "SEARCH[golang]", env)

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
