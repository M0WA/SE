# Agents

[← Manual home](README.md)

*`/admin/agents`*

The Agents list, under Settings -> Chat -> Agents, shows every agent you've defined for the chat page and lets you add a new one or open an existing one for editing.

![Agents](images/agents.png)

## What an agent is

An agent is a named specialization: a fixed system prompt bundled with a scoped subset of the MCP servers configured on the MCP servers page. Instead of writing a persona into the endpoint's one global system prompt (Settings -> Chat -> Settings) and living with it on every conversation, you define a handful of agents -- a fact-checker, a code reviewer, a terse summarizer -- each with its own instructions and its own set of tools, and let whoever is chatting pick the one that fits the question in front of them. Nothing about an agent changes the underlying model or endpoint; it only adds a leading instruction and narrows tool access for the turns it's active on.

## The list and its columns

Each row shows the agent's name, its description, and whether it's enabled. Name and description are exactly what a person picking an agent from the chat page's dropdown sees, so keep the description short and about the agent's purpose rather than about how it's built -- it's read by a human (or, eventually, a multi-agent planner) deciding whether this agent fits the question at hand, and it is never sent to the model as part of that agent's own conversation. Click a row's Edit icon (✎) to open its full detail page, or the Delete (trash) icon to remove it immediately after a confirmation -- there's no undo, so a mistaken delete means re-entering the system prompt from scratch.

## Adding an agent

Click "Add agent" to open a blank detail page (this takes you to /admin/agents/new, the same form as editing, just with empty fields and "Enabled" checked by default). Nothing is saved until you fill in a name and submit -- see the agent-detail page for what each field does.

## Enabled vs. disabled

Disabling an agent removes it from the chat page's agent picker (GET /agents, which the chat UI uses to build its dropdown, only returns enabled agents) without deleting its configuration. Use this to retire an agent temporarily -- while you're still tuning its system prompt, say -- without losing the work, or to keep an agent around as a default-agent candidate (Settings -> Chat -> Settings lets you pick a disabled agent as the endpoint's default ahead of turning it on) before rolling it out.

## If the page says agents aren't configured

The agents feature depends on an agent store being wired into the admin server; if it isn't, every agents endpoint returns 503 and the list page shows a "not configured" status instead of a table. This mirrors how the MCP servers and other optional admin features degrade -- it's a deployment/configuration state, not something you can fix by clicking around this page.

## Suggested global agents

A fresh install seeds five ready-to-use starter agents automatically, the first time the agents table is completely empty -- delete one and it stays deleted; delete all five back down to zero and the next restart re-seeds them, since that's indistinguishable from a genuinely fresh database. Each one starts with an **empty MCP-server scope** deliberately, not as an oversight: a real MCP server's row ID is deployment-specific data nothing can safely guess. To actually let one use web search, open it and check the box for whatever server row this deployment's web-search MCP server is configured as.

| Agent | Purpose | Needs an MCP server scoped to work as intended? |
|---|---|---|
| Researcher | Fact-checking generalist -- searches for and cites a primary source before answering anything it isn't certain of. | Yes (web search/fetch) |
| Quick answer | Short, direct answers; only searches when the question genuinely needs current information. A good default-agent pick. | No (works fine with zero tools; searches opportunistically if scoped) |
| This index | Prioritizes this deployment's own indexed documents over the open web. | Yes (web search/fetch) |
| Current events | For "what's happening now" questions -- calls get_datetime first, then always searches rather than trusting training knowledge. | Yes (web search/fetch, plus the get_datetime MCP server if configured) |
| Deep research | Slower and more thorough -- fetches and reads the top few results before answering, not just search snippets. | Yes (web search/fetch) |

> **Worth knowing:**
> - An agent with an empty MCP-server scope isn't "unrestricted" -- it gets no global MCP tools at all. See the agent-detail page's MCP-servers section for the full explanation.
> - Deleting an agent that's currently set as a chat endpoint's default agent (Settings -> Chat -> Settings) doesn't clear that setting for you -- the stored default_agent_id just stops matching anything, and turns fall back to running with no agent specialization.

---
← [MCP Server detail](mcp-server-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Agent detail](agent-detail.md) →
