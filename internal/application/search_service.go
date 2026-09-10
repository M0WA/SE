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

func (s *searchService) Search(_ context.Context, query string, topK int) ([]domain.SearchResult, error) {
	if len(domain.Tokenize(query)) == 0 {
		return nil, ErrEmptyQuery
	}
	if topK <= 0 {
		topK = 10
	}
	return s.index.Search(query, topK), nil
}
