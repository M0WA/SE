# User manual

Everything about running and using searchengine: installing and configuring a deployment, and every admin settings page and the public search/chat page, each screenshotted and explained field by field. For how the system is built rather than how to run it, see [docs/architecture/](../architecture/README.md).

## Installation & configuration

- **[Infrastructure options (IONOS)](infrastructure.md)** -- Infrastructure-agnostic: any host that can run a `.deb`, any Postgres/MySQL/SQLite database, any OpenAI-compatible inference endpoint.
- **[Installation](installation.md)** -- The ordered install flow for a fresh Debian/Ubuntu host.
- **[Environment variables](environment-variables.md)** -- All variables, read once at startup, typically from `/etc/searchengine/searchengine.env`.
- **[Config files](config-files.md)** -- Every file this project's packaging touches or ships, and how it gets installed.

## Getting started

- **[Signing In](login.md)** -- The gate in front of every admin page.
- **[Overview](overview.md)** -- The page you land on right after signing in, and the top entry in the admin nav rail.
- **[Search & Chat](search-chat.md)** -- The page every signed-in user lands on after logging in -- not an admin screen.
- **[Your account](account.md)** -- Self-service page for a regular-user (not admin) account to change its password and set a personal chat prompt.
- **[Your MCP servers](account-mcp-servers.md)** -- Your own personal MCP tool servers, visible and usable only by you -- like the admin catalog on [MCP servers](mcp-servers.md), scoped to one account.
- **[Your files](account-files.md)** -- Files attached to your pinned chats, or produced by the model via write_file during chat.

## Content

- **[Documents](documents.md)** -- Where you find indexed pages and their domains, and where the crawl vocabulary lives.
- **[Domain detail](domain-detail.md)** -- The complete, current view of every page indexed under one domain.
- **[Vocabulary term detail](vocabulary-term.md)** -- Shows exactly which indexed pages contain one specific term, how strongly, and with what context -- the postings list underlying BM25 ranking and typo/fuzzy correction.
- **[Content Dedup](content-dedup.md)** -- Finds documents byte-identical or near-identical to another already-indexed one and merges each group into one canonical document.

## Index

- **[Crawl](crawl.md)** -- Start a new crawl of a site, a one-off run or a repeating schedule.
- **[Schedule detail](schedule-detail.md)** -- The edit view for one existing crawl schedule.
- **[Jobs](jobs.md)** -- The operational view of crawling: every schedule set up, and every job ever executed, most recent first.

## Relevance

- **[Search debug](search-debug.md)** -- Runs a query like the public search box, but shows the raw BM25 and semantic scores unblended, plus a term lookup tool.
- **[PageRank](pagerank.md)** -- Scores every document's link authority; watch the score evolve, read the algorithm's constants, force a recompute.
- **[Embeddings](embeddings.md)** -- Overview of the semantic vector space: which providers are on, which score search, and a corpus-wide re-embed button.
- **[HTTP embedding endpoints](embedding-endpoints.md)** -- Lists every configured HTTP embedding endpoint and is where you add, edit, or remove them.
- **[Embedding endpoint detail](embedding-endpoint-detail.md)** -- The add/edit form for one endpoint: connection details, chunking, and two live probes.

## Chat

- **[Chat Settings](chat-settings.md)** -- Configures the chat-completions backend, system prompt, default agent, web-search behavior, and context budget.
- **[MCP Servers](mcp-servers.md)** -- Lists every MCP server connection -- the tools your chat model can reach beyond training.
- **[MCP Server detail](mcp-server-detail.md)** -- The add/edit page for a single MCP server connection.
- **[Agents](agents.md)** -- Shows every agent you've defined for the chat page and lets you add or edit one.
- **[Agent detail](agent-detail.md)** -- Where you write an agent's system prompt, describe it, scope its MCP tool access, and enable or disable it.

## System

- **[Settings](settings.md)** -- The single page for every global tuning knob: ranking blend, crawler defaults, blocked/boosted words and domains, and system-level limits.
- **[Settings: Search & ranking](settings-search-ranking.md)** -- The "Search & ranking" group on the [Settings](settings.md) page.
- **[Settings: Crawling](settings-crawling.md)** -- The "Crawling" group on the [Settings](settings.md) page.
- **[Settings: Content rules](settings-content-rules.md)** -- The "Content rules" group on the [Settings](settings.md) page.
- **[Settings: System](settings-system.md)** -- The "System" group on the [Settings](settings.md) page.
- **[Database](database.md)** -- Live diagnostics for the shared SQL database, plus two irreversible bulk-delete actions.
- **[Users](users.md)** -- Lists every regular-user account -- accounts that can sign in and chat, but are always refused on admin routes.
- **[User detail](user-detail.md)** -- Where you create or edit a user account -- set or reset their password, read or edit their personal chat prompt.
