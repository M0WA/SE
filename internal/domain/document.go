package domain

// Document represents a crawled and indexed web page.
type Document struct {
	ID    string
	URL   string
	Title string
	Text  string
	Links []string
}

// SearchResult is a single ranked result returned to the caller.
type SearchResult struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// IndexedDocument is a lightweight summary of a document held in the SQL
// index, without its full text -- for admin/diagnostic listings.
type IndexedDocument struct {
	ID        string
	URL       string
	Title     string
	DocLength int
}
