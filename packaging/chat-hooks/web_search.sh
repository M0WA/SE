#!/bin/sh
# web_search hook script -- see ../chat-hooks/README.md for the full setup.
#
# $1: the search query (a regex capture group's raw text, passed as a real
# argv element by internal/adapters/hookrunner -- never shell-interpolated)
# -- curl -G with --data-urlencode encodes it correctly regardless of what
# characters it contains, and being passed as an argv element (not through a
# shell) means it can never be reinterpreted as additional curl flags or
# shell syntax either.
#
# WEB_SEARCH_BASE_URL: the SearXNG instance's base URL, read from the
# process environment -- set by internal/adapters/hookrunner.Runner from
# ChatService.Chat's own env map, which is built from the
# ADMIN-CONFIGURED domain.ChatEndpoint.WebSearchBaseURL setting
# (Settings -> Chat), never a value derived from the model's own output or
# a capture group -- see ports.HookScriptRunner's and
# internal/adapters/hookrunner.Runner.RunHookScript's doc comments for why
# this doesn't reopen the injection surface application.runChatHooks's
# security doc comment describes: only $1 above ever carries
# match-derived data, and env vars are never eval'd or otherwise
# shell-interpreted. Falls back to 127.0.0.1:8888 if unset (e.g. this
# script invoked standalone rather than through ChatService.Chat).
#
# Expects the self-hosted SearXNG instance from ../searxng/ (search.formats
# must include "json" -- see ../searxng/README.md).
set -eu
curl -s -G "${WEB_SEARCH_BASE_URL:-http://127.0.0.1:8888}/search" \
  --data-urlencode "q=${1:-}" \
  --data-urlencode "format=json"
