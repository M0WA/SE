package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// ChatHook is one admin-configured tool: exposed to the model as a native
// OpenAI-compatible tool-calling function (Name/Prompt-as-description/
// Parameters), whenever the model actually invokes it, the single argument
// value it supplied is passed as an argv value to Script (see
// application.runToolCalls) -- a lightweight, deliberately narrow
// tool-calling mechanism (e.g. a "web_search" hook that lets the model ask
// for a follow-up search). Multi-row, like EmbeddingHTTPEndpoint, unlike
// the single-row ChatEndpoint -- an admin can define several hooks over
// time.
type ChatHook struct {
	ID   string
	Name string // human label, e.g. "web_search" -- also the tool's function name, must be unique among active hooks
	// Description is this tool's own description, sent to the model as part
	// of the request's tools list (see application.toolDefsFrom) -- this,
	// together with Name and Parameters, is what tells the model the tool
	// exists and what it's for; unlike Prompt below, it's part of the tool
	// definition itself, not a separate injected message.
	Description string
	// Parameters is a JSON-schema object describing this tool's single
	// argument, e.g. {"type":"object","properties":{"query":{"type":
	// "string"}},"required":["query"]} -- MUST have exactly one property.
	// That property's value (whatever the model supplies when it calls the
	// tool) is the only thing ever passed to Script, and only as an opaque
	// argv value (see runToolCalls's security doc comment). This
	// single-property constraint is what lets Script stay a plain
	// one-argument script (see web_search.sh/web_fetch.sh) regardless of
	// how the argument is named or described. A Parameters value that
	// isn't valid JSON, isn't an object schema, or doesn't have exactly one
	// property is skipped at call time rather than failing the turn --
	// validation at Create/Update time (admin handler layer) is expected to
	// keep this from happening in practice, but rows can pre-date that
	// validation.
	Parameters json.RawMessage
	// Script is a SCRIPT FILENAME ONLY, never a path -- see
	// ports.HookScriptRunner and runToolCalls's security doc comment for
	// why (it is resolved against a fixed, admin-controlled script
	// directory by the runner, never influenced by the model's call itself).
	Script  string
	Enabled bool
	// Prompt is optional extra steering text for this hook -- when non-empty
	// AND this hook is "active" for a turn (see GatedByWebSearch and
	// ChatService.Chat), it is injected as its own leading system message,
	// positioned after the endpoint's persistent SystemPrompt. Native
	// tool-calling already tells the model *what* the tool does and *when*
	// it's callable (via Name + Description + Parameters, sent as part of
	// the request's own tools list) -- Prompt is for guidance beyond that,
	// e.g. "prefer fetching the top 3 results". Empty Prompt means no
	// hook-specific message is added, even when the hook is active.
	Prompt string
	// GatedByWebSearch, when true, ties this hook's activation (and its
	// Prompt injection, and whether it's even offered to the model as a
	// tool at all) to the SAME effective web-search toggle that already
	// gates the deterministic web-search context injection
	// (endpoint.WebSearchEnabled, overridden per-question by
	// ChatOptions.WebSearch) -- the existing public chat UI's "Web"
	// checkbox, no new UI control needed. false (the default, for a
	// hypothetical future non-web hook) means this hook is active whenever
	// Enabled is true, unaffected by the Web toggle -- today's existing
	// behavior.
	GatedByWebSearch bool
}

// toolParametersSchema is the minimal shape ChatHook.Parameters is parsed
// as -- just enough to enforce and read the "exactly one property" rule,
// not a general JSON-schema validator.
type toolParametersSchema struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// SingleParameterName returns the lone property name in h.Parameters, or an
// error if Parameters isn't valid JSON, isn't an object schema, or doesn't
// have exactly one property -- see Parameters's own doc comment for why
// every hook's schema is constrained this way.
func (h ChatHook) SingleParameterName() (string, error) {
	var schema toolParametersSchema
	if err := json.Unmarshal(h.Parameters, &schema); err != nil {
		return "", fmt.Errorf("parameters is not valid JSON: %w", err)
	}
	if len(schema.Properties) != 1 {
		return "", fmt.Errorf("parameters must have exactly one property, got %d", len(schema.Properties))
	}
	for name := range schema.Properties {
		return name, nil
	}
	return "", fmt.Errorf("parameters must have exactly one property")
}

// ChatHookResult is the outcome of running one tool-called ChatHook's
// script once, surfaced on ChatResult so the caller can decide how (or
// whether) to show it.
type ChatHookResult struct {
	HookName string
	// ToolCallID correlates this result back to the ToolCall.ID it answers
	// -- see application.toolResultMessages, which builds one
	// ChatMessage{Role: ChatRoleTool, ToolCallID: ...} per result.
	ToolCallID string
	// Input is the model-supplied argument's own raw text passed to Script
	// -- the tool call's own argument (e.g. a URL for a "fetch"-named hook,
	// a search term for a "search"-named hook), surfaced here so the UI can
	// show what was actually requested without parsing Output.
	Input  string
	Output string // the script's raw stdout
	Err    string // non-empty if the script failed or timed out, or the call was skipped (unknown hook, malformed arguments, or the per-turn cap was hit); Output is empty then
}

// ChatHookIDPattern is every valid ChatHook.ID -- same shape as
// EmbeddingEndpointIDPattern (lowercase alphanumeric/underscore, 1-20
// characters): short enough to fit the sqlrepo MySQL dialect's chat_hooks.id
// VARCHAR(20) column.
var ChatHookIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,20}$`)

// NewChatHookID derives an ID from a display name the same way
// NewEmbeddingEndpointID does (lowercased, non-alphanumeric runs collapsed,
// trimmed to fit ChatHookIDPattern), appending the shortest numeric suffix
// that avoids colliding with a key in existing (every other configured
// hook's ID). Falls back to a timestamp-derived ID if name has no
// alphanumeric characters.
func NewChatHookID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "hook")
}
