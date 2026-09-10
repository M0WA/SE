package ports

import (
	"context"

	"searchengine/internal/domain"
)

// --- Secondary (driven) ports ---

type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

type RobotsChecker interface {
	Allowed(ctx context.Context, url string) bool
}

type Repository interface {
	Save(ctx context.Context, doc domain.Document) error
	All(ctx context.Context) ([]domain.Document, error)
}

type Indexer interface {
	Add(doc domain.Document)
	Search(query string, topK int) []domain.SearchResult
	DocCount() int
}

// EmbeddingProvider converts text into a vector.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Dimensions() int
}

// SQLRepository is the port to the relational database.
type SQLRepository interface {
	SaveDocument(ctx context.Context, doc domain.Document, embedding []float32) error
	PostingsForTerm(ctx context.Context, term string) ([]domain.PostingStats, error)
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	AllEmbeddings(ctx context.Context) (map[string][]float32, error)
	DocumentByID(ctx context.Context, docID string) (domain.Document, error)
}

// --- Primary (driving) ports ---

type SearchService interface {
	Search(ctx context.Context, query string, topK int) ([]domain.SearchResult, error)
}

type CrawlerService interface {
	Crawl(ctx context.Context, seedURLs []string, maxPages int) (int, error)
}
