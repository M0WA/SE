package domain

// Renderer selects how a crawl fetches each page's HTML: a plain HTTP
// request (RendererNone, the default), or a real headless browser that
// executes the page's JavaScript and waits for it to finish loading
// before extracting the final DOM (RendererChromium/RendererFirefox) --
// for a site whose real content only exists after client-side rendering.
//
// RendererDefault ("") is distinct from RendererNone ("none"): as a
// per-crawl override (see ScheduledCrawl.Renderer / CrawlOptions.Renderer),
// "" means "inherit whatever the Tuning page's global default currently
// is," while "none" explicitly forces plain HTTP even when the global
// default has rendering turned on. The global default itself
// (OperationalSettingsValues.DefaultRenderer) is never "" in practice --
// it normalizes to RendererNone when blank, the same way every other
// operational setting falls back to its own default.
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
