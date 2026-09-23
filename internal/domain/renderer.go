package domain

// Renderer selects how a crawl fetches HTML: plain HTTP (RendererNone) or a
// headless browser (RendererChromium/RendererFirefox). RendererDefault ("")
// means "inherit the Tuning page's global default" as a per-crawl override.
const (
	RendererDefault  = ""
	RendererNone     = "none"
	RendererChromium = "chromium"
	RendererFirefox  = "firefox"
)

// ValidRenderer reports whether name is a recognized renderer choice.
// RendererDefault ("") is only valid for a per-crawl override field;
// callers setting the global default itself should reject "" too.
func ValidRenderer(name string) bool {
	switch name {
	case RendererDefault, RendererNone, RendererChromium, RendererFirefox:
		return true
	default:
		return false
	}
}
