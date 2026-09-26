package domain

import "time"

// GPUModeSettings configures the optional "Vision" (image/video generation)
// capability on the public chat page: switching the shared self-hosted GPU
// (gpu.mo-sys.de) between its normal chat/embedding role (vLLM) and a
// ComfyUI+LTX-Video generation role, since both can't run at once on one
// GPU's worth of VRAM. Deliberately its own settings row, independent of
// ChatVisionSettings (which is the unrelated, already-existing image
// *understanding* capability -- caption/similarity against an attached
// image, never touching the GPU's own running model).
type GPUModeSettings struct {
	// Enabled is the master kill switch: false means the Vision toggle
	// doesn't even appear on the public chat page, and every /vision/api/*
	// route returns 404 -- the capability doesn't exist at all until an
	// admin deliberately turns it on, regardless of anything else here.
	Enabled bool
	// ControlBaseURL is the GPU-side control service's own base URL, e.g.
	// http://10.7.226.11:8002 -- reached over the private LAN only, never
	// the public internet (see netguard.ConfiguredEndpointURLAllowed).
	ControlBaseURL string
	// ControlAPIKey authenticates to the control service (sent as the
	// X-Internal-Token header), encrypted at rest by restapi the same way
	// ChatEndpoint.APIKey/ChatVisionSettings.CaptionAPIKey are -- this
	// struct just carries whatever string it's given.
	ControlAPIKey string
	// SwitchTimeoutSeconds bounds how long a single chat<->vision switch
	// is allowed to take before it's reported as failed. <= 0 uses a
	// built-in default.
	SwitchTimeoutSeconds int
	// IdleRevertMinutes reverts an idle Vision session back to chat mode
	// automatically after this many minutes with no activity (see the
	// heartbeat mechanism) -- chat is the shared default every user
	// depends on, so a forgotten tab must not leave the deployment
	// chat-less indefinitely. 0 disables the revert.
	IdleRevertMinutes int
	UpdatedAt         time.Time
}
