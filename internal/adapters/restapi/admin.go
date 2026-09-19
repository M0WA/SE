package restapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const defaultDocumentListLimit = 100

// requireMethod writes 405 and reports false if the request method isn't
// the one this endpoint accepts.
func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

// respondOrNotFound writes okPayload as a 200 JSON response if err is nil,
// a 404 with notFoundMsg if err is notFound, or err's own message as a 500
// otherwise -- the "success / not-found / other error" 3-way response
// every admin endpoint backed by a store that can report a specific
// not-found sentinel needs.
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

// intQueryParam reads name from r's query string as an int, falling back
// to def if it's missing, non-numeric, or -- when positiveOnly is set --
// not greater than zero. Shared by every endpoint that accepts an optional
// ?limit=/?top_k=-style override with its own default.
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
	serveStatic(w, r, "text/html; charset=utf-8", adminHTML)
}

func (h *Handler) handleAdminDocumentsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminDocumentsHTML)
}

// handleAdminDomainPage serves the per-domain subpage template; the
// domain name in the path is read client-side (JS) to fetch that
// domain's documents, so the same static page works for every host.
func (h *Handler) handleAdminDomainPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminDomainHTML)
}

// handleAdminVocabularyTermPage serves the vocabulary term-detail subpage
// template; like handleAdminDomainPage, the term itself is read client-side
// from the page's own URL querystring, so the same static page works for
// every term.
func (h *Handler) handleAdminVocabularyTermPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminVocabularyTermHTML)
}

func (h *Handler) handleAdminSettingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminSettingsHTML)
}

func (h *Handler) handleAdminChatSettingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminChatSettingsHTML)
}

func (h *Handler) handleAdminChatHooksPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminChatHooksHTML)
}

func (h *Handler) handleAdminJobsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminJobsHTML)
}

func (h *Handler) handleAdminSearchPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminSearchHTML)
}

// handleAdminSearchResultPage serves the per-result score-breakdown
// subpage: one static template whose JS reads the query/doc ID from its
// own URL and re-runs /admin/api/search, since every score is normalized
// against that search's own candidate batch -- there's no way to
// reproduce it except by recomputing that batch.
func (h *Handler) handleAdminSearchResultPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminSearchResultHTML)
}

func (h *Handler) handleAdminCrawlPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", crawlHTML)
}

type adminStatsResponse struct {
	Driver    string  `json:"driver"`
	TotalDocs int     `json:"total_docs"`
	AvgDocLen float64 `json:"avg_doc_len"`
}

func (h *Handler) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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
	// equal to VocabularySize when search is empty -- what the page uses to
	// compute how many pages exist.
	MatchedCount int             `json:"matched_count"`
	Terms        []adminTermStat `json:"terms"`
}

