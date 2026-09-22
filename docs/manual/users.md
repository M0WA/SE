# Users

[← Manual home](README.md)

*`/admin/users`*

The Users page lists every regular-user account — accounts that can sign in to the public search site and use chat, but are always refused on every admin route, no matter what. It's separate from the single admin login you used to reach this backend in the first place, which isn't a database row at all and doesn't appear here.

![Users](images/users.png)

## Why user accounts exist

A User row lets someone sign in to the public-facing search page and chat without giving them any access to this admin UI — there's no privilege escalation path from a user session to an admin one; the role is resolved fresh from the server-side session on every request, not something a client can influence. There's no self-service signup: every account is created, edited, and deleted here, by whoever can reach /admin. This is the right tool when you want to give a colleague or a service account search/chat access without handing them the keys to crawl configuration, database maintenance, or anything else under /admin.

## The user list

Each row shows a username and when the account was created. "Edit" opens the account's own subpage to change its password or personal prompt; "Delete" removes the account immediately after a confirmation prompt — the person can no longer sign in afterward, and this cannot be undone (there's no way to recover a deleted account or its custom prompt). If no accounts exist yet, the page just tells you to click "Add user" to create the first one. The list itself isn't paginated or searchable — with typical account counts for a self-hosted deployment that hasn't been a problem.

## Adding a user

"Add user" opens a blank version of the same subpage the edit links use. You set a username (permanent — it can't be changed after creation, since the account's internal ID is derived from it) and a password of at least 8 characters, optionally with a personal prompt. Note that you can't reuse the admin login's own username for a regular-user account — the server rejects it outright, since the two would be ambiguous at login time otherwise.

---
← [Database](database.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [User detail](user-detail.md) →
