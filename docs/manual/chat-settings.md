# Chat Settings

[← Manual home](README.md)

*`/admin/chat/settings`*

This page configures the chat-completions backend that powers Chat mode on the public search page, plus the system prompt, default agent, web-search behavior, and context-length budget every chat turn uses. It's found under Chat -> Settings in the admin sidebar.

![Chat Settings](images/chat-settings.png)

## Chat endpoint

Enabled turns the Chat mode toggle on the public search page on or off — leave it off and visitors never see a chat option at all, regardless of what else is configured here. Base URL is the OpenAI-compatible chat-completions API root, e.g. http://localhost:8000/v1 for a self-hosted vLLM instance; the server posts to <base_url>/chat/completions, so paste the root, not the full completions path. Model is sent verbatim as the request's model field — it has to match a model name the backend at Base URL actually serves, so check that server's own model list before typing one in. API key is sent as an Authorization: Bearer header and is optional for a local, unauthenticated server; the field is always blank when you open this page even if a key is already stored (the server never echoes a saved key back), so leaving it blank on save keeps whatever key is already there. Check Remove the stored API key when you want to actually clear it — it's disabled until a key is on file, and it's the only way to get back to "no key" once one's been saved.

## System prompt

This text is injected as a system message ahead of every turn, for every question, regardless of which agent (if any) is selected. It never appears in the chat transcript itself — visitors never see it — and it's the one piece of context that's never dropped even when older conversation history gets trimmed to fit the token budget below. Use it for instance-wide instructions that should always apply (tone, scope, disclaimers), and leave agent-specific instructions to the Agents page instead so they only apply when that agent is picked.

## Agent

Default agent picks one of the agents defined on Chat -> Agents to specialize every conversation by default; its own system prompt is injected in addition to (after) the instance-wide system prompt above, and a visitor can override the choice per question from the chat page itself. Leaving it at (none) means no specialization at all — today's plain behavior, with only the global system prompt applied. The list includes every agent you've defined, even ones not yet enabled, so you can line up a default ahead of turning an agent on.

## Web search

Search the web is the site-wide default for whether chat turns can search the web; a visitor can flip it per question with the chat page's own Web toggle. There's no separate "web search feature" to turn on beyond this: turning it on simply makes every MCP server marked "gated by web search" (configured on Chat -> MCP servers) available to the model for that turn — the model itself then decides whether to actually call a web_search or web_fetch tool, this setting doesn't force a search on every question. SearXNG base URL points at your self-hosted SearXNG instance, e.g. http://127.0.0.1:8888; it's handed to every active MCP server process as an environment variable so the search tool knows which instance to query, so it has to be reachable from wherever the chat backend/MCP servers run, not just from your browser. Result count caps how many results the web_search tool returns per call; 0 (the default) means no cap, returning SearXNG's full result set as-is — lower it if the model is getting overwhelmed with results or you want to keep the context budget tight.

## Context budget

Max conversation length (tokens) bounds how much conversation — the leading system prompt, active MCP servers' own prompts, and message history combined — gets sent to the model per turn, estimated by character count rather than exact model tokenization. When the budget is exceeded, older messages are dropped first, oldest to newest, and the most recent message is always kept regardless. Leave it at 0 to auto-detect: on every save, the server asks the configured model for its own advertised maximum context length and sets the budget to 75% of that, reserving the rest for the model's reply — only type an explicit number here if you want a tighter cap than auto-detection would give you. The donut chart below the field is a live, client-side estimate (using the same rough character-count method as the backend) of how the global system prompt and active MCP servers' prompts currently split the budget, updating as you edit the system prompt or the max-length field — it's a preview to help you size the budget sensibly, not a value that gets saved.

## Saving

Save chat settings writes every field on this page in one request — it's a full replace, not a per-field patch, so all the values shown are what get stored, including an unchanged API key placeholder handled specially as described above. After a successful save the page reloads its own state, which is also when a newly stored API key's field goes back to blank-with-placeholder. A negative max conversation length or a negative result count is rejected before anything is saved; any other failure (for instance the chat endpoint not being configured on this instance at all) shows an error message in place of "Saved." rather than silently discarding your edits.

> **Worth knowing:**
> - Web search here is not a separate feature toggle — it's a gate on MCP servers. If turning it on does nothing, check that at least one server on Chat -> MCP servers has "gated by web search" set and is enabled.
> - The API key field is always blank on load by design, even when a key is already saved — don't mistake that for the key having been lost.
> - Auto-detecting the context budget requires Base URL and Model to already be filled in and reachable; if the probe fails or times out, the budget silently falls back to 0 (no trimming) rather than blocking the save.

---
← [Embedding endpoint detail](embedding-endpoint-detail.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [MCP Servers](mcp-servers.md) →
