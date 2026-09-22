# Your files

[← Manual home](README.md)

*`/account/files`*

Files you've uploaded, or that the model itself produced on your behalf via a write_file tool call during chat -- reachable from Your account, next to Your MCP servers.

![Your files](images/account-files.png)

## Uploading

Choose a file and upload it. It becomes available to the model the next time it looks, via the file-access tools (see mcp-files on the [MCP servers](mcp-servers.md) page) -- uploading doesn't inject the file's contents into your next message directly.

## Limits and ownership

Capped at 5 MiB per file and 100 files per account, neither configurable by an admin. Every file is owned by, and only ever visible or downloadable by, you -- there's no admin view of anyone's uploaded files at all.

## Downloading and deleting

Each row has a Download link and a Delete link -- deleting has no confirmation prompt, so download anything worth keeping before removing it.

---
← [Your MCP servers](account-mcp-servers.md) &nbsp;·&nbsp; [↑ Manual home](README.md) &nbsp;·&nbsp; [Documents](documents.md) →
