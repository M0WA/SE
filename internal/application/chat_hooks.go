package application

import (
	"context"
	"encoding/json"
	"fmt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxHookMatchesPerTurn bounds how many ChatHookResult a single call to
// runToolCalls can produce, across all tool calls combined -- a defensive
// cap against an adversarial completion (via indirect prompt injection, see
// the security note below) engineered to request many tool calls and force
// many script executions in one turn.
const maxHookMatchesPerTurn = 5

// runToolCalls runs, for each of the model's toolCalls, the matching
// enabled hook's script (matched by Name) with the call's single argument
// as an argv value (plus env, forwarded to every RunHookScript call
// unchanged -- runToolCalls does not need to interpret it, just pass it
// through; see ports.HookScriptRunner's doc comment for what it carries and
// why). ALWAYS returns exactly one ChatHookResult per input ToolCall, in
// the same order, correlated by ToolCallID -- native tool-calling requires
// a matching tool-role response for every tool_call in the preceding
// assistant message (see application.toolResultMessages), so even a call
// this function can't actually run (naming a hook that's disabled or
// unknown, whose Parameters don't resolve to exactly one property -- see
// domain.ChatHook.SingleParameterName --, whose Arguments don't carry that
// property, or one beyond the per-turn maxHookMatchesPerTurn cap) still
// gets a result, with Err explaining why instead of a script ever running.
// Best-effort otherwise: a script error/timeout also just produces a
// ChatHookResult with Err set, never fails the whole chat turn -- the
// model's own answer always reaches the user regardless.
//
// SECURITY: a tool call's Arguments come from the model's own OUTPUT, which
// can itself be influenced by untrusted web content when search context is
// enabled (indirect prompt injection). The ONLY thing a tool call can ever
// influence is an argv VALUE passed to a script whose IDENTITY was fixed by
// the admin ahead of time (hook.Script, never derived from the call
// itself) -- this function and every ports.HookScriptRunner implementation
// must NEVER build a shell command line by string concatenation/
// interpolation; the argument is always passed as a real argv slice (e.g.
// exec.CommandContext(ctx, path, args...)), never through `sh -c` or
// similar. A tool call's argument is passed as an opaque argument; it is
// never evaluated, sourced, or interpreted as a path/URL/command by Go
// code.
func runToolCalls(ctx context.Context, hooks []domain.ChatHook, runner ports.HookScriptRunner, toolCalls []domain.ToolCall, env map[string]string) []domain.ChatHookResult {
	byName := make(map[string]domain.ChatHook, len(hooks))
	for _, h := range hooks {
		if h.Enabled {
			byName[h.Name] = h
		}
	}

	results := make([]domain.ChatHookResult, 0, len(toolCalls))
	for i, tc := range toolCalls {
		results = append(results, runOneToolCall(ctx, byName, runner, i, tc, env))
	}
	return results
}

// runOneToolCall handles a single ToolCall for runToolCalls, i being its
// position in the full toolCalls slice (used for the per-turn cap).
func runOneToolCall(ctx context.Context, byName map[string]domain.ChatHook, runner ports.HookScriptRunner, i int, tc domain.ToolCall, env map[string]string) domain.ChatHookResult {
	result := domain.ChatHookResult{HookName: tc.Name, ToolCallID: tc.ID}
	if i >= maxHookMatchesPerTurn {
		result.Err = "too many tool calls this turn"
		return result
	}
	h, found := byName[tc.Name]
	if !found {
		result.Err = "no matching enabled chat hook"
		return result
	}
	arg, err := singleToolCallArgument(h, tc)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	result.Input = arg
	output, err := runner.RunHookScript(ctx, h.Script, []string{arg}, env)
	if err != nil {
		result.Err = err.Error()
	} else {
		result.Output = output
	}
	return result
}

// singleToolCallArgument resolves a tool call's single argument value:
// h.Parameters must describe exactly one property (see
// domain.ChatHook.SingleParameterName), and tc.Arguments (the model's raw
// JSON object text) must actually carry a string value for it.
func singleToolCallArgument(h domain.ChatHook, tc domain.ToolCall) (string, error) {
	name, err := h.SingleParameterName()
	if err != nil {
		return "", fmt.Errorf("parameters: %w", err)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return "", fmt.Errorf("arguments is not a valid JSON object: %w", err)
	}
	value, ok := args[name]
	if !ok {
		return "", fmt.Errorf("arguments missing required property %q", name)
	}
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("property %q must be a string, got %T", name, value)
	}
	return s, nil
}
