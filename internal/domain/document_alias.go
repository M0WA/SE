package domain

// Document alias reasons -- see document_aliases' reason column. Purely
// descriptive; named constants avoid a typo'd literal.
const (
	DocumentAliasReasonCanonicalTag   = "canonical_tag"
	DocumentAliasReasonContentExact   = "content_exact"
	DocumentAliasReasonContentSimHash = "content_simhash"
)

// Content-dedup matching methods -- see
// OperationalSettingsValues.ContentDedupMethod.
const (
	ContentDedupMethodExact   = "exact"
	ContentDedupMethodSimHash = "simhash"
)

// DocumentAlias is one alias_url row within a DocumentAliasGroup, paired
// with its own reason -- a canonical document can accumulate aliases from
// multiple sources over time, so reason is per-alias, not per-group.
type DocumentAlias struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// DocumentAliasGroup is one canonical document plus every alias of it --
// the admin content-dedup "what's aliased" listing. Includes every reason,
// not just content-dedup merges: most rows are ordinary canonical_tag
// bookkeeping (no document ever existed for the alias), which the admin UI
// must label distinctly from an actual merge since nothing was deleted.
// CanonicalURL is empty when the canonical doc hasn't been crawled yet.
type DocumentAliasGroup struct {
	CanonicalID  string          `json:"canonical_id"`
	CanonicalURL string          `json:"canonical_url,omitempty"`
	Aliases      []DocumentAlias `json:"aliases"`
}
