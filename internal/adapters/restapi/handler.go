package restapi

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"searchengine/internal/application"
	"searchengine/internal/bootstrap"
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

//go:embed admin_chat_settings.html
var adminChatSettingsHTML []byte

//go:embed admin_mcp_servers.html
var adminMCPServersHTML []byte

//go:embed admin_mcp_server.html
var adminMCPServerHTML []byte

//go:embed admin_agents.html
var adminAgentsHTML []byte

//go:embed admin_agent.html
var adminAgentHTML []byte

//go:embed admin_search.html
var adminSearchHTML []byte

//go:embed admin_search_result.html
var adminSearchResultHTML []byte

//go:embed admin_vocabulary_term.html
var adminVocabularyTermHTML []byte

//go:embed admin_pagerank.html
var adminPageRankHTML []byte

//go:embed admin_embeddings.html
var adminEmbeddingsHTML []byte

//go:embed admin_embedding_endpoints.html
var adminEmbeddingEndpointsHTML []byte

//go:embed admin_embedding_endpoint.html
var adminEmbeddingEndpointHTML []byte

//go:embed admin_database.html
var adminDatabaseHTML []byte

//go:embed admin_content_dedup.html
var adminContentDedupHTML []byte

//go:embed admin_users.html
var adminUsersHTML []byte

//go:embed admin_user.html
var adminUserHTML []byte

//go:embed account.html
var accountHTML []byte

//go:embed account_mcp_servers.html
var accountMCPServersHTML []byte

//go:embed account_mcp_server.html
var accountMCPServerHTML []byte

//go:embed account_files.html
var accountFilesHTML []byte

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

//go:embed admin_content_dedup.js
var adminContentDedupJS []byte

//go:embed admin_documents.js
var adminDocumentsJS []byte

//go:embed admin_domain.js
var adminDomainJS []byte

//go:embed admin_pagerank.js
var adminPageRankJS []byte

//go:embed admin_embeddings.js
var adminEmbeddingsJS []byte

//go:embed admin_embedding_endpoints.js
var adminEmbeddingEndpointsJS []byte

//go:embed admin_embedding_endpoint.js
var adminEmbeddingEndpointJS []byte

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

//go:embed admin_chat_settings.js
var adminChatSettingsJS []byte

//go:embed admin_mcp_servers.js
var adminMCPServersJS []byte

//go:embed admin_mcp_server.js
var adminMCPServerJS []byte

//go:embed admin_agents.js
var adminAgentsJS []byte

//go:embed admin_agent.js
var adminAgentJS []byte

//go:embed admin_vocabulary_term.js
var adminVocabularyTermJS []byte

//go:embed admin_crawl.js
var adminCrawlJS []byte

//go:embed admin_users.js
var adminUsersJS []byte

//go:embed admin_user.js
var adminUserJS []byte

//go:embed account.js
var accountJS []byte

//go:embed account_mcp_servers.js
var accountMCPServersJS []byte

//go:embed account_mcp_server.js
var accountMCPServerJS []byte

//go:embed account_files.js
var accountFilesJS []byte

//go:embed index.js
var indexJS []byte

//go:embed login.js
var loginJS []byte

