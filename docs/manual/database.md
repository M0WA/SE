# Database

[← Manual home](README.md)

*`/admin/database`*

The Database page shows live diagnostics for the SQL database that search-server, admin-server and crawl-server all share, and holds two irreversible bulk-delete actions. Use it to check the connection is healthy, see how big each table has grown, and — only when you really mean it — wipe crawled content or reset configuration back to defaults.

![Database](images/database.png)

## Connection

Shows the database driver this deployment is running on: sqlite for a local or CI install, postgres for the dev deployment at se.mo-sys.de. This is read-only and set at install time by which DSN the binaries were started with — you can't change it from the UI. It's mostly useful for confirming you're looking at the environment you think you are before you touch anything in the danger zone below.

## Connection pool

Live numbers straight from Go's sql.DBStats for the connection pool this admin-server process holds: how many connections are open versus the configured maximum, how many are actively in use versus sitting idle, how many requests had to wait for a free connection and for how long, and how many connections have been closed because of the idle-connection limit, the idle-timeout, or the max-lifetime setting. A healthy pool normally shows a low wait count and wait duration; if wait count keeps climbing, the pool is undersized for the current load or something is holding connections open too long. This is diagnostic only — there's nothing to configure here, and the underlying pool limits are set at the code/config level, not editable through this page.

## Tables

A row-count per table in the database, sorted alphabetically — documents, postings, crawl jobs, sessions, settings tables, and everything else the schema defines. Use it as a sanity check after a crawl (document and posting counts should climb) or after a clear-content run (they should drop to zero) rather than as a tool in its own right. A table sitting at a surprising number — postings much smaller than expected relative to documents, for instance — is usually the first sign something upstream (a crawl, an indexing step) didn't finish cleanly.

## Danger zone — Clear content

Permanently deletes every crawled document and everything derived from it — postings, links, versions, embeddings, aliases — plus every crawl job. Every settings table (tuning, operational settings, overrides, chat/embedding endpoints, crawl schedules) is left untouched, so your configuration survives. This is the button to reach for when you want to start crawling from a clean slate without redoing any setup. It asks for a plain browser confirmation before running (no typed confirmation phrase), fires immediately, and there is no undo — the underlying repository call is a real DELETE across every content table, not a soft delete or a recycle bin.

## Danger zone — Clear settings

Permanently deletes every row of every settings table: tuning weights, operational settings, ranking overrides, the chat endpoint, embedding endpoints, and crawl schedules. Crawled content itself is left untouched. This admin-server process resets its own in-memory settings to their built-in defaults immediately so you see the effect right away, but search-server and crawl-server only pick up the change once you restart them — until then they keep running on whatever settings they last loaded into memory, out of sync with what the database now says. Use this when your tuning/configuration has gotten into a bad state and you'd rather start over than hunt down what's wrong; it's rare to need day to day. Like clear content, it's gated only by a plain confirmation dialog and cannot be undone.

---
← [Settings: System](settings-system.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Users](users.md) →
