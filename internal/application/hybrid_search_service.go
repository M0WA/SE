package application

import (
	"context"
	"errors"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchService struct {
	repo        ports.SQLRepository
	embedder    ports.EmbeddingProvider
	settings    *domain.TuningSettings
	opSettings  *domain.OperationalSettings
	corpusStats *domain.CorpusStatsCache
	overrides   *domain.RankingOverrides
	vocabulary  *domain.VocabularyCache
}

func NewHybridSearchService(repo ports.SQLRepository, embedder ports.EmbeddingProvider, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides, corpusStats *domain.CorpusStatsCache, vocabulary *domain.VocabularyCache) *hybridSearchService {
	return &hybridSearchService{repo: repo, embedder: embedder, settings: settings, opSettings: opSettings, corpusStats: corpusStats, overrides: overrides, vocabulary: vocabulary}
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

	seen := make(map[string]bool)
	uniqueTerms := make([]string, 0, len(terms))
	for _, term := range terms {
		if seen[term] {
			continue
		}
		seen[term] = true
		uniqueTerms = append(uniqueTerms, term)
	}

	// One batched query across every unique query term (rather than one
	// join query -- plus a separate doc-freq COUNT(*) query -- per term),
	// and corpus-wide stats (totalDocs/avgDocLen) read once from an
	// in-memory cache kept fresh by bootstrap.SyncCorpusStats, rather than
	// a full-table COUNT/AVG scan repeated for every term of every request.
	postingsByTerm, err := s.repo.PostingsForTerms(ctx, uniqueTerms)
	if err != nil {
		return nil, err
	}

	// Fuzzy (typo-tolerant) query matching: a term with zero postings hits
	// gets one chance at a bounded-edit-distance vocabulary lookup, and --
	// if found -- its near-miss substitute's postings stand in for it in
	// BM25 scoring only. A term that already matched something is never
	// touched, and the substitution is always reported back via
	// correctedTerms so a caller/UI can show it transparently rather than
	// silently rewriting the displayed query. When FuzzyMatchEnabled is
	// false, this whole block is skipped and behavior is byte-for-byte the
	// same as before this feature existed.
	opValues := s.opSettings.Get()
	scoringTerm := make(map[string]string, len(uniqueTerms)) // original -> term to actually score with (itself, unless corrected)
	var correctedTerms []domain.CorrectedTerm
	if opValues.FuzzyMatchEnabled {
		vocabulary := s.vocabulary.Get()
		var toFetch []string
		for _, term := range uniqueTerms {
			if len(postingsByTerm[term]) > 0 {
				continue
			}
			match, _, found := domain.NearestTerm(term, vocabulary, opValues.FuzzyMaxEditDistance)
			if !found {
				continue
			}
			scoringTerm[term] = match
			correctedTerms = append(correctedTerms, domain.CorrectedTerm{Original: term, Corrected: match})
			if _, ok := postingsByTerm[match]; !ok {
				toFetch = append(toFetch, match)
			}
		}
		if len(toFetch) > 0 {
			fetched, err := s.repo.PostingsForTerms(ctx, toFetch)
			if err != nil {
				return nil, err
			}
			for term, postings := range fetched {
				postingsByTerm[term] = postings
			}
		}
	}

	totalDocs, avgDocLen := s.corpusStats.Get()
	bm25PerDoc := make(map[string][]domain.PostingStats)
	for _, term := range uniqueTerms {
		lookupTerm := term
		if corrected, ok := scoringTerm[term]; ok {
			lookupTerm = corrected
		}
		for _, p := range postingsByTerm[lookupTerm] {
			p.TotalDocs = totalDocs
			p.AvgDocLen = avgDocLen
			bm25PerDoc[p.DocID] = append(bm25PerDoc[p.DocID], p)
		}
	}

	queryVec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	// Computed once per Search call rather than inside every per-candidate
	// cosine-similarity comparison below -- the query vector never changes
	// across those comparisons within one request.
	queryNorm := domain.VectorNorm(queryVec)

	// Score the semantic side against a bounded candidate set rather than
	// the entire corpus: every BM25 hit (however many that is -- bounded by
	// the query's own postings, not corpus size) plus a fixed-size sample
	// of the rest of the corpus, so a purely semantic match (no BM25 hits
	// at all) can still be found without a full-table scan on every
	// request.
	bm25HitIDs := make([]string, 0, len(bm25PerDoc))
	for id := range bm25PerDoc {
		bm25HitIDs = append(bm25HitIDs, id)
	}
	// A site: filter must never depend on whether its matches happen to be
	// a BM25 hit or land in the semantic sample below -- forcing them into
	// the fetch set here guarantees every document on the requested site(s)
	// is actually considered, not silently dropped by the pool bound.
	if len(parsed.Sites) > 0 {
		siteIDs, err := s.repo.DocumentIDsByHost(ctx, parsed.Sites)
		if err != nil {
			return nil, err
		}
		bm25HitIDs = append(bm25HitIDs, siteIDs...)
	}
	embeddings, err := s.repo.EmbeddingsForDocs(ctx, bm25HitIDs)
	if err != nil {
		return nil, err
	}
	// Fill the rest of the semantic candidate pool via Postgres pgvector's
	// ANN index when it's both admin-enabled (ANNSearchEnabled) and
	// actually available for this repository (ok -- see
	// sqlrepo.Repository.EnableANN/TopSemanticMatches: false for any
	// non-Postgres dialect, a Postgres server without the pgvector
	// extension, or a process where EnableANN never succeeded), falling
	// back to the existing bounded brute-force SampleEmbeddings sample
	// exactly as before ANN existed whenever it isn't.
	poolSize := opValues.SemanticCandidatePoolSize
	var sampled map[string]domain.EmbeddedVector
	if opValues.ANNSearchEnabled {
		annMatches, ok, annErr := s.repo.TopSemanticMatches(ctx, queryVec, poolSize)
		if annErr != nil {
			return nil, annErr
		}
		if ok {
			sampled = annMatches
		}
	}
	if sampled == nil {
		var sampleErr error
		sampled, sampleErr = s.repo.SampleEmbeddings(ctx, poolSize)
		if sampleErr != nil {
			return nil, sampleErr
		}
	}
	for id, vec := range sampled {
		if _, ok := embeddings[id]; !ok {
			embeddings[id] = vec
		}
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
	//
	// For a recency-sort request, this fetch itself goes through
	// DocumentsByIDsSortedByCrawledAt (crawled_at DESC, backed by
	// idx_documents_crawled_at) instead of the plain unordered
	// DocumentsByIDs, so recencyOrder below already reflects the DB's own
	// order -- no in-app sort.Slice over the candidates is needed later to
	// apply recency order.
	docCache := make(map[string]domain.Document)
	var recencyOrder []string
	if parsed.HasConstraints() || hasOverrides || recency {
		ids := make([]string, 0, len(candidateIDs))
		for id := range candidateIDs {
			ids = append(ids, id)
		}
		if recency {
			docs, err := s.repo.DocumentsByIDsSortedByCrawledAt(ctx, ids)
			if err != nil {
				return nil, err
			}
			recencyOrder = make([]string, 0, len(docs))
			for _, doc := range docs {
				docCache[doc.ID] = doc
				recencyOrder = append(recencyOrder, doc.ID)
			}
		} else {
			docs, err := s.repo.DocumentsByIDs(ctx, ids)
			if err != nil {
				return nil, err
			}
			for id, doc := range docs {
				docCache[id] = doc
			}
		}
		for id := range candidateIDs {
			doc, ok := docCache[id]
			if !ok {
				delete(candidateIDs, id)
				continue
			}
			if !parsed.Matches(doc.Title, doc.Text) || !parsed.SiteAllowed(doc) || overrides.Blocked(doc) {
				delete(candidateIDs, id)
			}
		}
	}

	alpha, k1, b := s.settings.Get()

	candidates := make([]domain.HybridResult, 0, len(candidateIDs))
	for id := range candidateIDs {
		bm25 := domain.BM25ScoreDocument(bm25PerDoc[id], k1, b)
		ev := embeddings[id]
		semantic := domain.CosineSimilarityWithNorms(queryVec, ev.Vector, queryNorm, ev.Norm)
		candidates = append(candidates, domain.HybridResult{
			DocID: id, BM25Score: bm25, SemanticSim: semantic, PageRank: ev.PageRank,
			CrawledAt: docCache[id].CrawledAt, CorrectedTerms: correctedTerms,
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
	// Blend in link authority: PageRankWeight defaults to (and, unless an
	// admin opts in, stays at) 0, in which case this is skipped entirely
	// and ranking is byte-for-byte identical to before PageRank existed.
	// Each candidate's raw PageRank is normalized against this batch's own
	// max so it's comparable in scale to the existing [0,1]-ish scores,
	// then blended additively -- a weight of 1 makes FinalScore driven
	// entirely by normalized PageRank, 0 leaves it untouched.
	if pageRankWeight := s.settings.PageRankWeight(); pageRankWeight > 0 {
		maxPageRank := 0.0
		for i := range ranked {
			if ranked[i].PageRank > maxPageRank {
				maxPageRank = ranked[i].PageRank
			}
		}
		if maxPageRank > 0 {
			for i := range ranked {
				normalizedPageRank := ranked[i].PageRank / maxPageRank
				ranked[i].FinalScore = ranked[i].FinalScore*(1-pageRankWeight) + normalizedPageRank*pageRankWeight
			}
			domain.SortByFinalScore(ranked)
		}
	}
	if recency {
		// Recency sort ignores BM25/semantic/final score entirely -- this
		// overrides whatever order CombineScores/boosting produced above.
		// recencyOrder already reflects idx_documents_crawled_at-backed
		// ORDER BY crawled_at DESC from the fetch above, so reordering
		// ranked to match it is just a lookup, not a sort.
		byID := make(map[string]domain.HybridResult, len(ranked))
		for _, r := range ranked {
			byID[r.DocID] = r
		}
		reordered := make([]domain.HybridResult, 0, len(ranked))
		for _, id := range recencyOrder {
			if r, ok := byID[id]; ok {
				reordered = append(reordered, r)
			}
		}
		ranked = reordered
	}
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}

	// Hydrate the final topK results' URL/Title/Snippet in one batched
	// fetch for whichever IDs docCache doesn't already hold (the common,
	// unconstrained-query fast path never populates docCache at all, so
	// this is where that path's only document fetch happens) instead of
	// one DocumentByID round trip per result.
	missingIDs := make([]string, 0, len(ranked))
	for i := range ranked {
		if _, ok := docCache[ranked[i].DocID]; !ok {
			missingIDs = append(missingIDs, ranked[i].DocID)
		}
	}
	if len(missingIDs) > 0 {
		fetched, err := s.repo.DocumentsByIDs(ctx, missingIDs)
		if err != nil {
			return nil, err
		}
		for id, doc := range fetched {
			docCache[id] = doc
		}
	}
	for i := range ranked {
		doc, ok := docCache[ranked[i].DocID]
		if !ok {
			continue
		}
		ranked[i].URL = doc.URL
		ranked[i].Title = doc.Title
		ranked[i].Snippet = domain.Snippet(doc.Text, terms, 200)
	}

	return ranked, nil
}
