package application

import (
	"context"
	"encoding/json"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// toolDefsFrom builds the tools list offered to the model from this turn's
// discovered MCP tools. A tool whose InputSchema is empty/invalid is still
// offered as-is (Complete doesn't validate InputSchema, only the connected
// MCP server itself does, when and if the model actually calls it) -- an
// empty InputSchema is defaulted to a bare no-properties object schema so
// the request never sends invalid JSON.
func toolDefsFrom(tools []domain.MCPTool) []domain.ToolDef {
	if len(tools) == 0 {
		return nil
	}
	out := make([]domain.ToolDef, len(tools))
	for i, t := range tools {
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = domain.ToolDef{Name: t.Name, Description: t.Description, Parameters: params}
	}
	return out
}

// runToolCalls runs every call in calls against session, producing exactly
// one domain.ToolCallResult per input call, even a failed one -- so
// toolResultMessages' one-message-per-tool_call invariant always holds. A
// nil session (no MCP servers active this turn) fails every call the same
// way a session that errors on CallTool would, rather than special-casing
// nil at each call site.
func runToolCalls(ctx context.Context, session ports.MCPSession, calls []domain.ToolCall) []domain.ToolCallResult {
	results := make([]domain.ToolCallResult, len(calls))
	for i, c := range calls {
		results[i] = domain.ToolCallResult{ToolName: c.Name, ToolCallID: c.ID, Arguments: c.Arguments}
		if session == nil {
			results[i].Err = "no MCP tool session is active for this turn"
			continue
		}
		output, err := session.CallTool(ctx, c.Name, c.Arguments)
		if err != nil {
			results[i].Err = err.Error()
			continue
		}
		results[i].Output = output
	}
	return results
}

// toolResultMessages builds one domain.ChatMessage{Role: ChatRoleTool} per
// result, correlated to the tool call it answers via ToolCallID -- native
// tool-calling expects exactly one such message per tool_call in the
// preceding assistant message (see runToolCalls, which always produces
// exactly one domain.ToolCallResult per input ToolCall, even a
// skipped/failed one, specifically so this invariant holds). A
// failed/skipped call's message carries its Err text instead of Output, so
// the model can say it couldn't complete the tool call rather than being
// left to guess why nothing came back.
func toolResultMessages(results []domain.ToolCallResult) []domain.ChatMessage {
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
