package domain

// Document repräsentiert eine gecrawlte und indexierte Webseite.
type Document struct {
	ID    string
	URL   string
	Title string
	Text  string
	Links []string
}

// SearchResult ist ein einzelnes gerankte Ergebnis, das nach außen geliefert wird.
type SearchResult struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}
