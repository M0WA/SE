package domain

// LinkScope controls how far a crawl follows links from its seed(s),
// strictest to loosest: Host (exact seed host), Domain (+ subdomains), TLD
// (any TLD too), Any (everywhere). LinkScopeDefault ("") means "inherit the
// Tuning page's global default" for a per-crawl override field.
const (
	LinkScopeDefault = ""
	LinkScopeHost    = "host"
	LinkScopeDomain  = "domain"
	LinkScopeTLD     = "tld"
	LinkScopeAny     = "any"
)

// ValidLinkScope reports whether name is a recognized link-scope choice.
// LinkScopeDefault ("") is only valid for a per-crawl override field; a
// caller setting the global default itself should reject "" too.
func ValidLinkScope(name string) bool {
	switch name {
	case LinkScopeDefault, LinkScopeHost, LinkScopeDomain, LinkScopeTLD, LinkScopeAny:
		return true
	default:
		return false
	}
}