type Handler struct {
	search        ports.SearchService
	crawler       ports.CrawlerService
	crawlJobs     ports.CrawlJobStore
	crawlSem      *crawlConcurrencySemaphore
	cancelMu      sync.Mutex
	cancelFuncs   map[string]context.CancelFunc
	jobs          ports.CrawlJobService
	debug         ports.DebugSearchService
	admin         ports.AdminRepository
	pageRank      ports.PageRankRepository
	embeddingRepo ports.EmbeddingRepository
	// contentDedupRepo, when set (admin-server only, its own
	// *sqlrepo.Repository -- same reasoning as pageRank/embeddingRepo
	// above), backs the content-dedup admin page's status/recompute
	// endpoints; without it, those endpoints report themselves unavailable.
	contentDedupRepo ports.ContentDedupRepository
	embedders        map[string]ports.EmbeddingProvider
	settings         *domain.TuningSettings
	opSettings       *domain.OperationalSettings
	overrides        *domain.RankingOverrides
	settingsStore    ports.SettingsStore
	scheduledCrawls  ports.ScheduledCrawlStore
	// embeddingEndpoints backs the admin API's HTTP embedding endpoint CRUD
	// (GET/POST/PATCH/DELETE /admin/api/embeddings/endpoints...) -- set on
	// admin-server only, the same *sqlrepo.Repository ScheduledCrawls uses.
	embeddingEndpoints ports.EmbeddingEndpointStore
	// chat backs the public POST /chat endpoint -- set on search-server
	// only, nil (and 503-reporting) everywhere else.
	chat *application.ChatService
	// chatEndpoints backs the admin API's chat endpoint config CRUD
	// (GET/PATCH /admin/api/chat-endpoint) -- set on admin-server only,
	// the same *sqlrepo.Repository embeddingEndpoints/scheduledCrawls use.
	chatEndpoints ports.ChatEndpointStore
	// mcpServers backs the admin API's MCP server CRUD
	// (GET/POST /admin/api/mcp-servers, GET/PATCH/DELETE
	// /admin/api/mcp-servers/{id}) -- set on admin-server only, the same
	// *sqlrepo.Repository chatEndpoints/embeddingEndpoints use.
	mcpServers ports.MCPServerStore
	// mcpTools backs the admin API's "list tools" connectivity test
	// (POST /admin/api/mcp-servers/test) -- set on admin-server only, the
	// same mcpclient.Provider search-server's ChatService uses for real
	// chat turns, just invoked here against a not-yet-saved candidate
	// config instead.
	mcpTools ports.MCPToolProvider
	// agents backs the admin API's agent CRUD (GET/POST /admin/api/agents,
	// GET/PATCH/DELETE /admin/api/agents/{id}) -- set on admin-server only,
	// the same *sqlrepo.Repository chatEndpoints/mcpServers use.
	agents ports.AgentStore
	// users backs the admin API's regular-user-account CRUD (GET/POST
	// /admin/api/users, PATCH/DELETE /admin/api/users/{id}) and
	// handleLogin's DB-backed-account lookup on admin-server, PLUS (the
	// same *sqlrepo.Repository) search-server's own self-service /account
	// routes and handleChat's per-user custom-prompt lookup -- see
	// account.go/chat.go's userCustomPromptFor. nil is valid (no
	// regular-user accounts exist; login checks only the hardcoded admin,
	// /account reports itself unavailable) for a Handler that never sets
	// it, e.g. crawl-server.
	// userMCPServers backs the self-service MCP server CRUD
	// (/account/api/mcp-servers...) -- set on search-server only, the same
	// *sqlrepo.Repository users uses. ChatService gets its own separate
	// reference to the same store (wired directly in cmd/search's main, not
	// through Handler) for merging a caller's own servers into a chat turn.
	userMCPServers ports.UserMCPServerStore
	// files backs the self-service file upload/list/download/delete API
	// (/account/api/files...) -- set on search-server only, the same
	// *sqlrepo.Repository userMCPServers uses. fileTokens is always
	// non-nil once New runs (unlike files, which is nil-safe/optional):
	// minting a token costs nothing when files itself isn't configured,
	// since requireConfigured on the /account/api/files handlers refuses
	// the request before a token would ever be validated.
	files           ports.FileStore
	fileTokens      *fileTokenStore
	users           ports.UserStore
	health          ports.HealthChecker
	onCrawlComplete func()
	dbDriver        string
	adminUser       string
	adminPass       string
	sessions        ports.SessionStore
	loginLimiter    *loginLimiter
	// crawlInternalToken, when set, is the shared secret
	// requireCrawlInternalToken checks RoutesCrawlInternal callers
	// against -- see its doc comment. Meaningless on RoutesSearch/
	// RoutesAdmin, which never use it.
	crawlInternalToken string
	// settingsEncryptionKey, when set, is the key the embedding endpoint
	// CRUD handlers encrypt each domain.EmbeddingHTTPEndpoint.APIKey with
	// before persisting it -- see settingscrypto's package doc comment.
	settingsEncryptionKey []byte
	// newEmbedder builds a throwaway ports.EmbeddingProvider from a given
	// candidate endpoint config -- always bootstrap.NewHTTPEmbedder in
	// production (see New), overridden by tests so testEmbeddingConnectivity
	// never makes a real network call from the test suite.
	newEmbedder func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider
	// chatModelProber is the shared ports.ChatCompleter used to auto-detect
	// a chat endpoint's own advertised max context length (see
	// handleAdminChatEndpoint's PATCH branch) -- always
	// bootstrap.NewHTTPChatCompleter() in production (see New), overridden
	// by tests so that auto-detection never makes a real network call from
	// the test suite. nil is valid (detection is skipped, same as any
	// other best-effort failure) for a Handler that never sets it (e.g.
	// crawl-server, which never serves chat-endpoint admin routes at all).
	chatModelProber ports.ChatCompleter
	// internalSearchAPIKey, when set, is an optional pre-shared key letting
	// a trusted local caller (e.g. a SearXNG engine plugin querying this
	// instance's own index as just another search engine) call /search
	// without a browser session, via requireAuthAPIOrInternalKey -- see its
	// doc comment. Empty by default, meaning the bypass does not exist at
	// all: /search stays session-cookie-only, exactly as before this field
	// existed.
	internalSearchAPIKey string
}

