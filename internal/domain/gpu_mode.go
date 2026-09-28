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
	UpdatedAt            time.Time
}

// GPUMode is one of the two workloads cmd/gpu-control switches the shared
// GPU between. GPUModeUnknown is the honest value before the control
// service's first successful probe, or after it restarts mid-switch.
type GPUMode string

const (
	GPUModeChat    GPUMode = "chat"
	GPUModeVision  GPUMode = "vision"
	GPUModeUnknown GPUMode = "unknown"
)

// GPUModeStatus mirrors cmd/gpu-control's own wire shape for GET/POST
// /gpu/api/mode exactly (see that package's own Status type) -- Target/
// Since/ExpiresAt are only meaningful while InProgress.
type GPUModeStatus struct {
	Mode       GPUMode
	Target     GPUMode
	InProgress bool
	Since      time.Time
	ExpiresAt  time.Time
	Detail     string
}

// VisionGenerateOptions carries POST /vision/api/generate's caller-
// selectable generation parameters (every other workflow parameter --
// model, sampler, sigmas, seed -- is fixed/randomized by cmd/gpu-control's
// own embedded template, never caller-selectable). Every field left at
// its zero value falls back to the template's own original fixed default
// (see cmd/gpu-control/generate.go's own defaults), so an older caller
// that only ever sent Prompt keeps getting identical behavior.
type VisionGenerateOptions struct {
	Prompt          string
	AspectRatio     string
	DurationSeconds int
	// Megapixels is the ResolutionSelector node's own actual output-size
	// dial (AspectRatio only picks the shape) -- 0 means "use the
	// template's own default".
	Megapixels float64
	// NegativePrompt overrides the template's own fixed "what to avoid"
	// text -- left empty, that fixed text is used unchanged.
	NegativePrompt string
	// EnhancePrompt toggles the template's own built-in LLM prompt-
	// rewrite pass (a real switch node in the graph, not a searchengine-
	// side feature) -- false (its own zero value) matches the template's
	// original default of leaving it off.
	EnhancePrompt bool
}

// GPUGenerateResult mirrors cmd/gpu-control's own GET /gpu/api/generate/{id}
// wire shape: Status is one of "pending", "done" (ViewURL set), or
// "failed" (Error set) -- see that package's own generate.go for what
// produces each. FileID is application-layer only (never comes from
// cmd/gpu-control) -- set once GPUModeService has saved the finished
// video into the requesting account's own files, so it survives a page
// reload/lost job id instead of being reachable only through the
// in-memory job id a browser tab happened to still have.
type GPUGenerateResult struct {
	Status  string
	ViewURL string
	Error   string
	FileID  string
}
