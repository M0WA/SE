# Agent detail

[← Manual home](README.md)

*`/admin/agents/{id}`*

The agent detail page, reached from the Agents list's Edit link or "Add agent" link, is where you write an agent's system prompt, describe it, scope its MCP tool access, and enable or disable it.

![Agent detail](images/agent-detail.png)

## Name and description

Name is the required label shown everywhere this agent appears: this list, the endpoint's default-agent dropdown, and the chat page's own agent picker. Description is read by whoever (or, eventually, whatever planning logic) is choosing an agent to hand a question to -- it is never injected into the model's context for a conversation running under this agent, so write it as "what this agent is good for" (e.g. "Verifies claims against sources before including them"), not as build notes about the prompt itself.

## System prompt

This is the field that actually makes the agent do anything: its contents are injected as the agent's own leading system message on every turn it's active for, layered in after the chat endpoint's persistent system prompt (Settings -> Chat -> Settings) and any per-user custom prompt, and before the conversation history. This is where the specialization lives -- write it the way you'd write any system prompt: concrete instructions and constraints, not a description of the agent. An empty system prompt is valid and means this agent adds nothing beyond what every conversation already gets, which only makes sense combined with a restricted MCP-server scope (an agent that exists purely to gate tool access, not to change behavior).

## MCP servers

This checkbox list scopes which of the globally configured MCP servers (Settings -> Chat -> MCP servers) this agent may call tools from. There is no "unscoped" option: leaving every box unchecked does not mean "every server is available" -- it means this agent gets no global tools at all. If you want an agent to have access to everything, check every box explicitly. This restriction only narrows the shared/admin catalog; it never touches a user's own personal MCP servers, which stay available to every agent regardless of what's checked here. A turn with no agent selected at all skips this scoping entirely and offers every globally active server, so the restrictive behavior only kicks in once an agent is actually active for that turn.

## Enabled

Controls whether this agent shows up in the chat page's own agent picker (via GET /agents, which filters to enabled agents only). A disabled agent still exists, can still be edited, and can still be picked as a chat endpoint's default agent ahead of time -- disabling just keeps it out of the day-to-day picker while you're not ready for people to select it themselves.

## Saving, and how the ID is derived

Save validates only that Name is non-empty -- MCP-server IDs aren't cross-checked against the actual server catalog, so if you later delete a server this agent references, nothing breaks; the stale ID just never matches anything at chat time. On first save, the agent's ID is minted from its name (lowercased, punctuation collapsed, deduplicated against every other agent's ID) and is permanent from then on -- renaming an agent later changes its display name everywhere but never its ID, so a chat endpoint's stored default_agent_id or a saved chat tab's agent selection keeps working across a rename.

## Deleting an agent

The Delete button (shown once you're editing an existing agent, not on the "Add agent" form) removes the agent immediately after a confirmation prompt and returns you to the Agents list. There's no recovery -- if you want to keep the configuration around but stop using it, disable the agent instead of deleting it.

## How a chat user picks this agent, and how it relates to the default agent

On the public chat page, an agent picker dropdown (populated from every enabled agent) lets a person choose which agent handles the conversation in the active tab; their choice is remembered per tab and sent with every message as agent_id. If they leave the picker on "Default agent," the conversation instead uses whatever agent is set as DefaultAgentID on the chat endpoint's settings (Settings -> Chat -> Settings -> "Default agent") -- which can be any configured agent, enabled or not, since an admin may want to line up a default ahead of enabling it for general selection. If no default is set and the user doesn't pick one either, the conversation runs with no agent specialization at all, exactly like the system behaved before agents existed.

> **Worth knowing:**
> - Because MCP-server scoping is all-or-nothing per box, an agent meant to have broad tool access needs its boxes re-checked whenever a new MCP server is added globally -- new servers don't automatically join an existing agent's scope.
> - The system prompt textarea has no length limit enforced by the form; keep in mind it's injected on every turn this agent is active for, so an overly long prompt eats into the conversation's token budget alongside the endpoint's own system prompt and history.

---
← [Agents](agents.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Settings](settings.md) →
