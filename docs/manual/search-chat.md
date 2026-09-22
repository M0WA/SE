# Search & Chat

[← Manual home](README.md)

*`/ (search + chat page)`*

This is the page every signed-in user lands on after logging in -- it is not an admin screen. It has two modes, switched with one control in the top-right: Chat, for asking a model questions that it can answer using the index, the web, and your own files, and Search, for querying the indexed pages directly. Everything on this page works the same whether you're a regular self-service account or an admin (admins additionally get a gear icon that jumps to /admin).

![Search & Chat](images/search-chat.png)

## Chat vs Search toggle

The pill-shaped control at the top switches the whole page between Chat and Search -- they are two independent views, not tabs of the same result: switching hides one set of controls and shows the other. Chat sits on the left and is selected by default when the page loads. Chat is for when you want a written answer synthesized from the index (and optionally the live web), with the model doing the reading for you; Search, on the right, is the plain, fast option when you already know roughly what you're looking for and want a ranked list of pages with snippets.

## Search mode: the query box and syntax

Type a query and press Search (or Enter) to run it against the indexed content -- results come back ranked by a blend of keyword matching and semantic similarity, not keyword matching alone, so a query that doesn't share exact words with a page can still surface it if the meaning is close. The "Search syntax" disclosure above the results lists the operators the box understands: plain words match loosely, +word forces a term to be present, -word excludes it, "exact phrase" matches that phrase literally (and -"exact phrase" excludes it), and site:example.com (or -site:example.com) restricts results to, or excludes, a domain and its subdomains. These combine freely in one query, e.g. cats +shelter -kitten site:example.com. If your query is misspelled, a quiet note above the results says which term(s) were fuzzy-corrected for scoring -- your typed query itself is never silently rewritten, only substituted for ranking purposes.

## Sort order

The dropdown next to the search box picks how results are ordered: "Best match" (the default) ranks by the combined relevance score; "Most recent" instead orders by how recently each page was crawled/updated, useful when you care about freshness more than exact topical fit -- e.g. checking whether a page has been re-indexed since a recent change. Switching this re-runs nothing by itself; re-submit the search to apply it.

## Reading a search result

Each result shows a clickable title (opens the original page in a new tab), the full URL underneath, and a text snippet with your matched terms highlighted. A score sits next to the title -- the higher the number, the better that page's overall match. Expanding the "Details" disclosure on a result breaks that score down into its two components, bm25 (keyword-match strength) and semantic (meaning-similarity), plus the final blended score -- useful if a result's ranking looks surprising and you want to see which signal drove it.

## Chat mode: asking a question

Switch to Chat, type a question in the text box at the bottom, and press Enter to send (Shift+Enter inserts a newline instead of sending, so you can compose a multi-line question first). The model answers using whatever context it's given -- the index, optionally the live web, and any files you've attached -- and its reply renders as formatted text (headings, lists, bold/italic, code blocks and links all work) rather than raw markdown. Each answer appears directly under your question in a running transcript, so a conversation reads top to bottom like any chat app.

## The Web toggle

The "Web" checkbox next to the mode switch controls whether the model is allowed to use live web search and page fetching to answer your question, on top of whatever else it knows -- it's checked by default. Leave it on for questions that need current or outside-the-index information; turn it off if you specifically want an answer grounded only in what's already indexed (faster, and avoids the model wandering off to outside sources for something the index already covers). This is read fresh on every message you send, so you can flip it mid-conversation and it only affects the next question, not answers already given.

## Picking an agent

The dropdown labeled "Default agent" next to the Web checkbox lets you choose a specific agent to answer with, if any have been configured on this deployment -- an agent bundles its own system prompt and set of enabled MCP tool servers, so picking one changes both the model's behavior and what tools it can reach for that conversation. Leaving it on "Default agent" uses whatever the deployment's administrator set as the fallback. Each chat tab remembers its own agent choice independently, so different tabs can be talking to different agents at once, and forking a tab carries its agent choice over to the fork.

## Chat tabs: new, fork, export, import

The strip above the transcript holds one tab per open conversation -- the + button starts a brand-new, empty chat; the fork icon (⎇) deep-copies the current tab's entire history into a new, independent tab, so you can branch a conversation without disturbing the original. Conversations are session-only: closing the page or reloading it discards them. The download icon (⬇) saves the active tab as a JSON file, and the upload icon (⬆) loads one back in as a new tab -- this is the only way to keep a conversation past a page reload, so export anything worth keeping. Closing a tab (× on its label) has no confirmation prompt -- export first if you might want it later -- and the last remaining tab can't be closed.

## Attaching files

The paperclip button next to the message box uploads a file for the model to read during this conversation -- it doesn't inject the file's contents into your message directly; instead the model discovers and reads it on its own the next time it looks, via its file-access tools. Attached files (and any file the model itself produces, e.g. via a write_file tool call) show up as small boxes below the transcript with a download link and a × to delete them immediately, no confirmation needed. This requires a regular signed-in user account with the files feature configured on the deployment -- an admin session, or a deployment without it configured, simply won't show any file boxes.

## Citations and tool results

When the model uses a tool mid-answer -- fetching a URL, running a search, writing a file -- that call shows up as a small closed-by-default fold directly under the answer, one per tool call. Expanding a fetch fold shows the URL it retrieved (a real clickable link) with the raw response tucked in a further nested fold; a search fold shows the query and, where the tool returned a recognizable result list, the top links directly, again with the raw JSON available if you want it. This is how you check what the model actually looked at before trusting an answer, without cluttering the main reply with that detail unless you ask for it.

## Token usage indicator

The small ring next to the Web checkbox tracks how much of the model's context window the current conversation is using, broken into the global system prompt, tool/MCP-server prompts, your own messages, the active agent's prompt, and prior conversation history -- hover it for the full breakdown and a percentage of the model's context limit. It's a heads-up for when a long conversation is approaching the point where old messages start getting silently dropped: when that happens, the affected answer carries its own visible note ("Older messages were dropped from context to fit the model's limit") so you know that particular reply wasn't generated with the full conversation in view.

## Signing out and account links

The header's top-right icons adapt to who's signed in: an admin session sees a gear icon linking to /admin (the internal admin UI), a regular user session instead sees a person icon linking to /account (where file uploads and personal MCP servers are managed). The sign-out button (present for both) ends the session and returns you to this page logged out; you'll need to sign back in via /login to search or chat again, since the page requires an authenticated session to load at all.

> **Worth knowing:**
> - Combine search operators freely in one query, e.g. cats +shelter -kitten site:example.com -- there's no separate advanced-search form, it's all in the one box.
> - Export a chat tab before closing it or navigating away -- nothing here persists across a page reload except what you explicitly download.
> - If a search result's ranking looks off, open its Details fold to see whether keyword (bm25) or meaning-based (semantic) matching drove the score.

---
← [Overview](overview.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Your account](account.md) →
