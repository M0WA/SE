#!/bin/sh
## Deployed to /usr/local/bin/postgres-exporter-datasource.sh, mode 755.
##
## prometheus-postgres-exporter needs a DATA_SOURCE_NAME env var; the
## searchengine binaries already have their own DB_DSN, pointed at the same
## Postgres server, in /etc/searchengine/searchengine.env. Rather than keep
## a second copy of that connection string (and its password) anywhere on
## disk, this wrapper is exec'd by the exporter's own systemd unit with
## DB_DSN already loaded into its environment (see
## prometheus-postgres-exporter.override.conf's EnvironmentFile=) and just
## renames it to the variable the exporter actually reads.
set -e
export DATA_SOURCE_NAME="$DB_DSN"
exec /usr/bin/prometheus-postgres-exporter "$@"
