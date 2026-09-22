# Settings: System

[← Manual home](README.md)

*`/admin/settings`*

The "System" group on the [Settings](settings.md) page.

## Database connection pool: Max open connections

The maximum number of simultaneous connections to the database — the default is 25. On SQLite this setting has no practical effect: SQLite always keeps a single connection regardless of what you set here, since it serializes writers at the file level and a larger pool would just add lock contention. It matters on the dev deployment's Postgres backend, where raising it lets more concurrent requests hit the database at once at the cost of more database-side resource usage.

## Database connection pool: Max idle connections

How many connections are kept warm (open but unused) between requests rather than closed immediately — the default is 25, matching max open connections. Keeping idle connections around avoids the overhead of reconnecting for every request, at the cost of holding those connections open on the database server even when they're not actively doing anything.

## Database connection pool: Connection max lifetime

How many minutes a pooled connection is reused before it's recycled and replaced with a fresh one — the default is 5. A running process re-reads and re-applies this setting within about 10 seconds of a save, but the effect on connections that are already open is limited to pool bookkeeping (when they're eligible to be closed and replaced) — it's not an instant forced reconnect of everything in the pool.

## Session: Session length

How many hours a signed-in admin session stays valid before it expires and requires signing in again — the default is 12. This only applies to sessions created after you save the change; anyone already signed in keeps whatever expiration their session was given at login. Shorten it for a more security-sensitive deployment where you want admins re-authenticating more often; lengthen it if 12 hours is genuinely too short for how this admin UI gets used day to day.

---
← [Settings: Content rules](settings-content-rules.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Database](database.md) →
