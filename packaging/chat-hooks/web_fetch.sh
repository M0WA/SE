#!/bin/sh
# $1: a URL to fetch (a regex capture group's raw text, passed as a real
# argv element by internal/adapters/hookrunner -- never shell-interpolated,
# so it can never be reinterpreted as additional curl flags or shell
# syntax). Equivalent to web_search.sh but for a specific URL instead of a
# search query.
#
# Best-effort guards only: http/https scheme required, redirects locked to
# the same two schemes, and the response capped at 1MB before hookrunner's
# own 64KB-of-stdout cap even applies. This does NOT protect against SSRF --
# a URL pointing at an internal/private address is still fetched as-is.
# Capture groups can be influenced by untrusted content when web-search/RAG
# context is enabled (see application.runChatHooks's own security note), so
# only enable this hook if that residual risk is acceptable for your
# deployment, or add a network-level restriction (e.g. a forward proxy
# allowlist) in front of it.
set -eu
case "$1" in
  http://*|https://*) ;;
  *) echo "web_fetch: only http/https URLs are allowed" >&2; exit 1 ;;
esac
curl -s -m 10 --proto '=http,https' --proto-redir '=http,https' -L --max-redirs 3 --max-filesize 1048576 "$1"
