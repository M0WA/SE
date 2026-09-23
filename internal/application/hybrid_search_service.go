package application

import (
	"context"
	"errors"
	"sort"
	"sync"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchService struct {
	repo ports.SQLRepository
	// embedders holds one ports.EmbeddingProvider per enabled provider.
	// Search embeds the query against every provider with a non-zero
	// weight (see resolveProviderWeights), not just one.
	embedders   map[string]ports.EmbeddingProvider
	settings    *domain.TuningSettings
	opSettings  *domain.OperationalSettings
	corpusStats *domain.CorpusStatsCache
	overrides   *domain.RankingOverrides
	vocabulary  *domain.VocabularyCache
}

func NewHybridSearchService(repo ports.SQLRepository, embedders map[string]ports.EmbeddingProvider, settings *domain.TuningSettings, opSettings *domain.OperationalSettings, overrides *domain.RankingOverrides, corpusStats *domain.CorpusStatsCache, vocabulary *domain.VocabularyCache) *hybridSearchService {
	return &hybridSearchService{repo: repo, embedders: embedders, settings: settings, opSettings: opSettings, corpusStats: corpusStats, overrides: overrides, vocabulary: vocabulary}
}

// resolveProviderWeights returns the provider->weight map this request
// scores semantic similarity with: opts.ProviderWeights if supplied, else
// opValues.EmbeddingSearchWeights -- filtered to weight > 0 with a live
// embedder. Empty means pure BM25, not an error.
func (s *hybridSearchService) resolveProviderWeights(opts ports.SearchQuery, opValues domain.OperationalSettingsValues) map[string]float64 {
	weights := opValues.EmbeddingSearchWeights
	if opts.ProviderWeights != nil {
		weights = opts.ProviderWeights
	}
	active := make(map[string]float64, len(weights))
	for provider, w := range weights {
		if w > 0 && s.embedders[provider] != nil {
			active[provider] = w
		}
	}
	return active
}

// fetchPostings runs BM25's side of a search: one batched postings lookup,
// plus (when FuzzyMatchEnabled) a vocabulary fallback substituting a
// near-miss term for one with zero hits. Split out so it can run
// concurrently with query embedding.
func (s *hybridSearchService) fetchPostings(ctx context.Context, uniqueTerms []string, opValues domain.OperationalSettingsValues) (postingsByTerm map[string][]domain.PostingStats, scoringTerm map[string]string, correctedTerms []domain.CorrectedTerm, err error) {
	// One batched query across every unique query term (rather than one
	// join query -- plus a separate doc-freq COUNT(*) query -- per term).
	postingsByTerm, err = s.repo.PostingsForTerms(ctx, uniqueTerms)
	if err != nil {
		return nil, nil, nil, err
	}

	scoringTerm = make(map[string]string, len(uniqueTerms)) // original -> term to actually score with (itself, unless corrected)
	if !opValues.FuzzyMatchEnabled {
		return postingsByTerm, scoringTerm, nil, nil
	}
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
		fetched, fetchErr := s.repo.PostingsForTerms(ctx, toFetch)
		if fetchErr != nil {
			return nil, nil, nil, fetchErr
		}
		for term, postings := range fetched {
			postingsByTerm[term] = postings
		}
	}
	return postingsByTerm, scoringTerm, correctedTerms, nil
}

