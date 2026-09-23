# Settings: System

[← Manual home](README.md)

*`/admin/settings`*

The "System" group on the [Settings](settings.md) page.

## Database connection pool: Max open connections

The maximum simultaneous database connections — default 25. On SQLite this has no practical effect: it always keeps a single connection, since it serializes writers at the file level and a larger pool would just add lock contention. It matters on the dev deployment's Postgres backend, where raising it lets more concurrent requests through at the cost of more database-side resource usage.

## Database connection pool: Max idle connections

How many connections are kept warm (open but unused) between requests rather than closed immediately — default 25, matching max open connections. This avoids reconnect overhead per request, at the cost of holding connections open on the database server even when idle.

## Database connection pool: Connection max lifetime

How many minutes a pooled connection is reused before being recycled — default 5. A running process re-reads this within ~10 seconds of a save, but the effect on already-open connections is limited to pool bookkeeping (when they become eligible to close and replace) — not an instant forced reconnect of the whole pool.

## Session: Session length

How many hours a signed-in admin session stays valid before expiring — default 12. Only applies to sessions created after saving; anyone already signed in keeps their session's original expiration. Shorten it for more frequent re-authentication in a security-sensitive deployment; lengthen it if 12 hours is genuinely too short day to day.

---
← [Settings: Content rules](settings-content-rules.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Database](database.md) →
