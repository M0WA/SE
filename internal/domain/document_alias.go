package domain

// Document alias reasons -- see document_aliases' reason column. Purely
// descriptive (never branched on); named constants avoid a typo'd literal
// that wouldn't match anything a human reviewing the table would recognize.
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
// with its own reason -- a single canonical document can accumulate
// aliases from more than one source over time (e.g. several rel=canonical
// tags recorded during ordinary crawling, plus a later content-dedup
// merge), so the reason is per-alias, not per-group.
type DocumentAlias struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// DocumentAliasGroup is one canonical document plus every alias of it --
// the admin content-dedup "what's aliased" listing, grouped from the live
// document_aliases table so it stays accurate across processes and time.
// This intentionally includes every reason, not just content-dedup merges:
// most rows here are ordinary canonical_tag bookkeeping from crawling (a
// document was never even created for the alias URL, let alone merged
// away), which the admin UI must label distinctly from an actual
// content_exact/content_simhash merge -- conflating the two under one
// undifferentiated "merged" label is misleading, since nothing was deleted
// for a canonical_tag alias. CanonicalURL is empty when the canonical doc
// hasn't been crawled yet (a forward-declared alias -- see
// RecordDocumentAlias).
type DocumentAliasGroup struct {
	CanonicalID  string          `json:"canonical_id"`
	CanonicalURL string          `json:"canonical_url,omitempty"`
	Aliases      []DocumentAlias `json:"aliases"`
}
