package domain

import "encoding/json"

// MCPServer is one admin-configured MCP (Model Context Protocol) server
// connection -- unlike the old ChatHook (one row = one script-backed tool),
// an MCPServer row is a CONNECTION exposing a dynamically-discovered set of
// tools. Multi-row, like EmbeddingHTTPEndpoint. See ChatService.Chat and
// ports.MCPToolProvider for how a turn discovers/calls tools per server.
type MCPServer struct {
	ID   string
	Name string // human label shown in the admin UI only, never sent to the model
	// Transport is "stdio" (a local process, spawned per chat turn -- see
	// ports.MCPToolProvider) or "http" (remote Streamable-HTTP, via BaseURL/APIKey).
	Transport string
	// Command/Args configure a "stdio" server -- Command runs directly
	// (never through a shell). Unlike ChatHook.Script's fixed script
	// directory, Command may be any path: same trust tier as configuring a
	// crawl schedule or an embedding endpoint's base URL.
	Command string
	Args    []string
	// BaseURL/APIKey configure an "http" server -- APIKey is optional, sent
	// as a Bearer token, opaque here (encryption happens in restapi).
	BaseURL string
	APIKey  string
	Enabled bool
	// Prompt is optional steering text for this server's tools collectively
	// -- injected as its own system message (after SystemPrompt) when this
	// server is active. MCP has no per-server prompt concept of its own, so
	// this is where a cross-tool workflow instruction lives (e.g. "after
	// searching, fetch results from a few different domains, not just the
	// top-ranked ones -- if a fetch is blocked, try a different domain").
	// Empty adds no message.
	Prompt string
	// GatedByWebSearch ties this server's activation to the same effective
	// "Web" toggle that gates web-search context injection
	// (WebSearchEnabled/ChatOptions.WebSearch). false (default) means
	// active whenever Enabled is true, unaffected by the toggle.
	GatedByWebSearch bool
	// SelfService is true for a row from a user's own personal server
	// store, false for the shared admin catalog -- set by the caller
	// merging both into one slice, never persisted. mcpclient refuses
	// "stdio" transport when true, defense-in-depth against a personal row
	// reaching local command execution.
	SelfService bool
}

// MCPTool is one tool discovered from an active MCPServer's tools/list
// response this turn, tagged with its origin server (ServerID) for routing
// a tool call back. Not admin-configured; rediscovered every turn.
type MCPTool struct {
	ServerID    string
	ServerName  string
	Name        string
	Description string
	InputSchema json.RawMessage // raw JSON schema object, as the server declared it
}

// NewMCPServerID derives an ID from a display name the same way NewUserID
// does, appending the shortest numeric suffix that avoids colliding with
// existing.
func NewMCPServerID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "mcp")
}
