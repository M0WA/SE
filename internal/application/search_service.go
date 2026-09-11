package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

var ErrEmptyQuery = errors.New("query must not be empty")

type searchService struct {
	index ports.Indexer
}

func NewSearchService(index ports.Indexer) ports.SearchService {
	return &searchService{index: index}
}

// Search ignores opts.Sort: the in-memory Indexer this wraps has no notion
// of crawl time, so it always ranks by relevance regardless -- only the
// SQL-backed hybrid search service supports recency sort.
func (s *searchService) Search(_ context.Context, query string, opts ports.SearchQuery) ([]domain.SearchResult, error) {
	if domain.ParseQuery(query).Empty() {
		return nil, ErrEmptyQuery
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}
	return s.index.Search(query, topK), nil
}
