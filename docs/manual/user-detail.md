# User detail

[← Manual home](README.md)

*`/admin/users/{id}`*

The per-user edit page is where you actually create a new user account or make changes to an existing one — set or reset their password, and read or edit their personal chat prompt on their behalf. It's the same form for both "Add user" and "Edit", just pre-filled and retitled when you're editing an existing account.

![User detail](images/user-detail.png)

## Username

Free text when adding a new user, and read-only once the account exists. It can't be changed after creation because the account's internal ID is derived from it at creation time (a slugified, uniqueness-checked version of the username) and other records reference that ID — renaming afterward would orphan the original identity. If you need a different username, delete the account and create a new one.

## Password

Required when adding a new user (minimum 8 characters, and it's also capped at 72 bytes — bcrypt's own hard limit, enforced here so you get a clear error instead of an opaque failure). When editing an existing user, leave this field blank to keep their current password unchanged; type a new one to reset it. The password is stored only as a bcrypt hash — it is never shown again after saving, not even to you, so if a user forgets their password, resetting it here (not looking it up) is the only path back in.

## Personal prompt (custom_prompt)

Free text, up to 4000 characters, injected as this user's own leading system message on every chat turn they send — in addition to, not instead of, the site-wide chat prompt configured elsewhere and any active MCP server's own prompt. Use it to give a specific user a standing instruction (a preferred answer style, a domain they work in, a language) without changing chat behavior for everyone else. It's the same field the user can set themselves from their own Account page, so anything you set here is just as visible and editable to them afterward — this isn't a private admin note. Leave it empty for no per-user injection.

## Saving and deleting

Save applies whatever is in the form: on a new account it creates the user and takes you straight to their new edit page; on an existing one it sends only the fields that make sense to change (password only if you typed one, the prompt always, since the textarea is the single source of truth for it on every save including clearing it back to empty). Delete removes the account immediately after a confirmation prompt and returns you to the user list — the account can no longer sign in, and, as with every delete in this admin UI, there is no undo.

---
← [Users](users.md) &nbsp;·&nbsp; [↑ Manual home](README.md)
