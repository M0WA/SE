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
	settings *domain.TuningSettings
}

func NewHybridSearchService(repo ports.SQLRepository, embedder ports.EmbeddingProvider, settings *domain.TuningSettings) *hybridSearchService {
	return &hybridSearchService{repo: repo, embedder: embedder, settings: settings}
}

func (s *hybridSearchService) Search(ctx context.Context, query string, topK int) ([]domain.HybridResult, error) {
	parsed := domain.ParseQuery(query)
	if parsed.Empty() {
		return nil, errors.New("query must not be empty")
	}
	if topK <= 0 {
		topK = 10
	}
	terms := parsed.AllTerms()

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

	// +required/-excluded/"phrase" constraints need each candidate's full
	// text, which the ranking step below doesn't otherwise fetch until
	// after truncating to topK -- so filter (and cache the fetched docs
	// for reuse below) before ranking, only when such constraints exist.
	docCache := make(map[string]domain.Document)
	if parsed.HasConstraints() {
		for id := range candidateIDs {
			doc, err := s.repo.DocumentByID(ctx, id)
			if err != nil {
				delete(candidateIDs, id)
				continue
			}
			docCache[id] = doc
			if !parsed.Matches(doc.Title, doc.Text) {
				delete(candidateIDs, id)
			}
		}
	}

	alpha, k1, b := s.settings.Get()

	candidates := make([]domain.HybridResult, 0, len(candidateIDs))
	for id := range candidateIDs {
		bm25 := domain.BM25ScoreDocument(bm25PerDoc[id], k1, b)
		semantic := domain.CosineSimilarity(queryVec, embeddings[id])
		candidates = append(candidates, domain.HybridResult{
			DocID: id, BM25Score: bm25, SemanticSim: semantic,
		})
	}

	ranked := domain.CombineScores(candidates, alpha)
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	for i := range ranked {
		doc, ok := docCache[ranked[i].DocID]
		if !ok {
			var err error
			doc, err = s.repo.DocumentByID(ctx, ranked[i].DocID)
			if err != nil {
				continue
			}
		}
		ranked[i].URL = doc.URL
		ranked[i].Title = doc.Title
		ranked[i].Snippet = domain.Snippet(doc.Text, terms, 200)
	}

	return ranked, nil
}