// Config wires a Handler's dependencies. Crawler/CrawlJobs are used only
// by crawl-server; Jobs only by admin-server. Most fields are optional:
// without AdminUser/AdminPass, auth fails closed; without a given
// repository/store, its admin endpoints report unavailable rather than
// erroring. See each field's own comment for specifics.
type Config struct {
	Search    ports.SearchService
	Crawler   ports.CrawlerService
	CrawlJobs ports.CrawlJobStore
	Jobs      ports.CrawlJobService
	Debug     ports.DebugSearchService
	Admin     ports.AdminRepository
	// PageRank, set on admin-server only, backs the PageRank debug page's
	// "force recalculation" button.
	PageRank ports.PageRankRepository
	// EmbeddingRepo, set on admin-server only, backs the Settings page's
	// "recompute embeddings" button. Embedders (the same map
	// bootstrap.NewEmbedders built at startup) is every enabled provider
	// this recompute refreshes, not just whichever is active for search.
	EmbeddingRepo ports.EmbeddingRepository
	Embedders     map[string]ports.EmbeddingProvider
	// ContentDedupRepo, set on admin-server only, backs the content-dedup
	// admin page's status display and "recompute now" button.
	ContentDedupRepo ports.ContentDedupRepository
	// NewEmbedder builds a throwaway ports.EmbeddingProvider from a given
	// candidate endpoint config, used by the embedding endpoint CRUD
	// handlers to test-probe a base URL/model/API key combination before
	// it's saved (see testEmbeddingConnectivity). Defaults to
	// bootstrap.NewHTTPEmbedder when nil -- tests override this to avoid a
	// real network call.
	NewEmbedder func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider
	// ChatModelProber, when set, is used to auto-detect a chat endpoint's
	// own advertised max context length when it's saved with
	// MaxContextTokens left unset (see handleAdminChatEndpoint's PATCH
	// branch) -- defaults to bootstrap.NewHTTPChatCompleter() when nil on
	// admin-server; left nil (skipping auto-detection entirely, same as
	// any other best-effort failure) on crawl-server, which never serves
	// this route.
	ChatModelProber ports.ChatCompleter
	Settings        *domain.TuningSettings
	OpSettings      *domain.OperationalSettings
	Overrides       *domain.RankingOverrides
	SettingsStore   ports.SettingsStore
	// ScheduledCrawls is set on admin-server only (backing the schedules
	// admin API) -- crawl-server's own ticker talks to the same store
	// directly, not through Handler.
	ScheduledCrawls ports.ScheduledCrawlStore
	// EmbeddingEndpoints is set on admin-server only, backing the HTTP
	// embedding endpoint CRUD API -- the same *sqlrepo.Repository
	// ScheduledCrawls uses.
	EmbeddingEndpoints ports.EmbeddingEndpointStore
	// Chat is set on search-server only, backing the public POST /chat
	// endpoint.
	Chat *application.ChatService
	// ChatEndpoints is set on admin-server only, backing the chat endpoint
	// config CRUD API -- the same *sqlrepo.Repository EmbeddingEndpoints
	// uses.
	ChatEndpoints ports.ChatEndpointStore
	// MCPServers is set on admin-server only, backing the MCP server CRUD
	// API -- the same *sqlrepo.Repository ChatEndpoints/EmbeddingEndpoints
	// uses.
	MCPServers ports.MCPServerStore
	// MCPTools is set on admin-server only, backing the "list tools"
	// connectivity test (POST /admin/api/mcp-servers/test) -- the same
	// mcpclient.Provider search-server's ChatService uses for real chat
	// turns.
	MCPTools ports.MCPToolProvider
	// Agents is set on admin-server only, backing the agent CRUD API -- the
	// same *sqlrepo.Repository ChatEndpoints/MCPServers uses.
	Agents ports.AgentStore
	// Users is set on admin-server (backing the regular-user-account CRUD
	// API and handleLogin's DB-backed-account lookup) AND search-server
	// (backing the self-service /account routes and handleChat's per-user
	// custom-prompt lookup) -- the same *sqlrepo.Repository
	// ChatEndpoints/MCPServers uses.
	Users ports.UserStore
	// UserMCPServers is set on search-server only, backing the self-service
	// MCP server CRUD API (/account/api/mcp-servers...) -- the same
	// *sqlrepo.Repository Users uses.
	UserMCPServers ports.UserMCPServerStore
	// Files is set on search-server only, backing the self-service file
	// upload/list/download/delete API (/account/api/files...) -- the same
	// *sqlrepo.Repository UserMCPServers uses.
	Files ports.FileStore
	// Health backs GET /healthz on every process; unset always reports
	// healthy (no DB connection to check).
	Health ports.HealthChecker
	// Sessions, when set (every production process, via a shared
	// "sessions" table), makes a login recognized by every process, not
	// just the one that issued it.
	Sessions ports.SessionStore
	// OnCrawlComplete, set on crawl-server only, runs synchronously right
	// after a crawl job finishes -- a caller wanting this to not delay the
	// job's reported completion should spawn its own goroutine inside it.
	OnCrawlComplete func()
	DBDriver        string
	AdminUser       string
	AdminPass       string
	// CrawlInternalToken, when set, is the shared secret
	// requireCrawlInternalToken enforces on RoutesCrawlInternal (checked
	// against every caller's X-Internal-Token header) and
	// internal/adapters/crawlclient.Client sends on every request --
	// see requireCrawlInternalToken's doc comment for why this exists
	// and why it's opt-in.
	CrawlInternalToken string
	// SettingsEncryptionKey, when set (see settingscrypto.ParseKey), is
	// the key the embedding endpoint CRUD handlers encrypt each
	// domain.EmbeddingHTTPEndpoint.APIKey with before persisting it.
	// Meaningless on crawl-server/search-server, which never call those
	// handlers.
	SettingsEncryptionKey []byte
	// InternalSearchAPIKey, when set, lets a trusted local caller (e.g. a
	// SearXNG engine plugin) call the public /search endpoint via the
	// X-Internal-API-Key header instead of a session cookie -- see
	// requireAuthAPIOrInternalKey's doc comment. Empty by default, meaning
	// the bypass does not exist at all.
	InternalSearchAPIKey string
}

