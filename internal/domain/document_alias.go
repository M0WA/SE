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

// DocumentAliasGroup is one canonical document plus every alias URL of it
// -- the admin content-dedup "what got merged" listing, grouped from the
// live document_aliases table so it stays accurate across processes and
// time. CanonicalURL is empty when the canonical doc hasn't been crawled
// yet (a forward-declared alias -- see RecordDocumentAlias).
type DocumentAliasGroup struct {
	CanonicalID  string   `json:"canonical_id"`
	CanonicalURL string   `json:"canonical_url,omitempty"`
	AliasURLs    []string `json:"alias_urls"`
}
