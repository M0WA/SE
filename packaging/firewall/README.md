# Host firewall (iptables)

Optional, additional hardening for `se.mo-sys.de` and `gpu.mo-sys.de` -- a
second, independent layer on top of each service's own bind-address choice
(same "defense in depth" reasoning as `CRAWL_INTERNAL_TOKEN` in
[`../README.md`](../../README.md) and [Environment
variables](../../docs/manual/environment-variables.md)): every service on
both hosts already binds `127.0.0.1` or a private-LAN IP except nginx and
sshd, so this firewall shouldn't change what's actually reachable today --
it just makes that guarantee hold even if a future service is ever
misconfigured to bind `0.0.0.0`.

**Policy: on both hosts, only SSH (22) is publicly reachable; on
`se.mo-sys.de` only, nginx (80/443) is too.** Everything else inbound from
the public internet is dropped. Traffic from either host's private LAN
subnet(s) is always allowed, regardless of port, since that's where
inter-host traffic this deployment actually depends on lives:

- `se.mo-sys.de` <-> `gpu.mo-sys.de` embedding/chat calls (`10.7.226.0/24`,
  shared by both hosts -- see [Infrastructure
  options](../../docs/manual/infrastructure.md)).
- `se.mo-sys.de` <-> the managed Postgres DBaaS instance (`192.168.3.0/24`
  on this deployment -- the private LAN address the database attaches to;
  see `ionosctl dbaas postgres cluster create` in that same doc).

Only IPv4 is covered -- neither host has a public IPv6 address configured
(`ip -6 addr show` shows only link-local `fe80::/10`), so there's nothing
for `ip6tables` to actually restrict; add it if that ever changes.

## Files

- `se-mo-sys-de.sh` -- idempotent script (safe to re-run) building
  `se.mo-sys.de`'s `INPUT` chain: loopback, established/related, ICMP echo,
  SSH, HTTP/HTTPS, and both private subnets above, then sets the chain's
  default policy to `DROP`.
- `gpu-mo-sys-de.sh` -- same shape for `gpu.mo-sys.de`: loopback,
  established/related, ICMP echo, SSH, and the `10.7.226.0/24` private
  subnet, then `DROP`.

Both scripts only touch the `INPUT` chain -- `FORWARD`/`DOCKER`/
`DOCKER-ISOLATION-STAGE-1` (Docker's own chains, both hosts run Docker
containers -- DCGM exporter, SearXNG, `mcp-sandbox`) are left completely
alone. Never regenerate these via a full `iptables-restore` table dump --
that replaces the whole `filter` table, including Docker's own chains, and
would break every container's networking on the next `docker` restart.

## Apply -- **verify before you make it reboot-safe**

A misapplied host firewall can be as final as `rm -rf /` when it locks you
out of the box entirely with no other way in. Never skip the verification
step or persist an unverified ruleset:

1. Copy the relevant script to the host and run it. This only changes the
   *running* kernel netfilter state -- nothing survives a reboot yet:
   ```sh
   scp packaging/firewall/se-mo-sys-de.sh root@se.mo-sys.de:/tmp/
   ssh root@se.mo-sys.de 'bash /tmp/se-mo-sys-de.sh'
   ```
2. **From a brand new connection** (don't reuse the one you just used to
   apply it -- confirm a fresh TCP handshake actually gets through the new
   rules), confirm SSH still works and every port that should stay public
   still answers (`curl https://se.mo-sys.de/healthz`, etc.).
3. Only once step 2 is confirmed working, make it reboot-safe:
   ```sh
   apt-get install -y iptables-persistent   # ships netfilter-persistent
   netfilter-persistent save
   ```
   Before this step, a bad rule is one reboot away from fixing itself; after
   it, it isn't -- that ordering is the entire point.

If you're applying this somewhere you can't immediately verify from a
second connection (no separate console/KVM access as a fallback), schedule
an unattended revert first as a safety net, e.g.:
```sh
echo 'iptables -P INPUT ACCEPT' | at now + 5 minutes
```
and cancel it (`atq`, `atrm <job>`) once step 2 confirms the ruleset is safe.

## Keep this in sync

Whenever a service's bind address changes (a new port opened on a public
interface, a new private-LAN peer added), update the matching script here
in the same change -- see `CLAUDE.md`'s "Keep firewall rules in sync"
section.
