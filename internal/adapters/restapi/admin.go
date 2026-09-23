package restapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const defaultDocumentListLimit = 100

// Response-body literals repeated across handlers, pulled into named
// constants to satisfy a duplicate-literal lint rule -- meaning unchanged.
const (
	msgMethodNotAllowed          = "method not allowed"
	contentTypeHTML              = "text/html; charset=utf-8"
	msgNameMustNotBeEmpty        = "name must not be empty"
	msgAgentNotFound             = "agent not found"
	configNameMCPServers         = "mcp servers"
	msgMCPServerNotFound         = "mcp server not found"
	configNameAdminDiagnostics   = "admin diagnostics"
	configNameScheduledCrawls    = "scheduled crawls"
	msgScheduledCrawlNotFound    = "scheduled crawl not found"
	configNameCrawlJobs          = "crawl jobs"
	configNameContentDedup       = "content dedup"
	configNameEmbeddingEndpoints = "embedding endpoints"
	msgEmbeddingEndpointNotFound = "embedding endpoint not found"
)

// requireMethod writes 405 and reports false if the request method isn't
// the one this endpoint accepts.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// requireGetOrHead is requireMethod for a handler accepting both GET and
// HEAD -- a static/health response, never a single-method API endpoint.
func requireGetOrHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// requireConfigured writes 503 and reports false if a dependency this
// endpoint needs wasn't wired up in Config.
func requireConfigured(w http.ResponseWriter, configured bool, what string) bool {
	if !configured {
		http.Error(w, what+" not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// respondOrNotFound writes okPayload as 200 if err is nil, notFoundMsg as
// 404 if err is notFound, else err's message as 500 -- the shared
// success/not-found/other-error response for a store with a not-found sentinel.
func respondOrNotFound(w http.ResponseWriter, err, notFound error, notFoundMsg string, okPayload interface{}) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, okPayload)
	case errors.Is(err, notFound):
		http.Error(w, notFoundMsg, http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// resolveUpdatedAPIKey implements the shared "blank means unchanged, clear
// flag means remove" convention for a redacted secret field: a non-empty
// newVal is encrypted, empty+clear removes it, empty+!clear keeps existing.
func (h *Handler) resolveUpdatedAPIKey(existing, newVal string, clearFlag bool) string {
	if newVal != "" {
		return h.encryptAPIKey(newVal)
	}
	if clearFlag {
		return ""
	}
	return existing
}

// mapSlice converts each element of in via f, preserving order/length --
// the shape every domain-to-wire-response conversion in this file repeats.
func mapSlice[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, x := range in {
		out[i] = f(x)
	}
	return out
}

// existingIDSet builds the "already-taken IDs" set a freshly minted slug
// ID is deduped against, optionally pre-seeded with reserved IDs (e.g. the
// built-in hash provider's own ID).
func existingIDSet[T any](existing []T, id func(T) string, seed ...string) map[string]bool {
	ids := make(map[string]bool, len(existing)+len(seed))
	for _, s := range seed {
		ids[s] = true
	}
	for _, e := range existing {
		ids[id(e)] = true
	}
	return ids
}

// decodeJSON decodes r's JSON body into a T, writing 400 and reporting
// false on error -- the shared check every PATCH/POST admin handler repeats.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var req T
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// intQueryParam reads name from the query string as an int, falling back
// to def if missing, non-numeric, or (if positiveOnly) not greater than
// zero. Shared by every ?limit=/?top_k=-style endpoint.
func intQueryParam(r *http.Request, name string, def int, positiveOnly bool) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || (positiveOnly && n <= 0) {
		return def
	}
	return n
}

func (h *Handler) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminHTML)
}

func (h *Handler) handleAdminDocumentsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminDocumentsHTML)
}

// handleAdminDomainPage serves the per-domain subpage template; the
// domain name is read client-side (JS), so one static page works for every host.
func (h *Handler) handleAdminDomainPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminDomainHTML)
}

// handleAdminVocabularyTermPage serves the term-detail subpage template;
// like handleAdminDomainPage, the term is read client-side from the URL,
// so one static page works for every term.
func (h *Handler) handleAdminVocabularyTermPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminVocabularyTermHTML)
}

func (h *Handler) handleAdminSettingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminSettingsHTML)
}

func (h *Handler) handleAdminChatSettingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminChatSettingsHTML)
}

func (h *Handler) handleAdminMCPServersPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminMCPServersHTML)
}

func (h *Handler) handleAdminMCPServerPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminMCPServerHTML)
}

func (h *Handler) handleAdminAgentsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminAgentsHTML)
}

func (h *Handler) handleAdminAgentPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminAgentHTML)
}

func (h *Handler) handleAdminUsersPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminUsersHTML)
}

func (h *Handler) handleAdminUserPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminUserHTML)
}

func (h *Handler) handleAdminJobsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminJobsHTML)
}

func (h *Handler) handleAdminSearchPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminSearchHTML)
}

// handleAdminSearchResultPage serves the per-result score-breakdown
// subpage: its JS reads the query/doc ID from the URL and re-runs
// /admin/api/search, since every score is normalized against that batch.
func (h *Handler) handleAdminSearchResultPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminSearchResultHTML)
}

func (h *Handler) handleAdminCrawlPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, crawlHTML)
}

type adminStatsResponse struct {
	Driver    string  `json:"driver"`
	TotalDocs int     `json:"total_docs"`
	AvgDocLen float64 `json:"avg_doc_len"`
}

func (h *Handler) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	totalDocs, avgDocLen, err := h.admin.CorpusStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, adminStatsResponse{
		Driver:    h.dbDriver,
		TotalDocs: totalDocs,
		AvgDocLen: avgDocLen,
	})
}

// defaultVocabularyPageSize is the vocabulary page's default items-per-page
// -- also the fallback for an invalid/absent limit query param.
const defaultVocabularyPageSize = 20

type adminTermStat struct {
	Term      string `json:"term"`
	DocFreq   int    `json:"doc_freq"`
	TotalFreq int    `json:"total_freq"`
}

type adminVocabularyResponse struct {
	// VocabularySize is the whole corpus's distinct-term count, unaffected
	// by search/limit/offset.
	VocabularySize int `json:"vocabulary_size"`
	// MatchedCount is how many terms match search (ignoring limit/offset),
	// equal to VocabularySize when search is empty -- used to compute page count.
	MatchedCount int             `json:"matched_count"`
	Terms        []adminTermStat `json:"terms"`
}

// vocabularySortParam whitelists sort/dir against the vocabulary page's
// sortable columns/directions, defaulting to the pre-pagination order
// (highest doc_freq first).
func vocabularySortParam(r *http.Request, name, def string, valid ...string) string {
	v := r.URL.Query().Get(name)
	for _, ok := range valid {
		if v == ok {
			return v
		}
	}
	return def
}

