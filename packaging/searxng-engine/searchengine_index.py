"""SearXNG engine: this instance's own search index.

Queries this deployment's own `cmd/search` binary's public `GET /search`
JSON API and folds its hits into SearXNG's blended results, cited like any
other engine (Bing, Brave, DuckDuckGo -- see ../searxng/settings.yml) --
replacing the old, separate "RAG" mechanism where the chat backend queried
its own index directly, out of band from ordinary web search.

Install: copy this file into a SearXNG installation's `searx/engines/`
directory and add an `engines:` entry for it in that installation's
`settings.yml`. See ../README.md for the full install/config steps and for
why /search needs `internal_api_key` set here to work at all.

This module follows SearXNG's standard "online engine" contract (a stable,
long-established interface -- see
https://docs.searxng.org/dev/engines/online.html): module-level config
variables, plus `request()`/`response()` functions SearXNG calls itself.
Nothing here talks to SearXNG's own internals directly.
"""

from urllib.parse import quote

# engine_type/categories/paging are read directly off this module by
# SearXNG's engine loader (searx/engines/__init__.py) -- not passed as
# arguments, so their exact names below matter.
engine_type = "online"
categories = ["general"]

# /search takes top_k (a result count), not a page number -- it has no
# offset/page param at all, so pageno-based paging isn't something this
# engine can support. False tells SearXNG this engine only ever answers
# page 1; SearXNG won't call request() again with pageno > 1 for it.
paging = False

# --- Per-instance configuration -------------------------------------------
# Every variable below is a plain module-level default, meant to be
# overridden per-instance from this engine's own block in SearXNG's
# settings.yml (SearXNG assigns any extra key in an `engines:` entry --
# beyond the handful it reserves for itself, like `name`/`engine`/
# `shortcut`/`disabled` -- straight onto the loaded module as an
# attribute of the same name; see ../README.md's Configure section).
# Never hardcode a real internal_api_key here -- set it in settings.yml,
# analogous to how ../searxng/settings.yml keeps server.secret_key as a
# placeholder rather than a committed real value.

# Base URL of this deployment's public search-server (cmd/search),
# reachable from wherever SearXNG itself runs -- 127.0.0.1 only works when
# both processes share a host/network namespace, as they do in this
# repo's own se.mo-sys.de deployment.
base_url = "http://127.0.0.1:8080"

# Passed straight through as /search's own ?top_k= query parameter.
top_k = 10

# Sent as the X-Internal-API-Key header when non-empty, letting this
# engine call /search without a browser session -- see
# requireAuthAPIOrInternalKey in internal/adapters/restapi/auth.go and
# ../README.md's explanation of SEARCH_INTERNAL_API_KEY. Left empty by
# default: an unset key means this engine will get 401s from every
# request until an admin sets it here AND sets SEARCH_INTERNAL_API_KEY on
# the search-server binary, matching.
internal_api_key = ""


def request(query, params):
    """Build the outgoing request SearXNG will issue for `query`.

    `params` is SearXNG's per-request dict; setting params['url'] is how
    an online engine tells SearXNG what to fetch. See
    https://docs.searxng.org/dev/engines/online.html for the full shape
    of `params` (headers, cookies, timeout, ...) -- only 'url' and
    'headers' are needed here.
    """
    params["url"] = f"{base_url}/search?q={quote(query)}&top_k={top_k}"
    if internal_api_key:
        params["headers"]["X-Internal-API-Key"] = internal_api_key
    return params


def response(resp):
    """Parse this instance's /search JSON response into SearXNG's result list.

    /search's body is searchResponse{query, results: []domain.SearchResult}
    (see internal/adapters/restapi/handler.go's searchResponse and
    internal/domain/document.go's SearchResult) -- each result at minimum
    carries 'url'/'title'/'snippet', plus ranking fields ('score',
    'bm25_score', 'semantic_sim') and 'corrected_terms' that SearXNG's
    plain result list has no slot for and which are intentionally dropped
    here rather than forced into an unrelated field.

    SearXNG expects response() to return a list of dicts, each with at
    least 'url' and 'title' -- 'content' is its own convention for the
    snippet/description text shown under a result.
    """
    data = resp.json()
    results = []
    for item in data.get("results") or []:
        results.append(
            {
                "url": item.get("url", ""),
                "title": item.get("title", ""),
                "content": item.get("snippet", ""),
            }
        )
    return results
