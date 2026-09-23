#!/bin/bash
# create-admin.sh -- seeds the first admin account directly into the
# database. There is no hardcoded admin account any more (see CLAUDE.md's
# "admin is just an is_admin=true users row"): every account, admin
# included, is created and managed from /admin/users once at least one
# admin can sign in. This script exists for the chicken-and-egg moment
# before that's true -- a fresh install, or one with zero is_admin=true
# rows for any reason -- where nothing can reach that page yet. Run it
# again any time to add another admin, or to reset one's password; nothing
# about it is one-time-only.
#
# Requires: htpasswd (apache2-utils) for bcrypt hashing, matching the same
# bcrypt.DefaultCost (10) this codebase's own admin_users.go uses; sqlite3
# or psql (postgresql-client) depending on DB_DRIVER.
#
# Usage:
#   ./create-admin.sh <username> [password]
#   DB_DRIVER/DB_DSN read from the environment if set, else parsed out of
#   /etc/searchengine/searchengine.env if present, else the same
#   sqlite/file:search.db?cache=shared default the Go binaries themselves
#   fall back to.

set -euo pipefail

usage() {
  echo "usage: $0 <username> [password]" >&2
  echo "  DB_DRIVER/DB_DSN: read from the environment, else /etc/searchengine/searchengine.env, else sqlite default." >&2
  exit 2
}

[ "$#" -ge 1 ] || usage
[ "$#" -le 2 ] || usage
username=$1
password=${2:-}

command -v htpasswd >/dev/null 2>&1 || {
  echo "error: htpasswd not found -- install it (apt-get install apache2-utils) and re-run." >&2
  exit 1
}

env_file="/etc/searchengine/searchengine.env"

# get_env_file_var reads KEY's value out of an EnvironmentFile-style
# KEY=VALUE file via grep/cut, never by sourcing it -- a DB_DSN containing
# "$" (a password with a literal dollar sign, not unheard of) would
# otherwise get silently mangled by shell variable expansion on source.
get_env_file_var() {
  local key="$1"
  [ -f "$env_file" ] || return 0
  grep -E "^${key}=" "$env_file" | tail -n1 | cut -d= -f2-
}

: "${DB_DRIVER:=$(get_env_file_var DB_DRIVER)}"
: "${DB_DRIVER:=sqlite}"
: "${DB_DSN:=$(get_env_file_var DB_DSN)}"
: "${DB_DSN:=file:search.db?cache=shared}"

if [ -z "$password" ]; then
  read -r -s -p "Password for '$username': " password
  echo >&2
  read -r -s -p "Confirm password: " password_confirm
  echo >&2
  [ "$password" = "$password_confirm" ] || { echo "error: passwords did not match." >&2; exit 1; }
fi

password_len=${#password}
# Mirrors admin_users.go's minUserPasswordLength/maxUserPasswordLength
# exactly -- a row created here must satisfy the same rule login enforces
# on every other account, and 72 bytes is bcrypt's own hard limit.
if [ "$password_len" -lt 8 ]; then
  echo "error: password must be at least 8 characters." >&2
  exit 1
fi
if [ "$password_len" -gt 72 ]; then
  echo "error: password must be at most 72 bytes." >&2
  exit 1
fi

# htpasswd's -B (bcrypt) -C 10 output is "<name>:$2y$10$<hash>" -- the $2y$
# prefix (vs. Go bcrypt's own $2a$/$2b$) is the same algorithm under a
# different OpenBSD-lineage version tag; golang.org/x/crypto/bcrypt (used
# by this codebase's own admin_users.go) accepts 2a/2b/2y interchangeably,
# so the hash this produces is a drop-in match for what CreateUser/
# UpdateUser store for every other account.
password_hash=$(htpasswd -nbBC 10 placeholder "$password" | cut -d: -f2-)
unset password password_confirm

sql_escape() {
  printf '%s' "$1" | sed "s/'/''/g"
}
username_esc=$(sql_escape "$username")
hash_esc=$(sql_escape "$password_hash")
now=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# slug_id mirrors domain.NewUserID/mintSlugID closely enough for a
# one-off bootstrap tool: lowercase, non-alphanumeric runs collapsed to
# "_", trimmed, capped at 20 chars (SlugIDPattern/the VARCHAR(20) id
# column) -- doesn't need byte-for-byte parity with the Go implementation,
# just a valid, likely-unique id.
slug_id() {
  local slug
  slug=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/_/g; s/^_+//; s/_+$//' | cut -c1-20)
  if [ -z "$slug" ]; then
    slug="user$(date +%s)"
    slug=${slug:0:20}
  fi
  printf '%s' "$slug"
}

case "$DB_DRIVER" in
  sqlite)
    command -v sqlite3 >/dev/null 2>&1 || {
      echo "error: sqlite3 not found -- install it (apt-get install sqlite3) and re-run." >&2
      exit 1
    }
    # DB_DSN is "file:<path>?cache=shared" (see bootstrap.OpenDB's own
    # default) -- strip the "file:" scheme and any "?..." query string to
    # get the plain filesystem path sqlite3 takes directly.
    sqlite_path=${DB_DSN#file:}
    sqlite_path=${sqlite_path%%\?*}
    db_query() { sqlite3 "$sqlite_path" "$1"; }
    db_exec() { sqlite3 "$sqlite_path" "$1"; }
    ;;
  postgres|pgx)
    command -v psql >/dev/null 2>&1 || {
      echo "error: psql not found -- install it (apt-get install postgresql-client) and re-run." >&2
      exit 1
    }
    db_query() { psql "$DB_DSN" -tAc "$1"; }
    db_exec() { psql "$DB_DSN" -c "$1" >/dev/null; }
    ;;
  *)
    echo "error: unsupported DB_DRIVER '$DB_DRIVER' -- this script handles sqlite and postgres only." >&2
    exit 1
    ;;
esac

existing_id=$(db_query "SELECT id FROM users WHERE username = '$username_esc';" | tr -d '[:space:]')

if [ -n "$existing_id" ]; then
  db_exec "UPDATE users SET password_hash = '$hash_esc', is_admin = true, updated_at = '$now' WHERE id = '$existing_id';"
  echo "Updated existing account '$username' (id=$existing_id): password reset, is_admin=true."
else
  id=$(slug_id "$username")
  # Trailing whitespace/CR trimmed per line only -- unlike the single-value
  # lookup above, this must keep one id per line for the exact-match loop
  # below.
  existing_ids=$(db_query "SELECT id FROM users;" | sed 's/[[:space:]]*$//')
  suffix=2
  candidate=$id
  while printf '%s\n' "$existing_ids" | grep -qx "$candidate"; do
    candidate="${id:0:$((20 - ${#suffix} - 1))}_${suffix}"
    suffix=$((suffix + 1))
  done
  id=$candidate
  db_exec "INSERT INTO users (id, username, password_hash, is_admin, custom_prompt, created_at, updated_at) VALUES ('$id', '$username_esc', '$hash_esc', true, '', '$now', '$now');"
  echo "Created admin account '$username' (id=$id)."
fi
