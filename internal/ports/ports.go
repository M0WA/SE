package ports

import (
	"context"
	"errors"

	"searchengine/internal/domain"
)

// ErrDocumentNotFound is returned by SQLRepository/AdminRepository's
// DeleteDocument when no document with the given ID exists.
var ErrDocumentNotFound = errors.New("document not found")

// --- Secondary (driven) ports ---

type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

// FetchOptions carries per-request credentials for sites that need a
// session cookie or HTTP Basic auth to crawl.
type FetchOptions struct {
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
}

// AuthFetcher is a Fetcher that also accepts per-request credentials.
// httpfetcher.Fetcher implements both this and the plain Fetcher (used by
// the robots-checker, which never needs credentials).
type AuthFetcher interface {
	FetchWithOptions(ctx context.Context, url string, opts FetchOptions) (string, error)
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
	ListDocuments(ctx context.Context, limit int) ([]domain.IndexedDocument, error)
	DeleteDocument(ctx context.Context, docID string) error
}

// AdminRepository is the subset of SQLRepository the admin diagnostics UI
// needs -- a narrower dependency than the full port.
type AdminRepository interface {
	CorpusStats(ctx context.Context) (totalDocs int, avgDocLen float64, err error)
	ListDocuments(ctx context.Context, limit int) ([]domain.IndexedDocument, error)
	PostingsForTerm(ctx context.Context, term string) ([]domain.PostingStats, error)
	DeleteDocument(ctx context.Context, docID string) error
}

// --- Primary (driving) ports ---

type SearchService interface {
	Search(ctx context.Context, query string, topK int) ([]domain.SearchResult, error)
}

// CrawlOptions is a single crawl request: seed URLs and page budget, plus
// optional credentials for sites that require a session cookie or HTTP
// Basic auth (applied to every fetch made during that crawl).
type CrawlOptions struct {
	SeedURLs      []string
	MaxPages      int
	Cookie        string
	BasicAuthUser string
	BasicAuthPass string
}

type CrawlerService interface {
	Crawl(ctx context.Context, opts CrawlOptions) (int, error)
}

// DebugSearchService exposes the raw, unblended hybrid search results
// (BM25/semantic/final score breakdown) for admin diagnostics.
type DebugSearchService interface {
	Search(ctx context.Context, query string, topK int) ([]domain.HybridResult, error)
}