// vocabularySortParam whitelists the sort/dir query params against the
// admin vocabulary page's actual sortable columns/directions, defaulting
// anything else exactly the way this endpoint always ordered before
// pagination/sorting existed (highest doc_freq first).
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
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	limit := intQueryParam(r, "limit", defaultVocabularyPageSize, true)
	offset := intQueryParam(r, "offset", 0, false)
	// Indexed terms are always lowercased at tokenize time (see
	// domain.Tokenize), so a mixed-case search would otherwise silently miss
	// every match.
	search := strings.ToLower(r.URL.Query().Get("search"))
	sortBy := vocabularySortParam(r, "sort", "doc_freq", "term", "doc_freq", "total_freq")
	sortDir := vocabularySortParam(r, "dir", "desc", "asc", "desc")
	vocabSize, matched, terms, err := h.admin.VocabularyStats(r.Context(), limit, offset, search, sortBy, sortDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminTermStat, len(terms))
	for i, t := range terms {
		out[i] = adminTermStat{Term: t.Term, DocFreq: t.DocFreq, TotalFreq: t.TotalFreq}
	}
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
// domain via ?domain= (used by the per-domain admin subpage) -- without
// that filter it's the whole corpus, still capped at limit.
func (h *Handler) handleAdminDocuments(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	limit := intQueryParam(r, "limit", defaultDocumentListLimit, true)
	docs, err := h.admin.ListDocuments(r.Context(), limit, r.URL.Query().Get("domain"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminDocument, len(docs))
	for i, d := range docs {
		out[i] = adminDocument{
			ID: d.ID, URL: d.URL, Host: d.Host, Title: d.Title,
			DocLength: d.DocLength, Version: d.Version, CrawledAt: d.CrawledAt,
			InternalLinks: d.InternalLinks, ExternalLinks: d.ExternalLinks, Backlinks: d.Backlinks,
			PageRank: d.PageRank,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// maxDeleteDomainDocs bounds how many of a domain's documents one bulk
// delete looks up and queues -- generous enough that no real domain hits
// it, but still a bound rather than an unbounded query.
const maxDeleteDomainDocs = 100000

type adminDeleteDomainResponse struct {
	Queued int `json:"queued"`
}

// handleAdminDeleteDomainDocuments removes every document in one domain.
// Fire-and-forget: looks up IDs synchronously, then queues deletion in a
// background goroutine (context.Background(), surviving the admin
// navigating away) and returns 202 immediately.
func (h *Handler) handleAdminDeleteDomainDocuments(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}

	go func() {
		ctx := context.Background()
		for _, id := range ids {
			if err := h.admin.DeleteDocument(ctx, id); err != nil {
				// %q, not %s: domainName is straight from the ?domain=
				// query parameter, so an embedded CR/LF (or any other
				// control character) would otherwise let a caller forge
				// what looks like a separate, fake log line -- %q quotes
				// and escapes it instead of writing it out raw.
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

// handleAdminSearchDomains backs the Documents page's domain search: an
// empty or missing q returns an empty list on purpose, so domains are
// discoverable by name rather than dumped in full by default.
func (h *Handler) handleAdminSearchDomains(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	limit := intQueryParam(r, "limit", defaultDomainSearchLimit, true)
	domains, err := h.admin.SearchDomains(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminDomainSummary, len(domains))
	for i, d := range domains {
		out[i] = adminDomainSummary{Host: d.Host, DocCount: d.DocCount}
	}
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
// panels. Registered as "GET /admin/api/documents/overview", so the
// method is already guaranteed -- no separate check needed here.
func (h *Handler) handleAdminDocumentsOverview(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	overview, err := h.admin.DocumentsOverview(r.Context(), defaultOverviewTopDomains)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	topDomains := make([]adminDomainSummary, len(overview.TopDomains))
	for i, d := range overview.TopDomains {
		topDomains[i] = adminDomainSummary{Host: d.Host, DocCount: d.DocCount}
	}
	ageBuckets := make([]adminAgeBucket, len(overview.AgeBuckets))
	for i, b := range overview.AgeBuckets {
		ageBuckets[i] = adminAgeBucket{Label: b.Label, Count: b.Count}
	}
	versionCounts := make([]adminVersionCount, len(overview.VersionCounts))
	for i, v := range overview.VersionCounts {
		versionCounts[i] = adminVersionCount{Version: v.Version, Count: v.Count}
	}
	storedVersionCounts := make([]adminStoredVersionsCount, len(overview.StoredVersionCounts))
	for i, s := range overview.StoredVersionCounts {
		storedVersionCounts[i] = adminStoredVersionsCount{StoredVersions: s.StoredVersions, DocCount: s.DocCount}
	}
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
// versions (an empty list just means it's never been re-crawled with
// different content, not an error).
func (h *Handler) handleAdminDocumentVersions(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	versions, err := h.admin.DocumentVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminDocumentVersion, len(versions))
	for i, v := range versions {
		out[i] = adminDocumentVersion{Version: v.Version, Title: v.Title, DocLength: v.DocLength, CrawledAt: v.CrawledAt}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminDeleteDocument is registered on the Go 1.22+ pattern
// "DELETE /admin/api/documents/{id}", since a wildcard path segment is
// exactly what that routing style is for. The mux never invokes this
// handler with an empty {id} (a bare or double-slash path either 404s or
// redirects before reaching here), so no separate empty-id check is needed.
func (h *Handler) handleAdminDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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

// postingsSnippetMaxLen bounds each match excerpt built for the vocabulary
// term-detail view -- the same length the public/debug search snippet uses
// (see domain.Snippet's callers in hybrid_search_service.go), so an excerpt
// here reads the same as everywhere else in the admin UI.
const postingsSnippetMaxLen = 200

// defaultPostingsLimit bounds the vocabulary term-detail view's page list --
// PostingsForTerm otherwise has no inherent bound, unlike the other admin
// list endpoints, since a term can appear in every indexed document.
const defaultPostingsLimit = 500

func (h *Handler) handleAdminPostings(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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
	ids := make([]string, len(postings))
	for i, p := range postings {
		ids[i] = p.DocID
	}
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
	// NormalizedPageRank is 0 whenever PageRankWeight is 0 or every result
	// in the batch has a zero PageRank -- see domain.HybridResult's field
	// doc comment.
	NormalizedPageRank float64          `json:"normalized_pagerank"`
	FinalScore         float64          `json:"final_score"`
	BM25Terms          []adminTermScore `json:"bm25_terms,omitempty"`
	// Alpha/K1/B/PageRankWeight are the tuning parameters that actually
	// produced this result -- identical across every result of one search,
	// like CorrectedTerms below.
	Alpha          float64 `json:"alpha"`
	K1             float64 `json:"k1"`
	B              float64 `json:"b"`
	PageRankWeight float64 `json:"pagerank_weight"`
	// CorrectedTerms describes the query, not this particular result -- it
	// is identical across every result of one search (see
	// domain.HybridResult.CorrectedTerms) -- and is present here so the
	// debug UI's raw JSON view can show a fuzzy-matched query term
	// transparently.
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
	out := make([]adminDebugResult, len(results))
	for i, res := range results {
		terms := make([]adminTermScore, len(res.BM25Terms))
		for j, t := range res.BM25Terms {
			terms[j] = adminTermScore{Term: t.Term, TermFreq: t.TermFreq, DocFreq: t.DocFreq, DocLength: t.DocLength, Score: t.Score}
		}
		out[i] = adminDebugResult{
			DocID: res.DocID, URL: res.URL, Title: res.Title, Snippet: res.Snippet,
			BM25Score: res.BM25Score, NormBM25: res.NormBM25, SemanticSim: res.SemanticSim,
			PageRank: res.PageRank, NormalizedPageRank: res.NormalizedPageRank, FinalScore: res.FinalScore,
			BM25Terms: terms,
			Alpha:     res.Alpha, K1: res.K1, B: res.B, PageRankWeight: res.PageRankWeight,
			CorrectedTerms: res.CorrectedTerms,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type tuningValues struct {
	Alpha float64 `json:"alpha"`
	K1    float64 `json:"k1"`
	B     float64 `json:"b"`
	// PageRankWeight blends a document's normalized link-authority score
	// into ranking (see hybridSearchService.Search) -- 0 (the default)
	// means no influence at all.
	PageRankWeight float64 `json:"pagerank_weight"`
}

// operationalValues mirrors domain.OperationalSettingsValues for the wire
// format: durations as whole seconds/hours, which are friendlier for an
// admin form (and JSON) than Go's time.Duration nanosecond encoding.
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
	DBMaxOpenConns            int    `json:"db_max_open_conns"`
	DBMaxIdleConns            int    `json:"db_max_idle_conns"`
	DBConnMaxLifetimeMinutes  int    `json:"db_conn_max_lifetime_minutes"`
	FuzzyMatchEnabled         bool   `json:"fuzzy_match_enabled"`
	FuzzyMaxEditDistance      int    `json:"fuzzy_max_edit_distance"`
	// PageRankRecomputeIntervalMinutes is how often cmd/crawl's ticker
	// recomputes every document's PageRank score (see
	// domain.OperationalSettingsValues for the full doc comment).
	PageRankRecomputeIntervalMinutes int `json:"pagerank_recompute_interval_minutes"`
	// ANNSearchEnabled forces the brute-force semantic fallback path even
	// when Postgres pgvector ANN is available, when set false (see
	// domain.OperationalSettingsValues for the full doc comment).
	ANNSearchEnabled bool `json:"ann_search_enabled"`
	// MaxRetainedCrawlJobs bounds crawl-server's persistent crawl job
	// history -- see domain.OperationalSettingsValues for the full doc
	// comment.
	MaxRetainedCrawlJobs int `json:"max_retained_crawl_jobs"`
	// MaxConcurrentCrawls bounds how many crawl jobs actually fetch pages
	// at once on crawl-server -- see domain.OperationalSettingsValues for
	// the full doc comment.
	MaxConcurrentCrawls int `json:"max_concurrent_crawls"`
	// DefaultRenderer is the crawler's global default rendering mode
	// (domain.RendererNone/RendererChromium/RendererFirefox) -- a
	// scheduled/one-off crawl's own renderer overrides this when set.
	DefaultRenderer string `json:"default_renderer"`
	// LinkScope is the crawler's global default for how far a crawl
	// follows discovered links (domain.LinkScopeHost/LinkScopeDomain/
	// LinkScopeAny) -- a scheduled/one-off crawl's own link_scope
	// overrides this when set.
	LinkScope string `json:"link_scope"`
	// MaxDocumentVersions bounds how many versions of a document (current
	// plus archived) are kept -- see domain.OperationalSettingsValues for
	// the full doc comment.
	MaxDocumentVersions int `json:"max_document_versions"`
	// TitleWeight is how many times a document's title is counted into its
	// indexed token stream, ahead of its body -- see
	// domain.OperationalSettingsValues for the full doc comment.
	TitleWeight int `json:"title_weight"`
	// EmbeddingHashEnabled/EmbeddingSearchWeights mirror the same-named
	// domain.OperationalSettingsValues fields -- see there for the full
	// doc comment, including why EmbeddingHashEnabled requires a process
	// restart to take effect and why every key in EmbeddingSearchWeights
	// must name a currently-enabled provider. Every configured HTTP
	// endpoint is managed separately via GET/POST
	// /admin/api/embeddings/endpoints, not through this settings payload.
	EmbeddingHashEnabled   bool               `json:"embedding_hash_enabled"`
	EmbeddingSearchWeights map[string]float64 `json:"embedding_search_weights"`
	// EmbeddingTitleWeight mirrors the same-named
	// domain.OperationalSettingsValues field -- see there for the full
	// doc comment.
	EmbeddingTitleWeight float64 `json:"embedding_title_weight"`
	// URLAliasWWWEnabled mirrors the same-named domain.
	// OperationalSettingsValues field -- see there for the full doc
	// comment.
	URLAliasWWWEnabled bool `json:"url_alias_www_enabled"`
	// ContentDedupEnabled/ContentDedupMethod/ContentDedupSimHashMaxDistance/
	// ContentDedupIntervalMinutes mirror the same-named domain.
	// OperationalSettingsValues fields -- see there for the full doc
	// comments.
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
// candidate HTTP embedding endpoint -- testEmbeddingConnectivity's Embed
// call and handleAdminEmbeddingsModels' ListModels call alike -- short
// enough that a hung/unreachable endpoint doesn't stall the request for
// too long, generous enough for a real (if slow) inference call to finish.
const embeddingConnectivityTestTimeout = 10 * time.Second

// modelLister is the narrow capability httpembed.Embedder implements
// beyond ports.EmbeddingProvider -- not every embedding provider has a
// remote catalog to list (hashembed doesn't), so this is a type assertion
// at the point of use rather than a method on ports.EmbeddingProvider
// itself. See httpembed.Embedder.ListModels's doc comment.
type modelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// embeddingCandidateRequest is a not-yet-saved HTTP endpoint config,
// probed by handleAdminEmbeddingsModels/handleAdminEmbeddingsTest so the
// "Test connection"/"List models" buttons work against the form as typed.
// ID, when set, names the already-saved endpoint being edited -- see
// resolveCandidateAPIKey for how a blank APIKey resolves from it.
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
// key at all" -- the endpoint form never echoes a stored key back (see
// embeddingEndpointResponse), so without this, testing an already-saved
// endpoint without retyping its key would always 401. A blank APIKey with
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
	// Error is set when base_url is non-empty but the ListModels call
	// itself fails (bad credentials, endpoint doesn't implement /models,
	// network error) -- a soft failure the admin UI shows as "couldn't
	// fetch model list," not a hard error, since the model field always
	// stays usable as free text either way.
	Error string `json:"error,omitempty"`
}

// handleAdminEmbeddingsModels lists the models a candidate (not-yet-saved)
// HTTP endpoint config reports, so the add/edit subpage can prefill model
// suggestions. Returns an empty list (200) rather than calling out at all
// when base_url is blank. No "not configured" gate: h.newEmbedder is
// always set by New.
func (h *Handler) handleAdminEmbeddingsModels(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req embeddingCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
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

// handleAdminEmbeddingsTest probes a candidate (not-yet-saved) HTTP
// endpoint config with one real Embed call, so the add/edit subpage can
// tell the admin immediately if it doesn't work, rather than only
// surfacing on the next real search. No "not configured" gate, same as
// handleAdminEmbeddingsModels.
func (h *Handler) handleAdminEmbeddingsTest(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var req embeddingCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	endpoint := h.resolveCandidateAPIKey(r.Context(), req.toEndpoint(), req.ID)
	writeJSON(w, http.StatusOK, adminEmbeddingTestResponse{Error: h.testEmbeddingConnectivity(r.Context(), endpoint)})
}

// testEmbeddingConnectivity makes one real Embed call against e to check
// a base URL/model/API key combination. A no-op when e.BaseURL is blank
// or h.newEmbedder isn't set (a test fixture that skips New).
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
// Failures are logged, not surfaced: the in-memory update already
// succeeded, and this is a best-effort convenience, not a transactional write.
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
	// ClearAPIKey is meaningful only to handleAdminUpdateEmbeddingEndpoint
	// (PATCH): since a GET response never echoes a stored key's real value
	// (see embeddingEndpointResponse), an edit form has no way to
	// distinguish "the admin left this blank because they don't want to
	// change it" from "the admin wants to remove it" -- APIKey left blank
	// means the former (preserve whatever's already stored); this explicit
	// flag is how the admin asks for the latter instead. Ignored by
	// handleAdminEmbeddingEndpoints' POST, which has no prior key to
	// preserve or clear in the first place.
	ClearAPIKey bool `json:"clear_api_key"`
}

type embeddingEndpointResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// HasAPIKey reports only whether a key is set, never its value -- same
	// redacted-summary treatment scheduledCrawlResponse already gives a
	// schedule's stored credentials.
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
		http.Error(w, "name must not be empty", http.StatusBadRequest)
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
// ChunkSizeTokens check for the same reason: MaxContextTokens shares the
// same "0 disables, negative is invalid" convention.
func validateChatEndpointRequest(w http.ResponseWriter, req chatEndpointRequest) bool {
	if req.MaxContextTokens < 0 {
		http.Error(w, "max_context_tokens must not be negative", http.StatusBadRequest)
		return false
	}
	return true
}

// encryptAPIKey seals apiKey via settingscrypto for storage (a no-op
// passthrough when h.settingsEncryptionKey is nil, or apiKey is already
// empty -- see Encrypt's doc comment).
func (h *Handler) encryptAPIKey(apiKey string) string {
	enc, err := settingscrypto.Encrypt(h.settingsEncryptionKey, apiKey)
	if err != nil {
		log.Printf("encrypting embedding endpoint API key: %v", err)
		return apiKey
	}
	return enc
}

// decryptAPIKey reverses encryptAPIKey for a stored value, used by
// resolveCandidateAPIKey to recover a real key for a probe. A decryption
// failure is logged and returns apiKey unchanged -- it then simply fails
// the probe's HTTP call with its own 401, rather than masking the failure.
func (h *Handler) decryptAPIKey(apiKey string) string {
	dec, err := settingscrypto.Decrypt(h.settingsEncryptionKey, apiKey)
	if err != nil {
		log.Printf("decrypting embedding endpoint API key: %v", err)
		return apiKey
	}
	return dec
}

// handleAdminEmbeddingEndpoints lists (GET) or creates (POST) HTTP
// embedding endpoint configs. A freshly created endpoint's ID is minted
// from its name, deduped against every existing ID plus the reserved
// "hash" (the built-in provider's own ID).
func (h *Handler) handleAdminEmbeddingEndpoints(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, "embedding endpoints") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		endpoints, err := h.embeddingEndpoints.ListEmbeddingEndpoints(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]embeddingEndpointResponse, len(endpoints))
		for i, e := range endpoints {
			out[i] = toEmbeddingEndpointResponse(e)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req embeddingEndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
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
		existingIDs := map[string]bool{domain.EmbeddingProviderHash: true}
		for _, e := range existing {
			existingIDs[e.ID] = true
		}
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminGetEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, "embedding endpoints") {
		return
	}
	e, err := h.embeddingEndpoints.GetEmbeddingEndpoint(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, "embedding endpoint not found", toEmbeddingEndpointResponse(e))
}

// handleAdminUpdateEmbeddingEndpoint replaces an endpoint's editable
// fields. APIKey is the one exception to "PATCH is a full replace": since
// GET never echoes its real value, blank means "unchanged," not "clear
// it" -- req.ClearAPIKey removes it explicitly. ID is never editable
// once created (it's baked into document_embeddings.provider and ANN names).
func (h *Handler) handleAdminUpdateEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, "embedding endpoints") {
		return
	}
	var req embeddingEndpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validateEmbeddingEndpointRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	existing, err := h.embeddingEndpoints.GetEmbeddingEndpoint(r.Context(), id)
	if err != nil {
		respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, "embedding endpoint not found", nil)
		return
	}
	apiKey := existing.APIKey
	if req.APIKey != "" {
		apiKey = h.encryptAPIKey(req.APIKey)
	} else if req.ClearAPIKey {
		apiKey = ""
	}
	e := domain.EmbeddingHTTPEndpoint{
		ID: id, Name: req.Name, BaseURL: req.BaseURL, APIKey: apiKey,
		Model: req.Model, Dimensions: req.Dimensions, RateLimitPerSecond: req.RateLimitPerSecond,
		Enabled: req.Enabled, ChunkSizeTokens: req.ChunkSizeTokens, TokenizeURL: req.TokenizeURL,
		CreatedAt: existing.CreatedAt,
	}
	err = h.embeddingEndpoints.UpdateEmbeddingEndpoint(r.Context(), e)
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, "embedding endpoint not found", toEmbeddingEndpointResponse(e))
}

func (h *Handler) handleAdminDeleteEmbeddingEndpoint(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.embeddingEndpoints != nil, "embedding endpoints") {
		return
	}
	err := h.embeddingEndpoints.DeleteEmbeddingEndpoint(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrEmbeddingEndpointNotFound, "embedding endpoint not found", map[string]bool{"ok": true})
}

type chatHookRequest struct {
	Name             string `json:"name"`
	Pattern          string `json:"pattern"`
	Script           string `json:"script"`
	Enabled          bool   `json:"enabled"`
	Prompt           string `json:"prompt"`
	GatedByWebSearch bool   `json:"gated_by_web_search"`
}

type chatHookResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Pattern          string `json:"pattern"`
	Script           string `json:"script"`
	Enabled          bool   `json:"enabled"`
	Prompt           string `json:"prompt"`
	GatedByWebSearch bool   `json:"gated_by_web_search"`
}

func toChatHookResponse(hk domain.ChatHook) chatHookResponse {
	return chatHookResponse{
		ID: hk.ID, Name: hk.Name, Pattern: hk.Pattern, Script: hk.Script, Enabled: hk.Enabled,
		Prompt: hk.Prompt, GatedByWebSearch: hk.GatedByWebSearch,
	}
}

// validateChatHookRequest requires a non-empty Name and Script, and a
// Pattern that compiles as a Go regexp with exactly one capture group --
// see domain.ChatHook.Pattern's doc comment and runChatHooks's security
// note (internal/application/chat_hooks.go) for why exactly one capture
// group matters: it's the only thing ever passed as an argv value to
// Script. Enforcing this at Create/Update time is what keeps
// runChatHooks's own (best-effort, log-and-skip) version of this same
// check from ever actually firing in practice.
func validateChatHookRequest(w http.ResponseWriter, req chatHookRequest) bool {
	if req.Name == "" {
		http.Error(w, "name must not be empty", http.StatusBadRequest)
		return false
	}
	if req.Script == "" {
		http.Error(w, "script must not be empty", http.StatusBadRequest)
		return false
	}
	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		http.Error(w, "pattern must be a valid regular expression: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if re.NumSubexp() != 1 {
		http.Error(w, "pattern must have exactly 1 capture group", http.StatusBadRequest)
		return false
	}
	return true
}

// handleAdminChatHooks lists (GET) or creates (POST) regex-triggered chat
// hooks, mirroring handleAdminEmbeddingEndpoints' style closely. A freshly
// created hook's ID is minted from its name, deduped against every
// existing ID (domain.NewChatHookID, the same convention
// domain.NewEmbeddingEndpointID uses).
func (h *Handler) handleAdminChatHooks(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chatHooks != nil, "chat hooks") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		hooks, err := h.chatHooks.ListChatHooks(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]chatHookResponse, len(hooks))
		for i, hk := range hooks {
			out[i] = toChatHookResponse(hk)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req chatHookRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if !validateChatHookRequest(w, req) {
			return
		}
		existing, err := h.chatHooks.ListChatHooks(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		existingIDs := make(map[string]bool, len(existing))
		for _, hk := range existing {
			existingIDs[hk.ID] = true
		}
		hk := domain.ChatHook{
			ID: domain.NewChatHookID(req.Name, existingIDs), Name: req.Name,
			Pattern: req.Pattern, Script: req.Script, Enabled: req.Enabled,
			Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
		}
		if err := h.chatHooks.CreateChatHook(r.Context(), hk); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, toChatHookResponse(hk))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAdminGetChatHook returns one hook by ID. ports.ChatHookStore has no
// single-row get (unlike EmbeddingEndpointStore), so this scans
// ListChatHooks -- an admin's hook list is small enough (like
// scheduled crawls) that this is never a real cost.
func (h *Handler) handleAdminGetChatHook(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chatHooks != nil, "chat hooks") {
		return
	}
	hooks, err := h.chatHooks.ListChatHooks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	for _, hk := range hooks {
		if hk.ID == id {
			writeJSON(w, http.StatusOK, toChatHookResponse(hk))
			return
		}
	}
	http.Error(w, "chat hook not found", http.StatusNotFound)
}

// handleAdminUpdateChatHook replaces a hook's editable fields. ID is never
// editable once created (mirrors handleAdminUpdateEmbeddingEndpoint).
func (h *Handler) handleAdminUpdateChatHook(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chatHooks != nil, "chat hooks") {
		return
	}
	var req chatHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validateChatHookRequest(w, req) {
		return
	}
	hk := domain.ChatHook{
		ID: r.PathValue("id"), Name: req.Name, Pattern: req.Pattern, Script: req.Script, Enabled: req.Enabled,
		Prompt: req.Prompt, GatedByWebSearch: req.GatedByWebSearch,
	}
	err := h.chatHooks.UpdateChatHook(r.Context(), hk)
	respondOrNotFound(w, err, ports.ErrChatHookNotFound, "chat hook not found", toChatHookResponse(hk))
}

func (h *Handler) handleAdminDeleteChatHook(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.chatHooks != nil, "chat hooks") {
		return
	}
	err := h.chatHooks.DeleteChatHook(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrChatHookNotFound, "chat hook not found", map[string]bool{"ok": true})
}

func (h *Handler) handleAdminEmbeddingEndpointPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminEmbeddingEndpointHTML)
}

