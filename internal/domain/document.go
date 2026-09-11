package domain

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
// index, without its full text -- for admin/diagnostic listings.
type IndexedDocument struct {
	ID        string
	URL       string
	Title     string
	DocLength int
}
