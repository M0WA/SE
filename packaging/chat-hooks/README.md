# Chat hook scripts

Scripts run by `internal/adapters/hookrunner.Runner` on behalf of an
admin-configured `domain.ChatHook` (Settings -> Chat -> Hooks): whenever a
chat turn's answer matches a hook's `Pattern`, the matched capture group is
passed as a single argv value to that hook's `Script` -- never through a
shell, never concatenated into a command string (see
`application.runChatHooks`'s and `hookrunner.Runner`'s own security doc
comments).

When a hook fires, its script's output is fed straight back to the model in
a follow-up completion call (the model's own tool-call text plus the
results, as a new turn) -- `ChatService.Chat` returns THAT follow-up answer
as the turn's Answer, not the model's bare tool-call text. A user never sees
the raw `<web_search>...</web_search>`-style syntax itself; they see the
model's real, results-informed answer. (`ChatResult.HookResults` still
carries the raw tool output separately, for the chat UI's own folded
transparency panel.) This repeats up to `maxHookFollowUpRounds` (2) times if
the model's own follow-up answer invokes a hook again -- e.g. a fetch came
back blocked/empty and it reasonably tries a different URL -- rather than
leaving that second tool call unprocessed.

## Suggested global system prompt (Settings -> Chat -> System prompt)

The endpoint-level System prompt is unconditional and always injected,
regardless of which hooks are active -- it's the right place for a general
instruction that isn't tied to any one tool:

```
The current time is %c. Assume your training data may be outdated. When
something could have changed or you are not certain, use web search or a
URL fetch instead of relying on memory, whenever those tools are available
to you. Double-check anything you get from the internet before relying on
it -- a follow-up web search or fetching the page itself -- and make sure
the information is actually recent, not just present.
```

"Whenever those tools are available to you" matters: the hooks themselves
are gated by the chat's Web toggle (`ChatHook.GatedByWebSearch`), so they
may not always be there to use -- the global prompt shouldn't imply they
always are.

`%c` is replaced with the current UTC date and time, rendered via a real
strftime(3) `%c` conversion (`application.strftime`) -- the same
`ctime(3)`-style rendering `date +%c` gives you (e.g. "Mon Sep 21
04:22:35 2026"; deliberately no time zone abbreviation, same as real
strftime's `%c` -- append `%Z` yourself in the prompt if you want one) --
every time a prompt is assembled for a turn.
`application.expandPromptPlaceholders` runs it against both the global
System prompt and each active hook's own Prompt, so either can use it to
give the model a concrete anchor for judging staleness instead of a vague
"could be outdated."

## Suggested per-hook prompts

Each hook has its own Prompt field (Settings -> Chat -> Hooks -> edit a
hook), injected only while that hook is active -- not the endpoint-level
System prompt, which is unconditional and shared by everything. A hook only
emits its invocation syntax if told to, and told firmly enough: a model
that already has an opinion about a well-known URL or topic will otherwise
just answer from its own (possibly wrong or outdated) memory instead of
actually calling the tool, which defeats the point of having one. Say so
explicitly rather than leaving it implied:

web_search's Prompt:

```
When the user asks about something that could have changed (people in
office, current events, prices, versions, schedules, or anything
time-sensitive), you must search with <web_search>query</web_search>
before answering, even if you already feel confident -- your training
data can be outdated. Output only the tag, nothing else, and wait for
real results as a new message before answering. Never guess or answer
from memory for time-sensitive facts.

Search results only give you a title, URL, and a short excerpt -- an
excerpt is not enough to verify a fact, and can be stale, truncated, or
taken out of context. Once you have results, fetch the URL that looks
most likely to answer the question with <web_fetch>https://...</web_fetch>
and confirm the fact against the actual page content before answering.
Only answer from the excerpts alone if fetching genuinely isn't possible.
```

The added paragraph only makes sense when the web_fetch hook is also
enabled (see below) -- it tells the model to chain the two tools rather
than treat a search engine's own excerpt as sufficient verification.

web_fetch's Prompt:

```
When the user asks about a specific URL or its content, you must fetch it
with <web_fetch>https://...</web_fetch> before answering, even if you
already feel confident or were given unrelated search results -- those
are not the page itself. Output only the tag, nothing else, and wait for
the real page content as a new message before answering. Never guess,
recall from memory, or describe what you assume the page contains.
```

The "even if you already feel confident" phrasing matters: a model with a
strong prior about a well-known URL or fact (e.g. wikipedia.org's title, a
head-of-state's name) will otherwise just answer from memory instead of
actually calling the tool, especially when RAG/deterministic web-search
context is also enabled and gives it something that merely looks like
"I already did research." Naming that failure mode explicitly and telling
it to call the tool anyway measurably improves (though, being an LLM,
never perfectly guarantees) actual tool use over a shorter, softer prompt.

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

Reads its SearXNG base URL from the WEB_SEARCH_BASE_URL environment
variable rather than a hardcoded address -- ChatService.Chat sets this from
the SAME admin-configured endpoint setting the deterministic web-search
context injection already uses (domain.ChatEndpoint.WebSearchBaseURL,
Settings -> Chat), so the two never drift out of sync. Falls back to
127.0.0.1:8888 if the variable is somehow unset (e.g. the script invoked
standalone rather than through a real chat turn).

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