func (h *Handler) handleAdminEmbeddingEndpointsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminEmbeddingEndpointsHTML)
}

// currentEmbeddingEndpoints lists every configured HTTP embedding endpoint,
// or an empty slice if h.embeddingEndpoints isn't configured or the list
// fails -- the same "degrade gracefully, never fail the caller over this"
// convention used throughout this file for optional dependencies.
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
	BaseURL              string `json:"base_url"`
	APIKey               string `json:"api_key"`
	Model                string `json:"model"`
	Enabled              bool   `json:"enabled"`
	RAGEnabled           bool   `json:"rag_enabled"`
	RAGResultCount       int    `json:"rag_result_count"`
	MaxContextTokens     int    `json:"max_context_tokens"`
	WebSearchEnabled     bool   `json:"web_search_enabled"`
	WebSearchBaseURL     string `json:"web_search_base_url"`
	WebSearchResultCount int    `json:"web_search_result_count"`
	// SystemPrompt mirrors domain.ChatEndpoint.SystemPrompt exactly -- see
	// that field's doc comment. Empty string is the default (no persistent
	// prompt injected).
	SystemPrompt string `json:"system_prompt"`
	// ClearAPIKey is meaningful only to a PATCH: since a GET response never
	// echoes a stored key's real value (see chatEndpointResponse), an edit
	// form has no way to distinguish "left blank because not being
	// changed" from "wants it removed" -- APIKey left blank means the
	// former (preserve whatever's already stored); this explicit flag is
	// how the admin asks for the latter instead. Mirrors
	// embeddingEndpointRequest.ClearAPIKey exactly.
	ClearAPIKey bool `json:"clear_api_key"`
}

