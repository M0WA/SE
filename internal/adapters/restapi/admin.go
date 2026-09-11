package restapi

import (
	"encoding/json"
	"errors"
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
	limit := defaultVocabularyTopTermsLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
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
}

// handleAdminDocuments lists indexed pages, optionally narrowed to one
// domain via ?domain= (used by the per-domain admin subpage) -- without
// that filter it's the whole corpus, still capped at limit.
func (h *Handler) handleAdminDocuments(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.admin != nil, "admin diagnostics") {
		return
	}
	limit := defaultDocumentListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
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
	limit := defaultDomainSearchLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
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
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, ports.ErrDocumentNotFound):
		http.Error(w, "document not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
}

func (h *Handler) handleAdminSearch(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireConfigured(w, h.debug != nil, "search debugging") {
		return
	}
	query := r.URL.Query().Get("q")
	topK := h.opSettings.Get().DefaultTopK
	if v := r.URL.Query().Get("top_k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			topK = n
		}
	}
	results, err := h.debug.Search(r.Context(), query, topK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := make([]adminDebugResult, len(results))
	for i, res := range results {
		out[i] = adminDebugResult{
			DocID: res.DocID, URL: res.URL, Title: res.Title, Snippet: res.Snippet,
			BM25Score: res.BM25Score, SemanticSim: res.SemanticSim, FinalScore: res.FinalScore,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type tuningValues struct {
	Alpha float64 `json:"alpha"`
	K1    float64 `json:"k1"`
	B     float64 `json:"b"`
}

// operationalValues mirrors domain.OperationalSettingsValues for the wire
// format: durations as whole seconds/hours, which are friendlier for an
// admin form (and JSON) than Go's time.Duration nanosecond encoding.
type operationalValues struct {
	FetchTimeoutSeconds int    `json:"fetch_timeout_seconds"`
	UserAgent           string `json:"user_agent"`
	DefaultMaxPages     int    `json:"default_max_pages"`
	MinTextLength       int    `json:"min_text_length"`
	DefaultTopK         int    `json:"default_top_k"`
	SessionTTLHours     int    `json:"session_ttl_hours"`
}

func toOperationalValues(v domain.OperationalSettingsValues) operationalValues {
	return operationalValues{
		FetchTimeoutSeconds: int(v.FetchTimeout / time.Second),
		UserAgent:           v.UserAgent,
		DefaultMaxPages:     v.DefaultMaxPages,
		MinTextLength:       v.MinTextLength,
		DefaultTopK:         v.DefaultTopK,
		SessionTTLHours:     int(v.SessionTTL / time.Hour),
	}
}

func (o operationalValues) toSettingsValues() domain.OperationalSettingsValues {
	return domain.OperationalSettingsValues{
		FetchTimeout:    time.Duration(o.FetchTimeoutSeconds) * time.Second,
		UserAgent:       o.UserAgent,
		DefaultMaxPages: o.DefaultMaxPages,
		MinTextLength:   o.MinTextLength,
		DefaultTopK:     o.DefaultTopK,
		SessionTTL:      time.Duration(o.SessionTTLHours) * time.Hour,
	}
}

type settingsResponse struct {
	Tuning      tuningValues      `json:"tuning"`
	Operational operationalValues `json:"operational"`
}

func (h *Handler) currentSettings() settingsResponse {
	alpha, k1, b := h.settings.Get()
	return settingsResponse{
		Tuning:      tuningValues{Alpha: alpha, K1: k1, B: b},
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
		h.opSettings.Set(req.Operational.toSettingsValues())
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
		writeJSON(w, http.StatusOK, h.currentOverrides())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type crawlRequest struct {
	SeedURLs      []string `json:"seed_urls"`
	MaxPages      int      `json:"max_pages"`
	Cookie        string   `json:"cookie"`
	BasicAuthUser string   `json:"basic_auth_user"`
	BasicAuthPass string   `json:"basic_auth_pass"`
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
		SeedURLs:      req.SeedURLs,
		MaxPages:      req.MaxPages,
		Cookie:        req.Cookie,
		BasicAuthUser: req.BasicAuthUser,
		BasicAuthPass: req.BasicAuthPass,
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
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, job)
	case errors.Is(err, ports.ErrCrawlJobNotFound):
		http.Error(w, "crawl job not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