func New(cfg Config) *Handler {
	sessions := cfg.Sessions
	if sessions == nil {
		sessions = newSessionStore()
	}
	newEmbedder := cfg.NewEmbedder
	if newEmbedder == nil {
		newEmbedder = bootstrap.NewHTTPEmbedder
	}
	chatModelProber := cfg.ChatModelProber
	if chatModelProber == nil {
		chatModelProber = bootstrap.NewHTTPChatCompleter()
	}
	return &Handler{
		search:                cfg.Search,
		crawler:               cfg.Crawler,
		crawlJobs:             cfg.CrawlJobs,
		crawlSem:              newCrawlConcurrencySemaphore(cfg.OpSettings),
		cancelFuncs:           make(map[string]context.CancelFunc),
		jobs:                  cfg.Jobs,
		debug:                 cfg.Debug,
		admin:                 cfg.Admin,
		pageRank:              cfg.PageRank,
		embeddingRepo:         cfg.EmbeddingRepo,
		contentDedupRepo:      cfg.ContentDedupRepo,
		embedders:             cfg.Embedders,
		settings:              cfg.Settings,
		opSettings:            cfg.OpSettings,
		overrides:             cfg.Overrides,
		settingsStore:         cfg.SettingsStore,
		scheduledCrawls:       cfg.ScheduledCrawls,
		embeddingEndpoints:    cfg.EmbeddingEndpoints,
		chat:                  cfg.Chat,
		chatEndpoints:         cfg.ChatEndpoints,
		mcpServers:            cfg.MCPServers,
		mcpTools:              cfg.MCPTools,
		agents:                cfg.Agents,
		users:                 cfg.Users,
		userMCPServers:        cfg.UserMCPServers,
		files:                 cfg.Files,
		fileTokens:            newFileTokenStore(),
		health:                cfg.Health,
		onCrawlComplete:       cfg.OnCrawlComplete,
		dbDriver:              cfg.DBDriver,
		adminUser:             cfg.AdminUser,
		adminPass:             cfg.AdminPass,
		sessions:              sessions,
		loginLimiter:          newLoginLimiter(),
		crawlInternalToken:    cfg.CrawlInternalToken,
		settingsEncryptionKey: cfg.SettingsEncryptionKey,
		newEmbedder:           newEmbedder,
		chatModelProber:       chatModelProber,
		internalSearchAPIKey:  cfg.InternalSearchAPIKey,
	}
}

