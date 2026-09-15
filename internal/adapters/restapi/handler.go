package restapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

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

//go:embed admin_domain.html
var adminDomainHTML []byte

//go:embed admin_schedule.html
var adminScheduleHTML []byte

//go:embed admin_jobs.html
var adminJobsHTML []byte

//go:embed admin_settings.html
var adminSettingsHTML []byte

//go:embed admin_search.html
var adminSearchHTML []byte

//go:embed admin_search_result.html
var adminSearchResultHTML []byte

//go:embed admin_vocabulary_term.html
var adminVocabularyTermHTML []byte

//go:embed admin_pagerank.html
var adminPageRankHTML []byte

//go:embed admin_database.html
var adminDatabaseHTML []byte

//go:embed style.css
var styleCSS []byte

//go:embed admin.js
var adminJS []byte

// Every other admin_*.js/*.js below is a page's own script, extracted from
// what used to be an inline <script> block in its matching .html file --
// admin.js above is the one shared-helpers file every admin page loads
// alongside its own.

//go:embed admin_page.js
var adminPageJS []byte

//go:embed admin_database.js
var adminDatabaseJS []byte

//go:embed admin_documents.js
var adminDocumentsJS []byte

//go:embed admin_domain.js
var adminDomainJS []byte

//go:embed admin_pagerank.js
var adminPageRankJS []byte

//go:embed admin_schedule.js
var adminScheduleJS []byte

//go:embed admin_search.js
var adminSearchJS []byte

//go:embed admin_search_result.js
var adminSearchResultJS []byte

//go:embed admin_jobs.js
var adminJobsJS []byte

//go:embed admin_settings.js
var adminSettingsJS []byte

//go:embed admin_vocabulary_term.js
var adminVocabularyTermJS []byte

//go:embed admin_crawl.js
var adminCrawlJS []byte

//go:embed index.js
var indexJS []byte

//go:embed login.js
var loginJS []byte

type Handler struct {
	search          ports.SearchService
	crawler         ports.CrawlerService
	crawlJobs       ports.CrawlJobStore
	crawlSem        chan struct{}
	cancelMu        sync.Mutex
	cancelFuncs     map[string]context.CancelFunc
	jobs            ports.CrawlJobService
	debug           ports.DebugSearchService
	admin           ports.AdminRepository
	pageRank        ports.PageRankRepository
	settings        *domain.TuningSettings
	opSettings      *domain.OperationalSettings
	overrides       *domain.RankingOverrides
	settingsStore   ports.SettingsStore
	scheduledCrawls ports.ScheduledCrawlStore
	health          ports.HealthChecker
	onCrawlComplete func()
	dbDriver        string
	adminUser       string
	adminPass       string
	sessions        ports.SessionStore
}

