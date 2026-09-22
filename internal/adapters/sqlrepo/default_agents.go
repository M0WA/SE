package sqlrepo

import (
	"context"
	"fmt"

	"searchengine/internal/domain"
)

// defaultAgents is a small, ready-to-use starter set covering the common
// chat personas this deployment's own tooling (web_search/web_fetch,
// get_datetime) supports -- see docs/configuration.md's "Suggested global
// agents" section for the rationale behind each one. Every ID here is a
// fixed literal, not domain.NewAgentID-minted, since seedDefaultAgents only
// ever runs once (see its own doc comment) -- there is no existing-ID set
// to dedupe against.
//
// Each one starts with an empty MCPServerIDs -- which now means NO global
// tools at all, not "every tool" (see domain.Agent.MCPServerIDs' own doc
// comment) -- deliberately, not as an oversight: a real MCP server's own ID
// is admin-configured, deployment-specific data (e.g. "web_tools" here,
// "mcp_web_1" there) that this package has no safe way to guess or
// hardcode. An admin wanting Researcher/Current events/Deep research/This
// index to actually use web tools scopes them to whichever server row
// actually exists on THIS deployment via the agent's own edit page, same as
// they would for any agent they created themselves.
var defaultAgents = []domain.Agent{
	{
		ID:          "researcher",
		Name:        "Researcher",
		Description: "Verifies claims against sources before answering; cites what it used.",
		SystemPrompt: "Before answering, search for and read at least one primary source when " +
			"the question involves current facts, statistics, or claims you're not certain of. " +
			"Cite the URLs you actually used. If sources disagree, say so rather than picking " +
			"one silently.",
		Enabled: true,
	},
	{
		ID:          "quick_answer",
		Name:        "Quick answer",
		Description: "Short, direct answers with no tool calls unless the question truly needs current information.",
		SystemPrompt: "Answer in 2-4 sentences unless asked for more. Only search the web if " +
			"the answer genuinely requires something you can't know (today's date, a very " +
			"recent event, a specific live fact) -- don't search reflexively.",
		Enabled: true,
	},
	{
		ID:          "this_index",
		Name:        "This index",
		Description: "Prioritizes this instance's own indexed documents over the open web.",
		SystemPrompt: "This deployment has its own search index blended into web search " +
			"results via a SearXNG engine. When answering, prefer and explicitly call out " +
			"results that come from this instance's own index before reaching for the open web.",
		Enabled: true,
	},
	{
		ID:          "current_events",
		Name:        "Current events",
		Description: "For \"what's happening now\" questions -- always checks the date and searches fresh.",
		SystemPrompt: "Call get_datetime first to know today's date before reasoning about " +
			"anything time-sensitive. Always search rather than relying on training knowledge " +
			"for anything that could have changed.",
		Enabled: true,
	},
	{
		ID:          "deep_research",
		Name:        "Deep research",
		Description: "Slower, more thorough -- cross-checks multiple sources before answering.",
		SystemPrompt: "After searching, fetch and read at least the top 2-3 relevant results " +
			"before answering, not just the search snippets. Cross-check claims across sources " +
			"when they matter. Cite what you actually read.",
		Enabled: true,
	},
}

// SeedDefaultAgents inserts defaultAgents the FIRST time this database's
// agents table is completely empty. A per-row "INSERT ... ON CONFLICT (id)
// DO NOTHING" (the content_dedup_lock sentinel row's own pattern) would
// resurrect exactly that row on every subsequent restart, which is right
// for an internal sentinel but wrong for admin-owned, individually-
// deletable content like this: as long as at least one agent (seeded or
// the admin's own) still exists, deleting any ONE default sticks across
// restarts, since the table is never empty again. The one caveat: deleting
// EVERY agent back down to zero rows is indistinguishable from "a
// genuinely fresh database" by this check, so the next restart re-seeds
// all five -- accepted as an edge case too narrow (a deliberate full
// reset) to design a persisted "already seeded" flag around.
//
// Deliberately NOT wired into migrate() -- migrate() runs for every
// process (search/admin/crawl) AND every test fixture that opens a
// Repository, and seeding agent rows there would silently inflate every
// sqlrepo test's fresh, otherwise-empty database. Agents are an
// admin-facing concern, so cmd/admin's own main() calls this explicitly,
// once, right after opening the DB -- search-server/crawl-server never
// call it, and a fresh test repository stays genuinely empty unless a test
// calls it itself.
func (r *Repository) SeedDefaultAgents(ctx context.Context) error {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&count); err != nil {
		return fmt.Errorf("counting agents: %w", err)
	}
	if count > 0 {
		return nil
	}
	for _, a := range defaultAgents {
		if err := r.CreateAgent(ctx, a); err != nil {
			return fmt.Errorf("seeding default agent %q: %w", a.ID, err)
		}
	}
	return nil
}