// securityHeaders applies to every RoutesSearch/RoutesAdmin request -- a
// defense-in-depth backstop alongside output escaping, not a substitute.
// script-src has no 'unsafe-inline' (all JS is external); style-src needs
// it for inline style="" attributes plus Google Fonts' stylesheet link.
var securityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "same-origin",
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for k, v := range securityHeaders {
			h.Set(k, v)
		}
		next.ServeHTTP(w, r)
	})
}

// RoutesSearch serves the public-facing search site only (index page,
// stylesheet, search API) -- the mux the internet-facing search-server
// binary listens with. Index and search both require the same signed-in
// session /admin and /login use; style.css and healthz stay open.
// GET /agents lists every enabled agent for the chat page's own picker --
// reachable by any signed-in session, unlike /admin/api/agents. /session,
// /account, /account.js and /account/api back the self-service account
// page a role=user session uses to change their password and set their
// personal chat prompt -- see account.go.
func (h *Handler) RoutesSearch() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.requireAuthPage(h.handleIndex))
	mux.HandleFunc("/style.css", h.handleStyle)
	mux.HandleFunc("/index.js", h.handleIndexJS)
	mux.HandleFunc("/search", h.requireAuthAPIOrInternalKey(h.handleSearch))
	mux.HandleFunc("POST /chat", h.requireAuthAPI(h.handleChat))
	mux.HandleFunc("/agents", h.requireAuthAPI(h.handleChatAgents))
	mux.HandleFunc("/session", h.requireAuthAPI(h.handleSession))
	mux.HandleFunc("/account", h.requireRegularUserAuthPage(h.handleAccountPage))
	mux.HandleFunc("/account.js", h.handleAccountJS)
	mux.HandleFunc("/account/api", h.requireRegularUserAuthAPI(h.handleAccount))
	mux.HandleFunc("/account/mcp-servers", h.requireRegularUserAuthPage(h.handleAccountMCPServersPage))
	mux.HandleFunc("/account_mcp_servers.js", h.handleAccountMCPServersJS)
	mux.HandleFunc("/account/mcp-servers/{id}", h.requireRegularUserAuthPage(h.handleAccountMCPServerPage))
	mux.HandleFunc("/account_mcp_server.js", h.handleAccountMCPServerJS)
	mux.HandleFunc("/account/api/mcp-servers", h.requireRegularUserAuthAPI(h.handleAccountMCPServers))
	mux.HandleFunc("GET /account/api/mcp-servers/{id}", h.requireRegularUserAuthAPI(h.handleAccountGetMCPServer))
	mux.HandleFunc("PATCH /account/api/mcp-servers/{id}", h.requireRegularUserAuthAPI(h.handleAccountUpdateMCPServer))
	mux.HandleFunc("DELETE /account/api/mcp-servers/{id}", h.requireRegularUserAuthAPI(h.handleAccountDeleteMCPServer))
	mux.HandleFunc("/account/files", h.requireRegularUserAuthPage(h.handleAccountFilesPage))
	mux.HandleFunc("/account_files.js", h.handleAccountFilesJS)
	// /account/api/files is deliberately NOT wrapped in
	// requireRegularUserAuthAPI: cmd/mcp-files calls it with a bearer
	// token, not a session cookie, so handleAccountFiles/handleAccountFile
	// resolve and gate the caller themselves -- see fileAccessUserID.
	mux.HandleFunc("/account/api/files", h.handleAccountFiles)
	mux.HandleFunc("/account/api/files/{id}", h.handleAccountFile)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return withSecurityHeaders(mux)
}

