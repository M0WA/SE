# MCP Server detail

[← Manual home](README.md)

*`/admin/mcp-servers/{id}`*

The add/edit page for a single MCP server connection. Reached from "Add server" or by clicking Edit on a row in the MCP Servers list. Existing servers show their real live tool list on load; a new or edited server can be probed before you ever save it.

![MCP Server detail](images/mcp-server-detail.png)

## Name

A human label for this connection, e.g. "web tools" or "internal sandbox". It's shown only in this admin UI — never sent to the model — so it can be whatever's clearest to you, unlike the tool names themselves, which the server defines.

## Transport: stdio vs http

Transport picks how this server is reached. "stdio" spawns Command as a local child process fresh at the start of each chat turn and keeps it open for that turn's duration, talking MCP over its stdin/stdout — this is what all four built-in servers (mcp-datetime, mcp-web, mcp-files, mcp-sandbox) use, and what you'd pick for any other locally installed server binary. "http" instead connects to a remote MCP endpoint over Streamable HTTP, for a third-party or otherwise remotely hosted server. Switching this dropdown shows only the fields that transport actually uses — Command/Arguments for stdio, Base URL/API key for http — so you're never left filling in something the backend will silently ignore.

## Command and Arguments (stdio only)

Command is the executable to spawn — an absolute path (e.g. /usr/bin/searchengine-mcp-web) or a name resolvable on this process's own PATH. It's run directly, never through a shell, so shell syntax like pipes or globs won't work here. Arguments are real argv elements passed to that process, one per line: for mcp-sandbox that's flags like -network (grant the sandbox outbound network access, off by default — this also unlocks an extra "packages" argument on run_python/run_go, letting the model request pip packages/Go modules to be installed before its code runs; never offered at all without -network, since installing needs a real network call), -memory/-cpus/-pids-limit/-timeout (resource limits), and -dns/-host-dns/-host-network for controlling DNS and networking mode when -network is set; for mcp-files it's -base-url (defaults to http://127.0.0.1:8080). An admin filling in Command is granted the same trust level as one configuring a crawl schedule or an embedding endpoint's base URL — real trust, no extra sandboxing at this layer, so only point Command at binaries you actually trust.

## Base URL and API key (http only)

Base URL is the remote MCP endpoint's address, e.g. https://example.com/mcp. API key is optional and sent as an Authorization: Bearer header on every request to that URL — leave it blank if the remote server doesn't require auth. Once saved, the key is encrypted at rest and never shown again in the form; the field always starts blank on reload, with the placeholder text telling you whether a key is already stored. Leaving API key blank on save keeps whatever key is already stored — to actually remove a stored key, check "Remove the stored API key" instead, which only becomes available once a key is set.

## List tools / Refresh tools

This button connects to the server exactly as the form is currently filled in — even before you've saved anything — and shows the real tool names and descriptions it reports back live. It runs automatically the moment an existing server's page finishes loading, and you can re-run it any time after changing fields to sanity-check a new Command, Base URL, or API key before committing to Save. A result of zero tools is ambiguous by nature (the server may have failed to connect, or may legitimately expose nothing) and the status message says so rather than guessing; check the server's own log if that happens unexpectedly. This is also the only place tool descriptions appear in the admin UI — they come from the server itself, not from anything you type here.

## Prompt (optional)

This is not where you describe what the server does — each tool's own name and description, visible via List tools above, already tells the model that, live from the server. Prompt is for extra cross-tool steering that no single tool's description can carry on its own, e.g. "after searching, fetch the top 3 results with the fetch tool before answering." When non-empty and this server is active for a turn, it's injected as its own system message, positioned after the chat endpoint's persistent system prompt (set under Settings → Chat) — in addition to that prompt, not instead of it. Leave it empty if the tool descriptions already say everything the model needs; most servers don't need one.

## Enabled

Turns this server on or off. A disabled server is never connected to and its tools are never offered to the model, regardless of any other setting — this is the safe way to temporarily pull a server without deleting its configuration.

## Gated by web search

When checked, this server (and its Prompt, if set) is only active on a turn where the chat's own "Web" toggle is on — the same web-search setting configured under Settings → Chat, or overridden per-question in the chat UI itself. There's no separate control for this; it reuses that one toggle. When unchecked (the default), this server is active whenever Enabled is checked, independent of whether web search is on for that turn. This exists so a server like mcp-web can be wired to only run when the user has actually asked for web-grounded answers, rather than being offered on every turn regardless of intent.

## Save and Delete

Save creates a new server (redirecting you to its own detail page once created) or updates an existing one's editable fields — the server's ID, once minted from its name, never changes. Delete removes the server outright after a confirmation prompt warning that its tools stop being offered immediately; there's no undo, so prefer unchecking Enabled if you might want it back.

> **Worth knowing:**
> - A stdio server's Command is spawned fresh per chat turn, not left running as a background service — so a crashing or slow-starting binary shows up as a per-turn failure, not a persistent outage you'd see in systemd.
> - mcp-sandbox's -network flag is off by default for good reason: enabling it (or -host-network, which goes further and shares the host's own network namespace) is a real privilege elevation for whatever code the model chooses to run inside it.

---
← [MCP Servers](mcp-servers.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Agents](agents.md) →
