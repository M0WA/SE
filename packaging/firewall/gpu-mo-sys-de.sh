#!/bin/bash
# gpu-mo-sys-de.sh -- host firewall for gpu.mo-sys.de. See ../README.md for
# the policy this implements and the mandatory verify-before-persist steps.
# Idempotent: safe to re-run (checks each rule before adding it), only ever
# touches the INPUT chain -- never Docker's own FORWARD/DOCKER chains (this
# host runs the DCGM exporter container).
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

# SSH -- from anywhere. The only publicly reachable service on this host:
# vllm-chat/vllm-embed bind their private-LAN IP (10.7.226.11) already,
# never 0.0.0.0 -- this is the second, independent layer, see ../README.md.
add -p tcp --dport 22

# Private LAN subnet -- se.mo-sys.de's embedding/chat calls, allowed on any
# port regardless of the rule above (10.7.226.0/24, shared with se.mo-sys.de).
add -s 10.7.226.0/24

# Default-deny anything else inbound. Set last, once every explicit ACCEPT
# above is in place -- see ../README.md's verify-before-persist steps
# before assuming this is safe to leave in place across a reboot.
iptables -P INPUT DROP

echo "gpu.mo-sys.de firewall applied (not yet reboot-safe -- see ../README.md)."
