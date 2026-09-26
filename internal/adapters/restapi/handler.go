package restapi

import (
	"context"
	_ "embed" // enables every //go:embed directive below (pages, scripts, stylesheet)
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

// jsContentType is the Content-Type every embedded page script is served
// with, pulled out since serveStatic repeats it per script below.
const jsContentType = "text/javascript; charset=utf-8"

//go:embed crawl.html
var crawlHTML []byte

//go:embed login.html
var loginHTML []byte

//go:embed admin.html
var adminHTML []byte

//go:embed admin_documents.html
var adminDocumentsHTML []byte

//go:embed admin_document_upload.html
var adminDocumentUploadHTML []byte

//go:embed admin_document_detail.html
var adminDocumentDetailHTML []byte

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
// an inline <script> block in its .html file -- admin.js is the shared-helpers
// file every admin page also loads.

//go:embed admin_page.js
var adminPageJS []byte

//go:embed admin_database.js
var adminDatabaseJS []byte

//go:embed admin_content_dedup.js
var adminContentDedupJS []byte

//go:embed admin_documents.js
var adminDocumentsJS []byte

//go:embed admin_document_upload.js
var adminDocumentUploadJS []byte

//go:embed admin_document_detail.js
var adminDocumentDetailJS []byte

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
	// contentDedupRepo, when set (admin-server only), backs the
	// content-dedup admin page's status/recompute endpoints; without it,
	// those endpoints report unavailable.
	contentDedupRepo ports.ContentDedupRepository
	embedders        map[string]ports.EmbeddingProvider
	settings         *domain.TuningSettings
	opSettings       *domain.OperationalSettings
	overrides        *domain.RankingOverrides
	settingsStore    ports.SettingsStore
	scheduledCrawls  ports.ScheduledCrawlStore
	// embeddingEndpoints backs the admin API's HTTP embedding endpoint CRUD
	// -- set on admin-server only, same *sqlrepo.Repository as ScheduledCrawls.
	embeddingEndpoints ports.EmbeddingEndpointStore
	// chat backs the public POST /chat endpoint -- set on search-server
	// only, nil (and 503-reporting) everywhere else.
	chat *application.ChatService
	// chatEndpoints backs the admin API's chat endpoint config CRUD -- set
	// on admin-server only, same *sqlrepo.Repository as embeddingEndpoints.
	chatEndpoints ports.ChatEndpointStore
	// chatVision backs the admin API's chat vision settings CRUD -- set on
	// admin-server only, same *sqlrepo.Repository as chatEndpoints.
	chatVision ports.ChatVisionStore
	// gpuMode backs the admin API's GPU mode (Vision image/video
	// generation) settings CRUD -- set on admin-server only, same
	// *sqlrepo.Repository as chatEndpoints.
	gpuMode ports.GPUModeStore
	// gpuModeService backs the public GET/POST /vision/api/mode and POST
	// /vision/api/heartbeat endpoints, plus handleChat's own availability
	// check -- set on search-server only, nil (and 404-reporting)
	// everywhere else. Distinct from gpuMode above (that's the admin CRUD
	// port; this is the application-layer use case wrapping it plus
	// ports.GPUModeController).
	gpuModeService *application.GPUModeService
	// documentJobs backs the admin Document-upload feature's job CRUD --
	// set on admin-server only, same *sqlrepo.Repository as chatEndpoints.
	documentJobs ports.DocumentJobStore
	// semanticMatcher backs the internal vision-similarity endpoint (see
	// handleVisionSimilarity) -- set on search-server only, same
	// *sqlrepo.Repository as embeddingRepo.
	semanticMatcher ports.SemanticMatcher
	// internalVisionAPIKey gates the internal vision-similarity endpoint
	// the same way internalSearchAPIKey gates /search -- a trusted local
	// caller (cmd/mcp-vision, spawned per chat turn) presents it in place
	// of a session cookie. Empty (the default) disables the endpoint.
	internalVisionAPIKey string
	// mcpServers backs the admin API's MCP server CRUD -- set on
	// admin-server only, same *sqlrepo.Repository as chatEndpoints.
	mcpServers ports.MCPServerStore
	// mcpTools backs the admin API's "list tools" connectivity test -- set
	// on admin-server only, the same mcpclient.Provider ChatService uses,
	// invoked here against a not-yet-saved candidate config.
	mcpTools ports.MCPToolProvider
	// agents backs the admin API's agent CRUD -- set on admin-server only,
	// same *sqlrepo.Repository as chatEndpoints/mcpServers.
	agents ports.AgentStore
	// users backs the admin API's user CRUD and handleLogin's DB-backed
	// lookup on admin-server (the only login path -- there is no separate
	// hardcoded admin account), plus search-server's /account routes and
	// handleChat's per-user prompt lookup (see userCustomPromptFor). nil is
	// valid (no accounts at all, so login always fails closed) for a
	// Handler that never sets it, e.g. crawl-server.
	// userMCPServers backs the self-service MCP server CRUD -- set on
	// search-server only, same *sqlrepo.Repository as users. ChatService
	// holds its own separate reference (wired in cmd/search's main) for
	// merging a caller's own servers into a chat turn.
	userMCPServers ports.UserMCPServerStore
	// files backs the self-service file API -- set on search-server only,
	// same *sqlrepo.Repository as userMCPServers. fileTokens is always
	// non-nil once New runs (unlike optional files): minting a token costs
	// nothing when files isn't configured, since requireConfigured refuses
	// the request first.
	files      ports.FileStore
	fileTokens *fileTokenStore
	// chats backs the self-service pinned-chat CRUD -- set on search-server
	// only, same *sqlrepo.Repository as files. Also consulted by
	// handleUploadFile/fileAccessTokenFor to verify a client-supplied
	// chat_id before trusting it.
	chats           ports.ChatStore
	users           ports.UserStore
	health          ports.HealthChecker
	onCrawlComplete func()
	dbDriver        string
	sessions        ports.SessionStore
	loginLimiter    *loginLimiter
	// crawlInternalToken, when set, is the shared secret
	// requireCrawlInternalToken checks RoutesCrawlInternal callers
	// against. Meaningless on RoutesSearch/RoutesAdmin.
	crawlInternalToken string
	// settingsEncryptionKey, when set, is the key the embedding endpoint
	// CRUD handlers encrypt each APIKey with before persisting it.
	settingsEncryptionKey []byte
	// newEmbedder builds a throwaway ports.EmbeddingProvider from a
	// candidate config -- bootstrap.NewHTTPEmbedder in production,
	// overridden by tests so testEmbeddingConnectivity never calls the network.
	newEmbedder func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider
	// chatModelProber auto-detects a chat endpoint's advertised max context
	// length (see handleAdminChatEndpoint's PATCH branch) -- production
	// uses bootstrap.NewHTTPChatCompleter(), tests override it to avoid a
	// real call. nil is valid (detection skipped) for crawl-server, which
	// never serves this route.
	chatModelProber ports.ChatCompleter
	// internalSearchAPIKey, when set, is an optional pre-shared key letting
	// a trusted local caller (e.g. a SearXNG plugin) call /search without a
	// browser session, via requireAuthAPIOrInternalKey. Empty by default,
	// meaning the bypass doesn't exist: /search stays session-cookie-only.
	internalSearchAPIKey string
}