type chatEndpointResponse struct {
	BaseURL string `json:"base_url"`
	// HasAPIKey reports only whether a key is set, never its value -- same
	// redacted-summary treatment toEmbeddingEndpointResponse already gives.
	HasAPIKey            bool   `json:"has_api_key"`
	Model                string `json:"model"`
	Enabled              bool   `json:"enabled"`
	RAGEnabled           bool   `json:"rag_enabled"`
	RAGResultCount       int    `json:"rag_result_count"`
	MaxContextTokens     int    `json:"max_context_tokens"`
	WebSearchEnabled     bool   `json:"web_search_enabled"`
	WebSearchBaseURL     string `json:"web_search_base_url"`
	WebSearchResultCount int    `json:"web_search_result_count"`
	// SystemPrompt mirrors domain.ChatEndpoint.SystemPrompt exactly -- see
	// chatEndpointRequest.SystemPrompt's doc comment.
	SystemPrompt string    `json:"system_prompt"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toChatEndpointResponse(e domain.ChatEndpoint) chatEndpointResponse {
	return chatEndpointResponse{
		BaseURL: e.BaseURL, HasAPIKey: e.APIKey != "", Model: e.Model, Enabled: e.Enabled,
		RAGEnabled: e.RAGEnabled, RAGResultCount: e.RAGResultCount,
		MaxContextTokens: e.MaxContextTokens, UpdatedAt: e.UpdatedAt,
		WebSearchEnabled: e.WebSearchEnabled, WebSearchBaseURL: e.WebSearchBaseURL,
		WebSearchResultCount: e.WebSearchResultCount, SystemPrompt: e.SystemPrompt,
	}
}

// defaultChatEndpointResponse is what handleAdminChatEndpoint's GET returns
// when nothing has ever been saved -- a settings page GET should never fail
// just because it hasn't been configured yet, same spirit as
// /admin/api/settings always succeeding.
func defaultChatEndpointResponse() chatEndpointResponse {
	return chatEndpointResponse{
		RAGEnabled: true, RAGResultCount: domain.DefaultChatRAGResultCount,
		WebSearchResultCount: domain.DefaultChatWebSearchResultCount,
	}
}

// handleAdminChatEndpoint is single-row admin config CRUD for the chat
// endpoint (GET current config, PATCH to upsert it), mirroring
// handleAdminEmbeddingEndpoints/handleAdminUpdateEmbeddingEndpoint's style
// closely -- see chatEndpointRequest.ClearAPIKey's doc comment for the
// "blank api_key on update means unchanged" convention shared with that
// endpoint.
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
		var req chatEndpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
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
		if req.APIKey != "" {
			apiKey = h.encryptAPIKey(req.APIKey)
		} else if req.ClearAPIKey {
			apiKey = ""
		}
		e := domain.ChatEndpoint{
			BaseURL: req.BaseURL, APIKey: apiKey, Model: req.Model, Enabled: req.Enabled,
			RAGEnabled: req.RAGEnabled, RAGResultCount: req.RAGResultCount,
			MaxContextTokens: req.MaxContextTokens,
			WebSearchEnabled: req.WebSearchEnabled, WebSearchBaseURL: req.WebSearchBaseURL,
			WebSearchResultCount: req.WebSearchResultCount, SystemPrompt: req.SystemPrompt,
		}
		e.Clamp()
		e.UpdatedAt = time.Now().UTC()
		if err := h.chatEndpoints.SetChatEndpoint(r.Context(), e); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toChatEndpointResponse(e))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		var req settingsResponse
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// overridesValues mirrors domain.RankingOverridesValues for the wire
// format, keeping JSON tags (and the map-vs-slice choice for the wire
// format) out of the domain type.
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
		var req overridesValues
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		h.overrides.Set(req.toSettingsValues())
		h.persistSetting(r.Context(), ports.SettingsKeyOverrides, h.overrides.Get())
		writeJSON(w, http.StatusOK, h.currentOverrides())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAdminCrawlJobs serves the Jobs collection: GET lists every job,
// DELETE clears every ended one (the "Clear ended jobs" button). Both
// live on this path rather than a separate "/clear-ended" sub-path, to
// avoid colliding with the "{id}" wildcard route below.
func (h *Handler) handleAdminCrawlJobs(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, "crawl jobs") {
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAdminCrawlJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, "crawl jobs") {
		return
	}
	job, err := h.jobs.GetCrawlJob(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrCrawlJobNotFound, "crawl job not found", job)
}

func (h *Handler) handleAdminCancelCrawlJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, "crawl jobs") {
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

// scheduledCrawlRequest is the wire shape for creating (POST) and
// replacing (PATCH) a crawl -- no separate "just run once" shape or
// explicit "recurring" flag: IntervalMinutes 0 means "run once," positive
// means "repeat" -- see toScheduledCrawl.
type scheduledCrawlRequest struct {
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	RespectRobots bool     `json:"respect_robots"`
	UserAgent     string   `json:"user_agent"`
	Cookie        string   `json:"cookie"`
	BasicAuthUser string   `json:"basic_auth_user"`
	BasicAuthPass string   `json:"basic_auth_pass"`
	// ClearCookie/ClearBasicAuth are meaningful only to
	// handleAdminUpdateSchedule (PATCH): since a GET response never
	// echoes a stored credential's real value (see scheduledCrawlResponse),
	// an edit form has no way to distinguish "the admin left this blank
	// because they don't want to change it" from "the admin wants to
	// remove it" -- Cookie/BasicAuthUser/BasicAuthPass left blank means
	// the former (preserve whatever's already stored); these two explicit
	// flags are how the admin asks for the latter instead. Ignored by
	// handleAdminSchedules' POST, which has no prior credential to
	// preserve or clear in the first place.
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
	// this crawl follows discovered links -- "" (domain.LinkScopeDefault)
	// means "inherit the global default"; "host"/"domain"/"any" choose
	// explicitly.
	LinkScope string `json:"link_scope"`
	// AllowedDomains/BlockedDomains/FollowIndexedDomains mirror
	// ports.CrawlOptions' fields of the same name -- see its doc comment
	// for the exact allow/block precedence against LinkScope.
	AllowedDomains       []string `json:"allowed_domains"`
	BlockedDomains       []string `json:"blocked_domains"`
	FollowIndexedDomains bool     `json:"follow_indexed_domains"`
	// MaxRuns caps how many times a recurring crawl repeats before
	// disabling itself; 0 (the default) means unlimited. Meaningless when
	// IntervalMinutes is 0 (a one-off crawl already stops after its one
	// run).
	MaxRuns int `json:"max_runs"`
	// Renderer overrides the Tuning page's global default rendering mode
	// for this crawl alone -- "" (domain.RendererDefault) means "inherit
	// the global default"; "none"/"chromium"/"firefox" choose explicitly.
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
	// its value -- same redacted-summary treatment ports.CrawlJobRequest
	// already gives a one-off crawl's credentials (see its doc comment),
	// now applied here too so a scheduled crawl's stored Cookie/
	// BasicAuthUser/BasicAuthPass never round-trip through a GET response.
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
// by handleAdminSchedules' POST and handleAdminUpdateSchedule's PATCH.
// Recurring is derived from IntervalMinutes (positive = repeat, 0 = run
// once); a recurring entry's first run is interval_minutes from now, a
// one-off's is right now. created_at is harmless-if-ignored for an update.
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

// handleAdminSchedules lists (GET) or creates (POST) crawls -- every one
// an admin triggers, one-off or recurring, is one of these; a freshly
// created entry is always enabled. POST enforces at most one schedule per
// domain: a submission matching an existing schedule's seed host replaces
// its options in place instead of inserting a second row.
func (h *Handler) handleAdminSchedules(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		schedules, err := h.scheduledCrawls.ListScheduledCrawls(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]scheduledCrawlResponse, len(schedules))
		for i, s := range schedules {
			out[i] = toScheduledCrawlResponse(s)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req scheduledCrawlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// scheduledCrawlIDForSameDomain returns the ID of an already-existing
// schedule sharing seedURLs' first host, or "" if none (empty/unparseable
// seedURLs never match) -- keeps handleAdminSchedules' POST "one schedule
// per domain" true.
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
// reschedules it (next_run_at = interval_minutes from now). Cookie/
// BasicAuthUser/BasicAuthPass are the exception to "PATCH is a full
// replace" -- GET never echoes them, so blank means "unchanged."
func (h *Handler) handleAdminUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	var req scheduledCrawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validateScheduledCrawlRequest(w, req) {
		return
	}
	id := r.PathValue("id")
	existing, err := h.scheduledCrawls.GetScheduledCrawl(r.Context(), id)
	if err != nil {
		respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", nil)
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
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}

func (h *Handler) handleAdminDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	err := h.scheduledCrawls.DeleteScheduledCrawl(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}

// handleAdminGetSchedule backs the schedule-detail/edit subpage's initial
// load -- a single schedule's full options, the same shape ListScheduledCrawls'
// entries already have.
func (h *Handler) handleAdminGetSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	s, err := h.scheduledCrawls.GetScheduledCrawl(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", toScheduledCrawlResponse(s))
}

// handleAdminRunScheduleNow marks a schedule due immediately -- crawl-server's
// own scheduler ticker picks it up on its next tick and creates the actual
// CrawlJob, the same path a freshly created one-off crawl already goes
// through -- so this handler itself never talks to crawl-server directly.
func (h *Handler) handleAdminRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	err := h.scheduledCrawls.RunScheduledCrawlNow(r.Context(), r.PathValue("id"), time.Now().UTC())
	if errors.Is(err, ports.ErrScheduledCrawlInProgress) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}

// handleAdminToggleSchedule flips only Enabled -- unlike PATCHing the
// schedule (handleAdminUpdateSchedule), it never touches NextRunAt, so
// pausing/resuming from the Jobs list's checkbox doesn't reschedule the
// crawl or reorder that list (sorted by NextRunAt).
func (h *Handler) handleAdminToggleSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
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
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}

func (h *Handler) handleAdminSchedulePage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminScheduleHTML)
}

func (h *Handler) handleAdminPageRankPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminPageRankHTML)
}

func (h *Handler) handleAdminEmbeddingsPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminEmbeddingsHTML)
}

type adminPageRankResponse struct {
	TotalDocs   int     `json:"total_docs"`
	MinPageRank float64 `json:"min_pagerank"`
	MaxPageRank float64 `json:"max_pagerank"`
	AvgPageRank float64 `json:"avg_pagerank"`
	// Damping/MaxIterations/Epsilon are domain.PageRank's fixed algorithm
	// constants -- not configurable, but worth showing on the debug page
	// alongside the values they actually produced.
	Damping                  float64 `json:"damping"`
	MaxIterations            int     `json:"max_iterations"`
	Epsilon                  float64 `json:"epsilon"`
	PageRankWeight           float64 `json:"pagerank_weight"`
	RecomputeIntervalMinutes int     `json:"recompute_interval_minutes"`
	// RecomputeInProgress/LastRecomputedAt/LastRecomputeDocuments/
	// LastRecomputeIterations/LastRecomputeFinalDelta reflect
	// domain.PageRankStatus, persisted by any process that ran a recompute
	// (this admin-server's own "force recalculation" click, another
	// admin-server instance, or cmd/crawl's periodic ticker/post-crawl
	// trigger) -- so this shows the real cross-process state, not just
	// whatever this one browser tab remembers triggering.
	RecomputeInProgress bool       `json:"recompute_in_progress"`
	LastRecomputedAt    *time.Time `json:"last_recomputed_at,omitempty"`
	// No omitempty on these three: a run over an empty link graph
	// legitimately scores 0 documents in 0 iterations, and omitempty would
	// silently drop that real value the same way a genuinely-missing one
	// would -- the JS side only reads them once LastRecomputedAt is set
	// anyway, so there's nothing to gain by omitting a real zero.
	LastRecomputeDocuments  int     `json:"last_recompute_documents"`
	LastRecomputeIterations int     `json:"last_recompute_iterations"`
	LastRecomputeFinalDelta float64 `json:"last_recompute_final_delta"`
	LastRecomputeDurationMs int64   `json:"last_recompute_duration_ms"`
}

func (h *Handler) handleAdminPageRank(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	totalDocs, _, err := h.admin.CorpusStats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	min, max, avg, err := h.admin.PageRankDistribution(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := adminPageRankResponse{
		TotalDocs:                totalDocs,
		MinPageRank:              min,
		MaxPageRank:              max,
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

// handleAdminPageRankRecompute runs a full PageRank recompute
// synchronously (unlike bulk document delete's fire-and-forget) and
// reports documents scored, iterations, final delta, and duration -- an
// admin clicking "force recalculation" is waiting to see this run's result.
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
		if min, max, avg, err := h.admin.PageRankDistribution(r.Context()); err == nil {
			resp.MinPageRank, resp.MaxPageRank, resp.AvgPageRank = min, max, avg
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminEmbeddingRecomputeStatusResponse struct {
	TotalDocs  int  `json:"total_docs"`
	InProgress bool `json:"in_progress"`
	// LastRunAt/Documents/Failed/DurationMs reflect the persisted
	// cross-process status, not just this browser tab's own trigger. No
	// omitempty on Documents/Failed/DurationMs: 0 is a legitimate result
	// (an empty corpus), not the same as "missing".
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	Documents  int        `json:"documents"`
	Failed     int        `json:"failed"`
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
	if !status.LastRunAt.IsZero() {
		lastRunAt := status.LastRunAt
		resp.LastRunAt = &lastRunAt
		resp.Documents = status.Documents
		resp.Failed = status.Failed
		resp.DurationMs = status.DurationMs
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminEmbeddingsRecomputeStart kicks off a full-corpus embedding
// recompute in the background -- unlike PageRank's synchronous recompute,
// this is a real per-document network round-trip, too slow to do inline.
// Rejects a second trigger while one is running (409).
func (h *Handler) handleAdminEmbeddingsRecomputeStart(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.embeddingRepo != nil && len(h.embedders) > 0, "embedding recompute") {
		return
	}
	if application.LoadEmbeddingRecomputeStatus(r.Context(), h.settingsStore).InProgress {
		http.Error(w, "an embedding recompute is already in progress", http.StatusConflict)
		return
	}
	v := h.opSettings.Get()
	go func() {
		ctx := context.Background()
		if _, err := application.RunEmbeddingRecomputeJobWithStatus(ctx, h.embeddingRepo, h.embedders, h.settingsStore, v.EmbeddingTitleWeight); err != nil {
			log.Printf("recomputing embeddings: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

type adminContentDedupStatusResponse struct {
	InProgress bool `json:"in_progress"`
	// LastRunAt/GroupsFound/DocumentsMerged/DurationMs reflect the
	// persisted cross-process status (cmd/crawl's ticker, a post-crawl
	// trigger, or this page's own button), not just this tab's trigger.
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	GroupsFound     int        `json:"groups_found"`
	DocumentsMerged int        `json:"documents_merged"`
	DurationMs      int64      `json:"duration_ms"`
}

func (h *Handler) handleAdminContentDedupStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.contentDedupRepo != nil, "content dedup") {
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

// handleAdminContentDedupRecomputeStart kicks off a full-corpus content
// dedup pass in the background, mirroring
// handleAdminEmbeddingsRecomputeStart's fire-and-forget shape -- a
// corpus-wide scan (plus simhash banding) can take real time. Rejects a
// second trigger while one is running (409): real DB contention otherwise.
func (h *Handler) handleAdminContentDedupRecomputeStart(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.contentDedupRepo != nil, "content dedup") {
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
			// The fast-path check above already rejected the common case
			// (200/409 back to this same request) -- this is the rarer
			// case where cmd/crawl's own scheduler won the race in the
			// gap between that check and this goroutine actually starting.
			// Expected, not worth logging as an error.
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
// reason is included (not just the URL) so the admin UI can tell an actual
// content-dedup merge apart from ordinary canonical_tag bookkeeping
// recorded during crawling, which never deleted or merged anything.
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

// handleAdminContentDedupAliasGroups lists every canonical document that
// has at least one alias of it, from any reason -- ordinary canonical_tag
// bookkeeping recorded during crawling as well as an actual content-dedup
// merge (see each entry's own reason) -- the "what's actually aliased"
// transparency listing a black-box merge count can't provide, letting an
// admin verify a dedup pass did what they expect before trusting it
// further.
func (h *Handler) handleAdminContentDedupAliasGroups(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "content dedup") {
		return
	}
	limit := intQueryParam(r, "limit", defaultAliasGroupsPageSize, true)
	offset := intQueryParam(r, "offset", 0, false)
	groups, total, err := h.admin.ListDocumentAliasGroups(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminDocumentAliasGroup, len(groups))
	for i, g := range groups {
		aliases := make([]adminDocumentAlias, len(g.Aliases))
		for j, a := range g.Aliases {
			aliases[j] = adminDocumentAlias{URL: a.URL, Reason: a.Reason}
		}
		out[i] = adminDocumentAliasGroup{CanonicalID: g.CanonicalID, CanonicalURL: g.CanonicalURL, Aliases: aliases}
	}
	writeJSON(w, http.StatusOK, adminAliasGroupsResponse{Total: total, Groups: out})
}

func (h *Handler) handleAdminContentDedupPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminContentDedupHTML)
}

func (h *Handler) handleAdminDatabasePage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminDatabaseHTML)
}

// adminDBPoolStats is sql.DBStats' wire shape, with its one time.Duration
// field converted to milliseconds -- JSON has no native duration type, and
// milliseconds reads more directly than a raw nanosecond count.
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
// handleAdminDatabase and handleAdminOverviewMetrics, the two admin panels
// that both surface the same live pool via AdminRepository.PoolStats.
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
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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

// handleAdminClearContent permanently deletes every crawled document
// (postings, links, versions, embeddings, aliases) and every crawl job --
// see ports.AdminRepository.ClearContent's own doc comment. Every settings
// table is left untouched.
func (h *Handler) handleAdminClearContent(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	if err := h.admin.ClearContent(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// defaultTuningAlpha mirrors the literal every cmd/*/main.go bootstraps
// domain.NewTuningSettings with -- k1/b already have named
// domain.DefaultBM25K1/DefaultBM25B constants; alpha never got one.
const defaultTuningAlpha = 0.5

// handleAdminClearSettings permanently deletes every row of every settings
// table -- see ports.AdminRepository.ClearSettings's own doc comment. This
// process's own in-memory settings are reset to their built-in defaults
// immediately afterward (for immediate feedback on this admin-server), but
// other processes (search-server, crawl-server) only pick up the change
// once restarted -- deliberately not attempted automatically, since
// bootstrap.SyncSettings can't tell "cleared on purpose" apart from a
// transient DB read error.
func (h *Handler) handleAdminClearSettings(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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

// Lookback windows for the admin Overview page's tier-2 panels: the
// crawl-outcome donut and documents-indexed trend look back 30 days; the
// higher-volume crawl_job_pages panels use a narrower 14 to keep their
// one-bar/point-per-day charts readable.
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

type adminPageRankBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// adminOverviewRunningJob is the inline-visible detail for one currently
// running crawl job on the Overview page -- just enough to summarize it
// (seedSummary already renders SeedURLs client-side the same way the
// Jobs/Schedules tables do), not the full CrawlJobSummary.
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
	PageRankBuckets    []adminPageRankBucket    `json:"pagerank_buckets"`
	// PageRankOrphanThreshold documents the fixed cutoff PageRankOrphanCount/
	// PageRankOrphanPercent were computed against (domain.
	// PageRankOrphanThreshold), so the client can label the stat tile
	// correctly without hardcoding the number itself.
	PageRankOrphanThreshold float64 `json:"pagerank_orphan_threshold"`
	PageRankOrphanCount     int     `json:"pagerank_orphan_count"`
	PageRankOrphanPercent   float64 `json:"pagerank_orphan_percent"`
	PageRankTotalDocs       int     `json:"pagerank_total_docs"`
}

// handleAdminOverviewMetrics backs the Overview page's operational panels
// beyond handleAdminDocumentsOverview's corpus summary: crawl/schedule
// health, DB pool stats, and tier-2 trend charts. Only h.admin is
// required; h.jobs/h.scheduledCrawls degrade to zero-valued fields if unset.
func (h *Handler) handleAdminOverviewMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.admin != nil, "admin diagnostics") {
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
		for _, j := range jobs {
			switch j.Status {
			case domain.CrawlJobRunning:
				resp.RunningCrawlJobs++
				resp.RunningJobs = append(resp.RunningJobs, adminOverviewRunningJob{
					ID: j.ID, SeedURLs: j.Request.SeedURLs, PagesCrawled: j.PagesCrawled,
				})
			case domain.CrawlJobQueued:
				resp.QueuedCrawlJobs++
			}
		}
	}

	if h.scheduledCrawls != nil {
		schedules, err := h.scheduledCrawls.ListScheduledCrawls(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		for _, s := range schedules {
			if s.Enabled {
				resp.SchedulesEnabled++
			} else {
				resp.SchedulesDisabled++
			}
			if s.InProgress {
				resp.SchedulesInProgress++
			}
			if s.Enabled && s.NextRunAt.Before(now) {
				resp.SchedulesOverdue++
			}
		}
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
		resp.PageRankBuckets = append(resp.PageRankBuckets, adminPageRankBucket{Label: b.Label, Count: b.Count})
	}
	resp.PageRankOrphanCount = orphanCount
	resp.PageRankTotalDocs = totalDocs
	if totalDocs > 0 {
		resp.PageRankOrphanPercent = float64(orphanCount) / float64(totalDocs) * 100
	}

	writeJSON(w, http.StatusOK, resp)
}

// groupDailyFetchOutcomes regroups sqlrepo's flat (day, status, count)
// rows into one entry per day with every outcome nested underneath, the
// shape the Overview page's stacked-bar chart wants. Rows arrive already
// ordered by day, so a single pass suffices.
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
