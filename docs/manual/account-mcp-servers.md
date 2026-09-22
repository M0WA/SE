# Your MCP servers

[← Manual home](README.md)

*`/account/mcp-servers`*

Your own personal MCP tool servers, visible and usable only by you -- the same underlying idea as the admin-configured catalog on the [MCP servers](mcp-servers.md) page, but scoped to one account rather than shared deployment-wide.

![Your MCP servers](images/account-mcp-servers.png)

## How this differs from the admin catalog

Every row here is owned by, and only ever visible or editable by, your own account. Transport is always `http` -- there's no `stdio` option, since a `stdio` server runs a real local command on the server's own machine, a trust tier only an admin-configured row may use. A personal server is merged into your chat turns unconditionally -- unlike the shared catalog, it's never narrowed by whatever Agent you have selected.

## Adding a server

Give it a name, a base URL (an HTTP MCP endpoint you control or trust), an optional API key, and a prompt telling the model when to use it. Check "Gated by web search" if this server should only be offered when the chat page's Web toggle is on -- same convention the shared catalog uses.

---
← [Your account](account.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Your files](account-files.md) →