// Config wires a Handler's dependencies. Crawler/CrawlJobs are used only
// by crawl-server; Jobs only by admin-server. Most fields are optional:
// without a given repository/store, its endpoints report unavailable
// rather than erroring. Auth is entirely DB-backed (Users) -- without any
// IsAdmin=true row, sign-in fails closed for /admin and /crawl; see
// packaging/create-admin.sh for seeding the first one.
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
	// "recompute embeddings" button. Embedders is every enabled provider
	// this recompute refreshes, not just the one active for search.
	EmbeddingRepo ports.EmbeddingRepository
	Embedders     map[string]ports.EmbeddingProvider
	// ContentDedupRepo, set on admin-server only, backs the content-dedup
	// admin page's status display and "recompute now" button.
	ContentDedupRepo ports.ContentDedupRepository
	// NewEmbedder builds a throwaway ports.EmbeddingProvider from a
	// candidate config, used to test-probe a base URL/model/API key before
	// saving (see testEmbeddingConnectivity). Defaults to
	// bootstrap.NewHTTPEmbedder; tests override to avoid a real call.
	NewEmbedder func(domain.EmbeddingHTTPEndpoint) ports.EmbeddingProvider
	// ChatModelProber, when set, auto-detects a chat endpoint's max context
	// length when saved with MaxContextTokens unset (see
	// handleAdminChatEndpoint's PATCH branch) -- defaults to
	// bootstrap.NewHTTPChatCompleter() on admin-server; left nil on
	// crawl-server, which never serves this route.
	ChatModelProber ports.ChatCompleter
	Settings        *domain.TuningSettings
	OpSettings      *domain.OperationalSettings
	Overrides       *domain.RankingOverrides
	SettingsStore   ports.SettingsStore
	// ScheduledCrawls is set on admin-server only (backing the schedules
	// admin API) -- crawl-server's ticker talks to the same store directly.
	ScheduledCrawls ports.ScheduledCrawlStore
	// EmbeddingEndpoints is set on admin-server only, backing the HTTP
	// embedding endpoint CRUD API -- same *sqlrepo.Repository as ScheduledCrawls.
	EmbeddingEndpoints ports.EmbeddingEndpointStore
	// Chat is set on search-server only, backing the public POST /chat
	// endpoint.
	Chat *application.ChatService
	// ChatEndpoints is set on admin-server only, backing the chat endpoint
	// config CRUD API -- same *sqlrepo.Repository as EmbeddingEndpoints.
	ChatEndpoints ports.ChatEndpointStore
	// ChatVision is set on admin-server only, backing the chat vision
	// settings CRUD API -- same *sqlrepo.Repository as ChatEndpoints.
	ChatVision ports.ChatVisionStore
	// GPUMode is set on admin-server only, backing the GPU mode (Vision
	// image/video generation) settings CRUD API -- same *sqlrepo.Repository
	// as ChatEndpoints.
	GPUMode ports.GPUModeStore
	// GPUModeService is set on search-server only, backing the public
	// GET/POST /vision/api/mode, POST /vision/api/heartbeat, and
	// handleChat's own availability check.
	GPUModeService *application.GPUModeService
	// DocumentJobs is set on admin-server only, backing the Document-upload
	// feature's job CRUD API -- same *sqlrepo.Repository as ChatEndpoints.
	DocumentJobs ports.DocumentJobStore
	// MCPServers is set on admin-server only, backing the MCP server CRUD
	// API -- same *sqlrepo.Repository as ChatEndpoints/EmbeddingEndpoints.
	MCPServers ports.MCPServerStore
	// MCPTools is set on admin-server only, backing the "list tools"
	// connectivity test -- same mcpclient.Provider ChatService uses for
	// real chat turns.
	MCPTools ports.MCPToolProvider
	// Agents is set on admin-server only, backing the agent CRUD API -- the
	// same *sqlrepo.Repository ChatEndpoints/MCPServers uses.
	Agents ports.AgentStore
	// Users is set on admin-server (user CRUD, handleLogin's DB-backed
	// lookup) and search-server (/account routes, handleChat's per-user
	// prompt lookup) -- same *sqlrepo.Repository as ChatEndpoints/MCPServers.
	Users ports.UserStore
	// UserMCPServers is set on search-server only, backing the self-service
	// MCP server CRUD API -- same *sqlrepo.Repository as Users.
	UserMCPServers ports.UserMCPServerStore
	// Files is set on search-server only, backing the self-service file API
	// -- same *sqlrepo.Repository as UserMCPServers.
	Files ports.FileStore
	// Chats is set on search-server only, backing the self-service
	// pinned-chat CRUD API -- same *sqlrepo.Repository as Files.
	Chats ports.ChatStore
	// Health backs GET /healthz on every process; unset always reports
	// healthy (no DB connection to check).
	Health ports.HealthChecker
	// Sessions, when set (every production process, via a shared
	// "sessions" table), makes a login recognized by every process.
	Sessions ports.SessionStore
	// OnCrawlComplete, set on crawl-server only, runs synchronously right
	// after a crawl finishes -- spawn your own goroutine inside it to avoid
	// delaying the job's reported completion.
	OnCrawlComplete func()
	DBDriver        string
	// CrawlInternalToken, when set, is the shared secret
	// requireCrawlInternalToken enforces on RoutesCrawlInternal, checked
	// against X-Internal-Token; crawlclient.Client sends it on every request.
	CrawlInternalToken string
	// SettingsEncryptionKey, when set (see settingscrypto.ParseKey), is
	// the key the embedding endpoint CRUD handlers encrypt each APIKey
	// with before persisting it. Meaningless on crawl-server/search-server.
	SettingsEncryptionKey []byte
	// InternalSearchAPIKey, when set, lets a trusted local caller (e.g. a
	// SearXNG plugin) call /search via X-Internal-API-Key instead of a
	// session cookie. Empty by default, meaning the bypass doesn't exist.
	InternalSearchAPIKey string
	// SemanticMatcher backs the internal vision-similarity endpoint -- set
	// on search-server only, same *sqlrepo.Repository as EmbeddingRepo.
	SemanticMatcher ports.SemanticMatcher
	// InternalVisionAPIKey, when set, lets cmd/mcp-vision (spawned per
	// chat turn) call the internal vision-similarity endpoint via
	// X-Internal-API-Key -- same mechanism as InternalSearchAPIKey, but a
	// deliberately separate key/env var (CHAT_VISION_INTERNAL_API_KEY):
	// least-privilege, so a SearXNG plugin's key can't also drive this,
	// and vice versa. Empty by default, meaning the endpoint refuses
	// every call.
	InternalVisionAPIKey string
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
		chatVision:            cfg.ChatVision,
		gpuMode:               cfg.GPUMode,
		gpuModeService:        cfg.GPUModeService,
		documentJobs:          cfg.DocumentJobs,
		mcpServers:            cfg.MCPServers,
		mcpTools:              cfg.MCPTools,
		agents:                cfg.Agents,
		users:                 cfg.Users,
		userMCPServers:        cfg.UserMCPServers,
		files:                 cfg.Files,
		fileTokens:            newFileTokenStore(),
		chats:                 cfg.Chats,
		health:                cfg.Health,
		onCrawlComplete:       cfg.OnCrawlComplete,
		dbDriver:              cfg.DBDriver,
		sessions:              sessions,
		loginLimiter:          newLoginLimiter(),
		crawlInternalToken:    cfg.CrawlInternalToken,
		settingsEncryptionKey: cfg.SettingsEncryptionKey,
		newEmbedder:           newEmbedder,
		chatModelProber:       chatModelProber,
		internalSearchAPIKey:  cfg.InternalSearchAPIKey,
		semanticMatcher:       cfg.SemanticMatcher,
		internalVisionAPIKey:  cfg.InternalVisionAPIKey,
	}
}

