package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

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

func (h *Handler) handleAdminTuningPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminTuningHTML)
}

func (h *Handler) handleAdminSearchPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminSearchHTML)
}

func (h *Handler) handleAdminOverridesPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminOverridesHTML)
}

func (h *Handler) handleAdminCrawlPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", crawlHTML)
}

func (h *Handler) handleAdminSchedulesPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", adminSchedulesHTML)
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

const defaultVocabularyTopTermsLimit = 20

type adminTermStat struct {
	Term      string `json:"term"`
	DocFreq   int    `json:"doc_freq"`
	TotalFreq int    `json:"total_freq"`
}

type adminVocabularyResponse struct {
	VocabularySize int             `json:"vocabulary_size"`
	TopTerms       []adminTermStat `json:"top_terms"`
}

func (h *Handler) handleAdminVocabulary(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	limit := intQueryParam(r, "limit", defaultVocabularyTopTermsLimit, true)
	vocabSize, topTerms, err := h.admin.VocabularyStats(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminTermStat, len(topTerms))
	for i, t := range topTerms {
		out[i] = adminTermStat{Term: t.Term, DocFreq: t.DocFreq, TotalFreq: t.TotalFreq}
	}
	writeJSON(w, http.StatusOK, adminVocabularyResponse{VocabularySize: vocabSize, TopTerms: out})
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

type adminDocumentsOverview struct {
	TopDomains []adminDomainSummary `json:"top_domains"`
	AgeBuckets []adminAgeBucket     `json:"age_buckets"`
}

// handleAdminDocumentsOverview backs the Documents page's summary charts:
// document count per (top) domain, and how recently pages were crawled.
// Registered as "GET /admin/api/documents/overview", so the method is
// already guaranteed -- no separate check needed here.
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
	writeJSON(w, http.StatusOK, adminDocumentsOverview{TopDomains: topDomains, AgeBuckets: ageBuckets})
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
	TermFreq  int    `json:"term_freq"`
	DocLength int    `json:"doc_length"`
}

type adminPostingsResponse struct {
	Term     string         `json:"term"`
	DocFreq  int            `json:"doc_freq"`
	Postings []adminPosting `json:"postings"`
}

