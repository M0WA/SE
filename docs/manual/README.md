# User manual

Everything about running and using searchengine: installing and configuring a deployment, and every admin settings page and the public search/chat page, each screenshotted and explained field by field. For how the system is built rather than how to run it, see [docs/architecture/](../architecture/README.md).

## Installation & configuration

- **[Infrastructure options (IONOS)](infrastructure.md)** -- This project is infrastructure-agnostic -- any host that can run a `.deb`, any Postgres/MySQL/SQLite database, any OpenAI-compatible inference endpoint.
- **[Installation](installation.md)** -- This is the ordered install flow for a fresh Debian/Ubuntu host, for whoever is standing up a new deployment rather than using an existing one.
- **[Environment variables](environment-variables.md)** -- All variables are read once at process startup, typically from `/etc/searchengine/searchengine.env` via systemd's `EnvironmentFile=`.
- **[Config files](config-files.md)** -- Every file this project's packaging touches or ships, and how it gets installed.

## Getting started

- **[Signing In](login.md)** -- This is the gate in front of every admin page.
- **[Overview](overview.md)** -- This is the page you land on right after signing in, and the top entry in the admin navigation rail.
- **[Search & Chat](search-chat.md)** -- This is the page every signed-in user lands on after logging in -- it is not an admin screen.
- **[Your account](account.md)** -- Self-service page for a signed-in regular-user (not admin) account to change their own password and set a personal chat prompt -- no admin involvement needed for either.
- **[Your MCP servers](account-mcp-servers.md)** -- Your own personal MCP tool servers, visible and usable only by you -- the same underlying idea as the admin-configured catalog on the [MCP servers](mcp-servers.md) page, but scoped to one account rather than shared deployment-wide.
- **[Your files](account-files.md)** -- Files attached to your pinned chats, or that the model itself produced on your behalf via a write_file tool call during chat -- reachable from Your account, next to Your MCP servers.

## Content

- **[Documents](documents.md)** -- The Documents page is where you find indexed pages and the domains they belong to, and where the crawl vocabulary lives.
- **[Domain detail](domain-detail.md)** -- This page is the complete, current view of every page indexed under a single domain — reached by clicking a domain from the Documents search.
- **[Vocabulary term detail](vocabulary-term.md)** -- Opened by clicking a term on the Documents page's Vocabulary table, this page shows exactly which indexed pages contain one specific term, how strongly, and with what surrounding context — the postings list that underlies both BM25 ranking and typo/fuzzy correction for that term.
- **[Content Dedup](content-dedup.md)** -- The Content Dedup page finds documents that are byte-identical or near-identical to another already-indexed document — a mirror site, a copy of the same article under a different host — and merges each group into one canonical document, so a search result never lists the same content twice.

## Crawling

- **[Crawl](crawl.md)** -- This is where you start a new crawl of a site, either as a single one-off run or as a repeating schedule.
- **[Schedule detail](schedule-detail.md)** -- This page is the edit view for one existing crawl schedule — reached by clicking the edit (pencil) icon on a row in the Schedules table on the Jobs page.
- **[Jobs](jobs.md)** -- The Jobs page is the operational view of crawling on this instance: every schedule that's been set up, and every job (an actual triggered crawl run) that's ever executed, most recent first.

## Relevance

- **[Search debug](search-debug.md)** -- This page runs a query the same way the public search box does, but shows you the machinery underneath: the raw BM25 and semantic-similarity scores for every result, unblended, plus a second tool for looking up exactly which documents contain a given term.
- **[PageRank](pagerank.md)** -- PageRank scores every document's link authority — how much weight other pages lend it by linking to it — and this page is where you watch that score evolve, read the algorithm's fixed constants, and force a fresh recompute on demand.
- **[Embeddings](embeddings.md)** -- This page is the overview of the search engine's semantic vector space: which embedding providers are turned on, which one (or blend of several) actually scores search results, and a button to re-embed the whole corpus after you change a provider's model or dimensions.
- **[HTTP embedding endpoints](embedding-endpoints.md)** -- This page lists every configured HTTP embedding endpoint -- external OpenAI-compatible embeddings APIs, whether a local inference server (Ollama, llama.cpp, LM Studio) or a hosted provider -- and is where you add, edit, or remove them.
- **[Embedding endpoint detail](embedding-endpoint-detail.md)** -- The add/edit form for one HTTP embedding endpoint's full configuration: connection details, chunking behavior, and two live probes (List available models, Test connection) that call the endpoint for real before you save.

## Chat

- **[Chat Settings](chat-settings.md)** -- This page configures the chat-completions backend that powers Chat mode on the public search page, plus the system prompt, default agent, web-search behavior, and context-length budget every chat turn uses.
- **[MCP Servers](mcp-servers.md)** -- This page lists every MCP server connection you've configured — the tools your chat model can reach beyond its own training, from web search to running code in a sandbox.
- **[MCP Server detail](mcp-server-detail.md)** -- The add/edit page for a single MCP server connection.
- **[Agents](agents.md)** -- The Agents list, under Settings -> Chat -> Agents, shows every agent you've defined for the chat page and lets you add a new one or open an existing one for editing.
- **[Agent detail](agent-detail.md)** -- The agent detail page, reached from the Agents list's Edit link or "Add agent" link, is where you write an agent's system prompt, describe it, scope its MCP tool access, and enable or disable it.

## System

- **[Settings](settings.md)** -- This is the single page for every global tuning knob in searchengine: how ranking blends BM25 and semantic scores, how the crawler behaves by default, which words or domains get blocked or boosted, and system-level limits like the database pool and session length.
- **[Settings: Search & ranking](settings-search-ranking.md)** -- The "Search & ranking" group on the [Settings](settings.md) page.
- **[Settings: Crawling](settings-crawling.md)** -- The "Crawling" group on the [Settings](settings.md) page.
- **[Settings: Content rules](settings-content-rules.md)** -- The "Content rules" group on the [Settings](settings.md) page.
- **[Settings: System](settings-system.md)** -- The "System" group on the [Settings](settings.md) page.
- **[Database](database.md)** -- The Database page shows live diagnostics for the SQL database that search-server, admin-server and crawl-server all share, and holds two irreversible bulk-delete actions.
- **[Users](users.md)** -- The Users page lists every regular-user account — accounts that can sign in to the public search site and use chat, but are always refused on every admin route, no matter what.
- **[User detail](user-detail.md)** -- The per-user edit page is where you actually create a new user account or make changes to an existing one — set or reset their password, and read or edit their personal chat prompt on their behalf.
