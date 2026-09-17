package domain

// Renderer selects how a crawl fetches HTML: plain HTTP (RendererNone),
// or a headless browser executing JS (RendererChromium/RendererFirefox).
// RendererDefault ("") means "inherit the Tuning page's global default"
// as a per-crawl override, while "none" explicitly forces plain HTTP.
const (
	RendererDefault  = ""
	RendererNone     = "none"
	RendererChromium = "chromium"
	RendererFirefox  = "firefox"
)

// ValidRenderer reports whether name is a recognized renderer choice.
// RendererDefault ("") only counts as valid for a per-crawl override
// field (meaning "inherit the global default"); callers that never allow
// inheriting (the global default setting itself) should reject "" too.
func ValidRenderer(name string) bool {
	switch name {
	case RendererDefault, RendererNone, RendererChromium, RendererFirefox:
		return true
	default:
		return false
	}
}
