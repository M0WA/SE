#!/bin/bash
# se-mo-sys-de.sh -- host firewall for se.mo-sys.de. See ../README.md for
# the policy this implements and the mandatory verify-before-persist steps.
# Idempotent: safe to re-run (checks each rule before adding it), only ever
# touches the INPUT chain -- never Docker's own FORWARD/DOCKER chains.
set -euo pipefail

add() {
  iptables -C INPUT "$@" -j ACCEPT 2>/dev/null || iptables -A INPUT "$@" -j ACCEPT
}

# Loopback and already-established/related connections first -- every
# other rule below only ever matches a genuinely new inbound connection.
add -i lo
add -m conntrack --ctstate ESTABLISHED,RELATED

# Basic diagnostics.
add -p icmp --icmp-type echo-request

# SSH -- from anywhere, both hosts.
add -p tcp --dport 22

# nginx -- from anywhere. The only other publicly reachable service on
# this host; everything else (search-server/admin-server/crawl-server,
# SearXNG, Prometheus, its exporters) binds 127.0.0.1 already, this is the
# second, independent layer -- see ../README.md.
add -p tcp --dport 80
add -p tcp --dport 443

# Private LAN subnets -- inter-host traffic this deployment depends on,
# allowed on any port regardless of the rules above:
#   10.7.226.0/24  -- shared with gpu.mo-sys.de (embedding/chat calls)
#   192.168.3.0/24 -- the managed Postgres DBaaS instance's private LAN
add -s 10.7.226.0/24
add -s 192.168.3.0/24

# Default-deny anything else inbound. Set last, once every explicit ACCEPT
# above is in place -- see ../README.md's verify-before-persist steps
# before assuming this is safe to leave in place across a reboot.
iptables -P INPUT DROP

echo "se.mo-sys.de firewall applied (not yet reboot-safe -- see ../README.md)."