// securityHeaders applies to every RoutesSearch/RoutesAdmin request -- a
// defense-in-depth backstop, not a substitute for output escaping.
// script-src has no 'unsafe-inline' (all JS is external); style-src needs
// it for inline style="" attributes plus Google Fonts.
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
// binary listens with. Index and search require the same signed-in
// session /admin and /login use; style.css and healthz stay open. GET
// /agents is reachable by any signed-in session, unlike /admin/api/agents.
// /session, /account, /account.js and /account/api back the self-service
// account page -- see account.go.
func (h *Handler) RoutesSearch() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.requireAuthPage(h.handleIndex))
	mux.HandleFunc("/style.css", h.handleStyle)
	mux.HandleFunc("/index.js", h.handleIndexJS)
	mux.HandleFunc("/search", h.requireAuthAPIOrInternalKey(h.handleSearch))
	mux.HandleFunc("POST /chat", h.requireAuthAPI(h.handleChat))
	mux.HandleFunc("/agents", h.requireAuthAPI(h.handleChatAgents))
	mux.HandleFunc("/session", h.requireAuthAPI(h.handleSession))
	mux.HandleFunc("/account", h.requireAuthPage(h.handleAccountPage))
	mux.HandleFunc("/account.js", h.handleAccountJS)
	mux.HandleFunc("/account/api", h.requireAuthAPI(h.handleAccount))
	mux.HandleFunc("/account/mcp-servers", h.requireAuthPage(h.handleAccountMCPServersPage))
	mux.HandleFunc("/account_mcp_servers.js", h.handleAccountMCPServersJS)
	mux.HandleFunc("/account/mcp-servers/{id}", h.requireAuthPage(h.handleAccountMCPServerPage))
	mux.HandleFunc("/account_mcp_server.js", h.handleAccountMCPServerJS)
	mux.HandleFunc("/account/api/mcp-servers", h.requireAuthAPI(h.handleAccountMCPServers))
	mux.HandleFunc("GET /account/api/mcp-servers/{id}", h.requireAuthAPI(h.handleAccountGetMCPServer))
	mux.HandleFunc("PATCH /account/api/mcp-servers/{id}", h.requireAuthAPI(h.handleAccountUpdateMCPServer))
	mux.HandleFunc("DELETE /account/api/mcp-servers/{id}", h.requireAuthAPI(h.handleAccountDeleteMCPServer))
	mux.HandleFunc("/account/files", h.requireAuthPage(h.handleAccountFilesPage))
	mux.HandleFunc("/account_files.js", h.handleAccountFilesJS)
	// /account/api/files is deliberately NOT wrapped in requireAuthAPI:
	// cmd/mcp-files calls it with a bearer token, so
	// handleAccountFiles/handleAccountFile gate it themselves -- see
	// fileAccessUserID.
	// /search/api/vision-similarity is deliberately NOT wrapped in
	// requireAuthAPI, same reasoning as /account/api/files: cmd/mcp-vision
	// calls it with X-Internal-API-Key, self-gated inside the handler.
	mux.HandleFunc("/search/api/vision-similarity", h.handleVisionSimilarity)
	mux.HandleFunc("/account/api/files", h.handleAccountFiles)
	mux.HandleFunc("/account/api/files/{id}", h.handleAccountFile)
	mux.HandleFunc("/account/api/chats", h.requireAuthAPI(h.handleAccountChats))
	mux.HandleFunc("PATCH /account/api/chats/{id}", h.requireAuthAPI(h.handleAccountUpdateChat))
	mux.HandleFunc("DELETE /account/api/chats/{id}", h.requireAuthAPI(h.handleAccountDeleteChat))
	mux.HandleFunc("GET /vision/api/mode", h.requireAuthAPI(h.handleVisionMode))
	mux.HandleFunc("POST /vision/api/mode", h.requireAuthAPI(h.handleVisionModeSwitch))
	mux.HandleFunc("POST /vision/api/heartbeat", h.requireAuthAPI(h.handleVisionHeartbeat))
	mux.HandleFunc("/healthz", h.handleHealthz)
	return withSecurityHeaders(mux)
}

