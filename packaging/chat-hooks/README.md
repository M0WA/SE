# Chat hook scripts

Scripts run by `internal/adapters/hookrunner.Runner` on behalf of an
admin-configured `domain.ChatHook` (Settings -> Chat -> Hooks). Each hook is
exposed to the configured chat model as a native OpenAI-compatible
tool-calling function -- `Name` is the tool's function name, `Description`
and `Parameters` (a JSON-schema object) are sent as part of the request's
own `tools` list, and `Parameters` is constrained to exactly one property
(see `domain.ChatHook.Parameters`'s doc comment). Whenever the model
actually invokes the tool, the single value it supplied for that property is
passed as a single argv value to the hook's `Script` -- never through a
shell, never concatenated into a command string (see
`application.runToolCalls`'s and `hookrunner.Runner`'s own security doc
comments).

This requires the configured chat endpoint to actually support native
tool-calling (an OpenAI-compatible `tools`/`tool_calls` request/response
convention) -- for the self-hosted vLLM reference deployment
(`gpu.mo-sys.de`), that means it's launched with `--enable-auto-tool-choice
--tool-call-parser hermes` (Qwen2.5-Instruct's own matching parser). An
endpoint without tool-calling support simply never returns `tool_calls`, so
configured hooks are silently never invoked -- not an error, just inert.

When the model invokes a tool, its own tool-call message plus a
`domain.ChatRoleTool` result message (correlated by the call's own ID) are
fed back to it in a follow-up completion call -- `ChatService.Chat` returns
THAT follow-up answer as the turn's `Answer`. A user never sees raw
tool-call JSON; they see the model's real, results-informed answer.
(`ChatResult.HookResults` still carries the raw tool output separately, for
the chat UI's own folded transparency panel.) This repeats up to
`maxHookFollowUpRounds` (4) times if the model's own follow-up answer
invokes a tool again -- e.g. a fetch came back blocked/empty and it
reasonably tries a different URL, or web_search's own suggested Prompt below
chains into fetching multiple results -- rather than leaving that tool call
unprocessed.

If the model is STILL trying to invoke one more tool once that round budget
is spent, `ChatService.Chat` doesn't just return an empty answer. It makes
one last completion call with NO tools offered at all (so the model
literally cannot request another one), telling it plainly that no more tool
calls are available and to answer now with whatever it already gathered, and
uses THAT answer instead. This is what guarantees "always answer with a
result" (see the global system prompt below) even against a model that
doesn't fully respect the round budget on its own -- a real code-level
backstop, not just a prompt-text request.

## Suggested global system prompt (Settings -> Chat -> System prompt)

The endpoint-level System prompt is unconditional and always injected,
regardless of which hooks are active -- it's the right place for a general
instruction that isn't tied to any one tool:

```
The current date and time is %c. Verify what you find with a follow-up
search or fetch before trusting it, and if one doesn't give you what you
need, try a different query or URL rather than giving up. Always end your
turn with a real answer using the best information you have -- never
leave only a tool call with no answer.
```

Deliberately doesn't say "use web search/fetch when something could be
outdated, even if you feel confident" -- that instruction now lives once,
per-tool, in each active hook's own `Description` (see below), which the
model already sees every time that tool is offered; repeating it here at a
generic level would just say the same thing twice.

Two things remain beyond the date anchor: it tells the model to keep
trying a different angle (query/URL) rather than stop at the first
unhelpful result -- this is real, load-bearing behavior a single tool's
own description can't express, since it spans multiple tool calls -- and
it tells the model to always produce a real answer, whatever happened
with the tools. That second instruction has a code-level backstop too --
`ChatService.Chat`'s own force-final-answer fallback (see above)
guarantees this even if a model ignores the instruction, but stating it
plainly up front makes the model's own last answer more likely to already
be the real thing, rather than relying on that fallback's extra
completion call every time.

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

## Suggested hook Name/Description/Parameters, and per-hook prompts

Each hook's `Name`/`Description`/`Parameters` are what actually tell the
model the tool exists and when to use it -- sent as part of the request's
`tools` list on every turn the hook is active, exactly like any other
OpenAI-compatible function definition. `Prompt` (Settings -> Chat -> Hooks
-> edit a hook) is optional EXTRA steering beyond that -- injected only
while the hook is active, unlike the endpoint-level System prompt, which is
unconditional and shared by everything.

web_search:

- Name: `web_search`
- Description: `Search the web for current information on a topic. Use this when the user asks about something that could have changed -- current events, prices, versions, schedules, who holds a position, or anything time-sensitive -- even if you feel confident.`
- Parameters: `{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}`
- Prompt: `An excerpt alone is rarely enough to verify a fact -- fetch the most relevant 3 results with the web_fetch tool, one per fetch, and confirm the answer against them before responding.`

The Prompt here carries exactly what's hook-specific and not already
implied by Name/Description: that a search result's excerpt alone isn't
enough, so it should chain into fetching (up to) 3 of the results with
web_fetch to actually verify the answer against real page content.

web_fetch:

- Name: `web_fetch`
- Description: `Fetch the text content of a specific URL. Use this when the user asks about a specific URL or its content, even if you feel confident or were given unrelated search results -- those are not the page itself.`
- Parameters: `{"type":"object","properties":{"url":{"type":"string","description":"The URL to fetch"}},"required":["url"]}`
- Prompt: (none needed -- Description already covers it)

The "even if you feel confident" phrasing in both descriptions matters: a
model with a strong prior about a well-known URL or fact (e.g.
wikipedia.org's title, a head-of-state's name) will otherwise just answer
from memory instead of actually calling the tool, especially when
RAG/deterministic web-search context is also enabled and gives it something
that merely looks like "I already did research." Naming that failure mode
explicitly measurably improves (though, being an LLM, never perfectly
guarantees) actual tool use over a shorter, softer description.

`ChatService.Chat`'s own `maxHookFollowUpRounds` is 4 -- sized for
web_search's own suggested chain (one search, then fetching 3 results:
4 tool calls total). If a model still wants one more tool call once that
budget is spent, its own force-final-answer fallback (see the top of this
file) makes one last, tool-free completion call instead of ever leaving
the user with an empty response.

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

Proxies a tool call's single argument straight to the self-hosted SearXNG
instance from `../searxng/` (`GET /search?q=...&format=json` over loopback
-- see `../searxng/README.md` for how that instance is set up and why
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
fetches the tool call's single argument directly (`http`/`https` only,
redirects locked to the same two schemes, response capped at 1MB). Requires
`curl`. **SSRF caveat**: the URL comes from the model's own output, which
can be indirectly attacker-influenced (see `application.runToolCalls`'s
security note) -- this script does not block requests to internal/private
addresses. Only enable this hook if that's an acceptable risk for your
deployment, or add a network-level restriction (e.g. a forward proxy
allowlist) in front of it.

## Adding another hook script

- Give it a distinct filename with no path separators -- `ChatHook.Script`
  is validated as a bare filename by `hookrunner.Runner` and resolved only
  against its configured directory, so a script can never live anywhere
  else or be reached via `../`.
- Give the hook a `Parameters` schema with exactly one property (any name);
  its value, whatever the model supplies when it calls the tool, is what
  `$1` receives -- `hookrunner.Runner` passes it as a real argv element,
  exactly once per call, never via environment variables or stdin.
- Keep it fast and side-effect-light: `hookrunner.Runner` enforces a 10s
  timeout and a 64KB cap on captured stdout per run, and a chat turn can
  trigger several tool calls (see `runToolCalls`'s `maxHookMatchesPerTurn`).
