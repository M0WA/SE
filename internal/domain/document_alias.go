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
