package application

import (
	"context"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchAdapter struct {
	hybrid *hybridSearchService
}

// NewHybridAsSearchService adapts a hybrid (BM25 + semantic) search service to
// the plain ports.SearchService contract expected by the HTTP layer, mapping
// each domain.HybridResult's blended score into a domain.SearchResult.
func NewHybridAsSearchService(repo ports.SQLRepository, embedder ports.EmbeddingProvider, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides, corpusStats *domain.CorpusStatsCache) ports.SearchService {
	return &hybridSearchAdapter{hybrid: NewHybridSearchService(repo, embedder, settings, opSettings, overrides, corpusStats)}
}

func (a *hybridSearchAdapter) Search(ctx context.Context, query string, opts ports.SearchQuery) ([]domain.SearchResult, error) {
	hybridResults, err := a.hybrid.Search(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	results := make([]domain.SearchResult, len(hybridResults))
	for i, r := range hybridResults {
		results[i] = domain.SearchResult{
			URL: r.URL, Title: r.Title, Snippet: r.Snippet, Score: r.FinalScore,
			BM25Score: r.BM25Score, SemanticSim: r.SemanticSim,
		}
	}
	return results, nil
}