// RoutesAdmin serves login/session management plus every /admin and
// /admin/api/* route -- the mux the admin-server binary listens with,
// reachable only through a local nginx proxy, never directly.
func (h *Handler) RoutesAdmin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin.js", h.handleAdminJS)
	mux.HandleFunc("/login", h.handleLoginRoute)
	mux.HandleFunc("/login.js", h.handleLoginJS)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/healthz", h.handleHealthz)

	mux.HandleFunc("/admin", h.requireAdminAuthPage(h.handleAdminPage))
	mux.HandleFunc("/admin_page.js", h.handleAdminPageJS)
	mux.HandleFunc("/admin/documents", h.requireAdminAuthPage(h.handleAdminDocumentsPage))
	mux.HandleFunc("/admin_documents.js", h.handleAdminDocumentsJS)
	mux.HandleFunc("/admin/documents/{host}", h.requireAdminAuthPage(h.handleAdminDomainPage))
	mux.HandleFunc("/admin_domain.js", h.handleAdminDomainJS)
	mux.HandleFunc("/admin/vocabulary/term", h.requireAdminAuthPage(h.handleAdminVocabularyTermPage))
	mux.HandleFunc("/admin_vocabulary_term.js", h.handleAdminVocabularyTermJS)
	mux.HandleFunc("/admin/crawl", h.requireAdminAuthPage(h.handleAdminCrawlPage))
	mux.HandleFunc("/admin_crawl.js", h.handleAdminCrawlJS)
	mux.HandleFunc("/admin/schedule/{id}", h.requireAdminAuthPage(h.handleAdminSchedulePage))
	mux.HandleFunc("/admin_schedule.js", h.handleAdminScheduleJS)
	mux.HandleFunc("/admin/jobs", h.requireAdminAuthPage(h.handleAdminJobsPage))
	mux.HandleFunc("/admin_jobs.js", h.handleAdminJobsJS)
	mux.HandleFunc("/admin/settings", h.requireAdminAuthPage(h.handleAdminSettingsPage))
	mux.HandleFunc("/admin_settings.js", h.handleAdminSettingsJS)
	mux.HandleFunc("/admin/chat/settings", h.requireAdminAuthPage(h.handleAdminChatSettingsPage))
	mux.HandleFunc("/admin_chat_settings.js", h.handleAdminChatSettingsJS)
	mux.HandleFunc("/admin/mcp-servers", h.requireAdminAuthPage(h.handleAdminMCPServersPage))
	mux.HandleFunc("/admin_mcp_servers.js", h.handleAdminMCPServersJS)
	mux.HandleFunc("/admin/mcp-servers/{id}", h.requireAdminAuthPage(h.handleAdminMCPServerPage))
	mux.HandleFunc("/admin_mcp_server.js", h.handleAdminMCPServerJS)
	mux.HandleFunc("/admin/agents", h.requireAdminAuthPage(h.handleAdminAgentsPage))
	mux.HandleFunc("/admin_agents.js", h.handleAdminAgentsJS)
	mux.HandleFunc("/admin/agents/{id}", h.requireAdminAuthPage(h.handleAdminAgentPage))
	mux.HandleFunc("/admin_agent.js", h.handleAdminAgentJS)
	mux.HandleFunc("/admin/search", h.requireAdminAuthPage(h.handleAdminSearchPage))
	mux.HandleFunc("/admin_search.js", h.handleAdminSearchJS)
	mux.HandleFunc("/admin/search/result", h.requireAdminAuthPage(h.handleAdminSearchResultPage))
	mux.HandleFunc("/admin_search_result.js", h.handleAdminSearchResultJS)
	mux.HandleFunc("/admin/pagerank", h.requireAdminAuthPage(h.handleAdminPageRankPage))
	mux.HandleFunc("/admin_pagerank.js", h.handleAdminPageRankJS)
	mux.HandleFunc("/admin/embeddings", h.requireAdminAuthPage(h.handleAdminEmbeddingsPage))
	mux.HandleFunc("/admin_embeddings.js", h.handleAdminEmbeddingsJS)
	mux.HandleFunc("/admin/embeddings/endpoints", h.requireAdminAuthPage(h.handleAdminEmbeddingEndpointsPage))
	mux.HandleFunc("/admin_embedding_endpoints.js", h.handleAdminEmbeddingEndpointsJS)
	mux.HandleFunc("/admin/embeddings/endpoint/{id}", h.requireAdminAuthPage(h.handleAdminEmbeddingEndpointPage))
	mux.HandleFunc("/admin_embedding_endpoint.js", h.handleAdminEmbeddingEndpointJS)
	mux.HandleFunc("/admin/database", h.requireAdminAuthPage(h.handleAdminDatabasePage))
	mux.HandleFunc("/admin_database.js", h.handleAdminDatabaseJS)
	mux.HandleFunc("/admin/content_dedup", h.requireAdminAuthPage(h.handleAdminContentDedupPage))
	mux.HandleFunc("/admin_content_dedup.js", h.handleAdminContentDedupJS)
	mux.HandleFunc("/admin/users", h.requireAdminAuthPage(h.handleAdminUsersPage))
	mux.HandleFunc("/admin_users.js", h.handleAdminUsersJS)
	mux.HandleFunc("/admin/users/{id}", h.requireAdminAuthPage(h.handleAdminUserPage))
	mux.HandleFunc("/admin_user.js", h.handleAdminUserJS)

	mux.HandleFunc("/admin/api/stats", h.requireAdminAuthAPI(h.handleAdminStats))
	mux.HandleFunc("/admin/api/vocabulary", h.requireAdminAuthAPI(h.handleAdminVocabulary))
	mux.HandleFunc("/admin/api/documents", h.requireAdminAuthAPI(h.handleAdminDocuments))
	mux.HandleFunc("DELETE /admin/api/documents", h.requireAdminAuthAPI(h.handleAdminDeleteDomainDocuments))
	mux.HandleFunc("GET /admin/api/documents/overview", h.requireAdminAuthAPI(h.handleAdminDocumentsOverview))
	mux.HandleFunc("GET /admin/api/overview/metrics", h.requireAdminAuthAPI(h.handleAdminOverviewMetrics))
	mux.HandleFunc("DELETE /admin/api/documents/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteDocument))
	mux.HandleFunc("GET /admin/api/documents/{id}/versions", h.requireAdminAuthAPI(h.handleAdminDocumentVersions))
	mux.HandleFunc("/admin/api/domains", h.requireAdminAuthAPI(h.handleAdminSearchDomains))
	mux.HandleFunc("/admin/api/postings", h.requireAdminAuthAPI(h.handleAdminPostings))
	mux.HandleFunc("/admin/api/search", h.requireAdminAuthAPI(h.handleAdminSearch))
	mux.HandleFunc("/admin/api/settings", h.requireAdminAuthAPI(h.handleAdminSettings))
	mux.HandleFunc("/admin/api/chat-endpoint", h.requireAdminAuthAPI(h.handleAdminChatEndpoint))
	mux.HandleFunc("/admin/api/mcp-servers", h.requireAdminAuthAPI(h.handleAdminMCPServers))
	mux.HandleFunc("POST /admin/api/mcp-servers/test", h.requireAdminAuthAPI(h.handleAdminMCPServersTest))
	mux.HandleFunc("GET /admin/api/mcp-servers/{id}", h.requireAdminAuthAPI(h.handleAdminGetMCPServer))
	mux.HandleFunc("PATCH /admin/api/mcp-servers/{id}", h.requireAdminAuthAPI(h.handleAdminUpdateMCPServer))
	mux.HandleFunc("DELETE /admin/api/mcp-servers/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteMCPServer))
	mux.HandleFunc("/admin/api/agents", h.requireAdminAuthAPI(h.handleAdminAgents))
	mux.HandleFunc("GET /admin/api/agents/{id}", h.requireAdminAuthAPI(h.handleAdminGetAgent))
	mux.HandleFunc("PATCH /admin/api/agents/{id}", h.requireAdminAuthAPI(h.handleAdminUpdateAgent))
	mux.HandleFunc("DELETE /admin/api/agents/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteAgent))
	mux.HandleFunc("POST /admin/api/embeddings/models", h.requireAdminAuthAPI(h.handleAdminEmbeddingsModels))
	mux.HandleFunc("POST /admin/api/embeddings/test", h.requireAdminAuthAPI(h.handleAdminEmbeddingsTest))
	mux.HandleFunc("/admin/api/embeddings/endpoints", h.requireAdminAuthAPI(h.handleAdminEmbeddingEndpoints))
	mux.HandleFunc("GET /admin/api/embeddings/endpoints/{id}", h.requireAdminAuthAPI(h.handleAdminGetEmbeddingEndpoint))
	mux.HandleFunc("PATCH /admin/api/embeddings/endpoints/{id}", h.requireAdminAuthAPI(h.handleAdminUpdateEmbeddingEndpoint))
	mux.HandleFunc("DELETE /admin/api/embeddings/endpoints/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteEmbeddingEndpoint))
	mux.HandleFunc("/admin/api/overrides", h.requireAdminAuthAPI(h.handleAdminOverrides))
	mux.HandleFunc("/admin/api/crawl/jobs", h.requireAdminAuthAPI(h.handleAdminCrawlJobs))
	mux.HandleFunc("GET /admin/api/crawl/jobs/{id}", h.requireAdminAuthAPI(h.handleAdminCrawlJob))
	mux.HandleFunc("POST /admin/api/crawl/jobs/{id}/cancel", h.requireAdminAuthAPI(h.handleAdminCancelCrawlJob))
	mux.HandleFunc("/admin/api/schedules", h.requireAdminAuthAPI(h.handleAdminSchedules))
	mux.HandleFunc("GET /admin/api/schedules/{id}", h.requireAdminAuthAPI(h.handleAdminGetSchedule))
	mux.HandleFunc("DELETE /admin/api/schedules/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteSchedule))
	mux.HandleFunc("PATCH /admin/api/schedules/{id}", h.requireAdminAuthAPI(h.handleAdminUpdateSchedule))
	mux.HandleFunc("POST /admin/api/schedules/{id}/run", h.requireAdminAuthAPI(h.handleAdminRunScheduleNow))
	mux.HandleFunc("POST /admin/api/schedules/{id}/toggle", h.requireAdminAuthAPI(h.handleAdminToggleSchedule))
	mux.HandleFunc("GET /admin/api/pagerank", h.requireAdminAuthAPI(h.handleAdminPageRank))
	mux.HandleFunc("POST /admin/api/pagerank/recompute", h.requireAdminAuthAPI(h.handleAdminPageRankRecompute))
	mux.HandleFunc("GET /admin/api/embeddings/recompute", h.requireAdminAuthAPI(h.handleAdminEmbeddingsRecomputeStatus))
	mux.HandleFunc("POST /admin/api/embeddings/recompute", h.requireAdminAuthAPI(h.handleAdminEmbeddingsRecomputeStart))
	mux.HandleFunc("GET /admin/api/database", h.requireAdminAuthAPI(h.handleAdminDatabase))
	mux.HandleFunc("POST /admin/api/database/clear-content", h.requireAdminAuthAPI(h.handleAdminClearContent))
	mux.HandleFunc("POST /admin/api/database/clear-settings", h.requireAdminAuthAPI(h.handleAdminClearSettings))
	mux.HandleFunc("GET /admin/api/content-dedup", h.requireAdminAuthAPI(h.handleAdminContentDedupStatus))
	mux.HandleFunc("POST /admin/api/content-dedup/recompute", h.requireAdminAuthAPI(h.handleAdminContentDedupRecomputeStart))
	mux.HandleFunc("GET /admin/api/content-dedup/alias-groups", h.requireAdminAuthAPI(h.handleAdminContentDedupAliasGroups))
	mux.HandleFunc("/admin/api/users", h.requireAdminAuthAPI(h.handleAdminUsers))
	mux.HandleFunc("GET /admin/api/users/{id}", h.requireAdminAuthAPI(h.handleAdminGetUser))
	mux.HandleFunc("PATCH /admin/api/users/{id}", h.requireAdminAuthAPI(h.handleAdminUpdateUser))
	mux.HandleFunc("DELETE /admin/api/users/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteUser))
	return withSecurityHeaders(mux)
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

func (h *Handler) handleAdminContentDedupJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminContentDedupJS)
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

func (h *Handler) handleAdminEmbeddingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminEmbeddingsJS)
}

func (h *Handler) handleAdminEmbeddingEndpointsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminEmbeddingEndpointsJS)
}

func (h *Handler) handleAdminEmbeddingEndpointJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminEmbeddingEndpointJS)
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