// Config wires a Handler's dependencies. Crawler and CrawlJobs are used
// only by crawl-server (RoutesCrawlInternal); Jobs is used only by
// admin-server (RoutesAdmin), talking to crawl-server over the network.
// Debug, Admin, Settings, OperationalSettings, Overrides, SettingsStore,
// ScheduledCrawls, DBDriver, AdminUser and AdminPass are optional: without
// AdminUser/AdminPass configured, authentication fails closed (nobody can
// sign in, so /admin stays locked) rather than defaulting to open access.
// Without Debug/Admin/Settings/Overrides/Jobs/ScheduledCrawls, the
// corresponding admin endpoints report themselves unavailable. A nil
// OperationalSettings behaves like domain.DefaultOperationalSettings(), and
// a nil RankingOverrides like domain.DefaultRankingOverrides() (both via
// their nil-safe Get()). Without SettingsStore, an admin settings/overrides
// edit still applies to this process's own in-memory instance but isn't
// persisted for any other process to pick up. ScheduledCrawls is set on
// admin-server only (backing the schedules admin API) -- crawl-server's own
// scheduler ticker talks to the same store directly, not through Handler.
// PageRank, when set (admin-server only, its own *sqlrepo.Repository -- no
// need to proxy through crawl-server the way crawl-triggering does, since
// admin-server already has direct DB access), backs the PageRank debug
// page's "force recalculation" button; without it, that endpoint reports
// itself unavailable, same as the other optional dependencies.
// Health is set on every process to back GET /healthz; without it, /healthz
// always reports healthy (no DB connection to check). Sessions, when set
// (every production process passes its own *sqlrepo.Repository, which
// implements ports.SessionStore against a shared "sessions" table), makes a
// login recognized by every process serving the site, not just the one
// that issued it -- see ports.SessionStore's doc comment. Without it, New
// falls back to a private in-memory store, fine for tests but useless
// across real separate processes. OnCrawlComplete, when
// set (crawl-server only), is called synchronously right after a crawl job
// finishes successfully -- e.g. to trigger a PageRank recompute, since a
// completed crawl is exactly when the link graph changes. A caller that
// wants this to run without delaying the job's reported completion (or the
// concurrency semaphore's release -- see runCrawlJob) should spawn its own
// goroutine inside the callback; Handler itself makes no such decision.
type Config struct {
	Search          ports.SearchService
	Crawler         ports.CrawlerService
	CrawlJobs       ports.CrawlJobStore
	Jobs            ports.CrawlJobService
	Debug           ports.DebugSearchService
	Admin           ports.AdminRepository
	PageRank        ports.PageRankRepository
	Settings        *domain.TuningSettings
	OpSettings      *domain.OperationalSettings
	Overrides       *domain.RankingOverrides
	SettingsStore   ports.SettingsStore
	ScheduledCrawls ports.ScheduledCrawlStore
	Health          ports.HealthChecker
	Sessions        ports.SessionStore
	OnCrawlComplete func()
	DBDriver        string
	AdminUser       string
	AdminPass       string
}

func New(cfg Config) *Handler {
	sessions := cfg.Sessions
	if sessions == nil {
		sessions = newSessionStore()
	}
	return &Handler{
		search:          cfg.Search,
		crawler:         cfg.Crawler,
		crawlJobs:       cfg.CrawlJobs,
		crawlSem:        make(chan struct{}, maxConcurrentCrawls),
		cancelFuncs:     make(map[string]context.CancelFunc),
		jobs:            cfg.Jobs,
		debug:           cfg.Debug,
		admin:           cfg.Admin,
		pageRank:        cfg.PageRank,
		settings:        cfg.Settings,
		opSettings:      cfg.OpSettings,
		overrides:       cfg.Overrides,
		settingsStore:   cfg.SettingsStore,
		scheduledCrawls: cfg.ScheduledCrawls,
		health:          cfg.Health,
		onCrawlComplete: cfg.OnCrawlComplete,
		dbDriver:        cfg.DBDriver,
		adminUser:       cfg.AdminUser,
		adminPass:       cfg.AdminPass,
		sessions:        sessions,
	}
}

// RoutesSearch serves the public-facing search site only: the index page,
// its stylesheet, and the search API. No admin, login or crawl endpoints --
// this is the mux the internet-facing search-server binary listens with.
// The index page and the search API both require a signed-in session, the
// same one /admin and /login already use (the admin account is the only
// account this site has for now) -- style.css and healthz stay open so an
// unauthenticated visitor's redirect to /login still renders styled, and
// monitoring never needs to sign in.
func (h *Handler) RoutesSearch() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.requireAuthPage(h.handleIndex))
	mux.HandleFunc("/style.css", h.handleStyle)
	mux.HandleFunc("/index.js", h.handleIndexJS)
	mux.HandleFunc("/search", h.requireAuthAPI(h.handleSearch))
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