// RoutesAdmin serves login/session management plus every /admin and
// /admin/api/* route -- reachable only through a local nginx proxy, never directly.
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
	mux.HandleFunc("/admin/document-upload", h.requireAdminAuthPage(h.handleAdminDocumentUploadPage))
	mux.HandleFunc("/admin_document_upload.js", h.handleAdminDocumentUploadJS)
	mux.HandleFunc("/admin/document-upload/{id}", h.requireAdminAuthPage(h.handleAdminDocumentDetailPage))
	mux.HandleFunc("/admin_document_detail.js", h.handleAdminDocumentDetailJS)
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
	mux.HandleFunc("GET /admin/api/documents/{id}", h.requireAdminAuthAPI(h.handleAdminGetDocument))
	mux.HandleFunc("/admin/api/document-jobs", h.requireAdminAuthAPI(h.handleAdminDocumentJobs))
	mux.HandleFunc("POST /admin/api/document-jobs/import-s3", h.requireAdminAuthAPI(h.handleAdminImportDocumentFromS3))
	mux.HandleFunc("GET /admin/api/document-jobs/{id}", h.requireAdminAuthAPI(h.handleAdminDocumentJob))
	mux.HandleFunc("DELETE /admin/api/document-jobs/{id}", h.requireAdminAuthAPI(h.handleAdminDeleteDocumentJob))
	mux.HandleFunc("GET /admin/api/document-jobs/{id}/data", h.requireAdminAuthAPI(h.handleAdminDocumentJobData))
	mux.HandleFunc("/admin/api/domains", h.requireAdminAuthAPI(h.handleAdminSearchDomains))
	mux.HandleFunc("/admin/api/postings", h.requireAdminAuthAPI(h.handleAdminPostings))
	mux.HandleFunc("/admin/api/search", h.requireAdminAuthAPI(h.handleAdminSearch))
	mux.HandleFunc("/admin/api/settings", h.requireAdminAuthAPI(h.handleAdminSettings))
	mux.HandleFunc("/admin/api/chat-endpoint", h.requireAdminAuthAPI(h.handleAdminChatEndpoint))
	mux.HandleFunc("/admin/api/chat-vision", h.requireAdminAuthAPI(h.handleAdminChatVision))
	mux.HandleFunc("/admin/api/gpu-mode", h.requireAdminAuthAPI(h.handleAdminGPUMode))
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
	serveStatic(w, r, jsContentType, adminJS)
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveStatic(w, r, "text/html; charset=utf-8", indexHTML)
}

