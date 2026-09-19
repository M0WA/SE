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
# Expects the self-hosted SearXNG instance from ../searxng/ reachable at
# 127.0.0.1:8888 (search.formats must include "json" -- see
# ../searxng/README.md).
set -eu
curl -s -G "http://127.0.0.1:8888/search" \
  --data-urlencode "q=${1:-}" \
  --data-urlencode "format=json"