func (h *Handler) handleAdminVocabulary(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	limit := intQueryParam(r, "limit", defaultVocabularyPageSize, true)
	offset := intQueryParam(r, "offset", 0, false)
	// Indexed terms are always lowercased at tokenize time (domain.Tokenize),
	// so a mixed-case search would otherwise silently miss matches.
	search := strings.ToLower(r.URL.Query().Get("search"))
	sortBy := vocabularySortParam(r, "sort", "doc_freq", "term", "doc_freq", "total_freq")
	sortDir := vocabularySortParam(r, "dir", "desc", "asc", "desc")
	vocabSize, matched, terms, err := h.admin.VocabularyStats(r.Context(), limit, offset, search, sortBy, sortDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := mapSlice(terms, func(t domain.TermStat) adminTermStat {
		return adminTermStat{Term: t.Term, DocFreq: t.DocFreq, TotalFreq: t.TotalFreq}
	})
	writeJSON(w, http.StatusOK, adminVocabularyResponse{VocabularySize: vocabSize, MatchedCount: matched, Terms: out})
}

type adminDocument struct {
	ID            string    `json:"id"`
	URL           string    `json:"url"`
	Host          string    `json:"host"`
	Title         string    `json:"title"`
	DocLength     int       `json:"doc_length"`
	Version       int       `json:"version"`
	CrawledAt     time.Time `json:"crawled_at"`
	InternalLinks int       `json:"internal_links"`
	ExternalLinks int       `json:"external_links"`
	Backlinks     int       `json:"backlinks"`
	PageRank      float64   `json:"pagerank"`
}

// handleAdminDocuments lists indexed pages, optionally narrowed to one
// domain via ?domain= -- without it, the whole corpus, capped at limit.
func (h *Handler) handleAdminDocuments(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	limit := intQueryParam(r, "limit", defaultDocumentListLimit, true)
	docs, err := h.admin.ListDocuments(r.Context(), limit, r.URL.Query().Get("domain"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := mapSlice(docs, func(d domain.IndexedDocument) adminDocument {
		return adminDocument{
			ID: d.ID, URL: d.URL, Host: d.Host, Title: d.Title,
			DocLength: d.DocLength, Version: d.Version, CrawledAt: d.CrawledAt,
			InternalLinks: d.InternalLinks, ExternalLinks: d.ExternalLinks, Backlinks: d.Backlinks,
			PageRank: d.PageRank,
		}
	})
	writeJSON(w, http.StatusOK, out)
}

// maxDeleteDomainDocs bounds how many of a domain's documents one bulk
// delete looks up/queues -- generous enough no real domain hits it.
const maxDeleteDomainDocs = 100000

type adminDeleteDomainResponse struct {
	Queued int `json:"queued"`
}

// handleAdminDeleteDomainDocuments removes every document in one domain.
// Fire-and-forget: looks up IDs synchronously, then deletes in a
// background goroutine (context.Background()) and returns 202 immediately.
func (h *Handler) handleAdminDeleteDomainDocuments(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	domainName := r.URL.Query().Get("domain")
	if domainName == "" {
		http.Error(w, "domain must not be empty", http.StatusBadRequest)
		return
	}
	docs, err := h.admin.ListDocuments(r.Context(), maxDeleteDomainDocs, domainName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ids := mapSlice(docs, func(d domain.IndexedDocument) string { return d.ID })

	go func() {
		ctx := context.Background()
		for _, id := range ids {
			if err := h.admin.DeleteDocument(ctx, id); err != nil {
				// %q, not %s: domainName comes straight from ?domain=, so an
				// embedded CR/LF could forge a fake log line -- %q quotes/escapes it.
				log.Printf("bulk-deleting domain %q: deleting %q: %v", domainName, id, err)
			}
		}
	}()

	writeJSON(w, http.StatusAccepted, adminDeleteDomainResponse{Queued: len(ids)})
}

const defaultDomainSearchLimit = 20

type adminDomainSummary struct {
	Host     string `json:"host"`
	DocCount int    `json:"doc_count"`
}

func toAdminDomainSummary(d domain.DomainSummary) adminDomainSummary {
	return adminDomainSummary{Host: d.Host, DocCount: d.DocCount}
}

// handleAdminSearchDomains backs the Documents page's domain search: an
// empty/missing q returns an empty list, not the full list, by design.
func (h *Handler) handleAdminSearchDomains(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	limit := intQueryParam(r, "limit", defaultDomainSearchLimit, true)
	domains, err := h.admin.SearchDomains(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := mapSlice(domains, toAdminDomainSummary)
	writeJSON(w, http.StatusOK, out)
}

const defaultOverviewTopDomains = 8

type adminAgeBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type adminVersionCount struct {
	Version int `json:"version"`
	Count   int `json:"count"`
}

type adminStoredVersionsCount struct {
	StoredVersions int `json:"stored_versions"`
	DocCount       int `json:"doc_count"`
}

type adminDocumentsOverview struct {
	TopDomains          []adminDomainSummary       `json:"top_domains"`
	AgeBuckets          []adminAgeBucket           `json:"age_buckets"`
	TotalDomains        int                        `json:"total_domains"`
	VersionCounts       []adminVersionCount        `json:"version_counts"`
	StoredVersionCounts []adminStoredVersionsCount `json:"stored_version_counts"`
}

// handleAdminDocumentsOverview backs the admin Overview page's summary
// panels. Registered as "GET /admin/api/documents/overview", so no method check needed.
func (h *Handler) handleAdminDocumentsOverview(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	overview, err := h.admin.DocumentsOverview(r.Context(), defaultOverviewTopDomains)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	topDomains := mapSlice(overview.TopDomains, toAdminDomainSummary)
	ageBuckets := mapSlice(overview.AgeBuckets, func(b domain.AgeBucket) adminAgeBucket {
		return adminAgeBucket{Label: b.Label, Count: b.Count}
	})
	versionCounts := mapSlice(overview.VersionCounts, func(v domain.VersionCount) adminVersionCount {
		return adminVersionCount{Version: v.Version, Count: v.Count}
	})
	storedVersionCounts := mapSlice(overview.StoredVersionCounts, func(s domain.StoredVersionsCount) adminStoredVersionsCount {
		return adminStoredVersionsCount{StoredVersions: s.StoredVersions, DocCount: s.DocCount}
	})
	writeJSON(w, http.StatusOK, adminDocumentsOverview{
		TopDomains: topDomains, AgeBuckets: ageBuckets,
		TotalDomains: overview.TotalDomains, VersionCounts: versionCounts,
		StoredVersionCounts: storedVersionCounts,
	})
}

type adminDocumentVersion struct {
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	DocLength int       `json:"doc_length"`
	CrawledAt time.Time `json:"crawled_at"`
}

// handleAdminDocumentVersions lists a document's superseded prior
// versions -- an empty list just means it's never been re-crawled differently.
func (h *Handler) handleAdminDocumentVersions(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	versions, err := h.admin.DocumentVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := mapSlice(versions, func(v domain.DocumentVersion) adminDocumentVersion {
		return adminDocumentVersion{Version: v.Version, Title: v.Title, DocLength: v.DocLength, CrawledAt: v.CrawledAt}
	})
	writeJSON(w, http.StatusOK, out)
}

// handleAdminDeleteDocument is registered on "DELETE
// /admin/api/documents/{id}" -- the mux never invokes it with an empty
// {id}, so no separate empty-id check is needed.
func (h *Handler) handleAdminDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	err := h.admin.DeleteDocument(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrDocumentNotFound, "document not found", map[string]bool{"ok": true})
}

type adminPosting struct {
	DocID     string `json:"doc_id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	TermFreq  int    `json:"term_freq"`
	DocLength int    `json:"doc_length"`
}

type adminPostingsResponse struct {
	Term     string         `json:"term"`
	DocFreq  int            `json:"doc_freq"`
	Postings []adminPosting `json:"postings"`
}

// postingsSnippetMaxLen bounds each match excerpt for the vocabulary
// term-detail view -- same length the public/debug search snippet uses,
// so it reads consistently across the admin UI.
const postingsSnippetMaxLen = 200

// defaultPostingsLimit bounds the vocabulary term-detail page list --
// PostingsForTerm has no inherent bound, since a term can appear in every document.
const defaultPostingsLimit = 500

func (h *Handler) handleAdminPostings(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	term := r.URL.Query().Get("term")
	if term == "" {
		http.Error(w, "term must not be empty", http.StatusBadRequest)
		return
	}
	limit := intQueryParam(r, "limit", defaultPostingsLimit, true)
	postings, err := h.admin.PostingsForTerm(r.Context(), term, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ids := mapSlice(postings, func(p domain.PostingStats) string { return p.DocID })
	docs, err := h.admin.DocumentsByIDs(r.Context(), ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := adminPostingsResponse{Term: term, Postings: make([]adminPosting, len(postings))}
	if len(postings) > 0 {
		resp.DocFreq = postings[0].DocFreq
	}
	for i, p := range postings {
		doc := docs[p.DocID]
		resp.Postings[i] = adminPosting{
			DocID: p.DocID, URL: doc.URL, Title: doc.Title,
			Snippet:  domain.Snippet(doc.Text, nil, []string{term}, postingsSnippetMaxLen),
			TermFreq: p.TermFreq, DocLength: p.DocLength,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// adminTermScore is domain.TermScore's wire shape -- one query term's BM25
// contribution to a single result, for the per-result debug detail view.
type adminTermScore struct {
	Term      string  `json:"term"`
	TermFreq  int     `json:"term_freq"`
	DocFreq   int     `json:"doc_freq"`
	DocLength int     `json:"doc_length"`
	Score     float64 `json:"score"`
}

type adminDebugResult struct {
	DocID       string  `json:"doc_id"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Snippet     string  `json:"snippet"`
	BM25Score   float64 `json:"bm25_score"`
	NormBM25    float64 `json:"norm_bm25"`
	SemanticSim float64 `json:"semantic_sim"`
	PageRank    float64 `json:"pagerank"`
	// NormalizedPageRank is 0 whenever PageRankWeight is 0 or every result in
	// the batch has a zero PageRank -- see domain.HybridResult.
	NormalizedPageRank float64          `json:"normalized_pagerank"`
	FinalScore         float64          `json:"final_score"`
	BM25Terms          []adminTermScore `json:"bm25_terms,omitempty"`
	// Alpha/K1/B/PageRankWeight are the tuning parameters that produced this
	// result -- identical across every result of one search, like CorrectedTerms.
	Alpha          float64 `json:"alpha"`
	K1             float64 `json:"k1"`
	B              float64 `json:"b"`
	PageRankWeight float64 `json:"pagerank_weight"`
	// CorrectedTerms describes the query, not this result -- identical
	// across every result of one search, present so the debug UI can show a
	// fuzzy-matched term transparently.
	CorrectedTerms []domain.CorrectedTerm `json:"corrected_terms,omitempty"`
}

func (h *Handler) handleAdminSearch(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.debug != nil, "search debugging") {
		return
	}
	query := r.URL.Query().Get("q")
	topK := intQueryParam(r, "top_k", h.opSettings.Get().DefaultTopK, false)
	results, err := h.debug.Search(r.Context(), query, ports.SearchQuery{TopK: topK, Sort: parseSortParam(r), ProviderWeights: parseProviderWeightsParam(r)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := mapSlice(results, func(res domain.HybridResult) adminDebugResult {
		terms := mapSlice(res.BM25Terms, func(t domain.TermScore) adminTermScore {
			return adminTermScore{Term: t.Term, TermFreq: t.TermFreq, DocFreq: t.DocFreq, DocLength: t.DocLength, Score: t.Score}
		})
		return adminDebugResult{
			DocID: res.DocID, URL: res.URL, Title: res.Title, Snippet: res.Snippet,
			BM25Score: res.BM25Score, NormBM25: res.NormBM25, SemanticSim: res.SemanticSim,
			PageRank: res.PageRank, NormalizedPageRank: res.NormalizedPageRank, FinalScore: res.FinalScore,
			BM25Terms: terms,
			Alpha:     res.Alpha, K1: res.K1, B: res.B, PageRankWeight: res.PageRankWeight,
			CorrectedTerms: res.CorrectedTerms,
		}
	})
	writeJSON(w, http.StatusOK, out)
}

type tuningValues struct {
	Alpha float64 `json:"alpha"`
	K1    float64 `json:"k1"`
	B     float64 `json:"b"`
	// PageRankWeight blends a document's normalized link-authority score
	// into ranking -- 0 (default) means no influence.
	PageRankWeight float64 `json:"pagerank_weight"`
}

// operationalValues mirrors domain.OperationalSettingsValues for the wire
// format: durations as whole seconds/hours, friendlier for a form/JSON
// than Go's nanosecond time.Duration.
type operationalValues struct {
	FetchTimeoutSeconds       int    `json:"fetch_timeout_seconds"`
	UserAgent                 string `json:"user_agent"`
	DefaultMaxPages           int    `json:"default_max_pages"`
	MinTextLength             int    `json:"min_text_length"`
	DefaultTopK               int    `json:"default_top_k"`
	SessionTTLHours           int    `json:"session_ttl_hours"`
	CrawlDelayMs              int    `json:"crawl_delay_ms"`
	MaxResponseKB             int    `json:"max_response_kb"`
	SemanticCandidatePoolSize int    `json:"semantic_candidate_pool_size"`
	// SemanticRescoreCap mirrors the same-named domain.OperationalSettingsValues field.
	SemanticRescoreCap       int  `json:"semantic_rescore_cap"`
	DBMaxOpenConns           int  `json:"db_max_open_conns"`
	DBMaxIdleConns           int  `json:"db_max_idle_conns"`
	DBConnMaxLifetimeMinutes int  `json:"db_conn_max_lifetime_minutes"`
	FuzzyMatchEnabled        bool `json:"fuzzy_match_enabled"`
	FuzzyMaxEditDistance     int  `json:"fuzzy_max_edit_distance"`
	// PageRankRecomputeIntervalMinutes is how often cmd/crawl's ticker
	// recomputes PageRank -- see domain.OperationalSettingsValues.
	PageRankRecomputeIntervalMinutes int `json:"pagerank_recompute_interval_minutes"`
	// ANNSearchEnabled forces the brute-force semantic fallback even when
	// Postgres pgvector ANN is available, when false -- see
	// domain.OperationalSettingsValues.
	ANNSearchEnabled bool `json:"ann_search_enabled"`
	// MaxRetainedCrawlJobs bounds crawl-server's persistent job history --
	// see domain.OperationalSettingsValues.
	MaxRetainedCrawlJobs int `json:"max_retained_crawl_jobs"`
	// MaxConcurrentCrawls bounds how many crawl jobs fetch pages at once on
	// crawl-server -- see domain.OperationalSettingsValues.
	MaxConcurrentCrawls int `json:"max_concurrent_crawls"`
	// DefaultRenderer is the crawler's global default rendering mode -- a
	// scheduled/one-off crawl's own renderer overrides this when set.
	DefaultRenderer string `json:"default_renderer"`
	// LinkScope is the crawler's global default for how far a crawl follows
	// links -- a scheduled/one-off crawl's own link_scope overrides it when set.
	LinkScope string `json:"link_scope"`
	// MaxDocumentVersions bounds how many versions of a document are kept
	// -- see domain.OperationalSettingsValues.
	MaxDocumentVersions int `json:"max_document_versions"`
	// TitleWeight is how many times a title is counted into its indexed
	// token stream, ahead of the body -- see domain.OperationalSettingsValues.
	TitleWeight int `json:"title_weight"`
	// EmbeddingHashEnabled/EmbeddingSearchWeights mirror the same-named
	// domain.OperationalSettingsValues fields -- see there for why
	// EmbeddingHashEnabled needs a restart and every EmbeddingSearchWeights
	// key must name a currently-enabled provider. HTTP endpoints are
	// managed separately via /admin/api/embeddings/endpoints.
	EmbeddingHashEnabled   bool               `json:"embedding_hash_enabled"`
	EmbeddingSearchWeights map[string]float64 `json:"embedding_search_weights"`
	// EmbeddingTitleWeight mirrors the same-named domain.OperationalSettingsValues field.
	EmbeddingTitleWeight float64 `json:"embedding_title_weight"`
	// EmbeddingRecomputeConcurrency mirrors the same-named
	// domain.OperationalSettingsValues field -- surfaced on the embeddings
	// admin page (its own control there, not this general settings form)
	// rather than here, since it's specific to the recompute job rather
	// than an always-active knob. Still round-trips through this same
	// settings object, applied live on the recompute's next batch.
	EmbeddingRecomputeConcurrency int `json:"embedding_recompute_concurrency"`
	// URLAliasWWWEnabled mirrors the same-named domain.OperationalSettingsValues field.
	URLAliasWWWEnabled bool `json:"url_alias_www_enabled"`
	// ContentDedupEnabled/ContentDedupMethod/ContentDedupSimHashMaxDistance/
	// ContentDedupIntervalMinutes mirror the same-named
	// domain.OperationalSettingsValues fields.
	ContentDedupEnabled            bool   `json:"content_dedup_enabled"`
	ContentDedupMethod             string `json:"content_dedup_method"`
	ContentDedupSimHashMaxDistance int    `json:"content_dedup_simhash_max_distance"`
	ContentDedupIntervalMinutes    int    `json:"content_dedup_interval_minutes"`
}

func toOperationalValues(v domain.OperationalSettingsValues) operationalValues {
	return operationalValues{
		FetchTimeoutSeconds:              int(v.FetchTimeout / time.Second),
		UserAgent:                        v.UserAgent,
		DefaultMaxPages:                  v.DefaultMaxPages,
		MinTextLength:                    v.MinTextLength,
		DefaultTopK:                      v.DefaultTopK,
		SessionTTLHours:                  int(v.SessionTTL / time.Hour),
		CrawlDelayMs:                     v.CrawlDelayMs,
		MaxResponseKB:                    v.MaxResponseBytes / 1024,
		SemanticCandidatePoolSize:        v.SemanticCandidatePoolSize,
		SemanticRescoreCap:               v.SemanticRescoreCap,
		DBMaxOpenConns:                   v.DBMaxOpenConns,
		DBMaxIdleConns:                   v.DBMaxIdleConns,
		DBConnMaxLifetimeMinutes:         int(v.DBConnMaxLifetime / time.Minute),
		FuzzyMatchEnabled:                v.FuzzyMatchEnabled,
		FuzzyMaxEditDistance:             v.FuzzyMaxEditDistance,
		PageRankRecomputeIntervalMinutes: v.PageRankRecomputeIntervalMinutes,
		ANNSearchEnabled:                 v.ANNSearchEnabled,
		MaxRetainedCrawlJobs:             v.MaxRetainedCrawlJobs,
		MaxConcurrentCrawls:              v.MaxConcurrentCrawls,
		DefaultRenderer:                  v.DefaultRenderer,
		LinkScope:                        v.LinkScope,
		MaxDocumentVersions:              v.MaxDocumentVersions,
		TitleWeight:                      v.TitleWeight,
		EmbeddingHashEnabled:             v.EmbeddingHashEnabled,
		EmbeddingSearchWeights:           v.EmbeddingSearchWeights,
		EmbeddingTitleWeight:             v.EmbeddingTitleWeight,
		EmbeddingRecomputeConcurrency:    v.EmbeddingRecomputeConcurrency,
		URLAliasWWWEnabled:               v.URLAliasWWWEnabled,
		ContentDedupEnabled:              v.ContentDedupEnabled,
		ContentDedupMethod:               v.ContentDedupMethod,
		ContentDedupSimHashMaxDistance:   v.ContentDedupSimHashMaxDistance,
		ContentDedupIntervalMinutes:      v.ContentDedupIntervalMinutes,
	}
}

func (o operationalValues) toSettingsValues() domain.OperationalSettingsValues {
	return domain.OperationalSettingsValues{
		FetchTimeout:                     time.Duration(o.FetchTimeoutSeconds) * time.Second,
		UserAgent:                        o.UserAgent,
		DefaultMaxPages:                  o.DefaultMaxPages,
		MinTextLength:                    o.MinTextLength,
		DefaultTopK:                      o.DefaultTopK,
		SessionTTL:                       time.Duration(o.SessionTTLHours) * time.Hour,
		CrawlDelayMs:                     o.CrawlDelayMs,
		MaxResponseBytes:                 o.MaxResponseKB * 1024,
		SemanticCandidatePoolSize:        o.SemanticCandidatePoolSize,
		SemanticRescoreCap:               o.SemanticRescoreCap,
		DBMaxOpenConns:                   o.DBMaxOpenConns,
		DBMaxIdleConns:                   o.DBMaxIdleConns,
		DBConnMaxLifetime:                time.Duration(o.DBConnMaxLifetimeMinutes) * time.Minute,
		FuzzyMatchEnabled:                o.FuzzyMatchEnabled,
		FuzzyMaxEditDistance:             o.FuzzyMaxEditDistance,
		PageRankRecomputeIntervalMinutes: o.PageRankRecomputeIntervalMinutes,
		ANNSearchEnabled:                 o.ANNSearchEnabled,
		MaxRetainedCrawlJobs:             o.MaxRetainedCrawlJobs,
		MaxConcurrentCrawls:              o.MaxConcurrentCrawls,
		DefaultRenderer:                  o.DefaultRenderer,
		LinkScope:                        o.LinkScope,
		MaxDocumentVersions:              o.MaxDocumentVersions,
		TitleWeight:                      o.TitleWeight,
		EmbeddingHashEnabled:             o.EmbeddingHashEnabled,
		EmbeddingSearchWeights:           o.EmbeddingSearchWeights,
		EmbeddingTitleWeight:             o.EmbeddingTitleWeight,
		EmbeddingRecomputeConcurrency:    o.EmbeddingRecomputeConcurrency,
		URLAliasWWWEnabled:               o.URLAliasWWWEnabled,
		ContentDedupEnabled:              o.ContentDedupEnabled,
		ContentDedupMethod:               o.ContentDedupMethod,
		ContentDedupSimHashMaxDistance:   o.ContentDedupSimHashMaxDistance,
		ContentDedupIntervalMinutes:      o.ContentDedupIntervalMinutes,
	}
}

type settingsResponse struct {
	Tuning      tuningValues      `json:"tuning"`
	Operational operationalValues `json:"operational"`
}

// embeddingConnectivityTestTimeout bounds a single probe call against a
// candidate HTTP embedding endpoint -- short enough that a hung endpoint
// doesn't stall the request, generous enough for a real inference call.
const embeddingConnectivityTestTimeout = 10 * time.Second

// modelLister is the narrow capability httpembed.Embedder implements
// beyond ports.EmbeddingProvider -- not every provider has a remote
// catalog (hashembed doesn't), so this is a type assertion at point of use.
type modelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// chatModelProber is the narrow capability httpchat.Client implements
// beyond ports.ChatCompleter -- querying a model's advertised max context
// length, not every endpoint exposes this. Same type-assertion convention
// as modelLister above.
type chatModelProber interface {
	ModelMaxContextTokens(ctx context.Context, endpoint domain.ChatEndpoint) (tokens int, ok bool, err error)
}

// embeddingCandidateRequest is a not-yet-saved HTTP endpoint config,
// probed by handleAdminEmbeddingsModels/handleAdminEmbeddingsTest. ID,
// when set, names the already-saved endpoint being edited -- see
// resolveCandidateAPIKey.
type embeddingCandidateRequest struct {
	ID         string `json:"id"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

func (req embeddingCandidateRequest) toEndpoint() domain.EmbeddingHTTPEndpoint {
	return domain.EmbeddingHTTPEndpoint{BaseURL: req.BaseURL, APIKey: req.APIKey, Model: req.Model, Dimensions: req.Dimensions}
}

// resolveCandidateAPIKey treats a blank APIKey as "not retyped," not "no
// key" -- the form never echoes a stored key back, so testing a saved
// endpoint without retyping its key would otherwise always 401. Blank with
// no ID, or a failed lookup, leaves e unchanged.
func (h *Handler) resolveCandidateAPIKey(ctx context.Context, e domain.EmbeddingHTTPEndpoint, id string) domain.EmbeddingHTTPEndpoint {
	if e.APIKey != "" || id == "" || h.embeddingEndpoints == nil {
		return e
	}
	stored, err := h.embeddingEndpoints.GetEmbeddingEndpoint(ctx, id)
	if err != nil {
		return e
	}
	e.APIKey = h.decryptAPIKey(stored.APIKey)
	return e
}

type adminEmbeddingModelsResponse struct {
	Models []string `json:"models"`
	// Error is set when base_url is non-empty but ListModels itself fails
	// (bad credentials, no /models, network error) -- a soft failure shown
	// as "couldn't fetch model list," since model stays usable as free text.
	Error string `json:"error,omitempty"`
}

// handleAdminEmbeddingsModels lists the models a candidate endpoint
// reports, so the subpage can prefill suggestions. Returns an empty list
// when base_url is blank. No "not configured" gate: h.newEmbedder is
// always set by New.
func (h *Handler) handleAdminEmbeddingsModels(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeJSON[embeddingCandidateRequest](w, r)
	if !ok {
		return
	}
	if req.BaseURL == "" {
		writeJSON(w, http.StatusOK, adminEmbeddingModelsResponse{})
		return
	}
	endpoint := h.resolveCandidateAPIKey(r.Context(), req.toEndpoint(), req.ID)
	lister, ok := h.newEmbedder(endpoint).(modelLister)
	if !ok {
		writeJSON(w, http.StatusOK, adminEmbeddingModelsResponse{Error: "this provider doesn't support listing models"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), embeddingConnectivityTestTimeout)
	defer cancel()
	models, err := lister.ListModels(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, adminEmbeddingModelsResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, adminEmbeddingModelsResponse{Models: models})
}

type adminEmbeddingTestResponse struct {
	// Error is empty on a successful connection test.
	Error string `json:"error,omitempty"`
}

// handleAdminEmbeddingsTest probes a candidate endpoint with one real
// Embed call, so the subpage can flag a problem immediately rather than on
// the next real search. No "not configured" gate, same as handleAdminEmbeddingsModels.
func (h *Handler) handleAdminEmbeddingsTest(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeJSON[embeddingCandidateRequest](w, r)
	if !ok {
		return
	}
	endpoint := h.resolveCandidateAPIKey(r.Context(), req.toEndpoint(), req.ID)
	writeJSON(w, http.StatusOK, adminEmbeddingTestResponse{Error: h.testEmbeddingConnectivity(r.Context(), endpoint)})
}

// testEmbeddingConnectivity makes one real Embed call against e to check
// a base URL/model/API key combination. A no-op when BaseURL is blank or
// h.newEmbedder isn't set.
func (h *Handler) testEmbeddingConnectivity(ctx context.Context, e domain.EmbeddingHTTPEndpoint) string {
	if e.BaseURL == "" || h.newEmbedder == nil {
		return ""
	}
	testCtx, cancel := context.WithTimeout(ctx, embeddingConnectivityTestTimeout)
	defer cancel()
	if _, err := h.newEmbedder(e).Embed(testCtx, "connection test"); err != nil {
		return err.Error()
	}
	return ""
}

// persistSetting saves v (JSON-encoded) under key so every other
// process's next poll picks it up -- a no-op with no SettingsStore.
// Failures are logged, not surfaced: a best-effort convenience, not a
// transactional write.
func (h *Handler) persistSetting(ctx context.Context, key string, v interface{}) {
	if h.settingsStore == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("encoding %s setting: %v", key, err)
		return
	}
	if err := h.settingsStore.SaveSetting(ctx, key, string(data)); err != nil {
		log.Printf("saving %s setting: %v", key, err)
	}
}

type embeddingEndpointRequest struct {
	Name               string  `json:"name"`
	BaseURL            string  `json:"base_url"`
	APIKey             string  `json:"api_key"`
	Model              string  `json:"model"`
	Dimensions         int     `json:"dimensions"`
	RateLimitPerSecond float64 `json:"rate_limit_per_second"`
	Enabled            bool    `json:"enabled"`
	// ChunkSizeTokens/TokenizeURL mirror domain.EmbeddingHTTPEndpoint's
	// same-named fields -- see that type's doc comments.
	ChunkSizeTokens int    `json:"chunk_size_tokens"`
	TokenizeURL     string `json:"tokenize_url"`
	// ClearAPIKey is meaningful only to the PATCH handler: since GET never
	// echoes a stored key, an edit form can't distinguish "left blank,
	// don't change" from "remove it" -- blank APIKey means the former; this
	// flag asks for the latter. Ignored by POST, which has no prior key.
	ClearAPIKey bool `json:"clear_api_key"`
}

type embeddingEndpointResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// HasAPIKey reports only whether a key is set, never its value -- same
	// redacted-summary treatment scheduledCrawlResponse gives stored credentials.
	HasAPIKey          bool      `json:"has_api_key"`
	Model              string    `json:"model"`
	Dimensions         int       `json:"dimensions"`
	RateLimitPerSecond float64   `json:"rate_limit_per_second"`
	Enabled            bool      `json:"enabled"`
	ChunkSizeTokens    int       `json:"chunk_size_tokens"`
	TokenizeURL        string    `json:"tokenize_url"`
	CreatedAt          time.Time `json:"created_at"`
}

func toEmbeddingEndpointResponse(e domain.EmbeddingHTTPEndpoint) embeddingEndpointResponse {
	return embeddingEndpointResponse{
		ID: e.ID, Name: e.Name, BaseURL: e.BaseURL, HasAPIKey: e.APIKey != "",
		Model: e.Model, Dimensions: e.Dimensions, RateLimitPerSecond: e.RateLimitPerSecond,
		Enabled: e.Enabled, ChunkSizeTokens: e.ChunkSizeTokens, TokenizeURL: e.TokenizeURL,
		CreatedAt: e.CreatedAt,
	}
}

func validateEmbeddingEndpointRequest(w http.ResponseWriter, req embeddingEndpointRequest) bool {
	if req.Name == "" {
		http.Error(w, msgNameMustNotBeEmpty, http.StatusBadRequest)
		return false
	}
	if req.BaseURL == "" {
		http.Error(w, "base_url must not be empty", http.StatusBadRequest)
		return false
	}
	if req.Dimensions <= 0 {
		http.Error(w, "dimensions must be positive", http.StatusBadRequest)
		return false
	}
	if req.RateLimitPerSecond < 0 {
		http.Error(w, "rate_limit_per_second must not be negative", http.StatusBadRequest)
		return false
	}
	if req.ChunkSizeTokens < 0 {
		http.Error(w, "chunk_size_tokens must not be negative", http.StatusBadRequest)
		return false
	}
	return true
}

// validateChatEndpointRequest mirrors validateEmbeddingEndpointRequest's
// ChunkSizeTokens check: MaxContextTokens shares the same "0 disables,
// negative invalid" convention.
func validateChatEndpointRequest(w http.ResponseWriter, req chatEndpointRequest) bool {
	if req.MaxContextTokens < 0 {
		http.Error(w, "max_context_tokens must not be negative", http.StatusBadRequest)
		return false
	}
	if req.WebSearchResultCount < 0 {
		http.Error(w, "web_search_result_count must not be negative", http.StatusBadRequest)
		return false
	}
	return true
}

// encryptAPIKey seals apiKey via settingscrypto for storage (a no-op when
// h.settingsEncryptionKey is nil, or apiKey is already empty).
func (h *Handler) encryptAPIKey(apiKey string) string {
	enc, err := settingscrypto.Encrypt(h.settingsEncryptionKey, apiKey)
	if err != nil {
		log.Printf("encrypting embedding endpoint API key: %v", err)
		return apiKey
	}
	return enc
}

// decryptAPIKey reverses encryptAPIKey for a stored value, used by
// resolveCandidateAPIKey. A decryption failure is logged and returns
// apiKey unchanged -- it then just fails the probe with its own 401.
func (h *Handler) decryptAPIKey(apiKey string) string {
	dec, err := settingscrypto.Decrypt(h.settingsEncryptionKey, apiKey)
	if err != nil {
		log.Printf("decrypting embedding endpoint API key: %v", err)
		return apiKey
	}
	return dec
}

// handleAdminEmbeddingEndpoints lists (GET) or creates (POST) HTTP
// embedding endpoint configs. A new endpoint's ID is minted from its name,
// deduped against every existing ID plus the reserved "hash" provider ID.
func (h *Handler) handleAdminEmbeddingEndpoints(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, configNameEmbeddingEndpoints) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		endpoints, err := h.embeddingEndpoints.ListEmbeddingEndpoints(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(endpoints, toEmbeddingEndpointResponse))
	case http.MethodPost:
		req, ok := decodeJSON[embeddingEndpointRequest](w, r)
		if !ok {
			return
		}
		if !validateEmbeddingEndpointRequest(w, req) {
			return
		}
		existing, err := h.embeddingEndpoints.ListEmbeddingEndpoints(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existingIDs := existingIDSet(existing, func(e domain.EmbeddingHTTPEndpoint) string { return e.ID }, domain.EmbeddingProviderHash)
		e := domain.EmbeddingHTTPEndpoint{
			ID:   domain.NewEmbeddingEndpointID(req.Name, existingIDs),
			Name: req.Name, BaseURL: req.BaseURL, APIKey: h.encryptAPIKey(req.APIKey),
			Model: req.Model, Dimensions: req.Dimensions, RateLimitPerSecond: req.RateLimitPerSecond,
			Enabled: req.Enabled, ChunkSizeTokens: req.ChunkSizeTokens, TokenizeURL: req.TokenizeURL,
			CreatedAt: time.Now().UTC(),
		}
		if err := h.embeddingEndpoints.CreateEmbeddingEndpoint(r.Context(), e); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toEmbeddingEndpointResponse(e))
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminGetEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, configNameEmbeddingEndpoints) {
		return
	}
	e, err := h.embeddingEndpoints.GetEmbeddingEndpoint(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, msgEmbeddingEndpointNotFound, toEmbeddingEndpointResponse(e))
}

// handleAdminUpdateEmbeddingEndpoint replaces an endpoint's editable
// fields. APIKey is the one exception to "PATCH is a full replace": blank
// means unchanged, req.ClearAPIKey removes it explicitly. ID is never
// editable (it's baked into document_embeddings.provider and ANN names).
func (h *Handler) handleAdminUpdateEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, configNameEmbeddingEndpoints) {
		return
	}
	req, ok := decodeJSON[embeddingEndpointRequest](w, r)
	if !ok {
		return
	}
	if !validateEmbeddingEndpointRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	existing, err := h.embeddingEndpoints.GetEmbeddingEndpoint(r.Context(), id)
	if err != nil {
		respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, msgEmbeddingEndpointNotFound, nil)
		return
	}
	apiKey := h.resolveUpdatedAPIKey(existing.APIKey, req.APIKey, req.ClearAPIKey)
	e := domain.EmbeddingHTTPEndpoint{
		ID: id, Name: req.Name, BaseURL: req.BaseURL, APIKey: apiKey,
		Model: req.Model, Dimensions: req.Dimensions, RateLimitPerSecond: req.RateLimitPerSecond,
		Enabled: req.Enabled, ChunkSizeTokens: req.ChunkSizeTokens, TokenizeURL: req.TokenizeURL,
		CreatedAt: existing.CreatedAt,
	}
	err = h.embeddingEndpoints.UpdateEmbeddingEndpoint(r.Context(), e)
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, msgEmbeddingEndpointNotFound, toEmbeddingEndpointResponse(e))
}

func (h *Handler) handleAdminDeleteEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, configNameEmbeddingEndpoints) {
		return
	}
	err := h.embeddingEndpoints.DeleteEmbeddingEndpoint(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, msgEmbeddingEndpointNotFound, map[string]bool{"ok": true})
}

type mcpServerRequest struct {
	Name             string   `json:"name"`
	Transport        string   `json:"transport"`
	Command          string   `json:"command"`
	Args             []string `json:"args"`
	BaseURL          string   `json:"base_url"`
	APIKey           string   `json:"api_key"`
	Enabled          bool     `json:"enabled"`
	Prompt           string   `json:"prompt"`
	GatedByWebSearch bool     `json:"gated_by_web_search"`
	// ClearAPIKey mirrors embeddingEndpointRequest.ClearAPIKey exactly --
	// see that field's doc comment.
	ClearAPIKey bool `json:"clear_api_key"`
}

type mcpServerResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	BaseURL   string   `json:"base_url"`
	// HasAPIKey reports only whether a key is set, never its value -- same
	// redacted-summary treatment embeddingEndpointResponse gives.
	HasAPIKey        bool   `json:"has_api_key"`
	Enabled          bool   `json:"enabled"`
	Prompt           string `json:"prompt"`
	GatedByWebSearch bool   `json:"gated_by_web_search"`
}

func toMCPServerResponse(s domain.MCPServer) mcpServerResponse {
	return mcpServerResponse{
		ID: s.ID, Name: s.Name, Transport: s.Transport, Command: s.Command, Args: s.Args,
		BaseURL: s.BaseURL, HasAPIKey: s.APIKey != "", Enabled: s.Enabled,
		Prompt: s.Prompt, GatedByWebSearch: s.GatedByWebSearch,
	}
}

// normalizeMCPServerName lowercases req.Name for a "stdio" server -- Name
// is a cosmetic admin-UI label (see MCPServer.Name), so this keeps every
// local server's label consistent regardless of how the admin typed it.
func normalizeMCPServerName(req mcpServerRequest) mcpServerRequest {
	if req.Transport == "stdio" {
		req.Name = strings.ToLower(req.Name)
	}
	return req
}

// validateMCPServerRequest requires a non-empty Name and a Transport of
// "stdio" (needs Command) or "http" (needs BaseURL) -- mirroring
// mcpclient.connect's own switch, so an accepted row is always actionable.
func validateMCPServerRequest(w http.ResponseWriter, req mcpServerRequest) bool {
	if req.Name == "" {
		http.Error(w, msgNameMustNotBeEmpty, http.StatusBadRequest)
		return false
	}
	switch req.Transport {
	case "stdio":
		if req.Command == "" {
			http.Error(w, "command must not be empty for a stdio transport", http.StatusBadRequest)
			return false
		}
	case "http":
		if req.BaseURL == "" {
			http.Error(w, "base_url must not be empty for an http transport", http.StatusBadRequest)
			return false
		}
	default:
		http.Error(w, `transport must be "stdio" or "http"`, http.StatusBadRequest)
		return false
	}
	return true
}

// handleAdminMCPServers lists (GET) or creates (POST) admin-configured
// MCP servers, mirroring handleAdminEmbeddingEndpoints' style. A new
// server's ID is minted from its name, deduped against existing IDs.
func (h *Handler) handleAdminMCPServers(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.mcpServers != nil, configNameMCPServers) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		servers, err := h.mcpServers.ListMCPServers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(servers, toMCPServerResponse))
	case http.MethodPost:
		req, ok := decodeJSON[mcpServerRequest](w, r)
		if !ok {
			return
		}
		req = normalizeMCPServerName(req)
		if !validateMCPServerRequest(w, req) {
			return
		}
		existing, err := h.mcpServers.ListMCPServers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existingIDs := existingIDSet(existing, func(s domain.MCPServer) string { return s.ID })
		s := domain.MCPServer{
			ID: domain.NewMCPServerID(req.Name, existingIDs), Name: req.Name,
			Transport: req.Transport, Command: req.Command, Args: req.Args, BaseURL: req.BaseURL,
			APIKey: h.encryptAPIKey(req.APIKey), Enabled: req.Enabled,
			Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
		}
		if err := h.mcpServers.CreateMCPServer(r.Context(), s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toMCPServerResponse(s))
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// handleAdminGetMCPServer returns one server by ID. ports.MCPServerStore
// has no single-row get, so this scans ListMCPServers -- never a real cost
// at admin-list scale.
func (h *Handler) handleAdminGetMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.mcpServers != nil, configNameMCPServers) {
		return
	}
	servers, err := h.mcpServers.ListMCPServers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	for _, s := range servers {
		if s.ID == id {
			writeJSON(w, http.StatusOK, toMCPServerResponse(s))
			return
		}
	}
	http.Error(w, msgMCPServerNotFound, http.StatusNotFound)
}

// handleAdminUpdateMCPServer replaces a server's editable fields. APIKey
// is the one exception to "PATCH is a full replace" (see
// embeddingEndpointRequest.ClearAPIKey). ID is never editable.
func (h *Handler) handleAdminUpdateMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.mcpServers != nil, configNameMCPServers) {
		return
	}
	req, ok := decodeJSON[mcpServerRequest](w, r)
	if !ok {
		return
	}
	req = normalizeMCPServerName(req)
	if !validateMCPServerRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	servers, err := h.mcpServers.ListMCPServers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var existingAPIKey string
	found := false
	for _, s := range servers {
		if s.ID == id {
			existingAPIKey = s.APIKey
			found = true
			break
		}
	}
	if !found {
		http.Error(w, msgMCPServerNotFound, http.StatusNotFound)
		return
	}
	apiKey := h.resolveUpdatedAPIKey(existingAPIKey, req.APIKey, req.ClearAPIKey)
	s := domain.MCPServer{
		ID: id, Name: req.Name, Transport: req.Transport, Command: req.Command, Args: req.Args,
		BaseURL: req.BaseURL, APIKey: apiKey, Enabled: req.Enabled,
		Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
	}
	err = h.mcpServers.UpdateMCPServer(r.Context(), s)
	respondOrNotFound(w, err, ports.ErrMCPServerNotFound, msgMCPServerNotFound, toMCPServerResponse(s))
}

func (h *Handler) handleAdminDeleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.mcpServers != nil, configNameMCPServers) {
		return
	}
	err := h.mcpServers.DeleteMCPServer(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrMCPServerNotFound, msgMCPServerNotFound, map[string]bool{"ok": true})
}

type agentRequest struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt"`
	MCPServerIDs []string `json:"mcp_server_ids"`
	Enabled      bool     `json:"enabled"`
}

type agentResponse struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"system_prompt"`
	MCPServerIDs []string `json:"mcp_server_ids"`
	Enabled      bool     `json:"enabled"`
}

func toAgentResponse(a domain.Agent) agentResponse {
	return agentResponse{
		ID: a.ID, Name: a.Name, Description: a.Description,
		SystemPrompt: a.SystemPrompt, MCPServerIDs: a.MCPServerIDs, Enabled: a.Enabled,
	}
}

// validateAgentRequest requires a non-empty Name -- MCPServerIDs isn't
// cross-checked against mcp_servers (same convention as every other admin
// CRUD handler here); a stale ID just never matches anything when
// ChatService.Chat filters by it.
func validateAgentRequest(w http.ResponseWriter, req agentRequest) bool {
	if req.Name == "" {
		http.Error(w, msgNameMustNotBeEmpty, http.StatusBadRequest)
		return false
	}
	return true
}

// handleAdminAgents lists (GET) or creates (POST) admin-configured
// agents, mirroring handleAdminMCPServers' style. A new agent's ID is
// minted from its name, deduped against existing IDs.
func (h *Handler) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.agents != nil, "agents") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		agents, err := h.agents.ListAgents(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(agents, toAgentResponse))
	case http.MethodPost:
		req, ok := decodeJSON[agentRequest](w, r)
		if !ok {
			return
		}
		if !validateAgentRequest(w, req) {
			return
		}
		existing, err := h.agents.ListAgents(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existingIDs := existingIDSet(existing, func(a domain.Agent) string { return a.ID })
		a := domain.Agent{
			ID: domain.NewAgentID(req.Name, existingIDs), Name: req.Name,
			Description: req.Description, SystemPrompt: req.SystemPrompt,
			MCPServerIDs: req.MCPServerIDs, Enabled: req.Enabled,
		}
		if err := h.agents.CreateAgent(r.Context(), a); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toAgentResponse(a))
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// handleAdminGetAgent returns one agent by ID -- scans ListAgents, same
// tolerance as handleAdminGetMCPServer.
func (h *Handler) handleAdminGetAgent(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.agents != nil, "agents") {
		return
	}
	agents, err := h.agents.ListAgents(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	for _, a := range agents {
		if a.ID == id {
			writeJSON(w, http.StatusOK, toAgentResponse(a))
			return
		}
	}
	http.Error(w, msgAgentNotFound, http.StatusNotFound)
}

// handleAdminUpdateAgent replaces an agent's editable fields. ID is never
// editable once created (mirrors handleAdminUpdateMCPServer).
func (h *Handler) handleAdminUpdateAgent(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.agents != nil, "agents") {
		return
	}
	req, ok := decodeJSON[agentRequest](w, r)
	if !ok {
		return
	}
	if !validateAgentRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	a := domain.Agent{
		ID: id, Name: req.Name, Description: req.Description, SystemPrompt: req.SystemPrompt,
		MCPServerIDs: req.MCPServerIDs, Enabled: req.Enabled,
	}
	err := h.agents.UpdateAgent(r.Context(), a)
	respondOrNotFound(w, err, ports.ErrAgentNotFound, msgAgentNotFound, toAgentResponse(a))
}

func (h *Handler) handleAdminDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.agents != nil, "agents") {
		return
	}
	err := h.agents.DeleteAgent(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrAgentNotFound, msgAgentNotFound, map[string]bool{"ok": true})
}

// mcpServerCandidateRequest is a not-yet-saved MCP server config, probed
// by handleAdminMCPServersTest, mirroring embeddingCandidateRequest's
// shape. ID, when set, names the already-saved server being edited.
type mcpServerCandidateRequest struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	BaseURL   string   `json:"base_url"`
	APIKey    string   `json:"api_key"`
}

func (req mcpServerCandidateRequest) toServer() domain.MCPServer {
	return domain.MCPServer{
		ID: req.ID, Name: req.Name, Transport: req.Transport, Command: req.Command,
		Args: req.Args, BaseURL: req.BaseURL, APIKey: req.APIKey, Enabled: true,
	}
}

// resolveCandidateMCPServerAPIKey mirrors resolveCandidateAPIKey for MCP
// servers -- scans ListMCPServers since ports.MCPServerStore has no
// single-row get.
func (h *Handler) resolveCandidateMCPServerAPIKey(ctx context.Context, s domain.MCPServer, id string) domain.MCPServer {
	if s.APIKey != "" || id == "" || h.mcpServers == nil {
		return s
	}
	servers, err := h.mcpServers.ListMCPServers(ctx)
	if err != nil {
		return s
	}
	for _, existing := range servers {
		if existing.ID == id {
			s.APIKey = h.decryptAPIKey(existing.APIKey)
			return s
		}
	}
	return s
}

type mcpServerToolResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type adminMCPServerTestResponse struct {
	Tools []mcpServerToolResponse `json:"tools"`
	// Error is set when connecting produced no tools at all -- Provider.Open
	// is best-effort/silent per server, so a real connection failure and a
	// server that legitimately exposes zero tools are indistinguishable here.
	Error string `json:"error,omitempty"`
}

// mcpServerTestTimeout bounds a single "list tools" probe -- mirrors
// embeddingConnectivityTestTimeout's own reasoning.
const mcpServerTestTimeout = 10 * time.Second

// handleAdminMCPServersTest connects to a candidate MCP server config and
// lists its actual tools, so the subpage can show each tool's real
// name/description from the live tools/list response before saving. No
// "not configured" gate: only needs h.mcpTools, independent of h.mcpServers.
func (h *Handler) handleAdminMCPServersTest(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	req, ok := decodeJSON[mcpServerCandidateRequest](w, r)
	if !ok {
		return
	}
	if req.Transport != "stdio" && req.Transport != "http" {
		writeJSON(w, http.StatusOK, adminMCPServerTestResponse{Error: `transport must be "stdio" or "http"`})
		return
	}
	if (req.Transport == "stdio" && req.Command == "") || (req.Transport == "http" && req.BaseURL == "") {
		writeJSON(w, http.StatusOK, adminMCPServerTestResponse{})
		return
	}
	if h.mcpTools == nil {
		writeJSON(w, http.StatusOK, adminMCPServerTestResponse{Error: "mcp tool discovery is not available on this server"})
		return
	}
	server := h.resolveCandidateMCPServerAPIKey(r.Context(), req.toServer(), req.ID)
	ctx, cancel := context.WithTimeout(r.Context(), mcpServerTestTimeout)
	defer cancel()
	session, tools := h.mcpTools.Open(ctx, []domain.MCPServer{server}, nil)
	defer session.Close()
	if len(tools) == 0 {
		writeJSON(w, http.StatusOK, adminMCPServerTestResponse{Error: "could not connect, or this server exposes no tools -- check the server's own log for details"})
		return
	}
	writeJSON(w, http.StatusOK, adminMCPServerTestResponse{Tools: mapSlice(tools, func(t domain.MCPTool) mcpServerToolResponse {
		return mcpServerToolResponse{Name: t.Name, Description: t.Description}
	})})
}

func (h *Handler) handleAdminEmbeddingEndpointPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminEmbeddingEndpointHTML)
}

func (h *Handler) handleAdminEmbeddingEndpointsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminEmbeddingEndpointsHTML)
}

// currentEmbeddingEndpoints lists every configured HTTP embedding
// endpoint, or an empty slice if unconfigured/failing -- the same
// degrade-gracefully convention used throughout this file.
func (h *Handler) currentEmbeddingEndpoints(ctx context.Context) []domain.EmbeddingHTTPEndpoint {
	if h.embeddingEndpoints == nil {
		return nil
	}
	endpoints, err := h.embeddingEndpoints.ListEmbeddingEndpoints(ctx)
	if err != nil {
		log.Printf("listing embedding endpoints: %v", err)
		return nil
	}
	return endpoints
}

type chatEndpointRequest struct {
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"api_key"`
	Model            string `json:"model"`
	Enabled          bool   `json:"enabled"`
	MaxContextTokens int    `json:"max_context_tokens"`
	WebSearchEnabled bool   `json:"web_search_enabled"`
	WebSearchBaseURL string `json:"web_search_base_url"`
	// WebSearchResultCount mirrors domain.ChatEndpoint.WebSearchResultCount
	// exactly -- see that field's doc comment. 0 is the default (no cap).
	WebSearchResultCount int `json:"web_search_result_count"`
	// SystemPrompt mirrors domain.ChatEndpoint.SystemPrompt exactly -- empty
	// is the default (no persistent prompt injected).
	SystemPrompt string `json:"system_prompt"`
	// DefaultAgentID mirrors domain.ChatEndpoint.DefaultAgentID exactly --
	// empty means no agent specialization. Not cross-checked against
	// agents, same convention as domain.Agent.MCPServerIDs.
	DefaultAgentID string `json:"default_agent_id"`
	// ClearAPIKey is meaningful only to a PATCH: GET never echoes a stored
	// key, so blank means "left unchanged," this flag means "remove it" --
	// mirrors embeddingEndpointRequest.ClearAPIKey exactly.
	ClearAPIKey bool `json:"clear_api_key"`
}

type chatEndpointResponse struct {
	BaseURL string `json:"base_url"`
	// HasAPIKey reports only whether a key is set, never its value -- same
	// redacted-summary treatment toEmbeddingEndpointResponse already gives.
	HasAPIKey        bool   `json:"has_api_key"`
	Model            string `json:"model"`
	Enabled          bool   `json:"enabled"`
	MaxContextTokens int    `json:"max_context_tokens"`
	WebSearchEnabled bool   `json:"web_search_enabled"`
	WebSearchBaseURL string `json:"web_search_base_url"`
	// WebSearchResultCount mirrors chatEndpointRequest.WebSearchResultCount
	// exactly -- see that field's doc comment.
	WebSearchResultCount int `json:"web_search_result_count"`
	// SystemPrompt mirrors domain.ChatEndpoint.SystemPrompt exactly -- see
	// chatEndpointRequest.SystemPrompt's doc comment.
	SystemPrompt string `json:"system_prompt"`
	// DefaultAgentID mirrors chatEndpointRequest.DefaultAgentID exactly --
	// see that field's doc comment.
	DefaultAgentID string    `json:"default_agent_id"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toChatEndpointResponse(e domain.ChatEndpoint) chatEndpointResponse {
	return chatEndpointResponse{
		BaseURL: e.BaseURL, HasAPIKey: e.APIKey != "", Model: e.Model, Enabled: e.Enabled,
		MaxContextTokens: e.MaxContextTokens, UpdatedAt: e.UpdatedAt,
		WebSearchEnabled: e.WebSearchEnabled, WebSearchBaseURL: e.WebSearchBaseURL,
		WebSearchResultCount: e.WebSearchResultCount,
		SystemPrompt:         e.SystemPrompt,
		DefaultAgentID:       e.DefaultAgentID,
	}
}

// defaultChatEndpointResponse is what GET returns when nothing's been
// saved -- a settings page GET should never fail just for being unconfigured.
func defaultChatEndpointResponse() chatEndpointResponse {
	return chatEndpointResponse{}
}

// chatModelContextProbeTimeout bounds the best-effort auto-detection call
// against the model's /models endpoint -- short enough not to stall a
// save, generous enough for a real round trip.
const chatModelContextProbeTimeout = 10 * time.Second

// autoDetectMaxContextTokens best-effort probes e's model for its
// advertised max context length, so a chat endpoint saved with
// MaxContextTokens unset gets a real value instead of silently meaning
// "trimming disabled" -- fixes a live incident where an incomplete PATCH
// (a full replace, not a merge) kept resetting it to 0 unnoticed.
//
// Returns 0 whenever detection isn't possible: BaseURL/Model unset,
// h.chatModelProber nil, the prober doesn't implement probing, or the call
// errors -- pure best-effort, never a reason to fail the save.
func (h *Handler) autoDetectMaxContextTokens(ctx context.Context, e domain.ChatEndpoint) int {
	if e.BaseURL == "" || e.Model == "" || h.chatModelProber == nil {
		return 0
	}
	prober, ok := h.chatModelProber.(chatModelProber)
	if !ok {
		return 0
	}
	probeCtx, cancel := context.WithTimeout(ctx, chatModelContextProbeTimeout)
	defer cancel()
	modelMax, ok, err := prober.ModelMaxContextTokens(probeCtx, e)
	if err != nil || !ok {
		return 0
	}
	return domain.AutoMaxContextTokens(modelMax)
}

// handleAdminChatEndpoint is single-row CRUD for the chat endpoint (GET
// current config, PATCH to upsert), mirroring
// handleAdminEmbeddingEndpoints' style -- see chatEndpointRequest.ClearAPIKey
// for the shared "blank means unchanged" convention. MaxContextTokens left
// unset (<= 0) is auto-detected from the model -- see autoDetectMaxContextTokens.
func (h *Handler) handleAdminChatEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chatEndpoints != nil, "chat endpoint") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		e, err := h.chatEndpoints.GetChatEndpoint(r.Context())
		if errors.Is(err, ports.ErrChatEndpointNotConfigured) {
			writeJSON(w, http.StatusOK, defaultChatEndpointResponse())
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toChatEndpointResponse(e))
	case http.MethodPatch:
		req, ok := decodeJSON[chatEndpointRequest](w, r)
		if !ok {
			return
		}
		if !validateChatEndpointRequest(w, req) {
			return
		}
		apiKey := ""
		existing, err := h.chatEndpoints.GetChatEndpoint(r.Context())
		if err == nil {
			apiKey = existing.APIKey
		} else if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		apiKey = h.resolveUpdatedAPIKey(apiKey, req.APIKey, req.ClearAPIKey)
		e := domain.ChatEndpoint{
			BaseURL: req.BaseURL, APIKey: apiKey, Model: req.Model, Enabled: req.Enabled,
			MaxContextTokens: req.MaxContextTokens,
			WebSearchEnabled: req.WebSearchEnabled, WebSearchBaseURL: req.WebSearchBaseURL,
			WebSearchResultCount: req.WebSearchResultCount,
			SystemPrompt:         req.SystemPrompt,
			DefaultAgentID:       req.DefaultAgentID,
		}
		if e.MaxContextTokens <= 0 {
			e.MaxContextTokens = h.autoDetectMaxContextTokens(r.Context(), e)
		}
		e.UpdatedAt = time.Now().UTC()
		if err := h.chatEndpoints.SetChatEndpoint(r.Context(), e); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toChatEndpointResponse(e))
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) currentSettings() settingsResponse {
	alpha, k1, b := h.settings.Get()
	return settingsResponse{
		Tuning:      tuningValues{Alpha: alpha, K1: k1, B: b, PageRankWeight: h.settings.PageRankWeight()},
		Operational: toOperationalValues(h.opSettings.Get()),
	}
}

func (h *Handler) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.settings != nil, "tuning") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.currentSettings())
	case http.MethodPost:
		req, ok := decodeJSON[settingsResponse](w, r)
		if !ok {
			return
		}
		h.settings.Set(req.Tuning.Alpha, req.Tuning.K1, req.Tuning.B)
		h.settings.SetPageRankWeight(req.Tuning.PageRankWeight)
		v := req.Operational.toSettingsValues()
		v.EmbeddingSearchWeights = domain.ReconcileSearchWeights(v.EmbeddingSearchWeights, v.EmbeddingHashEnabled, h.currentEmbeddingEndpoints(r.Context()))
		h.opSettings.Set(v)
		h.persistSetting(r.Context(), ports.SettingsKeyTuning, h.settings.Values())
		h.persistSetting(r.Context(), ports.SettingsKeyOperational, h.opSettings.Get())
		writeJSON(w, http.StatusOK, h.currentSettings())
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// overridesValues mirrors domain.RankingOverridesValues for the wire
// format, keeping JSON tags out of the domain type.
type overridesValues struct {
	BlockedTerms   []string           `json:"blocked_terms"`
	BoostedTerms   map[string]float64 `json:"boosted_terms"`
	BlockedDomains []string           `json:"blocked_domains"`
	BoostedDomains map[string]float64 `json:"boosted_domains"`
}

func toOverridesValues(v domain.RankingOverridesValues) overridesValues {
	return overridesValues{
		BlockedTerms:   v.BlockedTerms,
		BoostedTerms:   v.BoostedTerms,
		BlockedDomains: v.BlockedDomains,
		BoostedDomains: v.BoostedDomains,
	}
}

func (o overridesValues) toSettingsValues() domain.RankingOverridesValues {
	return domain.RankingOverridesValues{
		BlockedTerms:   o.BlockedTerms,
		BoostedTerms:   o.BoostedTerms,
		BlockedDomains: o.BlockedDomains,
		BoostedDomains: o.BoostedDomains,
	}
}

func (h *Handler) currentOverrides() overridesValues {
	return toOverridesValues(h.overrides.Get())
}

func (h *Handler) handleAdminOverrides(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.overrides != nil, "ranking overrides") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.currentOverrides())
	case http.MethodPost:
		req, ok := decodeJSON[overridesValues](w, r)
		if !ok {
			return
		}
		h.overrides.Set(req.toSettingsValues())
		h.persistSetting(r.Context(), ports.SettingsKeyOverrides, h.overrides.Get())
		writeJSON(w, http.StatusOK, h.currentOverrides())
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// handleAdminCrawlJobs serves the Jobs collection: GET lists every job,
// DELETE clears ended ones. Both live here rather than a separate
// sub-path, to avoid colliding with the "{id}" wildcard route below.
func (h *Handler) handleAdminCrawlJobs(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, configNameCrawlJobs) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := h.jobs.ListCrawlJobs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	case http.MethodDelete:
		n, err := h.jobs.DeleteEndedCrawlJobs(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"removed": n})
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminCrawlJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, configNameCrawlJobs) {
		return
	}
	job, err := h.jobs.GetCrawlJob(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrCrawlJobNotFound, "crawl job not found", job)
}

func (h *Handler) handleAdminCancelCrawlJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, configNameCrawlJobs) {
		return
	}
	err := h.jobs.CancelCrawlJob(r.Context(), r.PathValue("id"))
	if errors.Is(err, ports.ErrCrawlJobNotFound) {
		http.Error(w, "crawl job not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, ports.ErrCrawlJobNotRunning) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// scheduledCrawlRequest is the wire shape for creating/replacing a crawl
// -- IntervalMinutes 0 means "run once," positive means "repeat" -- see toScheduledCrawl.
type scheduledCrawlRequest struct {
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	RespectRobots bool     `json:"respect_robots"`
	UserAgent     string   `json:"user_agent"`
	Cookie        string   `json:"cookie"`
	BasicAuthUser string   `json:"basic_auth_user"`
	BasicAuthPass string   `json:"basic_auth_pass"`
	// ClearCookie/ClearBasicAuth are meaningful only to the PATCH handler:
	// GET never echoes a stored credential, so blank means "unchanged,"
	// these flags mean "remove it." Ignored by POST, which has no prior credential.
	ClearCookie         bool `json:"clear_cookie"`
	ClearBasicAuth      bool `json:"clear_basic_auth"`
	UseSitemap          bool `json:"use_sitemap"`
	FetchTimeoutSeconds int  `json:"fetch_timeout_seconds"`
	MinTextLength       int  `json:"min_text_length"`
	CrawlDelayMs        int  `json:"crawl_delay_ms"`
	MaxResponseKB       int  `json:"max_response_kb"`
	PrioritizeUnindexed bool `json:"prioritize_unindexed"`
	IntervalMinutes     int  `json:"interval_minutes"`
	// LinkScope overrides the Tuning page's global default for how far
	// this crawl follows links -- "" means inherit the default;
	// "host"/"domain"/"any" choose explicitly.
	LinkScope string `json:"link_scope"`
	// AllowedDomains/BlockedDomains/FollowIndexedDomains mirror
	// ports.CrawlOptions' same-named fields -- see its doc comment for the
	// allow/block precedence against LinkScope.
	AllowedDomains       []string `json:"allowed_domains"`
	BlockedDomains       []string `json:"blocked_domains"`
	FollowIndexedDomains bool     `json:"follow_indexed_domains"`
	// MaxRuns caps how many times a recurring crawl repeats before
	// disabling itself; 0 means unlimited. Meaningless for a one-off crawl.
	MaxRuns int `json:"max_runs"`
	// Renderer overrides the Tuning page's global default rendering mode
	// for this crawl alone -- "" means inherit the default.
	Renderer string `json:"renderer"`
	Enabled  bool   `json:"enabled"`
}

type scheduledCrawlResponse struct {
	ID            string   `json:"id"`
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	RespectRobots bool     `json:"respect_robots"`
	UserAgent     string   `json:"user_agent"`
	// HasCookie/HasBasicAuth report only whether a credential is set, never
	// its value -- same redaction ports.CrawlJobRequest gives a one-off
	// crawl's credentials, so a scheduled crawl's never round-trips through GET.
	HasCookie            bool       `json:"has_cookie"`
	HasBasicAuth         bool       `json:"has_basic_auth"`
	LinkScope            string     `json:"link_scope"`
	AllowedDomains       []string   `json:"allowed_domains"`
	BlockedDomains       []string   `json:"blocked_domains"`
	FollowIndexedDomains bool       `json:"follow_indexed_domains"`
	UseSitemap           bool       `json:"use_sitemap"`
	FetchTimeoutSeconds  int        `json:"fetch_timeout_seconds"`
	MinTextLength        int        `json:"min_text_length"`
	CrawlDelayMs         int        `json:"crawl_delay_ms"`
	MaxResponseKB        int        `json:"max_response_kb"`
	PrioritizeUnindexed  bool       `json:"prioritize_unindexed"`
	Recurring            bool       `json:"recurring"`
	IntervalMinutes      int        `json:"interval_minutes"`
	MaxRuns              int        `json:"max_runs"`
	RunCount             int        `json:"run_count"`
	Renderer             string     `json:"renderer"`
	Enabled              bool       `json:"enabled"`
	LastRunAt            *time.Time `json:"last_run_at,omitempty"`
	NextRunAt            time.Time  `json:"next_run_at"`
	CreatedAt            time.Time  `json:"created_at"`
}

func toScheduledCrawlResponse(s domain.ScheduledCrawl) scheduledCrawlResponse {
	return scheduledCrawlResponse{
		ID: s.ID, SeedURLs: s.SeedURLs, MaxPages: s.MaxPages,
		RespectRobots: s.RespectRobots, UserAgent: s.UserAgent,
		HasCookie: s.Cookie != "", HasBasicAuth: s.BasicAuthUser != "" || s.BasicAuthPass != "",
		LinkScope: s.LinkScope, UseSitemap: s.UseSitemap,
		AllowedDomains: s.AllowedDomains, BlockedDomains: s.BlockedDomains,
		FollowIndexedDomains: s.FollowIndexedDomains,
		FetchTimeoutSeconds:  s.FetchTimeoutSeconds, MinTextLength: s.MinTextLength,
		CrawlDelayMs: s.CrawlDelayMs, MaxResponseKB: s.MaxResponseKB,
		PrioritizeUnindexed: s.PrioritizeUnindexed, Recurring: s.Recurring,
		IntervalMinutes: s.IntervalMinutes, MaxRuns: s.MaxRuns, RunCount: s.RunCount,
		Renderer: s.Renderer, Enabled: s.Enabled,
		LastRunAt: s.LastRunAt, NextRunAt: s.NextRunAt, CreatedAt: s.CreatedAt,
	}
}

// toScheduledCrawl builds the domain.ScheduledCrawl req describes, shared
// by POST and PATCH. Recurring is derived from IntervalMinutes; a
// recurring entry's first run is interval_minutes from now, a one-off's is now.
func (req scheduledCrawlRequest) toScheduledCrawl(id string, enabled bool, now time.Time) domain.ScheduledCrawl {
	recurring := req.IntervalMinutes > 0
	next := now
	if recurring {
		next = now.Add(time.Duration(req.IntervalMinutes) * time.Minute)
	}
	return domain.ScheduledCrawl{
		ID: id, SeedURLs: req.SeedURLs, MaxPages: req.MaxPages,
		RespectRobots: req.RespectRobots, UserAgent: req.UserAgent,
		Cookie: req.Cookie, BasicAuthUser: req.BasicAuthUser, BasicAuthPass: req.BasicAuthPass,
		LinkScope: req.LinkScope, UseSitemap: req.UseSitemap,
		AllowedDomains: req.AllowedDomains, BlockedDomains: req.BlockedDomains,
		FollowIndexedDomains: req.FollowIndexedDomains,
		FetchTimeoutSeconds:  req.FetchTimeoutSeconds, MinTextLength: req.MinTextLength,
		CrawlDelayMs: req.CrawlDelayMs, MaxResponseKB: req.MaxResponseKB,
		PrioritizeUnindexed: req.PrioritizeUnindexed, Recurring: recurring,
		IntervalMinutes: req.IntervalMinutes, MaxRuns: req.MaxRuns,
		Renderer: req.Renderer, Enabled: enabled,
		NextRunAt: next,
		CreatedAt: now,
	}
}

func validateScheduledCrawlRequest(w http.ResponseWriter, req scheduledCrawlRequest) bool {
	if len(req.SeedURLs) == 0 {
		http.Error(w, "seed_urls must not be empty", http.StatusBadRequest)
		return false
	}
	if req.IntervalMinutes < 0 {
		http.Error(w, "interval_minutes must not be negative", http.StatusBadRequest)
		return false
	}
	if req.MaxRuns < 0 {
		http.Error(w, "max_runs must not be negative", http.StatusBadRequest)
		return false
	}
	if !domain.ValidRenderer(req.Renderer) {
		http.Error(w, "renderer must be one of: (blank), none, chromium, firefox", http.StatusBadRequest)
		return false
	}
	if !domain.ValidLinkScope(req.LinkScope) {
		http.Error(w, "link_scope must be one of: (blank), host, domain, tld, any", http.StatusBadRequest)
		return false
	}
	return true
}

// handleAdminSchedules lists (GET) or creates (POST) crawls -- a freshly
// created entry is always enabled. POST enforces at most one schedule per
// domain: a submission matching an existing seed host replaces it in place.
func (h *Handler) handleAdminSchedules(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		schedules, err := h.scheduledCrawls.ListScheduledCrawls(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(schedules, toScheduledCrawlResponse))
	case http.MethodPost:
		req, ok := decodeJSON[scheduledCrawlRequest](w, r)
		if !ok {
			return
		}
		if !validateScheduledCrawlRequest(w, req) {
			return
		}
		existingID, err := h.scheduledCrawlIDForSameDomain(r.Context(), req.SeedURLs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if existingID != "" {
			s := req.toScheduledCrawl(existingID, true, time.Now().UTC())
			if err := h.scheduledCrawls.UpdateScheduledCrawl(r.Context(), s); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, toScheduledCrawlResponse(s))
			return
		}
		s := req.toScheduledCrawl(domain.NewScheduledCrawlID(), true, time.Now().UTC())
		if err := h.scheduledCrawls.CreateScheduledCrawl(r.Context(), s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toScheduledCrawlResponse(s))
	default:
		http.Error(w, msgMethodNotAllowed, http.StatusMethodNotAllowed)
	}
}

// scheduledCrawlIDForSameDomain returns the ID of a schedule already
// sharing seedURLs' first host, or "" if none -- keeps "one schedule per domain" true.
func (h *Handler) scheduledCrawlIDForSameDomain(ctx context.Context, seedURLs []string) (string, error) {
	if len(seedURLs) == 0 {
		return "", nil
	}
	host := domain.HostOf(seedURLs[0])
	if host == "" {
		return "", nil
	}
	existing, err := h.scheduledCrawls.ListScheduledCrawls(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range existing {
		if len(s.SeedURLs) > 0 && domain.HostOf(s.SeedURLs[0]) == host {
			return s.ID, nil
		}
	}
	return "", nil
}

// handleAdminUpdateSchedule replaces a schedule's editable fields and
// reschedules it. Cookie/BasicAuthUser/BasicAuthPass are the exception to
// "PATCH is a full replace" -- GET never echoes them, so blank means unchanged.
func (h *Handler) handleAdminUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	req, ok := decodeJSON[scheduledCrawlRequest](w, r)
	if !ok {
		return
	}
	if !validateScheduledCrawlRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	existing, err := h.scheduledCrawls.GetScheduledCrawl(r.Context(), id)
	if err != nil {
		respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, nil)
		return
	}
	s := req.toScheduledCrawl(id, req.Enabled, time.Now().UTC())
	if req.Cookie == "" && !req.ClearCookie {
		s.Cookie = existing.Cookie
	}
	if req.BasicAuthUser == "" && req.BasicAuthPass == "" && !req.ClearBasicAuth {
		s.BasicAuthUser = existing.BasicAuthUser
		s.BasicAuthPass = existing.BasicAuthPass
	}
	err = h.scheduledCrawls.UpdateScheduledCrawl(r.Context(), s)
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, map[string]bool{"ok": true})
}

func (h *Handler) handleAdminDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	err := h.scheduledCrawls.DeleteScheduledCrawl(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, map[string]bool{"ok": true})
}

// handleAdminGetSchedule backs the schedule-detail/edit subpage's initial
// load -- one schedule's full options, same shape ListScheduledCrawls gives.
func (h *Handler) handleAdminGetSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	s, err := h.scheduledCrawls.GetScheduledCrawl(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, toScheduledCrawlResponse(s))
}

// handleAdminRunScheduleNow marks a schedule due immediately --
// crawl-server's ticker picks it up next tick, same path a fresh one-off
// crawl goes through, so this handler never talks to crawl-server directly.
func (h *Handler) handleAdminRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	err := h.scheduledCrawls.RunScheduledCrawlNow(r.Context(), r.PathValue("id"), time.Now().UTC())
	if errors.Is(err, ports.ErrScheduledCrawlInProgress) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, map[string]bool{"ok": true})
}

// handleAdminToggleSchedule flips only Enabled -- unlike PATCH, it never
// touches NextRunAt, so pausing/resuming doesn't reschedule or reorder the list.
func (h *Handler) handleAdminToggleSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, configNameScheduledCrawls) {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	err := h.scheduledCrawls.SetScheduledCrawlEnabled(r.Context(), r.PathValue("id"), req.Enabled)
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, msgScheduledCrawlNotFound, map[string]bool{"ok": true})
}

func (h *Handler) handleAdminSchedulePage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminScheduleHTML)
}

func (h *Handler) handleAdminPageRankPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminPageRankHTML)
}

func (h *Handler) handleAdminEmbeddingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminEmbeddingsHTML)
}

type adminPageRankResponse struct {
	TotalDocs   int     `json:"total_docs"`
	MinPageRank float64 `json:"min_pagerank"`
	MaxPageRank float64 `json:"max_pagerank"`
	AvgPageRank float64 `json:"avg_pagerank"`
	// Damping/MaxIterations/Epsilon are domain.PageRank's fixed algorithm
	// constants -- not configurable, shown for context alongside their output.
	Damping                  float64 `json:"damping"`
	MaxIterations            int     `json:"max_iterations"`
	Epsilon                  float64 `json:"epsilon"`
	PageRankWeight           float64 `json:"pagerank_weight"`
	RecomputeIntervalMinutes int     `json:"recompute_interval_minutes"`
	// RecomputeInProgress/LastRecomputedAt/LastRecomputeDocuments/
	// LastRecomputeIterations/LastRecomputeFinalDelta reflect
	// domain.PageRankStatus, persisted by any process that ran a recompute
	// -- real cross-process state, not just this browser tab's memory.
	RecomputeInProgress bool       `json:"recompute_in_progress"`
	LastRecomputedAt    *time.Time `json:"last_recomputed_at,omitempty"`
	// No omitempty on these three: a run over an empty link graph
	// legitimately scores 0 documents in 0 iterations, and omitempty would
	// drop that real value the same way a missing one would.
	LastRecomputeDocuments  int     `json:"last_recompute_documents"`
	LastRecomputeIterations int     `json:"last_recompute_iterations"`
	LastRecomputeFinalDelta float64 `json:"last_recompute_final_delta"`
	LastRecomputeDurationMs int64   `json:"last_recompute_duration_ms"`
}

func (h *Handler) handleAdminPageRank(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	totalDocs, _, err := h.admin.CorpusStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	minPR, maxPR, avg, err := h.admin.PageRankDistribution(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := adminPageRankResponse{
		TotalDocs:                totalDocs,
		MinPageRank:              minPR,
		MaxPageRank:              maxPR,
		AvgPageRank:              avg,
		Damping:                  domain.PageRankDamping,
		MaxIterations:            domain.PageRankMaxIterations,
		Epsilon:                  domain.PageRankEpsilon,
		RecomputeIntervalMinutes: h.opSettings.Get().PageRankRecomputeIntervalMinutes,
	}
	if h.settings != nil {
		resp.PageRankWeight = h.settings.PageRankWeight()
	}
	status := application.LoadPageRankStatus(r.Context(), h.settingsStore)
	resp.RecomputeInProgress = status.InProgress
	if !status.LastRunAt.IsZero() {
		lastRunAt := status.LastRunAt
		resp.LastRecomputedAt = &lastRunAt
		resp.LastRecomputeDocuments = status.Documents
		resp.LastRecomputeIterations = status.Iterations
		resp.LastRecomputeFinalDelta = status.FinalDelta
		resp.LastRecomputeDurationMs = status.DurationMs
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminPageRankRecomputeResponse struct {
	Documents   int     `json:"documents"`
	Iterations  int     `json:"iterations"`
	FinalDelta  float64 `json:"final_delta"`
	DurationMS  int64   `json:"duration_ms"`
	MinPageRank float64 `json:"min_pagerank"`
	MaxPageRank float64 `json:"max_pagerank"`
	AvgPageRank float64 `json:"avg_pagerank"`
}

// handleAdminPageRankRecompute runs a full recompute synchronously
// (unlike bulk delete's fire-and-forget) and reports documents scored,
// iterations, final delta, duration -- the admin is waiting for this result.
func (h *Handler) handleAdminPageRankRecompute(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.pageRank != nil, "pagerank") {
		return
	}
	result, err := application.RunPageRankJobWithStatus(r.Context(), h.pageRank, h.settingsStore)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := adminPageRankRecomputeResponse{
		Documents:  result.Documents,
		Iterations: result.Iterations,
		FinalDelta: result.FinalDelta,
		DurationMS: result.DurationMs,
	}
	if h.admin != nil {
		if minPR, maxPR, avg, err := h.admin.PageRankDistribution(r.Context()); err == nil {
			resp.MinPageRank, resp.MaxPageRank, resp.AvgPageRank = minPR, maxPR, avg
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminEmbeddingRecomputeStatusResponse struct {
	TotalDocs  int  `json:"total_docs"`
	InProgress bool `json:"in_progress"`
	// Documents/Failed always reflect the persisted cross-process status,
	// live while InProgress is true (checkpointed once per batch -- see
	// RunEmbeddingRecomputeJob's onBatchDone) and final once it settles --
	// so a poller sees real, moving counts during a run, not just 0 until
	// it finishes. No omitempty: 0 is a legitimate result (empty corpus).
	Documents int `json:"documents"`
	Failed    int `json:"failed"`
	// LastRunAt/DurationMs describe the most recently COMPLETED run only --
	// nil/0 while a run is still in progress or none has ever finished.
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	DurationMs int64      `json:"duration_ms"`
}

func (h *Handler) handleAdminEmbeddingsRecomputeStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.embeddingRepo != nil && len(h.embedders) > 0, "embedding recompute") {
		return
	}
	resp := adminEmbeddingRecomputeStatusResponse{}
	if h.admin != nil {
		if totalDocs, _, err := h.admin.CorpusStats(r.Context()); err == nil {
			resp.TotalDocs = totalDocs
		}
	}
	status := application.LoadEmbeddingRecomputeStatus(r.Context(), h.settingsStore)
	resp.InProgress = status.InProgress
	resp.Documents = status.Documents
	resp.Failed = status.Failed
	if !status.LastRunAt.IsZero() {
		lastRunAt := status.LastRunAt
		resp.LastRunAt = &lastRunAt
		resp.DurationMs = status.DurationMs
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminEmbeddingsRecomputeStart kicks off a full-corpus recompute
// in the background -- unlike PageRank's synchronous one, this is a real
// per-document network round trip. Rejects a second trigger while running (409).
func (h *Handler) handleAdminEmbeddingsRecomputeStart(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.embeddingRepo != nil && len(h.embedders) > 0, "embedding recompute") {
		return
	}
	if application.LoadEmbeddingRecomputeStatus(r.Context(), h.settingsStore).InProgress {
		http.Error(w, "an embedding recompute is already in progress", http.StatusConflict)
		return
	}
	titleWeight := h.opSettings.Get().EmbeddingTitleWeight
	concurrency := func() int { return h.opSettings.Get().EmbeddingRecomputeConcurrency }
	go func() {
		ctx := context.Background()
		if _, err := application.RunEmbeddingRecomputeJobWithStatus(ctx, h.embeddingRepo, h.embedders, h.settingsStore, titleWeight, "", concurrency); err != nil {
			log.Printf("recomputing embeddings: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

type adminContentDedupStatusResponse struct {
	InProgress bool `json:"in_progress"`
	// LastRunAt/GroupsFound/DocumentsMerged/DurationMs reflect the
	// persisted cross-process status, not just this tab's trigger.
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	GroupsFound     int        `json:"groups_found"`
	DocumentsMerged int        `json:"documents_merged"`
	DurationMs      int64      `json:"duration_ms"`
}

func (h *Handler) handleAdminContentDedupStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.contentDedupRepo != nil, configNameContentDedup) {
		return
	}
	resp := adminContentDedupStatusResponse{}
	status := application.LoadContentDedupStatus(r.Context(), h.settingsStore)
	resp.InProgress = status.InProgress
	if !status.LastRunAt.IsZero() {
		lastRunAt := status.LastRunAt
		resp.LastRunAt = &lastRunAt
		resp.GroupsFound = status.GroupsFound
		resp.DocumentsMerged = status.DocumentsMerged
		resp.DurationMs = status.DurationMs
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminContentDedupRecomputeStart kicks off a full-corpus dedup
// pass in the background, mirroring the embeddings recompute's
// fire-and-forget shape. Rejects a second trigger while running (409):
// real DB contention otherwise.
func (h *Handler) handleAdminContentDedupRecomputeStart(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.contentDedupRepo != nil, configNameContentDedup) {
		return
	}
	if application.LoadContentDedupStatus(r.Context(), h.settingsStore).InProgress {
		http.Error(w, "a content dedup recompute is already in progress", http.StatusConflict)
		return
	}
	v := h.opSettings.Get()
	go func() {
		ctx := context.Background()
		_, err := application.RunContentDedupJobWithStatus(ctx, h.contentDedupRepo, h.settingsStore, v.ContentDedupMethod, v.ContentDedupSimHashMaxDistance)
		switch {
		case err == nil:
		case errors.Is(err, ports.ErrContentDedupAlreadyRunning):
			// The fast-path check above already rejected the common case --
			// this is the rarer race where cmd/crawl's scheduler won between
			// that check and this goroutine starting. Expected, not an error.
		default:
			log.Printf("recomputing content dedup: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// defaultAliasGroupsPageSize is the content-dedup alias-groups page's
// default items-per-page -- same conventions as defaultVocabularyPageSize.
const defaultAliasGroupsPageSize = 20

// adminDocumentAlias mirrors domain.DocumentAlias for the wire format --
// reason is included so the admin UI can tell an actual dedup merge apart
// from ordinary canonical_tag bookkeeping.
type adminDocumentAlias struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

type adminDocumentAliasGroup struct {
	CanonicalID  string               `json:"canonical_id"`
	CanonicalURL string               `json:"canonical_url"`
	Aliases      []adminDocumentAlias `json:"aliases"`
}

type adminAliasGroupsResponse struct {
	Total  int                       `json:"total"`
	Groups []adminDocumentAliasGroup `json:"groups"`
}

// handleAdminContentDedupAliasGroups lists every canonical document with
// at least one alias, from any reason -- the "what's actually aliased"
// transparency a black-box merge count can't provide, letting an admin
// verify a dedup pass before trusting it.
func (h *Handler) handleAdminContentDedupAliasGroups(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameContentDedup) {
		return
	}
	limit := intQueryParam(r, "limit", defaultAliasGroupsPageSize, true)
	offset := intQueryParam(r, "offset", 0, false)
	groups, total, err := h.admin.ListDocumentAliasGroups(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := mapSlice(groups, func(g domain.DocumentAliasGroup) adminDocumentAliasGroup {
		aliases := mapSlice(g.Aliases, func(a domain.DocumentAlias) adminDocumentAlias {
			return adminDocumentAlias{URL: a.URL, Reason: a.Reason}
		})
		return adminDocumentAliasGroup{CanonicalID: g.CanonicalID, CanonicalURL: g.CanonicalURL, Aliases: aliases}
	})
	writeJSON(w, http.StatusOK, adminAliasGroupsResponse{Total: total, Groups: out})
}

func (h *Handler) handleAdminContentDedupPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminContentDedupHTML)
}

func (h *Handler) handleAdminDatabasePage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminDatabaseHTML)
}

// adminDBPoolStats is sql.DBStats' wire shape, with its time.Duration
// field converted to milliseconds -- JSON has no native duration type.
type adminDBPoolStats struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationMS     int64 `json:"wait_duration_ms"`
	MaxIdleClosed      int64 `json:"max_idle_closed"`
	MaxIdleTimeClosed  int64 `json:"max_idle_time_closed"`
	MaxLifetimeClosed  int64 `json:"max_lifetime_closed"`
}

type adminDatabaseResponse struct {
	Driver    string           `json:"driver"`
	Pool      adminDBPoolStats `json:"pool"`
	TableRows map[string]int64 `json:"table_rows"`
}

// toAdminDBPoolStats maps sql.DBStats to its wire shape -- shared by
// handleAdminDatabase and handleAdminOverviewMetrics.
func toAdminDBPoolStats(stats sql.DBStats) adminDBPoolStats {
	return adminDBPoolStats{
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections,
		InUse:              stats.InUse,
		Idle:               stats.Idle,
		WaitCount:          stats.WaitCount,
		WaitDurationMS:     stats.WaitDuration.Milliseconds(),
		MaxIdleClosed:      stats.MaxIdleClosed,
		MaxIdleTimeClosed:  stats.MaxIdleTimeClosed,
		MaxLifetimeClosed:  stats.MaxLifetimeClosed,
	}
}

func (h *Handler) handleAdminDatabase(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	counts, err := h.admin.TableRowCounts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, adminDatabaseResponse{
		Driver:    h.dbDriver,
		Pool:      toAdminDBPoolStats(h.admin.PoolStats()),
		TableRows: counts,
	})
}

// handleAdminClearContent permanently deletes every crawled document and
// crawl job -- see ports.AdminRepository.ClearContent. Every settings
// table is left untouched.
func (h *Handler) handleAdminClearContent(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	if err := h.admin.ClearContent(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// defaultTuningAlpha mirrors the literal both main.go bootstraps use --
// k1/b already have named domain.DefaultBM25K1/B constants, alpha never got one.
const defaultTuningAlpha = 0.5

// handleAdminClearSettings permanently deletes every settings row -- see
// ports.AdminRepository.ClearSettings. This process's in-memory settings
// reset immediately; other processes only pick it up on restart, since
// bootstrap.SyncSettings can't tell "cleared on purpose" from a transient
// read error.
func (h *Handler) handleAdminClearSettings(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	if err := h.admin.ClearSettings(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if h.settings != nil {
		h.settings.Set(defaultTuningAlpha, domain.DefaultBM25K1, domain.DefaultBM25B)
		h.settings.SetPageRankWeight(0)
	}
	if h.opSettings != nil {
		h.opSettings.Set(domain.DefaultOperationalSettings().Get())
	}
	if h.overrides != nil {
		h.overrides.Set(domain.RankingOverridesValues{})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// Lookback windows for the Overview page's tier-2 panels: the
// crawl-outcome donut and documents-indexed trend look back 30 days; the
// higher-volume crawl_job_pages panels use 14 to keep per-day charts readable.
const (
	overviewJobOutcomeDays     = 30
	overviewDocumentsTrendDays = 30
	overviewThroughputDays     = 14
	overviewFetchDurationDays  = 14
)

type adminCrawlJobOutcome struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type adminDailyFetchOutcome struct {
	Date     string         `json:"date"`
	Outcomes map[string]int `json:"outcomes"`
}

type adminDailyCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type adminDailyDuration struct {
	Date          string  `json:"date"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
}

// adminOverviewRunningJob is the inline-visible detail for one running
// crawl job on the Overview page -- just enough to summarize it, not the
// full CrawlJobSummary.
type adminOverviewRunningJob struct {
	ID           string   `json:"id"`
	SeedURLs     []string `json:"seed_urls"`
	PagesCrawled int      `json:"pages_crawled"`
}

type adminOverviewMetrics struct {
	// Tier 1: pure aggregation over data already returned by ListCrawlJobs/
	// ListScheduledCrawls/PoolStats -- see handleAdminOverviewMetrics.
	RunningCrawlJobs    int                       `json:"running_crawl_jobs"`
	QueuedCrawlJobs     int                       `json:"queued_crawl_jobs"`
	RunningJobs         []adminOverviewRunningJob `json:"running_jobs"`
	SchedulesEnabled    int                       `json:"schedules_enabled"`
	SchedulesDisabled   int                       `json:"schedules_disabled"`
	SchedulesInProgress int                       `json:"schedules_in_progress"`
	SchedulesOverdue    int                       `json:"schedules_overdue"`
	Pool                adminDBPoolStats          `json:"pool"`

	// Tier 2: each backed by one new AdminRepository aggregate query.
	JobOutcomes        []adminCrawlJobOutcome   `json:"job_outcomes"`
	DailyFetchOutcomes []adminDailyFetchOutcome `json:"daily_fetch_outcomes"`
	DocumentsByDay     []adminDailyCount        `json:"documents_by_day"`
	FetchDurationByDay []adminDailyDuration     `json:"fetch_duration_by_day"`
	PageRankBuckets    []adminAgeBucket         `json:"pagerank_buckets"`
	// PageRankOrphanThreshold documents the fixed cutoff
	// PageRankOrphanCount/Percent were computed against, so the client can
	// label the stat tile without hardcoding the number.
	PageRankOrphanThreshold float64 `json:"pagerank_orphan_threshold"`
	PageRankOrphanCount     int     `json:"pagerank_orphan_count"`
	PageRankOrphanPercent   float64 `json:"pagerank_orphan_percent"`
	PageRankTotalDocs       int     `json:"pagerank_total_docs"`
}

// handleAdminOverviewMetrics backs the Overview page's operational panels
// beyond handleAdminDocumentsOverview: crawl/schedule health, DB pool
// stats, tier-2 trends. Only h.admin required; h.jobs/h.scheduledCrawls
// degrade to zero-valued fields if unset.
func (h *Handler) handleAdminOverviewMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, configNameAdminDiagnostics) {
		return
	}
	ctx := r.Context()
	resp := adminOverviewMetrics{PageRankOrphanThreshold: domain.PageRankOrphanThreshold}

	if h.jobs != nil {
		jobs, err := h.jobs.ListCrawlJobs(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.RunningCrawlJobs, resp.QueuedCrawlJobs, resp.RunningJobs = summarizeOverviewCrawlJobs(jobs)
	}

	if h.scheduledCrawls != nil {
		schedules, err := h.scheduledCrawls.ListScheduledCrawls(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.SchedulesEnabled, resp.SchedulesDisabled, resp.SchedulesInProgress, resp.SchedulesOverdue =
			summarizeOverviewSchedules(schedules, time.Now().UTC())
	}

	resp.Pool = toAdminDBPoolStats(h.admin.PoolStats())

	now := time.Now().UTC()
	outcomes, err := h.admin.CrawlJobOutcomes(ctx, now.AddDate(0, 0, -overviewJobOutcomeDays))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, o := range outcomes {
		resp.JobOutcomes = append(resp.JobOutcomes, adminCrawlJobOutcome{Status: string(o.Status), Count: o.Count})
	}

	dailyOutcomes, err := h.admin.DailyFetchOutcomes(ctx, now.AddDate(0, 0, -overviewThroughputDays))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp.DailyFetchOutcomes = groupDailyFetchOutcomes(dailyOutcomes)

	docsByDay, err := h.admin.DocumentsIndexedByDay(ctx, now.AddDate(0, 0, -overviewDocumentsTrendDays))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, d := range docsByDay {
		resp.DocumentsByDay = append(resp.DocumentsByDay, adminDailyCount{Date: d.Date, Count: d.Count})
	}

	durationByDay, err := h.admin.DailyFetchDuration(ctx, now.AddDate(0, 0, -overviewFetchDurationDays))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, d := range durationByDay {
		resp.FetchDurationByDay = append(resp.FetchDurationByDay, adminDailyDuration{Date: d.Date, AvgDurationMs: d.AvgDurationMs})
	}

	buckets, orphanCount, totalDocs, err := h.admin.PageRankHistogram(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, b := range buckets {
		resp.PageRankBuckets = append(resp.PageRankBuckets, adminAgeBucket{Label: b.Label, Count: b.Count})
	}
	resp.PageRankOrphanCount = orphanCount
	resp.PageRankTotalDocs = totalDocs
	if totalDocs > 0 {
		resp.PageRankOrphanPercent = float64(orphanCount) / float64(totalDocs) * 100
	}

	writeJSON(w, http.StatusOK, resp)
}

// summarizeOverviewCrawlJobs computes the running/queued job counts and
// running-job detail list from a full job list -- split out purely to
// keep handleAdminOverviewMetrics' complexity down; behavior unchanged.
func summarizeOverviewCrawlJobs(jobs []domain.CrawlJobSummary) (running, queued int, runningJobs []adminOverviewRunningJob) {
	for _, j := range jobs {
		switch j.Status {
		case domain.CrawlJobRunning:
			running++
			runningJobs = append(runningJobs, adminOverviewRunningJob{
				ID: j.ID, SeedURLs: j.Request.SeedURLs, PagesCrawled: j.PagesCrawled,
			})
		case domain.CrawlJobQueued:
			queued++
		}
	}
	return running, queued, runningJobs
}

// summarizeOverviewSchedules computes the enabled/disabled/in-progress/
// overdue schedule counts from a full list -- split out for the same
// reason as summarizeOverviewCrawlJobs.
func summarizeOverviewSchedules(schedules []domain.ScheduledCrawl, now time.Time) (enabled, disabled, inProgress, overdue int) {
	for _, s := range schedules {
		if s.Enabled {
			enabled++
		} else {
			disabled++
		}
		if s.InProgress {
			inProgress++
		}
		if s.Enabled && s.NextRunAt.Before(now) {
			overdue++
		}
	}
	return enabled, disabled, inProgress, overdue
}

// groupDailyFetchOutcomes regroups sqlrepo's flat (day, status, count)
// rows into one entry per day with every outcome nested -- the shape the
// stacked-bar chart wants. Rows arrive ordered by day, so one pass suffices.
func groupDailyFetchOutcomes(rows []domain.DailyFetchOutcome) []adminDailyFetchOutcome {
	var out []adminDailyFetchOutcome
	for _, row := range rows {
		if len(out) == 0 || out[len(out)-1].Date != row.Date {
			out = append(out, adminDailyFetchOutcome{Date: row.Date, Outcomes: map[string]int{}})
		}
		out[len(out)-1].Outcomes[string(row.Status)] = row.Count
	}
	return out
}
