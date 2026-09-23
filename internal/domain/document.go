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

// SearchResult is a single ranked result. BM25Score/SemanticSim are the
// unblended components behind Score, omitted when a backend has no
// breakdown. CorrectedTerms describes the query and repeats across results.
type SearchResult struct {
	URL            string          `json:"url"`
	Title          string          `json:"title"`
	Snippet        string          `json:"snippet"`
	Score          float64         `json:"score"`
	BM25Score      float64         `json:"bm25_score,omitempty"`
	SemanticSim    float64         `json:"semantic_sim,omitempty"`
	CorrectedTerms []CorrectedTerm `json:"corrected_terms,omitempty"`
}

// IndexedDocument is a lightweight summary of a document in the SQL index,
// without full text, for admin/diagnostic listings. Version counts up each
// time a re-crawl changes content; CrawledAt is when last (re-)confirmed.
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
	// PageRank is this document's link-authority score, last written by
	// RunPageRankJob -- a neutral 1/N default before the first run, never 0.
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
// number -- higher means more re-crawls changed it. Unaffected by
// MaxDocumentVersions pruning; the count keeps rising even as old rows
// are pruned.
type VersionCount struct {
	Version int
	Count   int
}

// StoredVersionsCount is how many documents have exactly StoredVersions
// rows retained (bounded by MaxDocumentVersions), distinct from
// VersionCount: a doc at version 20 may only have 3 history rows retained.
type StoredVersionsCount struct {
	StoredVersions int
	DocCount       int
}

// DocumentsOverview is the aggregate data behind the admin Overview page's
// summary panels: top domains, index freshness, domain count, version
// distribution, and how much history is actually retained.
type DocumentsOverview struct {
	TopDomains          []DomainSummary
	AgeBuckets          []AgeBucket
	TotalDomains        int
	VersionCounts       []VersionCount
	StoredVersionCounts []StoredVersionsCount
}
