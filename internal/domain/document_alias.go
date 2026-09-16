package domain

// Document alias reasons -- see ports.SQLRepository.RecordDocumentAlias and
// the document_aliases table's own reason column. Purely descriptive
// (never branched on), kept as named constants rather than inline string
// literals so the handful of call sites (application.crawlLoop for
// DocumentAliasReasonCanonicalTag; application.RunContentDedupJob for the
// content-hash/simhash reasons) can't typo a value that never matches
// anything a human reviewing the document_aliases table would recognize.
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

// DocumentAliasGroup is one canonical document plus every URL known to be
// an alias of it -- the admin content-dedup page's "what got merged"
// transparency listing (see ports.AdminRepository.ListDocumentAliasGroups),
// grouped directly from the live document_aliases table rather than a
// single run's in-memory result, so it stays accurate across processes and
// over time, not just right after the browser that triggered a run.
// CanonicalURL is empty when the canonical document itself hasn't actually
// been crawled yet (a forward-declared alias -- see RecordDocumentAlias).
type DocumentAliasGroup struct {
	CanonicalID  string   `json:"canonical_id"`
	CanonicalURL string   `json:"canonical_url,omitempty"`
	AliasURLs    []string `json:"alias_urls"`
}
