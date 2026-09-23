# Users

[← Manual home](README.md)

*`/admin/users`*

Lists every regular-user account — accounts that can sign in to the public search site and use chat, but are always refused on every admin route. Separate from the single admin login used to reach this backend, which isn't a database row and doesn't appear here.

![Users](images/users.png)

## Why user accounts exist

A User row lets someone sign in to the public search page and chat without any admin UI access — there's no privilege escalation path from a user session to an admin one; the role is resolved fresh from the server-side session every request. No self-service signup: every account is created, edited, and deleted here, by whoever can reach /admin. Use this to give a colleague or service account search/chat access without the keys to crawl configuration, database maintenance, or anything else under /admin.

## The user list

Each row shows a username and creation date. Edit (✎) opens the account's subpage to change its password or personal prompt; Delete (trash) removes the account after a confirmation prompt — no longer able to sign in afterward, and no undo (no way to recover a deleted account or its custom prompt). With no accounts yet, the page tells you to click "Add user." The list isn't paginated or searchable — fine for typical self-hosted account counts.

## Adding a user

"Add user" opens a blank version of the same subpage the edit links use. Set a username (permanent — the account's internal ID is derived from it) and a password of at least 8 characters, optionally with a personal prompt. You can't reuse the admin login's own username — the server rejects it, since the two would be ambiguous at login time.

---
← [Database](database.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [User detail](user-detail.md) →
