// Command gpu-control is a root-privileged HTTP control service that
// runs only on gpu.mo-sys.de -- never on se.mo-sys.de, and never exposed
// by any nginx/public interface. It switches the shared H200 GPU between
// its normal chat role (vllm-chat.service) and image/video generation
// (comfyui.service, running LTX-2.5), since both can't fit in VRAM at
// once. vllm-embed.service (search/indexing's own embedding model) is
// deliberately never touched here -- it stays running in both modes, so
// search/indexing keeps working regardless of chat/vision state.
//
// Reached only by searchengine's own internal/adapters/httpgpumode
// client (a later PR), over the private LAN, authenticated by a shared
// X-Internal-Token (GPU_CONTROL_TOKEN, generated once and stored in both
// this host's /etc/searchengine/gpu-control.env and searchengine's own
// admin-configured GPU mode settings). Binds the private-LAN interface
// explicitly (GPU_CONTROL_LISTEN_ADDR, default 10.7.226.11:8002), never
// 0.0.0.0 -- see packaging/gpu-control/README.md for the full deployment
// picture, including why no firewall change is needed.
//
// Unit names (chatUnit/comfyUnit below) are compile-time constants, never
// taken from a request body -- the only thing POST /gpu/api/mode ever
// selects is which of exactly two known transitions to run, executed via
// a fixed systemctl argv with no shell involved (see controller.go's
// realUnitRunner).
package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"searchengine/internal/bootstrap"
)

const (
	chatUnit  = "vllm-chat.service"
	comfyUnit = "comfyui.service"

	defaultListenAddr        = "10.7.226.11:8002"
	defaultSwitchTimeout     = 300 * time.Second
	defaultIdleRevertMinutes = 15
	defaultPollInterval      = 2 * time.Second
	defaultIdleCheckInterval = 30 * time.Second

	defaultComfyReadyURL = "http://127.0.0.1:8188/system_stats"
	defaultVLLMReadyURL  = "http://10.7.226.11:8001/v1/models"
)

func main() {
	token := bootstrap.GetEnv("GPU_CONTROL_TOKEN", "")
	if token == "" {
		log.Fatal("GPU_CONTROL_TOKEN must be set -- refusing to start a root-privileged control service with no auth")
	}
	addr := bootstrap.GetEnv("GPU_CONTROL_LISTEN_ADDR", defaultListenAddr)
	switchTimeoutSeconds := envInt("GPU_CONTROL_SWITCH_TIMEOUT_SECONDS", int(defaultSwitchTimeout/time.Second))
	idleRevertMinutes := envInt("GPU_CONTROL_IDLE_REVERT_MINUTES", defaultIdleRevertMinutes)
	comfyReadyURL := bootstrap.GetEnv("GPU_CONTROL_COMFY_READY_URL", defaultComfyReadyURL)
	vllmReadyURL := bootstrap.GetEnv("GPU_CONTROL_VLLM_READY_URL", defaultVLLMReadyURL)

	c := NewController(ControllerConfig{
		ChatUnit:          chatUnit,
		ComfyUnit:         comfyUnit,
		ComfyReadyURL:     comfyReadyURL,
		VLLMReadyURL:      vllmReadyURL,
		SwitchTimeout:     time.Duration(switchTimeoutSeconds) * time.Second,
		IdleRevertMinutes: idleRevertMinutes,
		PollInterval:      defaultPollInterval,
	})

	startupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	c.DetectInitialMode(startupCtx)
	cancel()

	go c.RunIdleRevertLoop(context.Background(), defaultIdleCheckInterval)

	log.Printf("gpu-control running on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newMux(c, token)))
}

// envInt mirrors bootstrap.GetEnv's own "env var, falling back to a
// default" convention for an int, the same way cmd/mcp-sandbox's
// envBool/envDuration do for their own types -- an unset or unparseable
// value is silently treated as unset (falls back) rather than crashing
// startup over a typo'd env var.
func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(bootstrap.GetEnv(key, ""))
	if err != nil {
		return fallback
	}
	return v
}
