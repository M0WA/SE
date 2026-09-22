package domain

import "encoding/json"

// MCPServer is one admin-configured MCP (Model Context Protocol) server
// connection -- unlike the old ChatHook (one row = one script-backed tool,
// removed in favor of this), an MCPServer row is a CONNECTION that can
// expose an arbitrary, dynamically-discovered set of tools (its own
// tools/list response), each with full structured arguments. Multi-row,
// like EmbeddingHTTPEndpoint -- an admin can configure several servers over
// time. See application.ChatService.Chat and ports.MCPToolProvider for how
// a turn discovers and calls tools across every active server.
type MCPServer struct {
	ID   string
	Name string // human label, e.g. "web tools" -- shown in the admin UI only, never sent to the model (unlike ChatHook.Name, which doubled as the tool's own function name)
	// Transport selects how this server is reached: "stdio" (a local
	// process, spawned fresh per chat turn and kept open for the turn's
	// duration -- see ports.MCPToolProvider) or "http" (a remote
	// Streamable-HTTP MCP endpoint, reached via BaseURL/APIKey).
	Transport string
	// Command/Args configure a "stdio" server -- Command is executed
	// directly (never through a shell), Args passed as real argv elements.
	// Unlike ChatHook.Script, which was resolved only against a fixed,
	// admin-controlled script directory, Command may be any path -- an
	// admin configuring a stdio MCP server is granted the same trust tier
	// as one configuring a crawl schedule or an embedding endpoint's own
	// base URL: real, but no narrower sandbox than that.
	Command string
	Args    []string
	// BaseURL/APIKey configure an "http" server -- APIKey is optional,
	// sent as a Bearer token; opaque at this layer (encryption happens in
	// restapi, same convention as ChatEndpoint.APIKey/
	// EmbeddingHTTPEndpoint.APIKey).
	BaseURL string
	APIKey  string
	Enabled bool
	// Prompt is optional extra steering text for this server's tools
	// collectively -- when non-empty AND this server is active for a turn
	// (see GatedByWebSearch and ChatService.Chat), it is injected as its
	// own leading system message, positioned after the endpoint's
	// persistent SystemPrompt. Each tool's own Name/Description/InputSchema
	// (sent as part of the turn's tools list) already tell the model *what*
	// each tool does and *when* to use it -- MCP's protocol has no
	// per-server prompt concept of its own, so Prompt is where a
	// cross-tool workflow instruction still lives (e.g. "after searching,
	// fetch the top 3 results with the fetch tool before answering").
	// Empty means no server-specific message is added, even when active.
	Prompt string
	// GatedByWebSearch, when true, ties this server's activation (and
	// whether its tools are even offered to the model at all) to the SAME
	// effective web-search toggle that already gates the deterministic
	// web-search context injection (endpoint.WebSearchEnabled, overridden
	// per-question by ChatOptions.WebSearch) -- the existing public chat
	// UI's "Web" checkbox, no new UI control needed. false (the default)
	// means this server is active whenever Enabled is true, unaffected by
	// the Web toggle.
	GatedByWebSearch bool
	// SelfService is true for a row sourced from ports.UserMCPServerStore
	// (a regular user's own personal server, see ChatService.Chat), false
	// for one sourced from the admin-configured ports.MCPServerStore
	// catalog -- set by the caller building a turn's active server list,
	// never persisted (each store's own CRUD already knows unambiguously
	// which one it is; this field only exists to travel WITH the value
	// once the two catalogs are merged into one slice for
	// ports.MCPToolProvider.Open). mcpclient refuses "stdio" transport
	// outright when this is true, as its own defense-in-depth against a
	// personal row ever reaching real local command execution -- see its
	// own doc comment -- independent of (not a replacement for) the
	// filtering ChatService.Chat already does before a personal server
	// ever reaches Open at all.
	SelfService bool
}

// MCPTool is one tool discovered from an active MCPServer's own tools/list
// response for this turn -- tagged with its origin server (ServerID) so a
// returned tool call can be routed back to the right connection. Not
// admin-configured; rediscovered fresh every turn (see
// ports.MCPToolProvider.Open).
type MCPTool struct {
	ServerID    string
	ServerName  string
	Name        string
	Description string
	InputSchema json.RawMessage // raw JSON schema object, as the server declared it
}

// NewMCPServerID derives an ID from a display name the same way NewUserID
// does (lowercased, non-alphanumeric runs collapsed, trimmed to fit
// SlugIDPattern), appending the shortest numeric suffix that avoids
// colliding with a key in existing (every other configured server's ID).
// Falls back to a timestamp-derived ID if name has no alphanumeric
// characters.
func NewMCPServerID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "mcp")
}
