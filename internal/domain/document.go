package domain

import "time"

// Document represents a crawled and indexed web page. CrawledAt is the zero
// time for callers that never populate it (e.g. a freshly-crawled Document
// not yet saved).
type Document struct {
	ID        string
	URL       string
	Title     string
	Text      string
	Links     []string
	CrawledAt time.Time
}

// SearchResult is a single ranked result returned to the caller. BM25Score
// and SemanticSim are the unblended components behind Score -- omitted
// when a backend has no such breakdown. CorrectedTerms describes the
// query, not this document, and is identical across every result of one search.
type SearchResult struct {
	URL            string          `json:"url"`
	Title          string          `json:"title"`
	Snippet        string          `json:"snippet"`
	Score          float64         `json:"score"`
	BM25Score      float64         `json:"bm25_score,omitempty"`
	SemanticSim    float64         `json:"semantic_sim,omitempty"`
	CorrectedTerms []CorrectedTerm `json:"corrected_terms,omitempty"`
}

// IndexedDocument is a lightweight summary of a document held in the SQL
// index, without its full text -- for admin/diagnostic listings. Version
// counts up each time a re-crawl of the same URL changes its content;
// CrawledAt is when this version was last (re-)confirmed.
type IndexedDocument struct {
	ID            string
	URL           string
	Host          string
	Title         string
	DocLength     int
	Version       int
	CrawledAt     time.Time
	InternalLinks int
	ExternalLinks int
	Backlinks     int
	// PageRank is this document's current link-authority score
	// (documents.pagerank), last written by application.RunPageRankJob --
	// a neutral 1/N default before the first run ever computes it (see
	// sqlrepo's backfillPageRank/SaveDocument), never a bare 0.
	PageRank float64
}

// DocumentVersion is one prior, superseded version of a document, kept so
// an admin can see what a re-crawled page's content used to be.
type DocumentVersion struct {
	Version   int
	Title     string
	DocLength int
	CrawledAt time.Time
}

// DomainSummary is one distinct crawled domain and how many pages of it
// are indexed -- the shape returned by a domain search/listing.
type DomainSummary struct {
	Host     string
	DocCount int
}

// AgeBucket is a count of documents whose most recent crawl falls in one
// named time range (e.g. "last 24h").
type AgeBucket struct {
	Label string
	Count int
}

// VersionCount is how many documents currently sit at a given version
// number -- version 1 was only ever crawled once; higher means it's been
// re-crawled and changed that many times. Unaffected by
// MaxDocumentVersions pruning -- the count keeps rising even once old
// rows are pruned from document_versions.
type VersionCount struct {
	Version int
	Count   int
}

// StoredVersionsCount is how many documents have exactly StoredVersions
// rows actually retained (bounded by MaxDocumentVersions), distinct from
// VersionCount: a document changed 20 times sits at version 20, but with
// MaxDocumentVersions=3 only has 3 rows of history retained.
type StoredVersionsCount struct {
	StoredVersions int
	DocCount       int
}

// DocumentsOverview is the aggregate data behind the admin Overview page's
// summary panels: which domains hold the most pages, how recently the
// index was last refreshed, how many distinct domains are indexed at all,
// how documents are distributed across version numbers, and how many
// versions of each document are actually retained in storage right now.
type DocumentsOverview struct {
	TopDomains          []DomainSummary
	AgeBuckets          []AgeBucket
	TotalDomains        int
	VersionCounts       []VersionCount
	StoredVersionCounts []StoredVersionsCount
}
