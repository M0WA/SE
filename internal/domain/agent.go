package domain

// Agent is an admin-defined specialization: a named, static system prompt
// plus an optional scope over the global MCPServer catalog. Multi-row, like
// MCPServer -- an admin can define several over time, selected per
// conversation via ChatEndpoint.DefaultAgentID or a per-question override.
type Agent struct {
	ID   string
	Name string // human label, shown in the admin UI and any agent picker
	// Description is never injected into the conversation -- it's for
	// something ELSE to reason about this agent (a picker, a planner), not
	// for the agent itself; that's what SystemPrompt is for.
	Description string
	// SystemPrompt is injected as a leading system-role message whenever
	// this agent is active, after the endpoint's own SystemPrompt/CustomPrompt.
	SystemPrompt string
	// MCPServerIDs scopes which GLOBAL MCPServer rows this agent may use.
	// Empty means NO global tools, not all of them -- select explicitly to
	// grant every server. Doesn't affect per-user MCP servers, which are
	// always available. Only consulted while this agent is active; see
	// ChatService.Chat's agentActive check.
	MCPServerIDs []string
	Enabled      bool
}

// AllowsServer reports whether serverID is in MCPServerIDs (false for an
// empty list -- see its doc comment). Only meaningful while this agent is
// active; a caller with no agent selected must not call this.
func (a Agent) AllowsServer(serverID string) bool {
	for _, id := range a.MCPServerIDs {
		if id == serverID {
			return true
		}
	}
	return false
}

// NewAgentID derives an ID from a display name like NewMCPServerID does,
// appending the shortest numeric suffix that avoids colliding with existing.
func NewAgentID(name string, existing map[string]bool) string {
	return mintSlugID(name, existing, "agent")
}
