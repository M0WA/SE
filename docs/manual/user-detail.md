# User detail

[← Manual home](README.md)

*`/admin/users/{id}`*

Where you create a new user account or edit an existing one — set or reset a password, and read or edit their personal chat prompt on their behalf. Same form for "Add user" and "Edit," just pre-filled and retitled for an existing account.

![User detail](images/user-detail.png)

## Username

Free text when adding a user, read-only once the account exists. It can't change afterward because the account's internal ID is derived from it at creation (a slugified, uniqueness-checked version) and other records reference that ID — renaming would orphan the original identity. Delete and recreate the account for a different username.

## Password

Required when adding a user (minimum 8 characters, capped at 72 bytes — bcrypt's hard limit, enforced here for a clear error instead of an opaque failure). When editing, leave blank to keep the current password, or type a new one to reset it. Stored only as a bcrypt hash, never shown again after saving — resetting here, not looking it up, is the only path back in for a forgotten password.

## Personal prompt (custom_prompt)

Free text, up to 4000 characters, injected as this user's own leading system message on every chat turn — in addition to, not instead of, the site-wide chat prompt and any active MCP server's prompt. Use it to give a user a standing instruction without changing chat behavior for everyone else. It's the same field the user can set themselves from their Account page, so anything you set here is just as visible and editable to them — not a private admin note. Leave empty for no per-user injection.

## Saving and deleting

Save applies the form: on a new account it creates the user and opens their edit page; on an existing one it sends only fields that make sense to change (password only if typed, the prompt always, since the textarea is the single source of truth including clearing it to empty). Delete removes the account after a confirmation prompt and returns to the user list — no undo, same as every delete in this admin UI.

---
← [Users](users.md) &nbsp;·&nbsp; [↑ Manual home](README.md)