func (h *Handler) handleIndexJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, indexJS)
}

func (h *Handler) handleLoginJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, loginJS)
}

func (h *Handler) handleAdminPageJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminPageJS)
}

func (h *Handler) handleAdminDatabaseJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminDatabaseJS)
}

func (h *Handler) handleAdminContentDedupJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminContentDedupJS)
}

func (h *Handler) handleAdminDocumentsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminDocumentsJS)
}

func (h *Handler) handleAdminDocumentUploadPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminDocumentUploadHTML)
}

func (h *Handler) handleAdminDocumentUploadJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminDocumentUploadJS)
}

func (h *Handler) handleAdminDocumentDetailPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, contentTypeHTML, adminDocumentDetailHTML)
}

func (h *Handler) handleAdminDocumentDetailJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminDocumentDetailJS)
}

func (h *Handler) handleAdminDomainJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminDomainJS)
}

func (h *Handler) handleAdminPageRankJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminPageRankJS)
}

func (h *Handler) handleAdminEmbeddingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminEmbeddingsJS)
}

func (h *Handler) handleAdminEmbeddingEndpointsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminEmbeddingEndpointsJS)
}

func (h *Handler) handleAdminEmbeddingEndpointJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminEmbeddingEndpointJS)
}

func (h *Handler) handleAdminScheduleJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminScheduleJS)
}