// RoutesAdmin serves login/session management plus every /admin and
// /admin/api/* route -- the mux the admin-server binary listens with,
// reachable only through a local nginx proxy, never directly.
func (h *Handler) RoutesAdmin() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin.js", h.handleAdminJS)
	mux.HandleFunc("/login", h.handleLoginRoute)
	mux.HandleFunc("/login.js", h.handleLoginJS)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/healthz", h.handleHealthz)

	mux.HandleFunc("/admin", h.requireAuthPage(h.handleAdminPage))
	mux.HandleFunc("/admin_page.js", h.handleAdminPageJS)
	mux.HandleFunc("/admin/documents", h.requireAuthPage(h.handleAdminDocumentsPage))
	mux.HandleFunc("/admin_documents.js", h.handleAdminDocumentsJS)
	mux.HandleFunc("/admin/documents/{host}", h.requireAuthPage(h.handleAdminDomainPage))
	mux.HandleFunc("/admin_domain.js", h.handleAdminDomainJS)
	mux.HandleFunc("/admin/vocabulary/term", h.requireAuthPage(h.handleAdminVocabularyTermPage))
	mux.HandleFunc("/admin_vocabulary_term.js", h.handleAdminVocabularyTermJS)
	mux.HandleFunc("/admin/crawl", h.requireAuthPage(h.handleAdminCrawlPage))
	mux.HandleFunc("/admin_crawl.js", h.handleAdminCrawlJS)
	mux.HandleFunc("/admin/schedule/{id}", h.requireAuthPage(h.handleAdminSchedulePage))
	mux.HandleFunc("/admin_schedule.js", h.handleAdminScheduleJS)
	mux.HandleFunc("/admin/jobs", h.requireAuthPage(h.handleAdminJobsPage))
	mux.HandleFunc("/admin_jobs.js", h.handleAdminJobsJS)
	mux.HandleFunc("/admin/settings", h.requireAuthPage(h.handleAdminSettingsPage))
	mux.HandleFunc("/admin_settings.js", h.handleAdminSettingsJS)
	mux.HandleFunc("/admin/search", h.requireAuthPage(h.handleAdminSearchPage))
	mux.HandleFunc("/admin_search.js", h.handleAdminSearchJS)
	mux.HandleFunc("/admin/search/result", h.requireAuthPage(h.handleAdminSearchResultPage))
	mux.HandleFunc("/admin_search_result.js", h.handleAdminSearchResultJS)
	mux.HandleFunc("/admin/pagerank", h.requireAuthPage(h.handleAdminPageRankPage))
	mux.HandleFunc("/admin_pagerank.js", h.handleAdminPageRankJS)
	mux.HandleFunc("/admin/database", h.requireAuthPage(h.handleAdminDatabasePage))
	mux.HandleFunc("/admin_database.js", h.handleAdminDatabaseJS)

	mux.HandleFunc("/admin/api/stats", h.requireAuthAPI(h.handleAdminStats))
	mux.HandleFunc("/admin/api/vocabulary", h.requireAuthAPI(h.handleAdminVocabulary))
	mux.HandleFunc("/admin/api/documents", h.requireAuthAPI(h.handleAdminDocuments))
	mux.HandleFunc("DELETE /admin/api/documents", h.requireAuthAPI(h.handleAdminDeleteDomainDocuments))
	mux.HandleFunc("GET /admin/api/documents/overview", h.requireAuthAPI(h.handleAdminDocumentsOverview))
	mux.HandleFunc("GET /admin/api/overview/metrics", h.requireAuthAPI(h.handleAdminOverviewMetrics))
	mux.HandleFunc("DELETE /admin/api/documents/{id}", h.requireAuthAPI(h.handleAdminDeleteDocument))
	mux.HandleFunc("GET /admin/api/documents/{id}/versions", h.requireAuthAPI(h.handleAdminDocumentVersions))
	mux.HandleFunc("/admin/api/domains", h.requireAuthAPI(h.handleAdminSearchDomains))
	mux.HandleFunc("/admin/api/postings", h.requireAuthAPI(h.handleAdminPostings))
	mux.HandleFunc("/admin/api/search", h.requireAuthAPI(h.handleAdminSearch))
	mux.HandleFunc("/admin/api/settings", h.requireAuthAPI(h.handleAdminSettings))
	mux.HandleFunc("/admin/api/overrides", h.requireAuthAPI(h.handleAdminOverrides))
	mux.HandleFunc("/admin/api/crawl/jobs", h.requireAuthAPI(h.handleAdminCrawlJobs))
	mux.HandleFunc("GET /admin/api/crawl/jobs/{id}", h.requireAuthAPI(h.handleAdminCrawlJob))
	mux.HandleFunc("POST /admin/api/crawl/jobs/{id}/cancel", h.requireAuthAPI(h.handleAdminCancelCrawlJob))
	mux.HandleFunc("/admin/api/schedules", h.requireAuthAPI(h.handleAdminSchedules))
	mux.HandleFunc("GET /admin/api/schedules/{id}", h.requireAuthAPI(h.handleAdminGetSchedule))
	mux.HandleFunc("DELETE /admin/api/schedules/{id}", h.requireAuthAPI(h.handleAdminDeleteSchedule))
	mux.HandleFunc("PATCH /admin/api/schedules/{id}", h.requireAuthAPI(h.handleAdminUpdateSchedule))
	mux.HandleFunc("POST /admin/api/schedules/{id}/run", h.requireAuthAPI(h.handleAdminRunScheduleNow))
	mux.HandleFunc("GET /admin/api/pagerank", h.requireAuthAPI(h.handleAdminPageRank))
	mux.HandleFunc("POST /admin/api/pagerank/recompute", h.requireAuthAPI(h.handleAdminPageRankRecompute))
	mux.HandleFunc("GET /admin/api/database", h.requireAuthAPI(h.handleAdminDatabase))
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

