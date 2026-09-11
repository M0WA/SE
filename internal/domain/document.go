package domain

import "time"

// Document represents a crawled and indexed web page.
type Document struct {
	ID    string
	URL   string
	Title string
	Text  string
	Links []string
}

// SearchResult is a single ranked result returned to the caller. BM25Score
// and SemanticSim are the unblended components behind Score -- omitted
// when a backend (e.g. the plain in-memory index) has no such breakdown.
type SearchResult struct {
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
	BM25Score   float64 `json:"bm25_score,omitempty"`
	SemanticSim float64 `json:"semantic_sim,omitempty"`
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

// DocumentsOverview is the aggregate data behind the admin Documents
// page's summary charts: which domains hold the most pages, and how
// recently the index was last refreshed.
type DocumentsOverview struct {
	TopDomains []DomainSummary
	AgeBuckets []AgeBucket
}
