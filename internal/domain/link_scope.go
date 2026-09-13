package domain

// LinkScope controls how far a crawl follows discovered links away from
// its seed URL(s): only the exact seed host(s) (LinkScopeHost), any
// subdomain of the seed's own registrable domain as well as that bare
// domain itself (LinkScopeDomain -- e.g. a seed of www.example.com also
// follows links to blog.example.com and example.com, but not
// example.org), or anywhere at all (LinkScopeAny).
//
// LinkScopeDefault ("") is distinct from LinkScopeDomain: as a per-crawl
// override (see ScheduledCrawl.LinkScope / ports.CrawlOptions.LinkScope),
// "" means "inherit whatever the Tuning page's global default currently
// is," the same convention Renderer already uses. The global default
// itself (OperationalSettingsValues.LinkScope) is never "" in practice --
// it normalizes to LinkScopeDomain when blank, matching this feature's
// confirmed default.
const (
	LinkScopeDefault = ""
	LinkScopeHost    = "host"
	LinkScopeDomain  = "domain"
	LinkScopeAny     = "any"
)

// ValidLinkScope reports whether name is a recognized link-scope choice.
// LinkScopeDefault ("") only counts as valid for a per-crawl override
// field (meaning "inherit the global default"); a caller that never
// allows inheriting (the global default setting itself) should reject ""
// too.
func ValidLinkScope(name string) bool {
	switch name {
	case LinkScopeDefault, LinkScopeHost, LinkScopeDomain, LinkScopeAny:
		return true
	default:
		return false
	}
}
