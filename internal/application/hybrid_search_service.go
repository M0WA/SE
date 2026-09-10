package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchService struct {
	repo     ports.SQLRepository
	embedder ports.EmbeddingProvider
	alpha    float64
}

func NewHybridSearchService(repo ports.SQLRepository, embedder ports.EmbeddingProvider, alpha float64) *hybridSearchService {
	return &hybridSearchService{repo: repo, embedder: embedder, alpha: alpha}
}

func (s *hybridSearchService) Search(ctx context.Context, query string, topK int) ([]domain.HybridResult, error) {
	terms := domain.Tokenize(query)
	if len(terms) == 0 {
		return nil, errors.New("query must not be empty")
	}
	if topK <= 0 {
		topK = 10
	}

	bm25PerDoc := make(map[string][]domain.PostingStats)
	seen := make(map[string]bool)
	for _, term := range terms {
		if seen[term] {
			continue
		}
		seen[term] = true
		postings, err := s.repo.PostingsForTerm(ctx, term)
		if err != nil {
			return nil, err
		}
		for _, p := range postings {
			bm25PerDoc[p.DocID] = append(bm25PerDoc[p.DocID], p)
		}
	}

	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	embeddings, err := s.repo.AllEmbeddings(ctx)
	if err != nil {
		return nil, err
	}

	candidateIDs := make(map[string]bool)
	for id := range bm25PerDoc {
		candidateIDs[id] = true
	}
	for id := range embeddings {
		candidateIDs[id] = true
	}

	candidates := make([]domain.HybridResult, 0, len(candidateIDs))
	for id := range candidateIDs {
		bm25 := domain.BM25ScoreDocument(bm25PerDoc[id])
		semantic := domain.CosineSimilarity(queryVec, embeddings[id])
		candidates = append(candidates, domain.HybridResult{
			DocID: id, BM25Score: bm25, SemanticSim: semantic,
		})
	}

	ranked := domain.CombineScores(candidates, s.alpha)
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	for i := range ranked {
		doc, err := s.repo.DocumentByID(ctx, ranked[i].DocID)
		if err != nil {
			continue
		}
		ranked[i].URL = doc.URL
		ranked[i].Title = doc.Title
		ranked[i].Snippet = domain.Snippet(doc.Text, terms, 200)
	}

	return ranked, nil
}
