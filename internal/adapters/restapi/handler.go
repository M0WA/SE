package restapi

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"

	"searchengine/internal/domain"
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

//go:embed admin_documents.html
var adminDocumentsHTML []byte

//go:embed admin_tuning.html
var adminTuningHTML []byte

//go:embed admin_search.html
var adminSearchHTML []byte

//go:embed admin_overrides.html
var adminOverridesHTML []byte

//go:embed style.css
var styleCSS []byte

//go:embed admin.js
var adminJS []byte

type Handler struct {
	search     ports.SearchService
	crawler    ports.CrawlerService
	crawlJobs  *domain.CrawlJobStore
	crawlSem   chan struct{}
	jobs       ports.CrawlJobService
	debug      ports.DebugSearchService
	admin      ports.AdminRepository
	settings   *domain.TuningSettings
	opSettings *domain.OperationalSettings
	overrides  *domain.RankingOverrides
	dbDriver   string
	adminUser  string
	adminPass  string
	sessions   *sessionStore
}

// Config wires a Handler's dependencies. Crawler and CrawlJobs are used
// only by crawl-server (RoutesCrawlInternal); Jobs is used only by
// admin-server (RoutesAdmin), talking to crawl-server over the network.
// Debug, Admin, Settings, OperationalSettings, Overrides, DBDriver,
// AdminUser and AdminPass are optional: without AdminUser/AdminPass
// configured, authentication fails closed (nobody can sign in, so /admin
// stays locked) rather than defaulting to open access. Without
// Debug/Admin/Settings/Overrides/Jobs, the corresponding admin endpoints
// report themselves unavailable. A nil OperationalSettings behaves like
// domain.DefaultOperationalSettings(), and a nil RankingOverrides like
// domain.DefaultRankingOverrides() (both via their nil-safe Get()).
type Config struct {
	Search     ports.SearchService
	Crawler    ports.CrawlerService
	CrawlJobs  *domain.CrawlJobStore
	Jobs       ports.CrawlJobService
	Debug      ports.DebugSearchService
	Admin      ports.AdminRepository
	Settings   *domain.TuningSettings
	OpSettings *domain.OperationalSettings
	Overrides  *domain.RankingOverrides
	DBDriver   string
	AdminUser  string
	AdminPass  string
}

func New(cfg Config) *Handler {
	return &Handler{
		search:     cfg.Search,
		crawler:    cfg.Crawler,
		crawlJobs:  cfg.CrawlJobs,
		crawlSem:   make(chan struct{}, maxConcurrentCrawls),
		jobs:       cfg.Jobs,
		debug:      cfg.Debug,
		admin:      cfg.Admin,
		settings:   cfg.Settings,
		opSettings: cfg.OpSettings,
		overrides:  cfg.Overrides,
		dbDriver:   cfg.DBDriver,
		adminUser:  cfg.AdminUser,
		adminPass:  cfg.AdminPass,
		sessions:   newSessionStore(),
	}
}

// RoutesSearch serves the public-facing search site only: the index page,
// its stylesheet, and the search API. No admin, login or crawl endpoints --
// this is the mux the internet-facing search-server binary listens with.
func (h *Handler) RoutesSearch() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/style.css", h.handleStyle)
	mux.HandleFunc("/search", h.handleSearch)
	return mux
}

// RoutesAdmin serves login/session management plus every /admin and
// /admin/api/* route -- the mux the admin-server binary listens with,
// reachable only through a local nginx proxy, never directly.
func (h *Handler) RoutesAdmin() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin.js", h.handleAdminJS)
	mux.HandleFunc("/login", h.handleLoginRoute)
	mux.HandleFunc("/logout", h.handleLogout)

	mux.HandleFunc("/admin", h.requireAuthPage(h.handleAdminPage))
	mux.HandleFunc("/admin/documents", h.requireAuthPage(h.handleAdminDocumentsPage))
	mux.HandleFunc("/admin/crawl", h.requireAuthPage(h.handleAdminCrawlPage))
	mux.HandleFunc("/admin/tuning", h.requireAuthPage(h.handleAdminTuningPage))
	mux.HandleFunc("/admin/search", h.requireAuthPage(h.handleAdminSearchPage))
	mux.HandleFunc("/admin/overrides", h.requireAuthPage(h.handleAdminOverridesPage))

	mux.HandleFunc("/admin/api/stats", h.requireAuthAPI(h.handleAdminStats))
	mux.HandleFunc("/admin/api/vocabulary", h.requireAuthAPI(h.handleAdminVocabulary))
	mux.HandleFunc("/admin/api/documents", h.requireAuthAPI(h.handleAdminDocuments))
	mux.HandleFunc("DELETE /admin/api/documents/{id}", h.requireAuthAPI(h.handleAdminDeleteDocument))
	mux.HandleFunc("/admin/api/postings", h.requireAuthAPI(h.handleAdminPostings))
	mux.HandleFunc("/admin/api/search", h.requireAuthAPI(h.handleAdminSearch))
	mux.HandleFunc("/admin/api/settings", h.requireAuthAPI(h.handleAdminSettings))
	mux.HandleFunc("/admin/api/overrides", h.requireAuthAPI(h.handleAdminOverrides))
	mux.HandleFunc("/admin/api/crawl", h.requireAuthAPI(h.handleAdminCrawl))
	mux.HandleFunc("/admin/api/crawl/jobs", h.requireAuthAPI(h.handleAdminCrawlJobs))
	mux.HandleFunc("GET /admin/api/crawl/jobs/{id}", h.requireAuthAPI(h.handleAdminCrawlJob))
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
	serveStatic(w, r, "text/css; charset=utf-8", styleCSS)
}

func (h *Handler) handleAdminJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminJS)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveStatic(w, r, "text/html; charset=utf-8", indexHTML)
}

// serveStatic answers a GET/HEAD request with a fixed, embedded payload --
// the entirety of every static asset handler (HTML pages, style.css,
// admin.js) except for how they're addressed and what content type/bytes
// they serve.
func serveStatic(w http.ResponseWriter, r *http.Request, contentType string, content []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(content)
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
	topK := h.opSettings.Get().DefaultTopK
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
