package domain

// Agent is an admin-defined specialization: a named, static system prompt
// plus an optional scope over the global MCPServer catalog. Reusable in two
// modes application.ChatService.Chat will grow to support -- single-agent
// (one Agent selected for a whole conversation, addressed by
// ChatEndpoint.DefaultAgentID or a per-question override) and multi-agent
// deep research (several Agents fanned out over sub-questions, each its own
// independent turn). Multi-row, like MCPServer -- an admin can define
// several agents over time.
type Agent struct {
	ID   string
	Name string // human label, shown in the admin UI and any agent picker
	// Description is never injected into this agent's own conversation --
	// it exists purely for something ELSE to reason about this agent: a
	// multi-agent planner deciding which agent fits a sub-question, or a
	// person picking an agent from a dropdown. Keep this separate from
	// SystemPrompt: one explains the agent, the other IS the agent.
	Description string
	// SystemPrompt is this agent's specialization -- injected as its own
	// leading system-role message in every turn it's active for, after the
	// endpoint's persistent SystemPrompt and any per-user CustomPrompt (see
	// application.ChatService.Chat). Empty means this Agent adds no message
	// of its own beyond what every conversation already gets.
	SystemPrompt string
	// MCPServerIDs, when non-empty, restricts which rows of the GLOBAL
	// MCPServer catalog this agent may use tools from -- an empty list
	// means every globally active server is available, the same as not
	// having an Agent selected at all. This scope only ever narrows the
	// shared/admin catalog: a user's own per-user MCP servers (a separate
	// store) are always available to every Agent regardless of this list.
	MCPServerIDs []string
	Enabled      bool
}

// NewAgentID derives an ID from a display name the same way NewMCPServerID
// does (lowercased, non-alphanumeric runs collapsed, trimmed to fit
// SlugIDPattern), appending the shortest numeric suffix that avoids
// colliding with a key in existing (every other configured agent's ID).
// Falls back to a timestamp-derived ID if name has no alphanumeric
// characters.
func NewAgentID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "agent")
}
