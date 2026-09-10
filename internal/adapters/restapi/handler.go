package restapi

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"

	"searchengine/internal/ports"
)

//go:embed index.html
var indexHTML []byte

//go:embed crawl.html
var crawlHTML []byte

//go:embed login.html
var loginHTML []byte

//go:embed admin.html
var adminHTML []byte

//go:embed style.css
var styleCSS []byte

type Handler struct {
	search    ports.SearchService
	crawler   ports.CrawlerService
	debug     ports.DebugSearchService
	admin     ports.AdminRepository
	dbDriver  string
	adminUser string
	adminPass string
	sessions  *sessionStore
}

// Config wires a Handler's dependencies. Debug, Admin, DBDriver, AdminUser
// and AdminPass are optional: without AdminUser/AdminPass configured,
// authentication fails closed (nobody can sign in, so /crawl and /admin
// stay locked) rather than defaulting to open access. Without Debug/Admin,
// the admin diagnostics endpoints report themselves unavailable.
type Config struct {
	Search    ports.SearchService
	Crawler   ports.CrawlerService
	Debug     ports.DebugSearchService
	Admin     ports.AdminRepository
	DBDriver  string
	AdminUser string
	AdminPass string
}

func New(cfg Config) *Handler {
	return &Handler{
		search:    cfg.Search,
		crawler:   cfg.Crawler,
		debug:     cfg.Debug,
		admin:     cfg.Admin,
		dbDriver:  cfg.DBDriver,
		adminUser: cfg.AdminUser,
		adminPass: cfg.AdminPass,
		sessions:  newSessionStore(),
	}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/style.css", h.handleStyle)
	mux.HandleFunc("/search", h.handleSearch)
	mux.HandleFunc("/crawl", h.requireAuthCrawl(h.handleCrawl))
	mux.HandleFunc("/login", h.handleLoginRoute)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/admin", h.requireAuthPage(h.handleAdminPage))
	mux.HandleFunc("/admin/api/stats", h.requireAuthAPI(h.handleAdminStats))
	mux.HandleFunc("/admin/api/documents", h.requireAuthAPI(h.handleAdminDocuments))
	mux.HandleFunc("/admin/api/postings", h.requireAuthAPI(h.handleAdminPostings))
	mux.HandleFunc("/admin/api/search", h.requireAuthAPI(h.handleAdminSearch))
	return mux
}

func (h *Handler) handleLoginRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.handleLogin(w, r)
		return
	}
	h.handleLoginPage(w, r)
}

func (h *Handler) handleStyle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(styleCSS)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(indexHTML)
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
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write(crawlHTML)
		}
		return
	}
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
