package restapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"searchengine/internal/ports"
)

const defaultDocumentListLimit = 100

func servePage(w http.ResponseWriter, r *http.Request, page []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(page)
}

func (h *Handler) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, adminHTML)
}

func (h *Handler) handleAdminDocumentsPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, adminDocumentsHTML)
}

func (h *Handler) handleAdminTuningPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, adminTuningHTML)
}

func (h *Handler) handleAdminSearchPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, adminSearchHTML)
}

func (h *Handler) handleAdminCrawlPage(w http.ResponseWriter, r *http.Request) {
	servePage(w, r, crawlHTML)
}

type adminStatsResponse struct {
	Driver    string  `json:"driver"`
	TotalDocs int     `json:"total_docs"`
	AvgDocLen float64 `json:"avg_doc_len"`
}

func (h *Handler) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.admin == nil {
		http.Error(w, "admin diagnostics not configured", http.StatusServiceUnavailable)
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

type adminDocument struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	DocLength int    `json:"doc_length"`
}

func (h *Handler) handleAdminDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.admin == nil {
		http.Error(w, "admin diagnostics not configured", http.StatusServiceUnavailable)
		return
	}
	limit := defaultDocumentListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	docs, err := h.admin.ListDocuments(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]adminDocument, len(docs))
	for i, d := range docs {
		out[i] = adminDocument{ID: d.ID, URL: d.URL, Title: d.Title, DocLength: d.DocLength}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminDeleteDocument is registered on the Go 1.22+ pattern
// "DELETE /admin/api/documents/{id}", since a wildcard path segment is
// exactly what that routing style is for.
func (h *Handler) handleAdminDeleteDocument(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin diagnostics not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "document id must not be empty", http.StatusBadRequest)
		return
	}
	err := h.admin.DeleteDocument(r.Context(), id)
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.admin == nil {
		http.Error(w, "admin diagnostics not configured", http.StatusServiceUnavailable)
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.debug == nil {
		http.Error(w, "search debugging not configured", http.StatusServiceUnavailable)
		return
	}
	query := r.URL.Query().Get("q")
	topK := 10
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

type settingsResponse struct {
	Alpha float64 `json:"alpha"`
	K1    float64 `json:"k1"`
	B     float64 `json:"b"`
}

func (h *Handler) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		http.Error(w, "tuning not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		alpha, k1, b := h.settings.Get()
		writeJSON(w, http.StatusOK, settingsResponse{Alpha: alpha, K1: k1, B: b})
	case http.MethodPost:
		var req settingsResponse
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		h.settings.Set(req.Alpha, req.K1, req.B)
		alpha, k1, b := h.settings.Get()
		writeJSON(w, http.StatusOK, settingsResponse{Alpha: alpha, K1: k1, B: b})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type crawlRequest struct {
	SeedURLs []string `json:"seed_urls"`
	MaxPages int      `json:"max_pages"`
}

type crawlResponse struct {
	CrawledCount int `json:"crawled_count"`
}

func (h *Handler) handleAdminCrawl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

	count, err := h.crawler.Crawl(r.Context(), req.SeedURLs, req.MaxPages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, crawlResponse{CrawledCount: count})
}
