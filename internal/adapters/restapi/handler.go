package restapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"searchengine/internal/ports"
)

type Handler struct {
	search  ports.SearchService
	crawler ports.CrawlerService
}

func New(search ports.SearchService, crawler ports.CrawlerService) *Handler {
	return &Handler{search: search, crawler: crawler}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", h.handleSearch)
	mux.HandleFunc("/crawl", h.handleCrawl)
	return mux
}

type searchResponse struct {
	Query   string      `json:"query"`
	Results interface{} `json:"results"`
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query().Get("q")
	topK := 10
	if v := r.URL.Query().Get("top_k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			topK = n
		}
	}

	results, err := h.search.Search(r.Context(), query, topK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{Query: query, Results: results})
}

type crawlRequest struct {
	SeedURLs []string `json:"seed_urls"`
	MaxPages int      `json:"max_pages"`
}

type crawlResponse struct {
	CrawledCount int `json:"crawled_count"`
}

func (h *Handler) handleCrawl(w http.ResponseWriter, r *http.Request) {
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
