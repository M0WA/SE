package sqlrepo

import (
	"context"
	"fmt"

	"searchengine/internal/domain"
)

// defaultAgents is a small starter set of personas for this deployment's
// tooling (web_search/web_fetch, get_datetime, run_python/run_go,
// read_file_base64) -- see docs/manual/agents.md's "Suggested global
// agents" for rationale. IDs are fixed literals, not domain.NewAgentID-
// minted, since SeedDefaultAgents only ever runs once (no existing-ID set
// to dedupe against).
//
// Each starts with empty MCPServerIDs -- meaning NO global tools, not
// "every tool" (see domain.Agent.MCPServerIDs) -- deliberately: a real MCP
// server ID is admin-configured, deployment-specific data this package
// can't guess. An admin wanting web/sandbox tools scopes them via the
// agent's edit page, to whichever server row actually exists here.
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
	{
		ID:          "image_analyst",
		Name:        "Image analyst",
		Description: "Analyzes an uploaded image's content and extracts any text in it (OCR).",
		// The sandbox's container root is read-only (see dockersandbox's
		// doc comment), ruling out tesseract-ocr's usual apt-get install.
		// easyocr is used instead since it's pure pip (bundles its own
		// models) -- at the cost of a large, uncached download every call,
		// so this agent needs a generous -timeout (minutes) and -memory
		// on its sandbox server row.
		SystemPrompt: "When asked to analyze an image or read text out of one, first call " +
			"read_file_base64 to get its base64 content (list_files first if you don't already " +
			"have the file id). Then call run_python with packages [\"Pillow\", \"easyocr\"] and code " +
			"that: decodes the base64 into bytes, loads it with PIL.Image to report its size/format " +
			"and describe what you can determine visually from pixel data (dominant colors, " +
			"aspect ratio), and separately runs easyocr.Reader(['en']).readtext(...) on it to extract " +
			"any text present, printing everything as plain text. This first call will be slow " +
			"(easyocr downloads its models fresh every time, nothing persists between sandbox " +
			"calls) -- warn the user this may take a while rather than assuming it failed. If the " +
			"call times out, say so plainly and suggest the admin raise this sandbox server's " +
			"-timeout.",
		Enabled: true,
	},
}

// SeedDefaultAgents inserts defaultAgents only the FIRST time the agents
// table is completely empty. Unlike a per-row "ON CONFLICT DO NOTHING"
// (content_dedup_lock's pattern, right for a sentinel), this lets deleting
// any one default stick across restarts, since the table is never empty
// again as long as one agent remains. Caveat: deleting every agent back to
// zero re-seeds all six on the next restart -- accepted as too narrow an
// edge case (a deliberate full reset) to persist an "already seeded" flag for.
//
// Deliberately NOT wired into migrate(), which runs for every process and
// test fixture -- seeding there would inflate every sqlrepo test's empty
// database. cmd/admin's main() calls this explicitly, once; search/crawl
// never do, so a test repository stays empty unless it calls it itself.
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
