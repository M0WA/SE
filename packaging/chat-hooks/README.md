# Chat hook scripts

Scripts run by `internal/adapters/hookrunner.Runner` on behalf of an
admin-configured `domain.ChatHook` (Settings -> Chat -> Hooks): whenever a
chat turn's answer matches a hook's `Pattern`, the matched capture group is
passed as a single argv value to that hook's `Script` -- never through a
shell, never concatenated into a command string (see
`application.runChatHooks`'s and `hookrunner.Runner`'s own security doc
comments).

When a hook fires, its script's output is fed straight back to the model in
one follow-up completion call (the model's own tool-call text plus the
results, as a new turn) -- `ChatService.Chat` returns THAT follow-up answer
as the turn's Answer, not the model's bare tool-call text. A user never sees
the raw `<web_search>...</web_search>`-style syntax itself; they see the
model's real, results-informed answer. (`ChatResult.HookResults` still
carries the raw tool output separately, for the chat UI's own folded
transparency panel.)

## Suggested system prompt (Settings -> Chat -> System prompt)

The model only emits a hook's invocation syntax if told to. For the
`web_search` hook below (pattern `<web_search>([^<]+)</web_search>`), an
admin-configured system prompt along these lines is what actually makes it
fire:

```
You may invoke a live web search at any time by outputting exactly
<web_search>your query here</web_search> and nothing else in that reply.
Do this whenever you need current or specific information you are not
already certain about. You will then be given the search results as a
new message and should answer the user's original question from them --
never just repeat or describe the tool call itself.
```

That last sentence matters: without it, a model that doesn't already expect
a follow-up turn may just restate the tool call again instead of actually
reading the results it's handed.

## Install

```sh
mkdir -p /etc/searchengine/hooks
cp web_search.sh /etc/searchengine/hooks/
chmod +x /etc/searchengine/hooks/web_search.sh
```

`/etc/searchengine` already holds this deployment's other static,
admin-relevant config (`searchengine.env`) -- these scripts are just as
static (an admin picks a filename via `ChatHook.Script`, the file itself is
deployed like any other config, never written to by the running services),
so `/etc/searchengine/hooks` follows that same convention rather than
`/var/lib/searchengine` (reserved for what the services themselves write at
runtime -- the SQL DB file, Playwright's cache, its `tmp`). `postinst`
currently only manages `/var/lib/searchengine`; creating `hooks/` and
installing scripts into it is a manual step here, the same way
`packaging/searxng/`'s Docker Compose file is copied into place by hand
rather than by the `.deb`.

`cmd/search`'s `NewChatService` is already wired with
`hookrunner.New(CHAT_HOOKS_DIR)` (default `/etc/searchengine/hooks`, see
`packaging/searchengine.env`) as its `ports.HookScriptRunner`, so a
`ChatHook` row fires as soon as this directory exists and holds the named
script -- there's no further Go wiring needed, only installing the scripts
themselves per the steps above.

## web_search.sh

Proxies a hook's capture group straight to the self-hosted SearXNG instance
from `../searxng/` (`GET /search?q=...&format=json` over loopback -- see
`../searxng/README.md` for how that instance is set up and why
`search.formats` must include `json`). Requires `curl`.

## Adding another hook script

- Give it a distinct filename with no path separators -- `ChatHook.Script`
  is validated as a bare filename by `hookrunner.Runner` and resolved only
  against its configured directory, so a script can never live anywhere
  else or be reached via `../`.
- Read its arguments positionally (`$1`, `$2`, ...) -- `hookrunner.Runner`
  passes a matched pattern's capture groups as real argv elements, exactly
  once per match, never via environment variables or stdin.
- Keep it fast and side-effect-light: `hookrunner.Runner` enforces a 10s
  timeout and a 64KB cap on captured stdout per run, and a chat turn can
  trigger several matches (see `runChatHooks`'s `maxHookMatchesPerTurn`).
