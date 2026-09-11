package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchService struct {
	repo      ports.SQLRepository
	embedder  ports.EmbeddingProvider
	settings  *domain.TuningSettings
	overrides *domain.RankingOverrides
}

func NewHybridSearchService(repo ports.SQLRepository, embedder ports.EmbeddingProvider, settings *domain.TuningSettings, overrides *domain.RankingOverrides) *hybridSearchService {
	return &hybridSearchService{repo: repo, embedder: embedder, settings: settings, overrides: overrides}
}

func (s *hybridSearchService) Search(ctx context.Context, query string, opts ports.SearchQuery) ([]domain.HybridResult, error) {
	parsed := domain.ParseQuery(query)
	if parsed.Empty() {
		return nil, errors.New("query must not be empty")
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}
	recency := opts.Sort == ports.SortRecency
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

	overrides := s.overrides.Get()
	hasOverrides := len(overrides.BlockedTerms) > 0 || len(overrides.BlockedDomains) > 0 ||
		len(overrides.BoostedTerms) > 0 || len(overrides.BoostedDomains) > 0

	// +required/-excluded/"phrase"/site: constraints and blocked
	// terms/domains need each candidate's full text and URL, which the
	// ranking step below doesn't otherwise fetch until after truncating to
	// topK -- so filter (and cache the fetched docs for reuse below,
	// including by the boost step and recency sort) before ranking,
	// whenever such constraints exist or recency sort needs every
	// candidate's CrawledAt.
	docCache := make(map[string]domain.Document)
	if parsed.HasConstraints() || hasOverrides || recency {
		for id := range candidateIDs {
			doc, err := s.repo.DocumentByID(ctx, id)
			if err != nil {
				delete(candidateIDs, id)
				continue
			}
			docCache[id] = doc
			if !parsed.Matches(doc.Title, doc.Text) || !parsed.SiteAllowed(doc) || overrides.Blocked(doc) {
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
			DocID: id, BM25Score: bm25, SemanticSim: semantic, CrawledAt: docCache[id].CrawledAt,
		})
	}

	ranked := domain.CombineScores(candidates, alpha)
	if hasOverrides {
		boosted := false
		for i := range ranked {
			if doc, ok := docCache[ranked[i].DocID]; ok {
				if f := overrides.BoostFactor(doc); f != 1.0 {
					ranked[i].FinalScore *= f
					boosted = true
				}
			}
		}
		if boosted {
			domain.SortByFinalScore(ranked)
		}
	}
	if recency {
		// Recency sort ignores BM25/semantic/final score entirely -- this
		// overrides whatever order CombineScores/boosting produced above.
		domain.SortByCrawledAt(ranked)
	}
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
