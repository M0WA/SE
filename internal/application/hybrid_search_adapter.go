package application

import (
	"context"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchAdapter struct {
	hybrid *hybridSearchService
}

// NewHybridAsSearchService adapts a hybrid search service to the plain
// ports.SearchService contract, mapping each HybridResult's blended score
// into a SearchResult.
func NewHybridAsSearchService(repo ports.SQLRepository, embedders map[string]ports.EmbeddingProvider, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides, corpusStats *domain.CorpusStatsCache, vocabulary *domain.VocabularyCache) ports.SearchService {
	return &hybridSearchAdapter{hybrid: NewHybridSearchService(repo, embedders, settings, opSettings, overrides, corpusStats, vocabulary)}
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
			CorrectedTerms: r.CorrectedTerms,
		}
	}
	return results, nil
}
