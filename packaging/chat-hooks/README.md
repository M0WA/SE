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

The model only emits a hook's invocation syntax if told to:

```
To search the web: output only <web_search>query</web_search>.
To fetch a URL: output only <web_fetch>https://...</web_fetch>.
You'll get the result as a new message -- answer from it, don't repeat the tool call.
```

## Install

```sh
mkdir -p /etc/searchengine/hooks
cp web_search.sh web_fetch.sh /etc/searchengine/hooks/
chmod +x /etc/searchengine/hooks/web_search.sh /etc/searchengine/hooks/web_fetch.sh
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

## web_fetch.sh

`web_search.sh`'s equivalent for a specific URL instead of a search query --
fetches the capture group directly (`http`/`https` only, redirects locked to
the same two schemes, response capped at 1MB). Requires `curl`.
**SSRF caveat**: the URL comes from the model's own output, which can be
indirectly attacker-influenced (see `application.runChatHooks`'s security
note) -- this script does not block requests to internal/private addresses.
Only enable this hook if that's an acceptable risk for your deployment, or
add a network-level restriction (e.g. a forward proxy allowlist) in front of
it.

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