func (h *Handler) handleAdminPostings(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	term := r.URL.Query().Get("term")
	if term == "" {
		http.Error(w, "term must not be empty", http.StatusBadRequest)
		return
	}
	postings, err := h.admin.PostingsForTerm(r.Context(), term)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := adminPostingsResponse{Term: term, Postings: make([]adminPosting, len(postings))}
	if len(postings) > 0 {
		resp.DocFreq = postings[0].DocFreq
	}
	for i, p := range postings {
		resp.Postings[i] = adminPosting{DocID: p.DocID, TermFreq: p.TermFreq, DocLength: p.DocLength}
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminDebugResult struct {
	DocID       string  `json:"doc_id"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Snippet     string  `json:"snippet"`
	BM25Score   float64 `json:"bm25_score"`
	SemanticSim float64 `json:"semantic_sim"`
	FinalScore  float64 `json:"final_score"`
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
	results, err := h.debug.Search(r.Context(), query, ports.SearchQuery{TopK: topK, Sort: parseSortParam(r)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]adminDebugResult, len(results))
	for i, res := range results {
		out[i] = adminDebugResult{
			DocID: res.DocID, URL: res.URL, Title: res.Title, Snippet: res.Snippet,
			BM25Score: res.BM25Score, SemanticSim: res.SemanticSim, FinalScore: res.FinalScore,
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
	}
}

type settingsResponse struct {
	Tuning      tuningValues      `json:"tuning"`
	Operational operationalValues `json:"operational"`
}

// persistSetting saves v (JSON-encoded) to the settings store under key, so
// every other process's next poll picks up this edit -- a no-op when no
// SettingsStore was configured (this process's in-memory update above still
// applies either way). Encoding/save failures are logged, not surfaced to
// the admin: the in-memory update already succeeded, and this is a
// best-effort convenience knob, not a transactional write.
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
		h.opSettings.Set(req.Operational.toSettingsValues())
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

type crawlRequest struct {
	SeedURLs            []string `json:"seed_urls"`
	MaxPages            int      `json:"max_pages"`
	Cookie              string   `json:"cookie"`
	BasicAuthUser       string   `json:"basic_auth_user"`
	BasicAuthPass       string   `json:"basic_auth_pass"`
	RespectRobots       bool     `json:"respect_robots"`
	UserAgent           string   `json:"user_agent"`
	AllowOffDomainLinks bool     `json:"allow_off_domain_links"`
	UseSitemap          bool     `json:"use_sitemap"`
}

type startCrawlResponse struct {
	JobID string `json:"job_id"`
}

// handleAdminCrawl starts a crawl job on crawl-server and returns
// immediately with its ID -- it does not wait for the crawl to finish.
// Progress is polled via handleAdminCrawlJobs/handleAdminCrawlJob.
func (h *Handler) handleAdminCrawl(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireConfigured(w, h.jobs != nil, "crawl jobs") {
		return
	}
	var req crawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.SeedURLs) == 0 {
		http.Error(w, "seed_urls must not be empty", http.StatusBadRequest)
		return
	}

	jobID, err := h.jobs.StartCrawlJob(r.Context(), ports.CrawlOptions{
		SeedURLs:            req.SeedURLs,
		MaxPages:            req.MaxPages,
		Cookie:              req.Cookie,
		BasicAuthUser:       req.BasicAuthUser,
		BasicAuthPass:       req.BasicAuthPass,
		RespectRobots:       req.RespectRobots,
		UserAgent:           req.UserAgent,
		AllowOffDomainLinks: req.AllowOffDomainLinks,
		UseSitemap:          req.UseSitemap,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, startCrawlResponse{JobID: jobID})
}

func (h *Handler) handleAdminCrawlJobs(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.jobs != nil, "crawl jobs") {
		return
	}
	jobs, err := h.jobs.ListCrawlJobs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handler) handleAdminCrawlJob(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.jobs != nil, "crawl jobs") {
		return
	}
	job, err := h.jobs.GetCrawlJob(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrCrawlJobNotFound, "crawl job not found", job)
}

// scheduledCrawlRequest is the wire shape for both creating a schedule
// (POST /admin/api/schedules) and replacing one's editable fields
// (PATCH /admin/api/schedules/{id}) -- deliberately no cookie/basic-auth
// fields, since a recurring schedule never stores credentials at rest.
type scheduledCrawlRequest struct {
	SeedURLs            []string `json:"seed_urls"`
	MaxPages            int      `json:"max_pages"`
	RespectRobots       bool     `json:"respect_robots"`
	UserAgent           string   `json:"user_agent"`
	AllowOffDomainLinks bool     `json:"allow_off_domain_links"`
	UseSitemap          bool     `json:"use_sitemap"`
	IntervalMinutes     int      `json:"interval_minutes"`
	Enabled             bool     `json:"enabled"`
}

type scheduledCrawlResponse struct {
	ID                  string     `json:"id"`
	SeedURLs            []string   `json:"seed_urls"`
	MaxPages            int        `json:"max_pages"`
	RespectRobots       bool       `json:"respect_robots"`
	UserAgent           string     `json:"user_agent"`
	AllowOffDomainLinks bool       `json:"allow_off_domain_links"`
	UseSitemap          bool       `json:"use_sitemap"`
	IntervalMinutes     int        `json:"interval_minutes"`
	Enabled             bool       `json:"enabled"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	NextRunAt           time.Time  `json:"next_run_at"`
	CreatedAt           time.Time  `json:"created_at"`
}

func toScheduledCrawlResponse(s domain.ScheduledCrawl) scheduledCrawlResponse {
	return scheduledCrawlResponse{
		ID: s.ID, SeedURLs: s.SeedURLs, MaxPages: s.MaxPages,
		RespectRobots: s.RespectRobots, UserAgent: s.UserAgent,
		AllowOffDomainLinks: s.AllowOffDomainLinks, UseSitemap: s.UseSitemap,
		IntervalMinutes: s.IntervalMinutes, Enabled: s.Enabled,
		LastRunAt: s.LastRunAt, NextRunAt: s.NextRunAt, CreatedAt: s.CreatedAt,
	}
}

// toScheduledCrawl builds the domain.ScheduledCrawl req describes -- shared
// by handleAdminSchedules' POST (a new schedule) and
// handleAdminUpdateSchedule (replacing an existing one's editable fields),
// since both otherwise build the identical seven fields from req by hand.
// next_run_at is always interval_minutes from now, the same rule a freshly
// created and a just-edited schedule both follow; created_at is only
// meaningful for a new schedule (UpdateScheduledCrawl's SQL never touches
// that column, so passing "now" there too is harmless).
func (req scheduledCrawlRequest) toScheduledCrawl(id string, enabled bool, now time.Time) domain.ScheduledCrawl {
	return domain.ScheduledCrawl{
		ID: id, SeedURLs: req.SeedURLs, MaxPages: req.MaxPages,
		RespectRobots: req.RespectRobots, UserAgent: req.UserAgent,
		AllowOffDomainLinks: req.AllowOffDomainLinks, UseSitemap: req.UseSitemap,
		IntervalMinutes: req.IntervalMinutes, Enabled: enabled,
		NextRunAt: now.Add(time.Duration(req.IntervalMinutes) * time.Minute),
		CreatedAt: now,
	}
}

func validateScheduledCrawlRequest(w http.ResponseWriter, req scheduledCrawlRequest) bool {
	if len(req.SeedURLs) == 0 {
		http.Error(w, "seed_urls must not be empty", http.StatusBadRequest)
		return false
	}
	if req.IntervalMinutes <= 0 {
		http.Error(w, "interval_minutes must be positive", http.StatusBadRequest)
		return false
	}
	return true
}

// handleAdminSchedules lists (GET) or creates (POST) recurring crawl
// schedules. A freshly created schedule is always enabled, with its first
// run interval_minutes from now -- the same rule editing an existing
// schedule's options applies (see handleAdminUpdateSchedule).
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

// handleAdminUpdateSchedule replaces a schedule's editable fields --
// seed(s), page budget, per-crawl options, interval and enabled flag.
// Editing reschedules it: next_run_at becomes interval_minutes from now,
// same as a freshly created schedule, rather than trying to preserve a
// stale cadence computed under the old interval.
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
	s := req.toScheduledCrawl(r.PathValue("id"), req.Enabled, time.Now().UTC())
	err := h.scheduledCrawls.UpdateScheduledCrawl(r.Context(), s)
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}

func (h *Handler) handleAdminDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.scheduledCrawls != nil, "scheduled crawls") {
		return
	}
	err := h.scheduledCrawls.DeleteScheduledCrawl(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrScheduledCrawlNotFound, "scheduled crawl not found", map[string]bool{"ok": true})
}
