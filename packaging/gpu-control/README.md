# gpu-control

A small, root-privileged HTTP control service that switches the shared
GPU on `gpu.mo-sys.de` between its normal chat role (`vllm-chat.service`)
and image/video generation (`comfyui.service`, running LTX-2.5), since
both can't fit in VRAM at once. Backs the public chat page's "Vision"
mode -- see `internal/adapters/httpgpumode`/`ports.GPUModeController`
(later PR) for the searchengine-side client, and
`docs/manual/chat-settings.md`'s "GPU mode (Vision)" section for the
admin-configured settings this service's own token/URL correspond to.

**Installs only on `gpu.mo-sys.de`, never on the main searchengine host.**
It is packaged as its own `.deb`
(`searchengine-gpu-control_<version>_amd64.deb`), separate from the main
`searchengine_<version>_amd64.deb` that bundles `search`/`admin`/`crawl`/
the `mcp-*` servers -- bundling this into that package would install a
root-privileged, systemctl-capable service onto `se.mo-sys.de` too, which
has no business running it and shouldn't hold `GPU_CONTROL_TOKEN` at all.

## Why a real Go binary, not a shell/systemd-socket hack

This repo's own CI (`go build/vet/test -race`, 100% coverage bar) and
`.deb` packaging pipeline come for free by being a normal Go binary in
this repo -- a shell script triggered by a socket unit would be smaller
but untestable and unreviewable for something that runs as root and can
stop the shared chat model for everyone.

## What it does and doesn't touch

- `vllm-chat.service` (the chat completions model, `:8001`) -- stopped to
  enter Vision mode, started to return to chat.
- `comfyui.service` (ComfyUI + LTX-2.5, `:8188` internally) -- the mirror
  image: started to enter Vision mode, stopped to return to chat. Stays
  systemd-**disabled** throughout (never enabled), so a host reboot can
  never race it against `vllm-chat.service` coming up on its own.
- `vllm-embed.service` (the embedding model backing search/indexing,
  `:8000`) is **never touched** -- it stays running in both modes, so
  search and indexing keep working regardless of chat/vision state.

Unit names are compile-time constants in `cmd/gpu-control`, never taken
from a request body -- the only thing `POST /gpu/api/mode` ever selects
is which of exactly two known transitions to run, via a fixed `systemctl`
argv with no shell involved.

## HTTP API

All routes except `/healthz` require a matching `X-Internal-Token`
header (constant-time compared, never optional -- the service refuses to
start with an empty `GPU_CONTROL_TOKEN`).

| Route | Purpose |
|---|---|
| `GET /gpu/api/mode` | Current `{mode, target, in_progress, since, expires_at, detail}`. |
| `POST /gpu/api/mode` `{"mode":"chat"\|"vision"}` | Begins a switch (`202`), or reports `200` if already there, or `409` if a switch to a *different* target is already in flight. Runs on a detached background goroutine, so a client disconnect never leaves the GPU half-switched. |
| `POST /gpu/api/heartbeat` | Resets the idle-revert timer (see below). `204`. |
| `GET /healthz` | Bare liveness check, unauthenticated. |

Generation endpoints (`/gpu/api/generate`, `/gpu/api/result`) are a
later PR, designed together with searchengine's own `/vision/api/*`
generation proxy so both sides agree on the ComfyUI blueprint-templating
shape up front, rather than guessing at it here first.

## Idle revert

While in Vision mode, if no `POST /gpu/api/heartbeat` arrives for
`GPU_CONTROL_IDLE_REVERT_MINUTES` (default 15), the service automatically
switches back to chat on its own -- chat is the shared default every user
depends on, so a forgotten browser tab must not leave the deployment
chat-less indefinitely. Set to `0` to disable.

## Install

1. Build/obtain `searchengine-gpu-control_<version>_amd64.deb` (see the
   root `Makefile`'s `deb-gpu-control` target, or a GitHub Release asset).
2. `dpkg -i searchengine-gpu-control_<version>_amd64.deb` on
   `gpu.mo-sys.de`.
3. Edit `/etc/searchengine/gpu-control.env` (mode `0600`, root-owned) --
   at minimum, set `GPU_CONTROL_TOKEN` (`openssl rand -hex 32`). The
   service will crash-loop under systemd until this is set; that's
   expected, not a bug.
4. `systemctl restart searchengine-gpu-control` once the token is set.
5. Enter the **same** token value into searchengine's own admin UI
   (Chat -> Settings -> "GPU mode (Vision)" -> Control endpoint token),
   along with Control endpoint URL = `http://10.7.226.11:8002` (this
   host's private-LAN address).

postinst enables and attempts to start the service automatically, same as
every other unit this repo ships -- step 3-4 above is only needed because,
unlike the other services, this one has no safe zero-config default.

## Firewall

**No change needed to `packaging/firewall/gpu-mo-sys-de.sh`.** This
service binds `10.7.226.11:8002` explicitly (never `0.0.0.0`), and that
script's existing ruleset already accepts the whole `10.7.226.0/24`
private subnet unconditionally -- see `CLAUDE.md`'s "Keep firewall rules
in sync" section, whose own trigger is a *public*-interface bind-address
change, which this isn't.

## Env vars

See `gpu-control.env`'s own inline comments for the full list
(`GPU_CONTROL_TOKEN`, `GPU_CONTROL_LISTEN_ADDR`,
`GPU_CONTROL_SWITCH_TIMEOUT_SECONDS`, `GPU_CONTROL_IDLE_REVERT_MINUTES`,
`GPU_CONTROL_COMFY_READY_URL`, `GPU_CONTROL_VLLM_READY_URL`) -- also
documented in `docs/manual/environment-variables.md`.
