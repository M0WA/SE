# gpu-control

A small, root-privileged HTTP control service that switches the shared
GPU on `gpu.mo-sys.de` between its normal chat role (`vllm-chat.service`)
and image/video generation (`comfyui.service`, running LTX-2.5), since
both can't fit in VRAM at once. Backs the public chat page's "Vision"
mode -- see `internal/adapters/httpgpumode`/`ports.GPUModeController`
for the searchengine-side client (called by both `cmd/search`, for
switching/generating, and `cmd/admin`, for the direct ComfyUI-UI proxy
below), and `docs/manual/chat-settings.md`'s "GPU mode (Vision)" section
for the admin-configured settings this service's own token/URL
correspond to.

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
| `POST /gpu/api/generate` `{"prompt":"...", "aspect_ratio":"...", "duration_seconds":N, "megapixels":N, "negative_prompt":"...", "enhance_prompt":bool}` | Submits a new text-to-video job to ComfyUI (`202` with `{"prompt_id":"..."}`), using the fixed, embedded workflow template below with the prompt, a fresh random seed, and every other given field substituted in (each optional field left unset/zero uses the template's own original default). `400` for an out-of-range `aspect_ratio`/`duration_seconds`/`megapixels`. `409` if the GPU isn't currently in Vision mode. |
| `GET /gpu/api/generate/{id}` | Polls that job: `{"status":"pending"\|"done"\|"failed", "view_url":"...", "error":"..."}` -- `view_url` (once `"done"`) is a `GET /gpu/api/view` URL. |
| `GET /gpu/api/view?filename=...&subfolder=...&type=...` | Narrowly forwards exactly those three (plus `preview`) query params to ComfyUI's own read-only `GET /view`, streaming the resulting file's bytes back -- never a general-purpose proxy, unlike `/gpu/comfy/` below. |
| `GET /gpu/comfy/*` | Raw, unrestricted reverse proxy to ComfyUI's entire local web UI (including its own WebSocket for live queue/progress) -- reached only by `cmd/admin`'s own admin-gated `/admin/comfy/` (never directly by a browser, and never by `cmd/search`), for manually inspecting or debugging a workflow. |
| `GET /healthz` | Bare liveness check, unauthenticated. |

### The embedded text-to-video workflow template

`cmd/gpu-control/ltx_t2v_workflow.json` is ComfyUI's own bundled "Text to
Video (LTX-2.5)" template, already flattened into ComfyUI's executable
API prompt format (node id -> `{class_type, inputs}`) -- picked because
its baked-in model filenames (`ltx-2.5-22b-distilled-transformer-comfy-
int8-convrot.safetensors`, the matching text encoders/VAEs) exactly match
what's actually installed on `gpu.mo-sys.de`'s ComfyUI. The as-shipped
template is a deeply nested subgraph (40+ primitive nodes bundled behind
one custom node), which ComfyUI's own frontend alone knows how to expand
into that flat, executable shape (via its `app.graphToPrompt()`) --
hand-converting it risks a subtly wrong graph that silently produces
nothing, so it was extracted by driving a real, running ComfyUI instance
with a headless Chromium session over the Chrome DevTools Protocol
(`Page.navigate` to ComfyUI's own UI, `Runtime.evaluate` to call
`app.loadGraphData()` then `app.graphToPrompt()`, same raw-CDP-WebSocket
technique as this repo's own screenshot recipe -- see the root
`CLAUDE.md`) rather than reverse-engineered by hand.

`cmd/gpu-control/generate.go` overrides seven things in that fixed graph
per request: the positive-prompt node's text (node `405:376`), one
`RandomNoise` node's seed (node `405:339`, the one the template itself
marks `randomize`), the `ResolutionSelector` node's aspect ratio and
megapixels (node `409` -- aspect ratio is one of the exact 8 enum values
ComfyUI's own `GET /object_info/ResolutionSelector` reports; megapixels
is clamped to `minMegapixels`-`maxMegapixels`, narrower than that node's
own 0.1-16.0 range since this is video, not a single image -- every
extra megapixel multiplies cost by the frame count too), the `Duration`
`PrimitiveInt` node's value in seconds (node `405:362`, clamped to
`minDurationSeconds`-`maxDurationSeconds`), the negative-prompt
`CLIPTextEncode` node's text (node `405:373`, left at the template's own
fixed text when not given), and the "Enable Prompt Enhance"
`PrimitiveBoolean` node (node `405:383`, gating a real switch node
already in the graph between the raw prompt and an LLM-rewritten version
of it) -- every other parameter (sampler, the upscale/refinement pass) is
whatever the template's own defaults are. If ComfyUI's installed models or this
template ever change, re-extract it the same way: tunnel to ComfyUI's UI
(`ssh -L 18188:127.0.0.1:8188 root@gpu.mo-sys.de`), drive a headless
Chromium against it to load the matching template file (found under
ComfyUI's own `comfyui_workflow_templates_json` package,
`video_ltx2_5_t2v.json` as of this writing) and call `app.graphToPrompt()`,
then update the node-id constants in `generate.go` if the resulting
graph's node ids happen to change. If the aspect-ratio enum itself ever
changes, re-confirm it live via
`curl http://127.0.0.1:8188/object_info/ResolutionSelector` (tunneled the
same way) and update `visionAspectRatios` in both `cmd/gpu-control/
generate.go` and `internal/adapters/restapi/vision_mode.go` (the latter
deliberately duplicates the whitelist rather than importing this binary's
own package -- see that file's own doc comment for why).

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
`GPU_CONTROL_COMFY_READY_URL`, `GPU_CONTROL_VLLM_READY_URL`,
`GPU_CONTROL_VLLM_API_KEY`) -- also documented in
`docs/manual/environment-variables.md`.