func (h *Handler) handleAdminSearchJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminSearchJS)
}

func (h *Handler) handleAdminSearchResultJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminSearchResultJS)
}

func (h *Handler) handleAdminJobsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminJobsJS)
}

func (h *Handler) handleAdminSettingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminSettingsJS)
}

func (h *Handler) handleAdminChatSettingsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminChatSettingsJS)
}

func (h *Handler) handleAdminMCPServersJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminMCPServersJS)
}

func (h *Handler) handleAdminMCPServerJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminMCPServerJS)
}

func (h *Handler) handleAdminAgentsJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminAgentsJS)
}

func (h *Handler) handleAdminAgentJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminAgentJS)
}

func (h *Handler) handleAdminUsersJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminUsersJS)
}

func (h *Handler) handleAdminUserJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminUserJS)
}

func (h *Handler) handleAdminVocabularyTermJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminVocabularyTermJS)
}

func (h *Handler) handleAdminCrawlJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, jsContentType, adminCrawlJS)
}

// serveStatic answers a GET/HEAD request with a fixed, embedded payload --
// every static asset handler (HTML pages, style.css, admin.js) differs
// only in how it's addressed and what content type/bytes it serves.
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

// parseSortParam reads ?sort=, defaulting/falling back to relevance
// ranking -- shared by /search and /admin/api/search.
func parseSortParam(r *http.Request) string {
	if r.URL.Query().Get("sort") == ports.SortRecency {
		return ports.SortRecency
	}
	return ports.SortRelevance
}

// parseProviderWeightsParam reads ?semantic=, a comma-separated list of
// "provider:weight" pairs (bare "provider" defaults to weight 1), into a
// ProviderWeights override. nil if absent; a malformed entry is skipped,
// not a whole-request failure.
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
// HealthChecker means always healthy; otherwise 503 on a failed DB ping).
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
