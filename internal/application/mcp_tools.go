package application

import (
	"context"
	"encoding/json"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// toolDefsFrom builds the tools list offered to the model from this turn's
// discovered MCP tools. An empty InputSchema is defaulted to a bare
// no-properties object schema so the request never sends invalid JSON.
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

// runToolCalls runs every call against session, producing exactly one
// ToolCallResult per input call, even a failed one, so
// toolResultMessages' one-message-per-tool_call invariant holds. A nil
// session fails every call the same way an erroring one would.
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

// toolResultMessages builds one ChatMessage{Role: ChatRoleTool} per
// result, correlated via ToolCallID -- native tool-calling expects exactly
// one such message per preceding tool_call (see runToolCalls). A
// failed/skipped call carries its Err text instead of Output.
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