// capBM25HitIDsForRescore bounds how many BM25 hits get a semantic score
// fetched, keeping the top-scoring `limit` by the same BM25 formula ranking
// uses -- a query term matching a large fraction of the corpus (a common
// word across a large crawled site) would otherwise force EmbeddingsForDocs
// to fetch and deserialize every single hit's embedding. BM25-only ranking
// is unaffected: bm25PerDoc/candidateIDs still hold every hit, so a
// document cut here is still scored and ranked on keyword relevance --
// it just contributes 0 to the semantic side, same as any other candidate
// missing an embedding for a given provider.
func capBM25HitIDsForRescore(ids []string, bm25PerDoc map[string][]domain.PostingStats, k1, b float64, limit int) []string {
	if limit <= 0 || len(ids) <= limit {
		return ids
	}
	type scoredID struct {
		id    string
		score float64
	}
	scored := make([]scoredID, len(ids))
	for i, id := range ids {
		scored[i] = scoredID{id: id, score: domain.BM25ScoreDocument(bm25PerDoc[id], k1, b)}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = scored[i].id
	}
	return out
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

	opValues := s.opSettings.Get()
	active := s.resolveProviderWeights(opts, opValues)

	// BM25 postings and each provider's query embedding are independent
	// until they feed the candidate pool below -- run concurrently rather
	// than paying each embedder's network round trip sequentially.
	var (
		wg             sync.WaitGroup
		postingsByTerm map[string][]domain.PostingStats
		scoringTerm    map[string]string
		correctedTerms []domain.CorrectedTerm
		postingsErr    error
		queryVecs      = make(map[string][]float32, len(active))
		embedErrsMu    sync.Mutex
		embedErrs      []error
	)
	wg.Add(1 + len(active))
	go func() {
		defer wg.Done()
		postingsByTerm, scoringTerm, correctedTerms, postingsErr = s.fetchPostings(ctx, uniqueTerms, opValues)
	}()
	var queryVecsMu sync.Mutex
	for provider := range active {
		provider := provider
		go func() {
			defer wg.Done()
			vec, err := s.embedders[provider].Embed(ctx, query)
			if err != nil {
				embedErrsMu.Lock()
				embedErrs = append(embedErrs, err)
				embedErrsMu.Unlock()
				return
			}
			queryVecsMu.Lock()
			queryVecs[provider] = vec
			queryVecsMu.Unlock()
		}()
	}
	wg.Wait()
	if postingsErr != nil {
		return nil, postingsErr
	}
	if len(embedErrs) > 0 {
		return nil, embedErrs[0]
	}

	// Fetched here (not down by CombineScores below, where it's also used)
	// so k1/b are available to cap the semantic-rescore candidate set below
	// with the exact same BM25 formula final ranking uses.
	alpha, k1, b := s.settings.Get()
	pageRankWeight := s.settings.PageRankWeight()

	totalDocs, avgDocLen := s.corpusStats.Get()
	bm25PerDoc := make(map[string][]domain.PostingStats)
	// bm25TermsPerDoc parallels bm25PerDoc index-for-index -- PostingStats
	// carries no term label, so this is the only place that association
	// exists, needed later for each result's TermScore breakdown.
	bm25TermsPerDoc := make(map[string][]string)
	for _, term := range uniqueTerms {
		lookupTerm := term
		if corrected, ok := scoringTerm[term]; ok {
			lookupTerm = corrected
		}
		for _, p := range postingsByTerm[lookupTerm] {
			p.TotalDocs = totalDocs
			p.AvgDocLen = avgDocLen
			bm25PerDoc[p.DocID] = append(bm25PerDoc[p.DocID], p)
			bm25TermsPerDoc[p.DocID] = append(bm25TermsPerDoc[p.DocID], lookupTerm)
		}
	}

	// Computed once here rather than inside every per-candidate
	// cosine-similarity comparison -- the query vector never changes.
	queryNorms := make(map[string]float64, len(queryVecs))
	for provider, vec := range queryVecs {
		queryNorms[provider] = domain.VectorNorm(vec)
	}

	// Score the semantic side against a bounded set, not the whole corpus:
	// every BM25 hit plus a fixed-size sample, so a purely semantic match
	// can still surface without a full scan.
	bm25HitIDs := mapKeys(bm25PerDoc)
	// A term matching a large fraction of the corpus would otherwise force
	// every single hit's embedding to be fetched and deserialized -- keep
	// only the top-scoring SemanticRescoreCap by BM25 alone. BM25-only
	// ranking is unaffected: bm25PerDoc/candidateIDs below still hold every
	// hit regardless of this cap.
	bm25HitIDs = capBM25HitIDsForRescore(bm25HitIDs, bm25PerDoc, k1, b, opValues.SemanticRescoreCap)
	// A site:/-site: host may exist only as a document_aliases row now --
	// expand both lists with the resolved canonical host so the filter
	// still matches, without touching SiteAllowed itself.
	expandAliasHosts := func(sites []string) ([]string, error) {
		if len(sites) == 0 {
			return sites, nil
		}
		aliasHosts, err := s.repo.ResolveAliasHosts(ctx, sites)
		if err != nil {
			return nil, err
		}
		return append(sites, aliasHosts...), nil
	}
	var err error
	if parsed.Sites, err = expandAliasHosts(parsed.Sites); err != nil {
		return nil, err
	}
	if parsed.ExcludedSites, err = expandAliasHosts(parsed.ExcludedSites); err != nil {
		return nil, err
	}
	// A site: filter must never depend on landing in the BM25/semantic
	// pool -- forcing matches into the fetch set here guarantees every
	// document on the requested site is considered.
	if len(parsed.Sites) > 0 {
		siteIDs, err := s.repo.DocumentIDsByHost(ctx, parsed.Sites)
		if err != nil {
			return nil, err
		}
		bm25HitIDs = append(bm25HitIDs, siteIDs...)
	}
	// embeddings is keyed [provider][docID] -- one vector space per
	// provider, since embeddings from different models can't be compared
	// directly (only their similarity scores blend -- see below).
	embeddings := make(map[string]map[string]domain.EmbeddedVector, len(active))
	poolSize := opValues.SemanticCandidatePoolSize
	candidateIDs := make(map[string]bool, len(bm25PerDoc))
	for id := range bm25PerDoc {
		candidateIDs[id] = true
	}
	// Each active provider's embedding fetch is an independent round trip
	// (EmbeddingsForDocs plus an ANN/sample fill) -- run them concurrently
	// rather than paying each provider's latency sequentially, same as the
	// postings/query-embedding fetch above. Results are merged into
	// embeddings/candidateIDs only after every goroutine finishes, since
	// both are plain maps, not safe for concurrent writes.
	type providerFetchResult struct {
		provider   string
		embeddings map[string]domain.EmbeddedVector
		err        error
	}
	resultsCh := make(chan providerFetchResult, len(active))
	var fetchWG sync.WaitGroup
	fetchWG.Add(len(active))
	for provider := range active {
		provider := provider
		go func() {
			defer fetchWG.Done()
			providerEmbeddings, err := s.repo.EmbeddingsForDocs(ctx, bm25HitIDs, provider)
			if err != nil {
				resultsCh <- providerFetchResult{provider: provider, err: err}
				return
			}
			// Fill the rest of this provider's pool via pgvector ANN when
			// available, else fall back to bounded brute-force sampling. Every
			// provider's candidate IDs are unioned below.
			var sampled map[string]domain.EmbeddedVector
			if opValues.ANNSearchEnabled {
				annMatches, ok, annErr := s.repo.TopSemanticMatches(ctx, queryVecs[provider], poolSize, provider)
				if annErr != nil {
					resultsCh <- providerFetchResult{provider: provider, err: annErr}
					return
				}
				if ok {
					sampled = annMatches
				}
			}
			if sampled == nil {
				var sampleErr error
				sampled, sampleErr = s.repo.SampleEmbeddings(ctx, poolSize, provider)
				if sampleErr != nil {
					resultsCh <- providerFetchResult{provider: provider, err: sampleErr}
					return
				}
			}
			for id, vec := range sampled {
				if _, ok := providerEmbeddings[id]; !ok {
					providerEmbeddings[id] = vec
				}
			}
			resultsCh <- providerFetchResult{provider: provider, embeddings: providerEmbeddings}
		}()
	}
	fetchWG.Wait()
	close(resultsCh)
	for res := range resultsCh {
		if res.err != nil {
			return nil, res.err
		}
		embeddings[res.provider] = res.embeddings
		for id := range res.embeddings {
			candidateIDs[id] = true
		}
	}

	overrides := s.overrides.Get()
	hasOverrides := len(overrides.BlockedTerms) > 0 || len(overrides.BlockedDomains) > 0 ||
		len(overrides.BoostedTerms) > 0 || len(overrides.BoostedDomains) > 0

	// Constraint/blocked-term/domain filters and recency sort all need
	// each candidate's full doc, which ranking otherwise wouldn't fetch
	// until after truncating to topK -- so fetch and filter first, caching
	// docs for reuse below. Recency sort fetches pre-ordered, so
	// recencyOrder needs no later sort.
	docCache := make(map[string]domain.Document)
	// docTokens caches each candidate's tokenized title+text (only when
	// needed) and is reused by the boost step below, avoiding re-tokenizing
	// the same document for constraints and blocked/boosted terms.
	docTokens := make(map[string]map[string]bool)
	needsTokens := len(parsed.Required) > 0 || len(parsed.ExcludedGroups) > 0 ||
		len(overrides.BlockedTerms) > 0 || len(overrides.BoostedTerms) > 0
	var recencyOrder []string
	if parsed.HasConstraints() || hasOverrides || recency {
		ids := mapKeys(candidateIDs)
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
			var tokens map[string]bool
			if needsTokens {
				tokens = domain.TokenSet(doc.Title, doc.Text)
				docTokens[id] = tokens
			}
			if !parsed.MatchesTokens(tokens, doc.Title, doc.Text) || !parsed.SiteAllowed(doc) || overrides.BlockedTokens(doc.URL, tokens) {
				delete(candidateIDs, id)
			}
		}
	}

	candidates := make([]domain.HybridResult, 0, len(candidateIDs))
	for id := range candidateIDs {
		bm25 := domain.BM25ScoreDocument(bm25PerDoc[id], k1, b)
		// Blend every active provider's cosine similarity by its weight,
		// normalized to the same scale regardless of provider count. A
		// provider missing this candidate's embedding contributes 0 rather
		// than excluding it -- self-heals once a recompute catches up.
		var semantic, totalWeight, pageRank float64
		for provider, w := range active {
			totalWeight += w
			ev, ok := embeddings[provider][id]
			if !ok {
				continue
			}
			semantic += w * domain.CosineSimilarityWithNorms(queryVecs[provider], ev.Vector, queryNorms[provider], ev.Norm)
			pageRank = ev.PageRank
		}
		if totalWeight > 0 {
			semantic /= totalWeight
		}
		candidates = append(candidates, domain.HybridResult{
			DocID: id, BM25Score: bm25, SemanticSim: semantic, PageRank: pageRank,
			CrawledAt: docCache[id].CrawledAt, CorrectedTerms: correctedTerms,
			Alpha: alpha, K1: k1, B: b, PageRankWeight: pageRankWeight,
		})
	}

	ranked := domain.CombineScores(candidates, alpha)
	if hasOverrides {
		boosted := false
		for i := range ranked {
			if doc, ok := docCache[ranked[i].DocID]; ok {
				if f := overrides.BoostFactorTokens(doc.URL, docTokens[ranked[i].DocID]); f != 1.0 {
					ranked[i].FinalScore *= f
					boosted = true
				}
			}
		}
		if boosted {
			domain.SortByFinalScore(ranked)
		}
	}
	// Blend in link authority: PageRankWeight defaults to 0 (unchanged
	// ranking). Each candidate's raw PageRank is normalized against this
	// batch's max, then blended additively into FinalScore.
	if pageRankWeight > 0 {
		maxPageRank := 0.0
		for i := range ranked {
			if ranked[i].PageRank > maxPageRank {
				maxPageRank = ranked[i].PageRank
			}
		}
		if maxPageRank > 0 {
			for i := range ranked {
				normalizedPageRank := ranked[i].PageRank / maxPageRank
				ranked[i].NormalizedPageRank = normalizedPageRank
				ranked[i].FinalScore = ranked[i].FinalScore*(1-pageRankWeight) + normalizedPageRank*pageRankWeight
			}
			domain.SortByFinalScore(ranked)
		}
	}
	if recency {
		// Recency sort ignores score entirely, overriding whatever
		// CombineScores/boosting produced above. recencyOrder already
		// reflects the ORDER BY crawled_at DESC fetch, so this is a
		// lookup, not a re-sort.
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
	// fetch for IDs docCache doesn't already hold (the unconstrained-query
	// fast path's only document fetch) instead of one round trip each.
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
	// highlightTerms swaps in each fuzzy-corrected term's real spelling --
	// the original never appears in document text, so it'd never match.
	highlightTerms := terms
	if len(scoringTerm) > 0 {
		highlightTerms = make([]string, len(terms))
		for i, t := range terms {
			if corrected, ok := scoringTerm[t]; ok {
				highlightTerms[i] = corrected
			} else {
				highlightTerms[i] = t
			}
		}
	}

	for i := range ranked {
		id := ranked[i].DocID
		ranked[i].BM25Terms = domain.BM25TermScores(bm25TermsPerDoc[id], bm25PerDoc[id], k1, b)
		doc, ok := docCache[id]
		if !ok {
			continue
		}
		ranked[i].URL = doc.URL
		ranked[i].Title = doc.Title
		ranked[i].Snippet = domain.Snippet(doc.Text, parsed.Phrases, highlightTerms, 200)
	}

	return ranked, nil
}
