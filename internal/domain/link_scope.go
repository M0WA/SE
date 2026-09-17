package domain

// LinkScope controls how far a crawl follows discovered links from its
// seed(s), strictest to most permissive: LinkScopeHost (exact seed host
// only), LinkScopeDomain (seed's registrable domain + subdomains),
// LinkScopeTLD (same, across any TLD too), LinkScopeAny (everywhere).
// LinkScopeDefault ("") means "inherit the Tuning page's global default"
// for a per-crawl override field; the global default itself is never "".
const (
	LinkScopeDefault = ""
	LinkScopeHost    = "host"
	LinkScopeDomain  = "domain"
	LinkScopeTLD     = "tld"
	LinkScopeAny     = "any"
)

// ValidLinkScope reports whether name is a recognized link-scope choice.
// LinkScopeDefault ("") only counts as valid for a per-crawl override
// field (meaning "inherit the global default"); a caller that never
// allows inheriting (the global default setting itself) should reject ""
// too.
func ValidLinkScope(name string) bool {
	switch name {
	case LinkScopeDefault, LinkScopeHost, LinkScopeDomain, LinkScopeTLD, LinkScopeAny:
		return true
	default:
		return false
	}
}