func (h *Handler) handleAdminChatSettingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminChatSettingsJS)
}

func (h *Handler) handleAdminMCPServersJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminMCPServersJS)
}

func (h *Handler) handleAdminMCPServerJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminMCPServerJS)
}

func (h *Handler) handleAdminAgentsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminAgentsJS)
}

func (h *Handler) handleAdminAgentJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminAgentJS)
}

func (h *Handler) handleAdminUsersJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminUsersJS)
}

func (h *Handler) handleAdminUserJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", adminUserJS)
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
	if !requireGetOrHead(w, r) {
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
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	query := r.URL.Query().Get("q")
	topK := intQueryParam(r, "top_k", h.opSettings.Get().DefaultTopK, false)

	results, err := h.search.Search(r.Context(), query, ports.SearchQuery{TopK: topK, Sort: parseSortParam(r), ProviderWeights: parseProviderWeightsParam(r)})
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

// parseProviderWeightsParam reads ?semantic=, a comma-separated list of
// "provider:weight" pairs (bare "provider" defaults to weight 1), e.g.
// "semantic=hash:0.3,ionos:0.7", into a ProviderWeights override. Returns
// nil (use the admin default) if absent; a malformed entry is skipped
// individually rather than failing the whole request.
func parseProviderWeightsParam(r *http.Request) map[string]float64 {
	raw := r.URL.Query().Get("semantic")
	if raw == "" {
		return nil
	}
	weights := make(map[string]float64)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		provider, weightStr, hasWeight := strings.Cut(pair, ":")
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		weight := 1.0
		if hasWeight {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(weightStr), 64)
			if err != nil {
				continue
			}
			weight = parsed
		}
		weights[provider] = weight
	}
	return weights
}

type healthResponse struct {
	Status string `json:"status"`
}

// handleHealthz is a minimal, unauthenticated liveness endpoint (no
// HealthChecker means always healthy; otherwise 503 on a failed DB ping),
// registered identically on all three Routes* muxes.
func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !requireGetOrHead(w, r) {
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