func (h *Handler) handleIndexJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", indexJS)
}

func (h *Handler) handleLoginJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", loginJS)
}

func (h *Handler) handleAdminPageJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminPageJS)
}

func (h *Handler) handleAdminDatabaseJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminDatabaseJS)
}

func (h *Handler) handleAdminDocumentsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminDocumentsJS)
}

func (h *Handler) handleAdminDomainJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminDomainJS)
}

func (h *Handler) handleAdminPageRankJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminPageRankJS)
}

func (h *Handler) handleAdminScheduleJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminScheduleJS)
}

func (h *Handler) handleAdminSearchJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminSearchJS)
}

func (h *Handler) handleAdminSearchResultJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminSearchResultJS)
}

func (h *Handler) handleAdminJobsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminJobsJS)
}

func (h *Handler) handleAdminSettingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminSettingsJS)
}

func (h *Handler) handleAdminVocabularyTermJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminVocabularyTermJS)
}

func (h *Handler) handleAdminCrawlJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminCrawlJS)
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
	topK := intQueryParam(r, "top_k", h.opSettings.Get().DefaultTopK, false)

	results, err := h.search.Search(r.Context(), query, ports.SearchQuery{TopK: topK, Sort: parseSortParam(r)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{Query: query, Results: results})
}

// parseSortParam reads the ?sort= query parameter, defaulting to
// (and falling back to, for anything unrecognized) relevance ranking --
// shared by the public /search and admin debug /admin/api/search endpoints.
func parseSortParam(r *http.Request) string {
	if r.URL.Query().Get("sort") == ports.SortRecency {
		return ports.SortRecency
	}
	return ports.SortRelevance
}

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz is a minimal, unauthenticated liveness endpoint for
// automated monitoring/systemd -- registered identically (and without going
// through requireAuthAPI) on RoutesSearch, RoutesAdmin and
// RoutesCrawlInternal. With no HealthChecker configured it reports healthy
// unconditionally; otherwise it reports 503 the moment the cheap DB ping
// fails.
func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.health != nil {
		if err := h.health.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
