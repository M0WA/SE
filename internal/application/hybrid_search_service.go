package application

import (
	"context"
	"errors"
	"sync"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type hybridSearchService struct {
	repo ports.SQLRepository
	// embedders holds one ports.EmbeddingProvider per currently-enabled
	// provider (domain.EmbeddingProviderHash, or a configured
	// domain.EmbeddingHTTPEndpoint's ID) -- mirrors sqlCrawlerService's
	// identical field. Search embeds the query against every provider that
	// currently has a non-zero weight (see resolveProviderWeights), not
	// just one -- see its own doc comment for why.
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
// actually scores semantic similarity with: opts.ProviderWeights if the
// caller supplied one (a full replacement of the admin default, the same
// "the param replaces, it doesn't blend" convention opts.TopK already
// follows), else opValues.EmbeddingSearchWeights. Either way, the result
// is filtered down to providers this process actually has a live embedder
// for (weight <= 0, or a provider unknown to s.embedders, is dropped
// silently -- s.embedders only ever contains providers this process
// itself has enabled/configured, so a stale or foreign provider ID here
// is simply ignored rather than erroring the request). An empty result
// means "no semantic scoring for this request" (pure BM25), not an error.
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

// fetchPostings runs the BM25 side of a search: one batched postings
// lookup across every unique query term, plus -- when
// opValues.FuzzyMatchEnabled -- a fallback vocabulary lookup for any term
// that matched nothing (a bounded-edit-distance near-miss substitute
// stands in for it in BM25 scoring only; a term that already matched
// something is never touched). correctedTerms reports every such
// substitution so a caller/UI can show it transparently rather than
// silently rewriting the displayed query. Split out from Search so it can
// run concurrently with the query's own embedding call -- the two are
// entirely independent until both feed into the semantic candidate pool.
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

	// The BM25 side (postings lookup, plus fuzzy-match fallback) and the
	// semantic side's query embedding(s) are entirely independent of each
	// other -- neither reads anything the other produces -- until both
	// feed into the semantic candidate pool below. For an HTTP embedding
	// provider in particular, Embed is a real network round-trip, so
	// running every active provider's Embed call concurrently with each
	// other and with the postings fetch (rather than sequentially, or only
	// starting them once BM25 scoring has fully finished) shaves that
	// latency off the request instead of paying for it once per provider.
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

	totalDocs, avgDocLen := s.corpusStats.Get()
	bm25PerDoc := make(map[string][]domain.PostingStats)
	// bm25TermsPerDoc parallels bm25PerDoc index-for-index (same doc, same
	// append order) -- PostingStats itself carries no term label, so this is
	// the only place that association exists, and it's needed later to
	// build each topK result's domain.TermScore breakdown.
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

	// Computed once per Search call rather than inside every per-candidate
	// cosine-similarity comparison below -- a query vector never changes
	// across those comparisons within one request.
	queryNorms := make(map[string]float64, len(queryVecs))
	for provider, vec := range queryVecs {
		queryNorms[provider] = domain.VectorNorm(vec)
	}

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
	// A site:/-site: host may only exist as a document_aliases row now (the
	// content merged, or www-folded, into a document under a different real
	// host) -- domain.ParsedQuery.SiteAllowed compares a candidate's own
	// (canonical) host, so it would otherwise never match either list here.
	// Expanding both lists with the resolved canonical host keeps the filter
	// correct without touching SiteAllowed itself.
	if len(parsed.Sites) > 0 {
		aliasHosts, err := s.repo.ResolveAliasHosts(ctx, parsed.Sites)
		if err != nil {
			return nil, err
		}
		parsed.Sites = append(parsed.Sites, aliasHosts...)
	}
	if len(parsed.ExcludedSites) > 0 {
		aliasHosts, err := s.repo.ResolveAliasHosts(ctx, parsed.ExcludedSites)
		if err != nil {
			return nil, err
		}
		parsed.ExcludedSites = append(parsed.ExcludedSites, aliasHosts...)
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
	// embeddings is keyed [provider][docID] -- one independent vector space
	// per active provider, since embeddings from different models can never
	// be compared directly (only their independently-computed similarity
	// scores can be blended -- see the scoring loop below).
	embeddings := make(map[string]map[string]domain.EmbeddedVector, len(active))
	poolSize := opValues.SemanticCandidatePoolSize
	candidateIDs := make(map[string]bool, len(bm25PerDoc))
	for id := range bm25PerDoc {
		candidateIDs[id] = true
	}
	for provider := range active {
		providerEmbeddings, err := s.repo.EmbeddingsForDocs(ctx, bm25HitIDs, provider)
		if err != nil {
			return nil, err
		}
		// Fill the rest of this provider's share of the semantic candidate
		// pool via Postgres pgvector's ANN index when it's both
		// admin-enabled (ANNSearchEnabled) and actually available for this
		// provider (ok -- see sqlrepo.Repository.EnableANN/
		// TopSemanticMatches: false for any non-Postgres dialect, a
		// Postgres server without the pgvector extension, or a process
		// where EnableANN never succeeded for this provider), falling back
		// to the existing bounded brute-force SampleEmbeddings sample
		// exactly as before ANN existed whenever it isn't. Every active
		// provider's own candidate IDs are unioned together below, so a
		// document need only turn up in ANY one provider's pool to be
		// scored against every active provider.
		var sampled map[string]domain.EmbeddedVector
		if opValues.ANNSearchEnabled {
			annMatches, ok, annErr := s.repo.TopSemanticMatches(ctx, queryVecs[provider], poolSize, provider)
			if annErr != nil {
				return nil, annErr
			}
			if ok {
				sampled = annMatches
			}
		}
		if sampled == nil {
			var sampleErr error
			sampled, sampleErr = s.repo.SampleEmbeddings(ctx, poolSize, provider)
			if sampleErr != nil {
				return nil, sampleErr
			}
		}
		for id, vec := range sampled {
			if _, ok := providerEmbeddings[id]; !ok {
				providerEmbeddings[id] = vec
			}
		}
		embeddings[provider] = providerEmbeddings
		for id := range providerEmbeddings {
			candidateIDs[id] = true
		}
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
	// docTokens caches each candidate's tokenized title+text, computed at
	// most once per candidate (only when a term-based check actually needs
	// it -- a pure site:/recency query needs no tokenization at all), then
	// reused by the boost step below -- otherwise a query with both
	// +required/-excluded terms and admin blocked/boosted terms would
	// tokenize the same document up to three times (once each for
	// constraint matching, blocking, and boosting).
	docTokens := make(map[string]map[string]bool)
	needsTokens := len(parsed.Required) > 0 || len(parsed.Excluded) > 0 ||
		len(overrides.BlockedTerms) > 0 || len(overrides.BoostedTerms) > 0
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

	alpha, k1, b := s.settings.Get()
	pageRankWeight := s.settings.PageRankWeight()

	candidates := make([]domain.HybridResult, 0, len(candidateIDs))
	for id := range candidateIDs {
		bm25 := domain.BM25ScoreDocument(bm25PerDoc[id], k1, b)
		// Blend every active provider's independently-computed cosine
		// similarity by its configured weight, normalized so the combined
		// score stays on the same [-1,1]-ish scale regardless of how many
		// providers are active or their raw weight magnitudes -- the same
		// weighted-sum-to-1 convention domain.CombineScores itself uses for
		// alpha below. A provider missing this candidate's embedding (not
		// yet recomputed for it) contributes 0 for its own share rather
		// than being excluded from the normalization -- self-heals as a
		// recompute catches the document up.
		//
		// PageRank is a per-document value redundantly joined onto every
		// provider's own row for that doc (see domain.EmbeddedVector), not
		// a per-provider quantity -- read it from whichever active
		// provider happens to have this candidate at all.
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
	// Blend in link authority: PageRankWeight defaults to (and, unless an
	// admin opts in, stays at) 0, in which case this is skipped entirely
	// and ranking is byte-for-byte identical to before PageRank existed.
	// Each candidate's raw PageRank is normalized against this batch's own
	// max so it's comparable in scale to the existing [0,1]-ish scores,
	// then blended additively -- a weight of 1 makes FinalScore driven
	// entirely by normalized PageRank, 0 leaves it untouched.
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
	// highlightTerms swaps in each fuzzy-corrected term's actual spelling in
	// place of the original: scoringTerm only ever contains a term that had
	// zero postings hits anywhere in the corpus, so the original literally
	// never appears in any document's text -- highlighting the original
	// there would never find a match, silently leaving a fuzzy-matched
	// result's excerpt unhighlighted even though it's exactly why that
	// result matched at all.
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
