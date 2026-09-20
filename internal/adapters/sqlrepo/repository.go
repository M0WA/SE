package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const crawledAtLayout = time.RFC3339Nano

// newDocumentPlaceholderPageRank is the pagerank a brand-new document row
// gets before RunPageRankJob has scored it (see SaveDocument's
// sql.ErrNoRows branch) -- order-of-magnitude only (~1/N for a
// mid-sized corpus), strictly positive, cheap (no query). Corrected the
// moment RunPageRankJob next runs.
const newDocumentPlaceholderPageRank = 1e-4

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type Repository struct {
	db      *sql.DB
	dialect Dialect
	// ann tracks whether Postgres pgvector-backed approximate
	// nearest-neighbor semantic search is available for this process --
	// see EnableANN, ANNAvailable and TopSemanticMatches in ann.go.
	ann annState
}

func New(ctx context.Context, driverName, dsn string) (*Repository, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening DB (%s): %w", driverName, err)
	}
	// A brand-new sqlite file's first Ping can race another process's
	// concurrent open/create (SQLITE_BUSY), before busy_timeout is even
	// set (below) -- observed in practice across search/admin/crawl
	// starting simultaneously. A no-op for every other dialect, and for
	// sqlite once the file already exists.
	if err := retrySQLiteBusy(ctx, func() error { return db.PingContext(ctx) }); err != nil {
		return nil, fmt.Errorf("DB ping (%s): %w", driverName, err)
	}

	repo := &Repository{db: db, dialect: NewDialect(driverName)}
	repo.applyDefaultPoolSettings()
	// search/admin/crawl each open and migrate the same SQLite file
	// independently at startup; SQLite's file-level locking serializes
	// their concurrent CREATE TABLE/INDEX statements. Without a busy
	// timeout, SQLite fails a migration outright on SQLITE_BUSY instead of
	// waiting out another process's in-flight migration.
	if repo.dialect.Name() == "sqlite" {
		if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			return nil, fmt.Errorf("setting busy_timeout (sqlite): %w", err)
		}
		// Three processes share one file; rollback-journal mode's writer
		// lock would block every concurrent reader until COMMIT, so WAL
		// mode lets readers use the last-committed snapshot instead.
		// synchronous=NORMAL is WAL's safe documented pairing. The
		// first-ever WAL setup on a brand-new file can SQLITE_BUSY
		// immediately, hence the retrySQLiteBusy wrap.
		if err := retrySQLiteBusy(ctx, func() error {
			_, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL")
			return err
		}); err != nil {
			return nil, fmt.Errorf("setting journal_mode=WAL (sqlite): %w", err)
		}
		if err := retrySQLiteBusy(ctx, func() error {
			_, err := db.ExecContext(ctx, "PRAGMA synchronous = NORMAL")
			return err
		}); err != nil {
			return nil, fmt.Errorf("setting synchronous=NORMAL (sqlite): %w", err)
		}
	}
	if err := repo.migrate(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

func NewWithDB(db *sql.DB, driverName string) *Repository {
	repo := &Repository{db: db, dialect: NewDialect(driverName)}
	repo.applyDefaultPoolSettings()
	return repo
}

// applyDefaultPoolSettings applies built-in pool defaults at construction,
// before any admin-configured value loads, so a fresh process never runs
// with Go's unbounded-open/2-idle defaults even briefly.
func (r *Repository) applyDefaultPoolSettings() {
	d := domain.DefaultOperationalSettings().Get()
	r.ConfigurePool(d.DBMaxOpenConns, d.DBMaxIdleConns, d.DBConnMaxLifetime)
}

// ConfigurePool applies connection-pool limits: called once at
// construction with built-in defaults, then again by bootstrap.SyncSettings
// whenever admin settings change. For SQLite, maxOpenConns/maxIdleConns
// are always clamped to 1: SQLite serializes writers at the file level, so
// more than one connection only risks "database is locked" errors.
func (r *Repository) ConfigurePool(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) {
	if r.dialect.Name() == "sqlite" {
		maxOpenConns = 1
		if maxIdleConns > 1 {
			maxIdleConns = 1
		}
	}
	r.db.SetMaxOpenConns(maxOpenConns)
	r.db.SetMaxIdleConns(maxIdleConns)
	r.db.SetConnMaxLifetime(connMaxLifetime)
}

// PoolStats reports the live connection pool's current limits and usage,
// for diagnostics and tests -- a thin passthrough to the underlying
// *sql.DB.
func (r *Repository) PoolStats() sql.DBStats {
	return r.db.Stats()
}

func (r *Repository) migrate(ctx context.Context) error {
	// search-server, admin-server and crawl-server each run this loop
	// independently at startup with no coordination between them. Postgres's
	// CREATE TABLE IF NOT EXISTS is not safe under true concurrency: the
	// existence check and the actual creation aren't atomic, so two
	// processes can both see a table missing and race to create it, with
	// the loser getting a duplicate-key error against the catalog (e.g.
	// "duplicate key value violates unique constraint
	// pg_type_typname_nsp_index") instead of a clean no-op -- observed in
	// practice the first time a brand new table (document_embeddings) was
	// added and all three binaries restarted at once during a package
	// upgrade. This is the exact same benign race isAlreadyExistsError
	// already tolerates for ALTER TABLE ADD COLUMN and CREATE INDEX below;
	// it was just never wired up for CREATE TABLE itself. Only bites a
	// table's very first creation -- once it exists for every process,
	// CREATE TABLE IF NOT EXISTS is a guaranteed fast no-op with no race
	// window.
	for _, stmt := range r.dialect.CreateSchemaSQL() {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	if _, err := r.db.ExecContext(ctx, r.dialect.SeedContentDedupLockSQL()); err != nil {
		return fmt.Errorf("seeding content dedup lock: %w", err)
	}
	if err := r.migrateDocumentColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateDocumentAliasColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateScheduledCrawlColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateEmbeddingEndpointColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateChatEndpointColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateChatHookColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateLegacyHTTPEmbeddingConfig(ctx); err != nil {
		return err
	}
	if err := r.ensureHostIndex(ctx); err != nil {
		return err
	}
	if err := r.ensureCrawledAtIndex(ctx); err != nil {
		return err
	}
	return r.ensureDocumentAliasHostIndex(ctx)
}

// migrateScheduledCrawlColumns adds per-crawl override columns to a
// scheduled_crawls table that predates them (CREATE TABLE IF NOT EXISTS
// only shapes a fresh table). A pre-existing schedule defaults to
// 0/false/” for each, same as a one-off crawl leaving them blank.
func (r *Repository) migrateScheduledCrawlColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "scheduled_crawls")
	if err != nil {
		return err
	}
	addColumn := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE scheduled_crawls ADD COLUMN "+ddl); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding %s column: %w", name, err)
		}
		return nil
	}
	if err := addColumn("fetch_timeout_seconds", "fetch_timeout_seconds INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("min_text_length", "min_text_length INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("crawl_delay_ms", "crawl_delay_ms INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("max_response_kb", "max_response_kb INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("prioritize_unindexed", "prioritize_unindexed BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	if err := addColumn("cookie", "cookie TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("basic_auth_user", "basic_auth_user TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("basic_auth_pass", "basic_auth_pass TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Recurring defaults to true for a pre-existing row -- every schedule
	// that predates this column really was a recurring one; the
	// run-once-then-disable shape is new.
	if err := addColumn("recurring", "recurring BOOLEAN NOT NULL DEFAULT true"); err != nil {
		return err
	}
	// max_runs 0 (the default) means unlimited for both a pre-existing row
	// and a freshly created one that never set it -- run_count 0 is simply
	// "hasn't run yet", true for every pre-existing row too since this
	// column didn't exist to increment before now.
	if err := addColumn("max_runs", "max_runs INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("run_count", "run_count INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// renderer defaults to '' (domain.RendererDefault) for a pre-existing
	// row -- inherit whatever the Tuning page's global default is, same as
	// a freshly created schedule that never set it.
	if err := addColumn("renderer", "renderer TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// link_scope replaces the old allow_off_domain_links boolean (left in
	// place, unused). Defaulting a pre-existing row to '' (inherit the
	// Tuning page's global default) rather than translating the old
	// boolean is deliberate -- downwards compatibility isn't a concern here.
	if err := addColumn("link_scope", "link_scope TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// allowed_domains/blocked_domains default to '[]' (no list -- LinkScope
	// alone decides scope, same as a freshly created schedule that never
	// set either) for a pre-existing row; follow_indexed_domains defaults
	// to false, same as every other boolean override added before it.
	if err := addColumn("allowed_domains", "allowed_domains TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := addColumn("blocked_domains", "blocked_domains TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := addColumn("follow_indexed_domains", "follow_indexed_domains BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	// in_progress tracks "a triggered run for this entry hasn't finished
	// yet" separately from enabled (the admin's own on/off toggle) -- see
	// domain.ScheduledCrawl.InProgress and application.TriggerDueCrawls for
	// why the two must never be conflated. Defaults to false for a
	// pre-existing row: nothing was mid-run when this column didn't exist.
	if err := addColumn("in_progress", "in_progress BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	// job_id backs ResetStaleInProgress's crash-recovery check (see
	// domain.ScheduledCrawl.JobID) -- '' for a pre-existing row is exactly
	// right, since in_progress also defaults to false for one.
	return addColumn("job_id", "job_id TEXT NOT NULL DEFAULT ''")
}

// migrateEmbeddingEndpointColumns adds the chunking columns (see
// domain.EmbeddingHTTPEndpoint.ChunkSizeTokens/TokenizeURL) to a table
// that predates them, each defaulting to "disabled" (0/”) to match a
// pre-existing endpoint's previous unchunked behavior.
func (r *Repository) migrateEmbeddingEndpointColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "embedding_http_endpoints")
	if err != nil {
		return err
	}
	addColumn := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE embedding_http_endpoints ADD COLUMN "+ddl); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding %s column: %w", name, err)
		}
		return nil
	}
	if err := addColumn("chunk_size_tokens", "chunk_size_tokens INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return addColumn("tokenize_url", "tokenize_url TEXT NOT NULL DEFAULT ''")
}

// migrateChatEndpointColumns adds max_context_tokens (see
// domain.ChatEndpoint.MaxContextTokens), the three web_search_* columns
// (see domain.ChatEndpoint.WebSearchEnabled/WebSearchBaseURL/
// WebSearchResultCount), and system_prompt (see
// domain.ChatEndpoint.SystemPrompt) to a chat_endpoint table that predates
// them -- max_context_tokens defaults to 0 ("disabled"), web_search_enabled
// to false and web_search_base_url to ” (both leave web search off, a
// pre-existing endpoint's previous behavior), web_search_result_count to 0
// (self-heals to the real default via domain.ChatEndpoint.Clamp on the
// next save, same convention as rag_result_count's own 0 default), and
// system_prompt to ” (no persistent prompt injected, a pre-existing
// endpoint's previous behavior).
func (r *Repository) migrateChatEndpointColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "chat_endpoint")
	if err != nil {
		return err
	}
	addColumn := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE chat_endpoint ADD COLUMN "+ddl); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding %s column: %w", name, err)
		}
		return nil
	}
	if err := addColumn("max_context_tokens", "max_context_tokens INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("web_search_enabled", "web_search_enabled BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	if err := addColumn("web_search_base_url", "web_search_base_url TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("web_search_result_count", "web_search_result_count INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	return addColumn("system_prompt", "system_prompt TEXT NOT NULL DEFAULT ''")
}

// migrateChatHookColumns adds prompt (see domain.ChatHook.Prompt) and
// gated_by_web_search (see domain.ChatHook.GatedByWebSearch) to a chat_hooks
// table that predates them -- prompt defaults to "" (no hook-specific
// system message injected, a pre-existing hook's previous behavior) and
// gated_by_web_search to false (active whenever Enabled is true, unaffected
// by the Web toggle -- also a pre-existing hook's previous, and today's
// only, behavior). chat_hooks is NOT a brand-new table -- real deployments
// have live rows in it already, so this can't be skipped the way
// CreateSchemaSQL alone would for a fresh install.
func (r *Repository) migrateChatHookColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "chat_hooks")
	if err != nil {
		return err
	}
	addColumn := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE chat_hooks ADD COLUMN "+ddl); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding %s column: %w", name, err)
		}
		return nil
	}
	if err := addColumn("prompt", "prompt TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumn("gated_by_web_search", "gated_by_web_search BOOLEAN NOT NULL DEFAULT false")
}

// legacyHTTPEmbeddingSettings decodes just the fields this migration cares
// about from a stored operational-settings JSON blob -- its own small
// struct since these fields no longer exist on the current
// OperationalSettingsValues; the old blob has no json tags, so these Go
// field names decode by exact name match regardless.
type legacyHTTPEmbeddingSettings struct {
	EmbeddingHTTPEnabled        bool
	EmbeddingHTTPBaseURL        string
	EmbeddingHTTPAPIKey         string
	EmbeddingHTTPModel          string
	EmbeddingHTTPDimensions     int
	EmbeddingRateLimitPerSecond float64
}

// migrateLegacyHTTPEmbeddingConfig is a one-time migration for an install
// that configured the old single-HTTP-endpoint feature: creates one
// endpoint row (ID "http", for continuity with pre-upgrade rows) from the
// old flat config. Guarded by a settings key, not row count, so a later
// admin deletion isn't resurrected on restart.
func (r *Repository) migrateLegacyHTTPEmbeddingConfig(ctx context.Context) error {
	_, migrated, err := r.GetSetting(ctx, ports.SettingsKeyEmbeddingEndpointsMigrated)
	if err != nil {
		return fmt.Errorf("checking embedding endpoints migration marker: %w", err)
	}
	if migrated {
		return nil
	}
	raw, found, err := r.GetSetting(ctx, ports.SettingsKeyOperational)
	if err != nil {
		return fmt.Errorf("reading legacy operational settings: %w", err)
	}
	if !found {
		return r.SaveSetting(ctx, ports.SettingsKeyEmbeddingEndpointsMigrated, "true")
	}
	var legacy legacyHTTPEmbeddingSettings
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		// An unparseable blob is a pre-existing problem SyncSettings
		// already tolerates -- mark migrated rather than retrying forever.
		return r.SaveSetting(ctx, ports.SettingsKeyEmbeddingEndpointsMigrated, "true")
	}
	if legacy.EmbeddingHTTPBaseURL != "" {
		name := legacy.EmbeddingHTTPModel
		if name == "" {
			name = "HTTP"
		}
		if err := r.CreateEmbeddingEndpoint(ctx, domain.EmbeddingHTTPEndpoint{
			ID:                 "http",
			Name:               name,
			BaseURL:            legacy.EmbeddingHTTPBaseURL,
			APIKey:             legacy.EmbeddingHTTPAPIKey,
			Model:              legacy.EmbeddingHTTPModel,
			Dimensions:         legacy.EmbeddingHTTPDimensions,
			RateLimitPerSecond: legacy.EmbeddingRateLimitPerSecond,
			Enabled:            legacy.EmbeddingHTTPEnabled,
			CreatedAt:          time.Now(),
		}); err != nil {
			return err
		}
	}
	return r.SaveSetting(ctx, ports.SettingsKeyEmbeddingEndpointsMigrated, "true")
}

// ensureHostIndex and ensureCrawledAtIndex both run after
// migrateDocumentColumns, since a database predating the host column
// needs it added before an index can be built over it. Both delegate to
// ensureIndex, which handles MySQL's lack of CREATE INDEX IF NOT EXISTS
// and the concurrent-migration race on Postgres/SQLite.
func (r *Repository) ensureHostIndex(ctx context.Context) error {
	return r.ensureIndex(ctx, "documents", "idx_documents_host", "host")
}

func (r *Repository) ensureCrawledAtIndex(ctx context.Context) error {
	return r.ensureIndex(ctx, "documents", "idx_documents_crawled_at", "crawled_at")
}

// ensureDocumentAliasHostIndex creates document_aliases(host)'s index only
// after migrateDocumentAliasColumns guarantees that column exists --
// building it via the static CreateSchemaSQL() list would fail against a
// pre-existing table from before the column was added.
func (r *Repository) ensureDocumentAliasHostIndex(ctx context.Context) error {
	return r.ensureIndex(ctx, "document_aliases", "idx_document_aliases_host", "host")
}

// ensureIndex creates a single-column index on table(column) if it doesn't
// already exist, tolerating both MySQL's lack of IF NOT EXISTS and the
// benign concurrent-creation race the other dialects can hit when
// search/admin/crawl all migrate on startup at once (see
// isAlreadyExistsError).
func (r *Repository) ensureIndex(ctx context.Context, table, indexName, column string) error {
	if r.dialect.Name() == "mysql" {
		_, _ = r.db.ExecContext(ctx, "CREATE INDEX "+indexName+" ON "+table+"("+column+")")
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+indexName+" ON "+table+"("+column+")"); err != nil && !isAlreadyExistsError(err) {
		return fmt.Errorf("creating %s: %w", indexName, err)
	}
	return nil
}

// isAlreadyExistsError reports whether err is the benign race where two of
// search/admin/crawl migrate the same not-yet-upgraded table at once: both
// see a column/index missing, both try to add it, and the loser gets an
// already-exists/duplicate error even though it ends up created either
// way. Covers Postgres's and MySQL's differently-phrased variants.
func isAlreadyExistsError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "duplicate key value violates unique constraint") ||
		strings.Contains(msg, "Duplicate column name")
}

// isForeignKeyViolationError reports whether err is a foreign key
// constraint failure, across all three dialects -- used by UpdateEmbedding
// to silently ignore an upsert for a doc_id that no longer exists, the
// same no-op a plain "UPDATE ... WHERE id=?" would produce.
func isForeignKeyViolationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "FOREIGN KEY constraint failed") ||
		strings.Contains(msg, "violates foreign key constraint") ||
		strings.Contains(msg, "foreign key constraint fails")
}

// isSQLiteBusyError reports whether err is modernc.org/sqlite's
// SQLITE_BUSY (plain or an extended result code variant) -- shows up when
// two processes race to convert a brand new file to WAL mode, see
// retrySQLiteBusy.
func isSQLiteBusyError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// retrySQLiteBusy runs fn, retrying with a short backoff on SQLITE_BUSY.
// PRAGMA busy_timeout doesn't cover the one-time WAL-mode conversion
// (SQLite re-opening the -shm file can SQLITE_BUSY immediately, before
// busy_timeout engages) -- a handful of short retries covers that startup
// race instead. See New()'s call sites.
func retrySQLiteBusy(ctx context.Context, fn func() error) error {
	const maxAttempts = 10
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = fn(); err == nil || !isSQLiteBusyError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return err
}

// migrateDocumentColumns adds host/version/crawled_at/norm_embedding to a
// documents table that predates them, and backfills host/pagerank for any
// pre-existing row, so an upgrade never needs a manual step. norm_embedding
// is retired (see SaveDocument) -- added for compatibility but never backfilled.
func (r *Repository) migrateDocumentColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "documents")
	if err != nil {
		return err
	}
	addColumn := func(name, ddl string) error {
		if existing[name] {
			return nil
		}
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE documents ADD COLUMN "+ddl); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding %s column: %w", name, err)
		}
		return nil
	}
	if err := addColumn("host", "host TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("version", "version INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := addColumn("crawled_at", "crawled_at TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("norm_embedding", "norm_embedding REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("pagerank", "pagerank REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := addColumn("content_hash", "content_hash TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn("simhash", "simhash TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := r.backfillHost(ctx); err != nil {
		return err
	}
	if err := r.backfillContentFingerprints(ctx); err != nil {
		return err
	}
	return r.backfillPageRank(ctx)
}

// migrateDocumentAliasColumns adds host to a document_aliases table that
// predates it (only matters for a database that ran this feature's very
// first release). Backfilled from each row's alias_url the same way
// documents.host is backfilled from url -- see backfillDocumentAliasHosts.
func (r *Repository) migrateDocumentAliasColumns(ctx context.Context) error {
	existing, err := r.existingColumns(ctx, "document_aliases")
	if err != nil {
		return err
	}
	if !existing["host"] {
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE document_aliases ADD COLUMN host TEXT NOT NULL DEFAULT ''"); err != nil && !isAlreadyExistsError(err) {
			return fmt.Errorf("adding host column to document_aliases: %w", err)
		}
	}
	return r.backfillDocumentAliasHosts(ctx)
}

// backfillDocumentAliasHosts fills in host for any document_aliases row
// saved before that column existed (it defaults to an empty string) --
// see migrateDocumentAliasColumns. A no-op once every row has it.
func (r *Repository) backfillDocumentAliasHosts(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, r.ph(`SELECT alias_url FROM document_aliases WHERE host = %s`, 1), "")
	if err != nil {
		return fmt.Errorf("finding document_aliases rows needing a host backfill: %w", err)
	}
	var pending []string
	for rows.Next() {
		var aliasURL string
		if err := rows.Scan(&aliasURL); err != nil {
			rows.Close()
			return fmt.Errorf("scanning row: %w", err)
		}
		pending = append(pending, aliasURL)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	updateSQL := r.ph(`UPDATE document_aliases SET host = %s WHERE alias_url = %s`, 1, 2)
	for _, aliasURL := range pending {
		if _, err := r.db.ExecContext(ctx, updateSQL, hostOf(aliasURL), aliasURL); err != nil {
			return fmt.Errorf("backfilling host for alias %s: %w", aliasURL, err)
		}
	}
	return nil
}

// existingColumns introspects which columns a table actually has, so
// migrateDocumentColumns only ALTERs in what's missing (dialects vary in
// whether ADD COLUMN IF NOT EXISTS is supported at all).
func (r *Repository) existingColumns(ctx context.Context, table string) (map[string]bool, error) {
	cols := make(map[string]bool)
	var rows *sql.Rows
	var err error
	if r.dialect.Name() == "sqlite" {
		rows, err = r.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	} else {
		rows, err = r.db.QueryContext(ctx,
			r.ph(`SELECT column_name FROM information_schema.columns WHERE table_name = %s`, 1), table)
	}
	if err != nil {
		return nil, fmt.Errorf("introspecting %s columns: %w", table, err)
	}
	defer rows.Close()

	if r.dialect.Name() == "sqlite" {
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return nil, fmt.Errorf("scanning column info: %w", err)
			}
			cols[name] = true
		}
	} else {
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, fmt.Errorf("scanning column info: %w", err)
			}
			cols[name] = true
		}
	}
	return cols, rows.Err()
}

// backfillHost fills in host for any row saved before that column existed
// (it defaults to an empty string), so domain search/filtering and the
// overview charts see every previously-indexed page too. A no-op once
// every row has it.
func (r *Repository) backfillHost(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, r.ph(`SELECT id, url FROM documents WHERE host = %s`, 1), "")
	if err != nil {
		return fmt.Errorf("finding rows needing a host backfill: %w", err)
	}
	type idURL struct{ id, url string }
	var pending []idURL
	for rows.Next() {
		var iu idURL
		if err := rows.Scan(&iu.id, &iu.url); err != nil {
			rows.Close()
			return fmt.Errorf("scanning row: %w", err)
		}
		pending = append(pending, iu)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	updateSQL := r.ph(`UPDATE documents SET host = %s WHERE id = %s`, 1, 2)
	for _, iu := range pending {
		if _, err := r.db.ExecContext(ctx, updateSQL, hostOf(iu.url), iu.id); err != nil {
			return fmt.Errorf("backfilling host for %s: %w", iu.id, err)
		}
	}
	return nil
}

// backfillContentFingerprints fills in content_hash/simhash for any row
// saved before those columns existed, computed from stored text (no
// re-crawl needed) -- lets RunContentDedupJob find duplicates predating
// the feature the moment it's enabled. A no-op once every row has one.
func (r *Repository) backfillContentFingerprints(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, r.ph(`SELECT id, text FROM documents WHERE content_hash = %s`, 1), "")
	if err != nil {
		return fmt.Errorf("finding rows needing a content fingerprint backfill: %w", err)
	}
	type idText struct{ id, text string }
	var pending []idText
	for rows.Next() {
		var it idText
		if err := rows.Scan(&it.id, &it.text); err != nil {
			rows.Close()
			return fmt.Errorf("scanning row: %w", err)
		}
		pending = append(pending, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	updateSQL := r.ph(`UPDATE documents SET content_hash = %s, simhash = %s WHERE id = %s`, 1, 2, 3)
	for _, it := range pending {
		contentHash := domain.ContentHash(it.text)
		simhash := domain.EncodeSimHash64(domain.SimHash64(it.text))
		if _, err := r.db.ExecContext(ctx, updateSQL, contentHash, simhash, it.id); err != nil {
			return fmt.Errorf("backfilling content fingerprint for %s: %w", it.id, err)
		}
	}
	return nil
}

// backfillPageRank fills in pagerank for any row saved before that column
// existed with a neutral 1/N score instead of a bare 0 -- 0 would unfairly
// rank a pre-existing document last the moment PageRankWeight is turned
// on. A real score is always strictly positive, so 0 unambiguously means
// "never assigned".
func (r *Repository) backfillPageRank(ctx context.Context) error {
	var totalDocs int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&totalDocs); err != nil {
		return fmt.Errorf("counting documents for pagerank backfill: %w", err)
	}
	if totalDocs == 0 {
		return nil
	}
	neutral := 1.0 / float64(totalDocs)
	if _, err := r.db.ExecContext(ctx, r.ph(`UPDATE documents SET pagerank = %s WHERE pagerank = %s`, 1, 2), neutral, 0); err != nil {
		return fmt.Errorf("backfilling pagerank: %w", err)
	}
	return nil
}

func (r *Repository) Close() error { return r.db.Close() }

// Ping confirms the database connection is alive, for GET /healthz -- a
// plain connection check, not a query against any application table.
func (r *Repository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// SaveDocument upserts doc keyed by its ID (deterministic from URL, so a
// re-crawl always lands on the same row). When content actually changes,
// the previous version is archived to document_versions and pruned back
// to maxVersions-1 (oldest first); re-confirming unchanged content just
// refreshes crawled_at.
func (r *Repository) SaveDocument(ctx context.Context, doc domain.Document, embeddings map[string][]float32, maxVersions, titleWeight int) error {
	// The title is repeated titleWeight times before the body (no
	// BM25F-style fielded formula, so this is how a title match counts
	// more than the same word in body text). A non-positive value (e.g.
	// an older test's zero) is treated as 1, not "no title at all". Only
	// affects documents crawled/re-crawled after the setting changes.
	if titleWeight <= 0 {
		titleWeight = 1
	}
	tokens := domain.Tokenize(strings.Repeat(doc.Title+" ", titleWeight) + doc.Text)
	// documents.embedding/norm_embedding are retired (vectors now live in
	// document_embeddings) -- migrations only ever add columns, never drop
	// them, so these stay NOT NULL but always written empty from here on.
	embBlob := EncodeEmbedding(nil)
	normEmbedding := domain.VectorNorm(nil)
	host := hostOf(doc.URL)
	now := time.Now().UTC().Format(crawledAtLayout)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	version := 1
	var pagerank float64
	var existingVersion int
	var existingText string
	var existingPageRank float64
	selectSQL := r.ph(`SELECT version, text, pagerank FROM documents WHERE id = %s`, 1)
	switch selectErr := tx.QueryRowContext(ctx, selectSQL, doc.ID).Scan(&existingVersion, &existingText, &existingPageRank); {
	case selectErr == sql.ErrNoRows:
		// New document: version stays 1, nothing to archive. Give it a
		// neutral placeholder pagerank rather than the column's bare-0
		// default, so it isn't unfairly ranked dead last on link authority
		// before the next application.RunPageRankJob run ever gets a
		// chance to score it -- see backfillPageRank for the same
		// reasoning applied to pre-existing rows.
		//
		// This used to be computed exactly as 1/(N+1) via a live `SELECT
		// COUNT(*) FROM documents` run inside this same transaction --
		// but that's a full, unindexed table scan on every single
		// never-before-seen-URL insert, so a crawl that discovers N new
		// pages did 1+2+...+N = O(N^2) row-scans overall (and held a
		// full-table read lock against concurrent crawl workers writing
		// to the same table). Exactness bought nothing: this value is
		// immediately superseded by the next RunPageRankJob run, same as
		// domain.PageRank's own (1-d)/N base term is a fixed value added
		// unconditionally every iteration rather than something derived
		// per node. newDocumentPlaceholderPageRank is that same kind of
		// fixed, always-positive placeholder -- cheap (no query at all)
		// and just as neutral, without the quadratic cost.
		pagerank = newDocumentPlaceholderPageRank
	case selectErr != nil:
		return fmt.Errorf("checking existing document: %w", selectErr)
	case existingText == doc.Text:
		version = existingVersion // unchanged content: not a new version
		pagerank = existingPageRank
	default:
		archiveSQL := r.ph(`INSERT INTO document_versions (doc_id, version, title, text, doc_length, crawled_at)
		                     SELECT id, version, title, text, doc_length, crawled_at FROM documents WHERE id = %s`, 1)
		if _, err := tx.ExecContext(ctx, archiveSQL, doc.ID); err != nil {
			return fmt.Errorf("archiving previous version: %w", err)
		}
		version = existingVersion + 1
		pagerank = existingPageRank

		// keep is how many archived rows may remain for this doc_id --
		// maxVersions counts the current (documents-table) row too, so a
		// maxVersions of 1 keeps no archived history at all (keep=0, which
		// LIMIT 0 below turns into "delete every archived row").
		keep := maxVersions - 1
		if keep < 0 {
			keep = 0
		}
		// The kept-versions LIMIT is nested inside a derived table (FROM
		// subquery), not the immediate operand of NOT IN, since MySQL
		// rejects "LIMIT & IN/ALL/ANY/SOME subquery" used directly there --
		// a derived table sidesteps that restriction on every dialect.
		pruneSQL := r.ph(`DELETE FROM document_versions WHERE doc_id = %s AND version NOT IN (
		                     SELECT version FROM (
		                       SELECT version FROM document_versions WHERE doc_id = %s ORDER BY version DESC LIMIT %s
		                     ) kept_versions
		                   )`, 1, 2, 3)
		if _, err := tx.ExecContext(ctx, pruneSQL, doc.ID, doc.ID, keep); err != nil {
			return fmt.Errorf("pruning old document versions: %w", err)
		}
	}

	// content_hash/simhash are always computed, regardless of whether the
	// content-dedup feature is enabled -- mirrors host/pagerank's own
	// always-populated treatment. application.RunContentDedupJob is what
	// actually acts on a match; this is just keeping every document's
	// fingerprint current so that job never needs a separate backfill pass
	// once it's turned on.
	contentHash := domain.ContentHash(doc.Text)
	simhash := domain.EncodeSimHash64(domain.SimHash64(doc.Text))

	if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentSQL(),
		doc.ID, doc.URL, doc.Title, doc.Text, len(tokens), embBlob, normEmbedding, pagerank, host, version, now, contentHash, simhash,
	); err != nil {
		return fmt.Errorf("saving document: %w", err)
	}

	// Write every enabled provider's actual vector into document_embeddings
	// (the real source of truth now), plus its pgvector ANN column on
	// Postgres wherever that provider's own EnableANN has succeeded.
	if err := r.saveDocumentEmbeddings(ctx, tx, doc.ID, embeddings); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM postings WHERE doc_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("deleting old postings: %w", err)
	}

	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	terms := make([]string, 0, len(counts))
	for term := range counts {
		terms = append(terms, term)
	}
	for start := 0; start < len(terms); start += saveDocumentInsertBatchSize {
		end := start + saveDocumentInsertBatchSize
		if end > len(terms) {
			end = len(terms)
		}
		if err := r.insertPostingsBatch(ctx, tx, doc.ID, terms[start:end], counts); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM links WHERE from_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("deleting old links: %w", err)
	}
	seenLinks := make(map[string]bool)
	links := make([]string, 0, len(doc.Links))
	for _, link := range doc.Links {
		// A link back to the page itself (e.g. a logo/home link) isn't a
		// meaningful internal link or backlink -- skip it so it can't
		// inflate either count.
		if link == doc.URL || seenLinks[link] {
			continue
		}
		seenLinks[link] = true
		links = append(links, link)
	}
	for start := 0; start < len(links); start += saveDocumentInsertBatchSize {
		end := start + saveDocumentInsertBatchSize
		if end > len(links) {
			end = len(links)
		}
		if err := r.insertLinksBatch(ctx, tx, doc.ID, links[start:end]); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// saveDocumentEmbeddings upserts one document_embeddings row per provider
// in embeddings, plus its pgvector ANN column when EnableANN has
// succeeded for it -- shared by SaveDocument and UpdateEmbedding via a
// caller-supplied transaction, so each call site's own atomicity needs
// are respected. dbExecer (*sql.DB or *sql.Tx) is what makes that possible.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (r *Repository) saveDocumentEmbeddings(ctx context.Context, exec dbExecer, docID string, embeddings map[string][]float32) error {
	upsertSQL := r.dialect.UpsertDocumentEmbeddingSQL()
	for provider, vec := range embeddings {
		embBlob := EncodeEmbedding(vec)
		norm := domain.VectorNorm(vec)
		if _, err := exec.ExecContext(ctx, upsertSQL, docID, provider, embBlob, norm); err != nil {
			return fmt.Errorf("saving %s embedding: %w", provider, err)
		}
		if r.ann.isAvailable(provider) {
			col := vectorColumnNameFor(provider)
			vecSQL := r.ph(`UPDATE documents SET `+col+` = %s::halfvec WHERE id = %s`, 1, 2)
			if _, err := exec.ExecContext(ctx, vecSQL, formatPgVectorLiteral(vec), docID); err != nil {
				return fmt.Errorf("saving %s embedding vector: %w", provider, err)
			}
		}
	}
	return nil
}

// saveDocumentInsertBatchSize bounds how many postings/links rows one
// multi-row INSERT in SaveDocument covers, replacing what used to be one
// INSERT per row. Each row is 3 placeholders; SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER is 999, so 300 rows/chunk (900 params) stays
// safely under it (Postgres/MySQL's own limits are never the constraint).
const saveDocumentInsertBatchSize = 300

// insertPostingsBatch runs one multi-row INSERT INTO postings statement
// covering the given terms (a saveDocumentInsertBatchSize-sized, or
// smaller, chunk from SaveDocument), looking each term's frequency up in
// counts.
func (r *Repository) insertPostingsBatch(ctx context.Context, tx *sql.Tx, docID string, terms []string, counts map[string]int) error {
	if len(terms) == 0 {
		return nil
	}
	var stmt strings.Builder
	stmt.WriteString("INSERT INTO postings (term, doc_id, term_freq) VALUES ")
	args := make([]interface{}, 0, len(terms)*3)
	pos := 1
	for i, term := range terms {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString("(" + r.placeholderList(3, pos) + ")")
		args = append(args, term, docID, counts[term])
		pos += 3
	}
	if _, err := tx.ExecContext(ctx, stmt.String(), args...); err != nil {
		return fmt.Errorf("saving postings batch: %w", err)
	}
	return nil
}

// insertLinksBatch runs one multi-row INSERT INTO links statement covering
// the given links (a saveDocumentInsertBatchSize-sized, or smaller, chunk
// from SaveDocument, already deduplicated and self-loop-filtered by the
// caller).
func (r *Repository) insertLinksBatch(ctx context.Context, tx *sql.Tx, docID string, links []string) error {
	if len(links) == 0 {
		return nil
	}
	var stmt strings.Builder
	stmt.WriteString("INSERT INTO links (from_id, to_url, to_host) VALUES ")
	args := make([]interface{}, 0, len(links)*3)
	pos := 1
	for i, link := range links {
		if i > 0 {
			stmt.WriteString(", ")
		}
		stmt.WriteString("(" + r.placeholderList(3, pos) + ")")
		args = append(args, docID, link, hostOf(link))
		pos += 3
	}
	if _, err := tx.ExecContext(ctx, stmt.String(), args...); err != nil {
		return fmt.Errorf("saving links batch: %w", err)
	}
	return nil
}

func (r *Repository) PostingsForTerm(ctx context.Context, term string, limit int) ([]domain.PostingStats, error) {
	totalDocs, avgDocLen, err := r.CorpusStats(ctx)
	if err != nil {
		return nil, err
	}

	var docFreq int
	dfQuery := r.ph(`SELECT COUNT(*) FROM postings WHERE term = %s`, 1)
	if err := r.db.QueryRowContext(ctx, dfQuery, term).Scan(&docFreq); err != nil {
		return nil, fmt.Errorf("querying doc freq: %w", err)
	}
	if docFreq == 0 {
		return nil, nil
	}

	query := r.ph(`SELECT p.doc_id, p.term_freq, d.doc_length
	               FROM postings p JOIN documents d ON d.id = p.doc_id
	               WHERE p.term = %s ORDER BY p.term_freq DESC LIMIT %s`, 1, 2)
	rows, err := r.db.QueryContext(ctx, query, term, limit)
	if err != nil {
		return nil, fmt.Errorf("querying postings: %w", err)
	}
	defer rows.Close()

	var out []domain.PostingStats
	for rows.Next() {
		var s domain.PostingStats
		if err := rows.Scan(&s.DocID, &s.TermFreq, &s.DocLength); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		s.DocFreq = docFreq
		s.TotalDocs = totalDocs
		s.AvgDocLen = avgDocLen
		out = append(out, s)
	}
	return out, rows.Err()
}

// PostingsForTerms batch-fetches postings for all of terms in one "WHERE
// term IN (...)" join, instead of one join + COUNT(*) per term. Each
// term's DocFreq is just the row count returned for it (one row per
// (term, doc_id) pair). TotalDocs/AvgDocLen are left zero -- the caller
// fetches those once per request, not once per term.
func (r *Repository) PostingsForTerms(ctx context.Context, terms []string) (map[string][]domain.PostingStats, error) {
	if len(terms) == 0 {
		return map[string][]domain.PostingStats{}, nil
	}
	args := make([]interface{}, len(terms))
	for i, t := range terms {
		args[i] = t
	}
	query := `SELECT p.term, p.doc_id, p.term_freq, d.doc_length
	          FROM postings p JOIN documents d ON d.id = p.doc_id
	          WHERE p.term IN (` + r.placeholderList(len(terms), 1) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying postings: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]domain.PostingStats, len(terms))
	for rows.Next() {
		var term string
		var s domain.PostingStats
		if err := rows.Scan(&term, &s.DocID, &s.TermFreq, &s.DocLength); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out[term] = append(out[term], s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for term, postings := range out {
		docFreq := len(postings)
		for i := range postings {
			postings[i].DocFreq = docFreq
		}
		out[term] = postings
	}
	return out, nil
}

func (r *Repository) CorpusStats(ctx context.Context) (int, float64, error) {
	var totalDocs int
	var avgLen sql.NullFloat64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), AVG(doc_length) FROM documents`).Scan(&totalDocs, &avgLen)
	if err != nil {
		return 0, 0, fmt.Errorf("querying corpus stats: %w", err)
	}
	if totalDocs == 0 || !avgLen.Valid {
		return totalDocs, 1, nil
	}
	return totalDocs, avgLen.Float64, nil
}

// PageRankDistribution reports the min, max and average documents.pagerank
// value across the whole corpus, for the admin PageRank debug page. All
// three are 0 for an empty corpus (MIN/MAX/AVG over zero rows are all
// NULL, which the COALESCE turns into 0).
func (r *Repository) PageRankDistribution(ctx context.Context) (min, max, avg float64, err error) {
	query := `SELECT COALESCE(MIN(pagerank), 0), COALESCE(MAX(pagerank), 0), COALESCE(AVG(pagerank), 0) FROM documents`
	if err := r.db.QueryRowContext(ctx, query).Scan(&min, &max, &avg); err != nil {
		return 0, 0, 0, fmt.Errorf("querying pagerank distribution: %w", err)
	}
	return min, max, avg, nil
}

// diagnosticsTables lists every table the schema creates (see each
// dialect's CreateSchemaSQL), in the order the admin database diagnostics
// page shows them.
var diagnosticsTables = []string{
	"documents", "postings", "document_versions", "document_embeddings", "links",
	"app_settings", "scheduled_crawls", "embedding_http_endpoints", "chat_endpoint", "crawl_jobs", "crawl_job_pages", "sessions",
}

// TableRowCounts reports how many rows each of diagnosticsTables currently
// holds, for the admin database diagnostics page. Table names are a fixed
// internal list, never user input, so building each query by concatenation
// (rather than a bound parameter, which SQL doesn't allow for identifiers)
// carries no injection risk.
func (r *Repository) TableRowCounts(ctx context.Context) (map[string]int64, error) {
	counts := make(map[string]int64, len(diagnosticsTables))
	for _, table := range diagnosticsTables {
		var n int64
		if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return nil, fmt.Errorf("counting %s: %w", table, err)
		}
		counts[table] = n
	}
	return counts, nil
}

// vocabularySortColumn whitelists sortBy against the only sortable
// columns -- sortBy reaches here from an HTTP query parameter, so it's
// never interpolated into the ORDER BY clause directly.
func vocabularySortColumn(sortBy string) string {
	switch sortBy {
	case "term":
		return "term"
	case "total_freq":
		return "total_freq"
	default:
		return "doc_freq"
	}
}

// VocabularyStats reports the total distinct term count (vocabSize) plus
// a limit/offset page of terms ordered by sortBy/sortDir (ties broken by
// term ascending). When search is non-empty, both the page and
// matchedCount (pre-limit/offset total) are restricted to matching terms;
// matchedCount equals vocabSize otherwise.
func (r *Repository) VocabularyStats(ctx context.Context, limit, offset int, search, sortBy, sortDir string) (int, int, []domain.TermStat, error) {
	var vocabSize int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT term) FROM postings`).Scan(&vocabSize); err != nil {
		return 0, 0, nil, fmt.Errorf("querying vocabulary size: %w", err)
	}

	orderCol := vocabularySortColumn(sortBy)
	dir := "DESC"
	if sortDir == "asc" {
		dir = "ASC"
	}

	matched := vocabSize
	var query string
	var args []interface{}
	if search == "" {
		query = r.ph(fmt.Sprintf(`SELECT term, COUNT(*) AS doc_freq, SUM(term_freq) AS total_freq
		               FROM postings GROUP BY term ORDER BY %s %s, term ASC LIMIT %%s OFFSET %%s`, orderCol, dir), 1, 2)
		args = []interface{}{limit, offset}
	} else {
		like := "%" + search + "%"
		if err := r.db.QueryRowContext(ctx, r.ph(`SELECT COUNT(DISTINCT term) FROM postings WHERE term LIKE %s`, 1), like).Scan(&matched); err != nil {
			return 0, 0, nil, fmt.Errorf("counting matching terms: %w", err)
		}
		query = r.ph(fmt.Sprintf(`SELECT term, COUNT(*) AS doc_freq, SUM(term_freq) AS total_freq
		               FROM postings WHERE term LIKE %%s GROUP BY term ORDER BY %s %s, term ASC LIMIT %%s OFFSET %%s`, orderCol, dir), 1, 2, 3)
		args = []interface{}{like, limit, offset}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("querying terms: %w", err)
	}
	defer rows.Close()

	var out []domain.TermStat
	for rows.Next() {
		var s domain.TermStat
		if err := rows.Scan(&s.Term, &s.DocFreq, &s.TotalFreq); err != nil {
			return 0, 0, nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, s)
	}
	return vocabSize, matched, out, rows.Err()
}

// AllTerms returns every distinct term in the postings table with its
// doc/total frequency -- the same shape as VocabularyStats' topN listing,
// but unbounded, since domain.VocabularyCache/domain.NearestTerm need the
// whole vocabulary to check a mistyped query term against, not just the
// most frequent terms.
func (r *Repository) AllTerms(ctx context.Context) ([]domain.TermStat, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT term, COUNT(*) AS doc_freq, SUM(term_freq) AS total_freq
	               FROM postings GROUP BY term`)
	if err != nil {
		return nil, fmt.Errorf("querying all terms: %w", err)
	}
	defer rows.Close()

	var out []domain.TermStat
	for rows.Next() {
		var s domain.TermStat
		if err := rows.Scan(&s.Term, &s.DocFreq, &s.TotalFreq); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// scanEmbeddingRows reads (id, embedding-blob, norm_embedding, pagerank)
// rows into a map, shared by EmbeddingsForDocs and SampleEmbeddings. norm
// and pagerank are read straight off their own precomputed columns rather
// than recomputed here from the deserialized vector.
func scanEmbeddingRows(rows *sql.Rows) (map[string]domain.EmbeddedVector, error) {
	out := make(map[string]domain.EmbeddedVector)
	for rows.Next() {
		var id string
		var embBlob []byte
		var norm, pagerank float64
		if err := rows.Scan(&id, &embBlob, &norm, &pagerank); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		vec, err := DecodeEmbedding(embBlob)
		if err != nil {
			return nil, fmt.Errorf("deserializing embedding (%s): %w", id, err)
		}
		out[id] = domain.EmbeddedVector{Vector: vec, Norm: norm, PageRank: pagerank}
	}
	return out, rows.Err()
}

// EmbeddingsForDocs batch-fetches provider's embeddings for exactly the
// given doc IDs (a single "WHERE provider = ? AND doc_id IN (...)" query
// against document_embeddings, joined to documents for pagerank), so a
// search only ever deserializes embeddings for documents it actually
// needs -- never the whole corpus.
func (r *Repository) EmbeddingsForDocs(ctx context.Context, ids []string, provider string) (map[string]domain.EmbeddedVector, error) {
	if len(ids) == 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, provider)
	for _, id := range ids {
		args = append(args, id)
	}
	query := `SELECT de.doc_id, de.embedding, de.norm_embedding, d.pagerank
	          FROM document_embeddings de JOIN documents d ON d.id = de.doc_id
	          WHERE de.provider = ` + r.dialect.Placeholder(1) + ` AND de.doc_id IN (` + r.placeholderList(len(ids), 2) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying embeddings for docs: %w", err)
	}
	defer rows.Close()
	return scanEmbeddingRows(rows)
}

// SampleEmbeddings returns up to limit of provider's embeddings from
// across the corpus (SQL-bounded via LIMIT, so the query cost never scales
// with corpus size), used to fill out a search's semantic candidate pool
// beyond its BM25 hits. A non-positive limit returns an empty map without
// touching the database.
func (r *Repository) SampleEmbeddings(ctx context.Context, limit int, provider string) (map[string]domain.EmbeddedVector, error) {
	if limit <= 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	query := r.ph(`SELECT de.doc_id, de.embedding, de.norm_embedding, d.pagerank
	               FROM document_embeddings de JOIN documents d ON d.id = de.doc_id
	               WHERE de.provider = %s ORDER BY de.doc_id LIMIT %s`, 1, 2)
	rows, err := r.db.QueryContext(ctx, query, provider, limit)
	if err != nil {
		return nil, fmt.Errorf("sampling embeddings: %w", err)
	}
	defer rows.Close()
	return scanEmbeddingRows(rows)
}

// DocumentsByIDs batch-fetches documents for the given IDs in one query
// instead of one round trip per candidate. An ID with no matching row is
// simply absent from the result, not an error -- a concurrently-deleted
// candidate is silently dropped rather than failing the whole search.
func (r *Repository) DocumentsByIDs(ctx context.Context, ids []string) (map[string]domain.Document, error) {
	if len(ids) == 0 {
		return map[string]domain.Document{}, nil
	}
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT id, url, title, text, crawled_at FROM documents WHERE id IN (` + r.placeholderList(len(ids), 1) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying documents: %w", err)
	}
	defer rows.Close()

	out := make(map[string]domain.Document, len(ids))
	for rows.Next() {
		var doc domain.Document
		var crawledAt string
		if err := rows.Scan(&doc.ID, &doc.URL, &doc.Title, &doc.Text, &crawledAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		doc.CrawledAt = parseCrawledAt(crawledAt)
		out[doc.ID] = doc
	}
	return out, rows.Err()
}

// DocumentsByIDsSortedByCrawledAt is DocumentsByIDs but returns a slice in
// descending crawled_at order (ties by id), pushed into SQL and served by
// idx_documents_crawled_at so hybrid search's recency-sort path never
// needs an in-app sort.Slice.
func (r *Repository) DocumentsByIDsSortedByCrawledAt(ctx context.Context, ids []string) ([]domain.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT id, url, title, text, crawled_at FROM documents WHERE id IN (` + r.placeholderList(len(ids), 1) +
		`) ORDER BY crawled_at DESC, id ASC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying documents sorted by crawled_at: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Document, 0, len(ids))
	for rows.Next() {
		var doc domain.Document
		var crawledAt string
		if err := rows.Scan(&doc.ID, &doc.URL, &doc.Title, &doc.Text, &crawledAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		doc.CrawledAt = parseCrawledAt(crawledAt)
		out = append(out, doc)
	}
	return out, rows.Err()
}

// maxDocumentIDsByHost caps how many IDs a single site: filter can force
// into the search candidate set -- a safety valve against an unbounded
// fetch for a pathologically large single-domain crawl, well above the
// default semantic candidate pool size since a site: query is scoped by
// the user's own intent, not a general relevance sample.
const maxDocumentIDsByHost = 5000

// hostMatchConditions builds "column = ? OR column LIKE ?" clauses (one
// per host, ORed) for exact-or-subdomain matching, mirroring domain.
// ParsedQuery.SiteAllowed's rule. startPos is the first placeholder
// position to use; nextPos is where the caller's next placeholder starts.
func (r *Repository) hostMatchConditions(column string, hosts []string, startPos int) (conditions []string, args []interface{}, nextPos int) {
	conditions = make([]string, 0, len(hosts))
	args = make([]interface{}, 0, len(hosts)*2)
	pos := startPos
	for _, h := range hosts {
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR %s LIKE %s)", column, r.dialect.Placeholder(pos), column, r.dialect.Placeholder(pos+1)))
		args = append(args, h, "%."+h)
		pos += 2
	}
	return conditions, args, pos
}

// DocumentIDsByHost returns IDs of documents whose host exactly matches
// or is a subdomain of one of hosts (via idx_documents_host), plus,
// unioned in, the canonical_id of any document_alias whose host matches
// -- so a merged-away or www-folded document is still findable by site:.
func (r *Repository) DocumentIDsByHost(ctx context.Context, hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	conditions, args, pos := r.hostMatchConditions("host", hosts, 1)
	query := `SELECT id FROM documents WHERE ` + strings.Join(conditions, " OR ") +
		r.ph(` LIMIT %s`, pos)
	args = append(args, maxDocumentIDsByHost)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying document ids by host: %w", err)
	}
	seen := make(map[string]bool)
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	aliasConditions, aliasArgs, aliasPos := r.hostMatchConditions("host", hosts, 1)
	aliasQuery := `SELECT DISTINCT canonical_id FROM document_aliases WHERE ` + strings.Join(aliasConditions, " OR ") +
		r.ph(` LIMIT %s`, aliasPos)
	aliasArgs = append(aliasArgs, maxDocumentIDsByHost)
	aliasRows, err := r.db.QueryContext(ctx, aliasQuery, aliasArgs...)
	if err != nil {
		return nil, fmt.Errorf("querying document ids by alias host: %w", err)
	}
	defer aliasRows.Close()
	for aliasRows.Next() {
		var id string
		if err := aliasRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning alias row: %w", err)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, aliasRows.Err()
}

// ResolveAliasHosts returns the actual documents.host of every canonical
// document reachable through a document_aliases row whose own host matches
// one of hosts (exact or subdomain, same rule as hostMatchConditions) -- see
// ports.SQLRepository.ResolveAliasHosts.
func (r *Repository) ResolveAliasHosts(ctx context.Context, hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	conditions, args, pos := r.hostMatchConditions("da.host", hosts, 1)
	query := `SELECT DISTINCT d.host FROM document_aliases da ` +
		`JOIN documents d ON d.id = da.canonical_id WHERE ` + strings.Join(conditions, " OR ") +
		r.ph(` LIMIT %s`, pos)
	args = append(args, maxDocumentIDsByHost)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolving alias hosts: %w", err)
	}
	defer rows.Close()
	var hostsOut []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		hostsOut = append(hostsOut, host)
	}
	return hostsOut, rows.Err()
}

// AllDocumentIDs lists every document ID, ordered by id for stable
// pagination -- used by RunEmbeddingRecomputeJob to walk the corpus in
// bounded batches via DocumentsByIDs rather than loading all text at once.
func (r *Repository) AllDocumentIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM documents ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("querying all document ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpdateEmbedding overwrites one document's embedding+norm_embedding per
// provider, without touching text/postings/links/versions/pagerank/host --
// used after a model change recomputes vectors from stored text (see
// RunEmbeddingRecomputeJob). Also refreshes each provider's pgvector column.
func (r *Repository) UpdateEmbedding(ctx context.Context, id string, embeddings map[string][]float32) error {
	// NOT wrapped in a shared transaction (unlike SaveDocument's call to
	// this helper): each provider's document_embeddings write must commit
	// independently of a subsequent, possibly-failing ANN-column write --
	// see TestEnableANN_RecreatesColumnWhenDimensionsChange. A document
	// deleted between listing and reaching this call is a harmless no-op:
	// document_embeddings' FK rejects the upsert, swallowed here below.
	if err := r.saveDocumentEmbeddings(ctx, r.db, id, embeddings); err != nil {
		if isForeignKeyViolationError(err) {
			return nil
		}
		return err
	}
	return nil
}

// DeleteDocument removes a document and every referencing row (postings,
// archived versions, outbound links) explicitly -- the schema's ON DELETE
// CASCADE is only a backstop, since SQLite's FK enforcement is off by
// default per-connection and a pooled connection may never have enabled it.
func (r *Repository) DeleteDocument(ctx context.Context, docID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, r.ph(`DELETE FROM documents WHERE id = %s`, 1), docID)
	if err != nil {
		return fmt.Errorf("deleting document (%s): %w", docID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking delete result (%s): %w", docID, err)
	}
	if n == 0 {
		return ports.ErrDocumentNotFound
	}

	for _, table := range []string{"postings", "document_versions"} {
		stmt := r.ph(`DELETE FROM `+table+` WHERE doc_id = %s`, 1)
		if _, err := tx.ExecContext(ctx, stmt, docID); err != nil {
			return fmt.Errorf("deleting %s for %s: %w", table, docID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM links WHERE from_id = %s`, 1), docID); err != nil {
		return fmt.Errorf("deleting links for %s: %w", docID, err)
	}

	return tx.Commit()
}

// RecordDocumentAlias upserts one document_aliases row -- see
// ports.SQLRepository's own doc comment for why canonicalID is never
// required to already name an existing documents row.
func (r *Repository) RecordDocumentAlias(ctx context.Context, aliasURL, canonicalID, reason string) error {
	now := time.Now().UTC().Format(crawledAtLayout)
	if _, err := r.db.ExecContext(ctx, r.dialect.UpsertDocumentAliasSQL(), aliasURL, canonicalID, reason, now, hostOf(aliasURL)); err != nil {
		return fmt.Errorf("recording document alias: %w", err)
	}
	return nil
}

// AllDocumentFingerprints lists every document's identity/fingerprint
// fields in one query -- see ports.ContentDedupRepository's own doc
// comment for why this doesn't fetch full domain.Document rows.
func (r *Repository) AllDocumentFingerprints(ctx context.Context) ([]domain.DocumentFingerprint, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, url, host, content_hash, simhash, crawled_at FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("querying document fingerprints: %w", err)
	}
	defer rows.Close()

	var out []domain.DocumentFingerprint
	for rows.Next() {
		var f domain.DocumentFingerprint
		var crawledAt string
		if err := rows.Scan(&f.ID, &f.URL, &f.Host, &f.ContentHash, &f.SimHash, &crawledAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		f.CrawledAt = parseCrawledAt(crawledAt)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MergeDocuments folds each loserIDs document into canonicalID in one
// transaction: repoints its existing aliases to canonicalID (path
// compression), records a fresh alias for its URL, then deletes it and
// every referencing child row (like DeleteDocument -- CASCADE isn't
// reliable here). loserIDs naming canonicalID, or already gone, are skipped.
func (r *Repository) MergeDocuments(ctx context.Context, canonicalID string, loserIDs []string, reason string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(crawledAtLayout)
	selectSQL := r.ph(`SELECT url FROM documents WHERE id = %s`, 1)
	repointSQL := r.ph(`UPDATE document_aliases SET canonical_id = %s WHERE canonical_id = %s`, 1, 2)

	for _, loserID := range loserIDs {
		if loserID == canonicalID {
			continue
		}
		var loserURL string
		switch err := tx.QueryRowContext(ctx, selectSQL, loserID).Scan(&loserURL); {
		case errors.Is(err, sql.ErrNoRows):
			continue // already merged/removed by a concurrent run
		case err != nil:
			return fmt.Errorf("looking up loser document %s: %w", loserID, err)
		}

		if _, err := tx.ExecContext(ctx, repointSQL, canonicalID, loserID); err != nil {
			return fmt.Errorf("repointing existing aliases of %s: %w", loserID, err)
		}
		if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentAliasSQL(), loserURL, canonicalID, reason, now, hostOf(loserURL)); err != nil {
			return fmt.Errorf("recording alias for %s: %w", loserID, err)
		}
		for _, table := range []string{"postings", "document_versions", "document_embeddings"} {
			stmt := r.ph(`DELETE FROM `+table+` WHERE doc_id = %s`, 1)
			if _, err := tx.ExecContext(ctx, stmt, loserID); err != nil {
				return fmt.Errorf("deleting %s for %s: %w", table, loserID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM links WHERE from_id = %s`, 1), loserID); err != nil {
			return fmt.Errorf("deleting links for %s: %w", loserID, err)
		}
		if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM documents WHERE id = %s`, 1), loserID); err != nil {
			return fmt.Errorf("deleting document %s: %w", loserID, err)
		}
	}
	return tx.Commit()
}

// TryAcquireContentDedupLock claims content_dedup_lock's single sentinel
// row via a conditional UPDATE (the same WHERE-current-value pattern
// RunScheduledCrawlNow uses for scheduled_crawls.in_progress) -- atomic
// across processes, unlike a check-then-act read-then-write, which is
// exactly what let two independent RunContentDedupJob calls interleave in
// production (see ports.ContentDedupRepository's doc comment).
func (r *Repository) TryAcquireContentDedupLock(ctx context.Context) (bool, error) {
	updateSQL := r.ph(`UPDATE content_dedup_lock SET in_progress = %s WHERE id = 1 AND in_progress = %s`, 1, 2)
	res, err := r.db.ExecContext(ctx, updateSQL, true, false)
	if err != nil {
		return false, fmt.Errorf("acquiring content dedup lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking content dedup lock acquisition: %w", err)
	}
	return n > 0, nil
}

// ReleaseContentDedupLock clears content_dedup_lock's sentinel row
// unconditionally -- always safe to call (including from a defer after a
// failed acquire), since setting an already-false flag to false again is a
// no-op.
func (r *Repository) ReleaseContentDedupLock(ctx context.Context) error {
	updateSQL := r.ph(`UPDATE content_dedup_lock SET in_progress = %s WHERE id = 1`, 1)
	if _, err := r.db.ExecContext(ctx, updateSQL, false); err != nil {
		return fmt.Errorf("releasing content dedup lock: %w", err)
	}
	return nil
}

// ListDocumentAliasGroups pages through every canonical document with at
// least one alias. Grouping happens in Go (fetch ordered pairs, group
// consecutive rows) rather than a dialect-specific GROUP_CONCAT/STRING_AGG,
// not worth the portability cost for an admin diagnostics page. A
// canonical_id with no matching documents row reports an empty
// CanonicalURL rather than being excluded -- deliberately: a forward-
// declared rel=canonical alias (see RecordDocumentAlias) legitimately
// names a canonical that hasn't been crawled yet, and is indistinguishable
// from that state alone from a canonical a content-dedup race left
// dangling (see TryAcquireContentDedupLock's doc comment) -- the latter is
// prevented going forward by that lock, not by hiding rows here.
func (r *Repository) ListDocumentAliasGroups(ctx context.Context, limit, offset int) ([]domain.DocumentAliasGroup, int, error) {
	var total int
	countSQL := `SELECT COUNT(DISTINCT canonical_id) FROM document_aliases`
	if err := r.db.QueryRowContext(ctx, countSQL).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting alias groups: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	idsSQL := r.ph(`SELECT DISTINCT canonical_id FROM document_aliases ORDER BY canonical_id LIMIT %s OFFSET %s`, 1, 2)
	idRows, err := r.db.QueryContext(ctx, idsSQL, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing alias group canonical ids: %w", err)
	}
	var canonicalIDs []string
	for idRows.Next() {
		var id string
		if err := idRows.Scan(&id); err != nil {
			idRows.Close()
			return nil, 0, fmt.Errorf("scanning canonical id: %w", err)
		}
		canonicalIDs = append(canonicalIDs, id)
	}
	idRows.Close()
	if err := idRows.Err(); err != nil {
		return nil, 0, err
	}
	if len(canonicalIDs) == 0 {
		return nil, total, nil
	}

	placeholders := make([]string, len(canonicalIDs))
	args := make([]interface{}, len(canonicalIDs))
	for i, id := range canonicalIDs {
		placeholders[i] = r.dialect.Placeholder(i + 1)
		args[i] = id
	}
	aliasSQL := `SELECT canonical_id, alias_url, reason FROM document_aliases WHERE canonical_id IN (` +
		strings.Join(placeholders, ",") + `) ORDER BY canonical_id, alias_url`
	aliasRows, err := r.db.QueryContext(ctx, aliasSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing aliases for groups: %w", err)
	}
	byCanonical := make(map[string][]domain.DocumentAlias, len(canonicalIDs))
	for aliasRows.Next() {
		var canonicalID, aliasURL, reason string
		if err := aliasRows.Scan(&canonicalID, &aliasURL, &reason); err != nil {
			aliasRows.Close()
			return nil, 0, fmt.Errorf("scanning alias row: %w", err)
		}
		byCanonical[canonicalID] = append(byCanonical[canonicalID], domain.DocumentAlias{URL: aliasURL, Reason: reason})
	}
	aliasRows.Close()
	if err := aliasRows.Err(); err != nil {
		return nil, 0, err
	}

	urlPlaceholders := make([]string, len(canonicalIDs))
	for i, id := range canonicalIDs {
		urlPlaceholders[i] = r.dialect.Placeholder(i + 1)
		args[i] = id
	}
	urlSQL := `SELECT id, url FROM documents WHERE id IN (` + strings.Join(urlPlaceholders, ",") + `)`
	urlRows, err := r.db.QueryContext(ctx, urlSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("looking up canonical document URLs: %w", err)
	}
	canonicalURLs := make(map[string]string, len(canonicalIDs))
	for urlRows.Next() {
		var id, url string
		if err := urlRows.Scan(&id, &url); err != nil {
			urlRows.Close()
			return nil, 0, fmt.Errorf("scanning canonical document row: %w", err)
		}
		canonicalURLs[id] = url
	}
	urlRows.Close()
	if err := urlRows.Err(); err != nil {
		return nil, 0, err
	}

	groups := make([]domain.DocumentAliasGroup, len(canonicalIDs))
	for i, id := range canonicalIDs {
		groups[i] = domain.DocumentAliasGroup{
			CanonicalID:  id,
			CanonicalURL: canonicalURLs[id],
			Aliases:      byCanonical[id],
		}
	}
	return groups, total, nil
}

// contentTables/settingsTables list every table ClearContent/ClearSettings
// deletes from, child tables before the parent they reference -- deletes
// are issued per-table (not a CASCADE) for the same cross-dialect-fragility
// reason DeleteDocument/MergeDocuments already avoid it (SQLite's foreign
// key enforcement isn't reliably on, and MySQL's TRUNCATE doesn't cascade
// at all). Table names are a fixed internal list, never user input, so
// building each statement by concatenation carries no injection risk, the
// same reasoning TableRowCounts already relies on.
var (
	contentTables = []string{
		"postings", "document_versions", "document_embeddings", "links",
		"documents", "document_aliases", "crawl_job_pages", "crawl_jobs",
	}
	settingsTables = []string{"app_settings", "chat_endpoint", "embedding_http_endpoints", "scheduled_crawls"}
)

func deleteAllRows(ctx context.Context, tx *sql.Tx, tables []string) error {
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}
	return nil
}

// ClearContent permanently deletes every crawled document and everything
// derived from it, plus every crawl job -- see ports.AdminRepository's own
// doc comment. Every settings table is left untouched.
func (r *Repository) ClearContent(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()
	if err := deleteAllRows(ctx, tx, contentTables); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearSettings permanently deletes every row of every settings table --
// see ports.AdminRepository's own doc comment for why this deliberately
// doesn't try to reset any process's own live in-memory settings.
func (r *Repository) ClearSettings(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()
	if err := deleteAllRows(ctx, tx, settingsTables); err != nil {
		return err
	}
	return tx.Commit()
}

// ListDocuments lists indexed pages, most recent ID first, optionally
// narrowed to a single host (backs the per-domain admin subpage).
// maxHostsIndexedBatch bounds how many hosts a single FollowIndexedDomains
// lookup batches per query -- a crawl could otherwise discover unbounded
// distinct off-scope hosts across its own pages' links.
const maxHostsIndexedBatch = 500

// HostsIndexed reports, for each of hosts, whether any document is already
// indexed for it (exact host match or a subdomain of it -- host = ? OR
// host LIKE '%.'+?, the same rule as DocumentIDsByHost), served by
// idx_documents_host. A host absent from the result was not found indexed.
func (r *Repository) HostsIndexed(ctx context.Context, hosts []string) (map[string]bool, error) {
	result := make(map[string]bool, len(hosts))
	if len(hosts) == 0 {
		return result, nil
	}
	if len(hosts) > maxHostsIndexedBatch {
		hosts = hosts[:maxHostsIndexedBatch]
	}
	conditions := make([]string, 0, len(hosts))
	args := make([]interface{}, 0, len(hosts)*2)
	pos := 1
	for _, h := range hosts {
		conditions = append(conditions, fmt.Sprintf("(host = %s OR host LIKE %s)", r.dialect.Placeholder(pos), r.dialect.Placeholder(pos+1)))
		args = append(args, h, "%."+h)
		pos += 2
	}
	query := `SELECT DISTINCT host FROM documents WHERE ` + strings.Join(conditions, " OR ")
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying hosts indexed: %w", err)
	}
	defer rows.Close()

	var matched []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, fmt.Errorf("scanning host: %w", err)
		}
		matched = append(matched, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range hosts {
		for _, m := range matched {
			if m == h || strings.HasSuffix(m, "."+h) {
				result[h] = true
				break
			}
		}
	}
	return result, nil
}

func (r *Repository) ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error) {
	var query string
	var args []interface{}
	if host != "" {
		query = r.ph(`SELECT id, url, host, title, doc_length, version, crawled_at, pagerank
		               FROM documents WHERE host = %s ORDER BY id LIMIT %s`, 1, 2)
		args = []interface{}{host, limit}
	} else {
		query = r.ph(`SELECT id, url, host, title, doc_length, version, crawled_at, pagerank
		               FROM documents ORDER BY id LIMIT %s`, 1)
		args = []interface{}{limit}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying documents: %w", err)
	}
	defer rows.Close()

	var out []domain.IndexedDocument
	for rows.Next() {
		var d domain.IndexedDocument
		var crawledAt string
		if err := rows.Scan(&d.ID, &d.URL, &d.Host, &d.Title, &d.DocLength, &d.Version, &crawledAt, &d.PageRank); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		d.CrawledAt = parseCrawledAt(crawledAt)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachLinkStats(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachLinkStats fills in InternalLinks, ExternalLinks and Backlinks for
// each document (mutated in place) using two batched queries -- one for
// this page's own outbound links classified against its own host, one for
// how many other indexed pages link to each of these URLs -- rather than
// one query per document.
func (r *Repository) attachLinkStats(ctx context.Context, docs []domain.IndexedDocument) error {
	if len(docs) == 0 {
		return nil
	}
	ids := make([]interface{}, len(docs))
	urls := make([]interface{}, len(docs))
	byID := make(map[string]*domain.IndexedDocument, len(docs))
	byURL := make(map[string]*domain.IndexedDocument, len(docs))
	for i := range docs {
		ids[i] = docs[i].ID
		urls[i] = docs[i].URL
		byID[docs[i].ID] = &docs[i]
		byURL[docs[i].URL] = &docs[i]
	}

	outQuery := fmt.Sprintf(`SELECT l.from_id,
	                          SUM(CASE WHEN l.to_host = d.host THEN 1 ELSE 0 END),
	                          SUM(CASE WHEN l.to_host != d.host THEN 1 ELSE 0 END)
	                          FROM links l JOIN documents d ON d.id = l.from_id
	                          WHERE l.from_id IN (%s)
	                          GROUP BY l.from_id`, r.placeholderList(len(ids), 1))
	outRows, err := r.db.QueryContext(ctx, outQuery, ids...)
	if err != nil {
		return fmt.Errorf("querying outbound link counts: %w", err)
	}
	for outRows.Next() {
		var fromID string
		var internal, external int
		if err := outRows.Scan(&fromID, &internal, &external); err != nil {
			outRows.Close()
			return fmt.Errorf("scanning outbound link counts: %w", err)
		}
		if d, ok := byID[fromID]; ok {
			d.InternalLinks, d.ExternalLinks = internal, external
		}
	}
	outRows.Close()
	if err := outRows.Err(); err != nil {
		return err
	}

	backlinkQuery := fmt.Sprintf(`SELECT to_url, COUNT(DISTINCT from_id)
	                               FROM links WHERE to_url IN (%s) GROUP BY to_url`,
		r.placeholderList(len(urls), 1))
	backlinkRows, err := r.db.QueryContext(ctx, backlinkQuery, urls...)
	if err != nil {
		return fmt.Errorf("querying backlink counts: %w", err)
	}
	defer backlinkRows.Close()
	for backlinkRows.Next() {
		var toURL string
		var count int
		if err := backlinkRows.Scan(&toURL, &count); err != nil {
			return fmt.Errorf("scanning backlink counts: %w", err)
		}
		if d, ok := byURL[toURL]; ok {
			d.Backlinks = count
		}
	}
	return backlinkRows.Err()
}

// LinkGraph loads the crawled link graph as a document-ID adjacency map
// in one query. links stores each link's raw target URL, not a document
// ID, so an uncrawled/unindexed target is naturally omitted by the join.
// Self-loops are defensively filtered even though SaveDocument already
// skips them, since two distinct source URLs could resolve to one ID.
func (r *Repository) LinkGraph(ctx context.Context) (map[string][]string, error) {
	// to_url resolves against documents.url first; if that misses but
	// names a known alias (merged away by RunContentDedupJob, or
	// www-folded), document_aliases.canonical_id is used instead, so a
	// link to a since-merged-away URL still credits the surviving document.
	query := `SELECT l.from_id, COALESCE(d.id, da.canonical_id) FROM links l
	          LEFT JOIN documents d ON d.url = l.to_url
	          LEFT JOIN document_aliases da ON da.alias_url = l.to_url
	          WHERE d.id IS NOT NULL OR da.canonical_id IS NOT NULL`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying link graph: %w", err)
	}
	defer rows.Close()

	graph := make(map[string][]string)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, fmt.Errorf("scanning link graph row: %w", err)
		}
		if from == to {
			continue
		}
		graph[from] = append(graph[from], to)
	}
	return graph, rows.Err()
}

// pageRankUpdateBatchSize bounds documents per UPDATE in UpdatePageRanks:
// each contributes 3 placeholders, so 200 stays well under any driver's
// per-statement parameter limit.
const pageRankUpdateBatchSize = 200

// UpdatePageRanks batch-writes every scores entry to documents.pagerank
// in one CASE-WHEN UPDATE per pageRankUpdateBatchSize documents, within a
// single transaction, rather than one UPDATE per document -- this can
// touch the entire corpus on every RunPageRankJob run. An ID not present
// in scores is left untouched.
func (r *Repository) UpdatePageRanks(ctx context.Context, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	for start := 0; start < len(ids); start += pageRankUpdateBatchSize {
		end := start + pageRankUpdateBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := r.updatePageRankBatch(ctx, tx, ids[start:end], scores); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// updatePageRankBatch runs one UPDATE documents SET pagerank = CASE id
// WHEN ... THEN ... END WHERE id IN (...) statement for the given ids.
func (r *Repository) updatePageRankBatch(ctx context.Context, tx *sql.Tx, ids []string, scores map[string]float64) error {
	// thenCast: Postgres infers a CASE expression's result type from its
	// THEN branches, and with every WHEN/THEN pair sent as an untyped
	// extended-protocol parameter, it resolves the whole CASE to text
	// rather than double precision, then refuses to assign that text
	// result to the double-precision pagerank column ("column "pagerank"
	// is of type double precision but expression is of type text"). An
	// explicit cast on each THEN parameter settles the type unambiguously.
	// SQLite (dynamically typed) and MySQL (which infers numeric literals
	// fine here) need no such cast.
	thenCast := "%s"
	if r.dialect.Name() == "postgres" {
		thenCast = "CAST(%s AS DOUBLE PRECISION)"
	}

	var stmt strings.Builder
	stmt.WriteString("UPDATE documents SET pagerank = CASE id ")
	args := make([]interface{}, 0, len(ids)*3)
	pos := 1
	for _, id := range ids {
		stmt.WriteString("WHEN " + r.dialect.Placeholder(pos) + " THEN " + fmt.Sprintf(thenCast, r.dialect.Placeholder(pos+1)) + " ")
		args = append(args, id, scores[id])
		pos += 2
	}
	stmt.WriteString("END WHERE id IN (" + r.placeholderList(len(ids), pos) + ")")
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := tx.ExecContext(ctx, stmt.String(), args...); err != nil {
		return fmt.Errorf("batch-updating pagerank: %w", err)
	}
	return nil
}

// placeholderList builds n comma-separated placeholders starting at
// startPos (e.g. "?, ?, ?" for sqlite/mysql, "$1, $2, $3" for postgres)
// for an IN (...) clause of variable width.
func (r *Repository) placeholderList(n, startPos int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = r.dialect.Placeholder(startPos + i)
	}
	return strings.Join(parts, ", ")
}

// SearchDomains finds distinct crawled domains whose hostname contains q,
// most-documents-first, capped at limit (or every domain if q is empty).
// The admin Documents page then matches q as a regex client-side against
// this batch, since a substring LIKE can't express what a regex can.
func (r *Repository) SearchDomains(ctx context.Context, q string, limit int) ([]domain.DomainSummary, error) {
	query := r.ph(`SELECT host, COUNT(*) AS cnt FROM documents
	               WHERE host LIKE %s GROUP BY host ORDER BY cnt DESC, host ASC LIMIT %s`, 1, 2)
	rows, err := r.db.QueryContext(ctx, query, "%"+q+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("searching domains: %w", err)
	}
	defer rows.Close()

	var out []domain.DomainSummary
	for rows.Next() {
		var s domain.DomainSummary
		if err := rows.Scan(&s.Host, &s.DocCount); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DocumentVersions lists a document's superseded prior versions, most
// recent first (the current content lives in ListDocuments/DocumentsByIDs,
// not here).
func (r *Repository) DocumentVersions(ctx context.Context, docID string) ([]domain.DocumentVersion, error) {
	query := r.ph(`SELECT version, title, doc_length, crawled_at FROM document_versions
	               WHERE doc_id = %s ORDER BY version DESC`, 1)
	rows, err := r.db.QueryContext(ctx, query, docID)
	if err != nil {
		return nil, fmt.Errorf("querying document versions: %w", err)
	}
	defer rows.Close()

	var out []domain.DocumentVersion
	for rows.Next() {
		var v domain.DocumentVersion
		var crawledAt string
		if err := rows.Scan(&v.Version, &v.Title, &v.DocLength, &crawledAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		v.CrawledAt = parseCrawledAt(crawledAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// DocumentsOverview aggregates the corpus for the admin Documents page's
// summary charts: which domains hold the most pages, and how recently
// each page's content was last (re-)confirmed by a crawl.
func (r *Repository) DocumentsOverview(ctx context.Context, topDomains int) (domain.DocumentsOverview, error) {
	var overview domain.DocumentsOverview

	topQuery := r.ph(`SELECT host, COUNT(*) AS cnt FROM documents
	                   GROUP BY host ORDER BY cnt DESC, host ASC LIMIT %s`, 1)
	rows, err := r.db.QueryContext(ctx, topQuery, topDomains)
	if err != nil {
		return overview, fmt.Errorf("querying top domains: %w", err)
	}
	for rows.Next() {
		var s domain.DomainSummary
		if err := rows.Scan(&s.Host, &s.DocCount); err != nil {
			rows.Close()
			return overview, fmt.Errorf("scanning row: %w", err)
		}
		overview.TopDomains = append(overview.TopDomains, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return overview, err
	}

	now := time.Now().UTC()
	last24h := now.Add(-24 * time.Hour).Format(crawledAtLayout)
	last7d := now.Add(-7 * 24 * time.Hour).Format(crawledAtLayout)
	last30d := now.Add(-30 * 24 * time.Hour).Format(crawledAtLayout)

	buckets := []struct {
		label        string
		since, until string
	}{
		{"last 24h", last24h, ""},
		{"last 7d", last7d, last24h},
		{"last 30d", last30d, last7d},
		{"older", "", last30d},
	}
	for _, b := range buckets {
		count, err := r.countDocumentsCrawled(ctx, b.since, b.until)
		if err != nil {
			return overview, err
		}
		overview.AgeBuckets = append(overview.AgeBuckets, domain.AgeBucket{Label: b.label, Count: count})
	}

	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT host) FROM documents`).Scan(&overview.TotalDomains); err != nil {
		return overview, fmt.Errorf("counting distinct domains: %w", err)
	}

	versionRows, err := r.db.QueryContext(ctx, `SELECT version, COUNT(*) AS cnt FROM documents GROUP BY version ORDER BY version ASC`)
	if err != nil {
		return overview, fmt.Errorf("querying version counts: %w", err)
	}
	defer versionRows.Close()
	for versionRows.Next() {
		var v domain.VersionCount
		if err := versionRows.Scan(&v.Version, &v.Count); err != nil {
			return overview, fmt.Errorf("scanning version count row: %w", err)
		}
		overview.VersionCounts = append(overview.VersionCounts, v)
	}
	if err := versionRows.Err(); err != nil {
		return overview, err
	}

	// storedQuery counts, per document, 1 (the current row) plus however
	// many archived rows it still has in document_versions, then groups
	// documents by that total -- how many versions are actually retained
	// right now, capped by MaxDocumentVersions, as opposed to VersionCounts
	// above (a document's version NUMBER, uncapped, counting every change
	// it's ever had regardless of what's since been pruned).
	storedQuery := `SELECT stored_versions, COUNT(*) AS doc_count FROM (
	                   SELECT d.id, 1 + COALESCE(dv.cnt, 0) AS stored_versions
	                   FROM documents d
	                   LEFT JOIN (SELECT doc_id, COUNT(*) AS cnt FROM document_versions GROUP BY doc_id) dv
	                     ON dv.doc_id = d.id
	                 ) counted
	                 GROUP BY stored_versions ORDER BY stored_versions ASC`
	storedRows, err := r.db.QueryContext(ctx, storedQuery)
	if err != nil {
		return overview, fmt.Errorf("querying stored version counts: %w", err)
	}
	defer storedRows.Close()
	for storedRows.Next() {
		var s domain.StoredVersionsCount
		if err := storedRows.Scan(&s.StoredVersions, &s.DocCount); err != nil {
			return overview, fmt.Errorf("scanning stored version count row: %w", err)
		}
		overview.StoredVersionCounts = append(overview.StoredVersionCounts, s)
	}
	if err := storedRows.Err(); err != nil {
		return overview, err
	}

	return overview, nil
}

// countDocumentsCrawled counts documents whose crawled_at falls in
// [since, until) -- empty means unbounded on that side. crawled_at is
// RFC3339Nano UTC text, which sorts lexicographically = chronologically,
// so plain string comparison works across all three dialects.
func (r *Repository) countDocumentsCrawled(ctx context.Context, since, until string) (int, error) {
	var query string
	var args []interface{}
	switch {
	case since != "" && until != "":
		query = r.ph(`SELECT COUNT(*) FROM documents WHERE crawled_at >= %s AND crawled_at < %s`, 1, 2)
		args = []interface{}{since, until}
	case since != "":
		query = r.ph(`SELECT COUNT(*) FROM documents WHERE crawled_at >= %s`, 1)
		args = []interface{}{since}
	default:
		query = r.ph(`SELECT COUNT(*) FROM documents WHERE crawled_at < %s`, 1)
		args = []interface{}{until}
	}
	var n int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting documents by age: %w", err)
	}
	return n, nil
}

// SaveSetting upserts key's value (an admin-configured settings JSON blob),
// stamping updated_at so callers could reason about staleness if they ever
// need to -- nothing currently reads that column back.
func (r *Repository) SaveSetting(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(crawledAtLayout)
	if _, err := r.db.ExecContext(ctx, r.dialect.UpsertSettingSQL(), key, value, now); err != nil {
		return fmt.Errorf("saving setting (%s): %w", key, err)
	}
	return nil
}

// GetSetting loads key's stored value. found is false (with a nil error)
// when no row exists yet -- a fresh database, or a key nothing has ever
// saved to -- so callers fall back to their hardcoded default rather than
// treating that as a failure.
func (r *Repository) GetSetting(ctx context.Context, key string) (value string, found bool, err error) {
	query := r.ph(`SELECT value FROM app_settings WHERE setting_key = %s`, 1)
	err = r.db.QueryRowContext(ctx, query, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("loading setting (%s): %w", key, err)
	}
	return value, true, nil
}

// scheduledCrawlColumns is the column list (and order) every
// scheduled_crawls SELECT below scans, shared with the INSERT/UPDATE
// statements so all four stay in sync.
const scheduledCrawlColumns = `id, seed_urls, max_pages, respect_robots, user_agent,
	cookie, basic_auth_user, basic_auth_pass,
	link_scope, allowed_domains, blocked_domains, follow_indexed_domains,
	use_sitemap, fetch_timeout_seconds, min_text_length,
	crawl_delay_ms, max_response_kb, prioritize_unindexed, recurring,
	interval_minutes, max_runs, run_count, renderer, enabled, in_progress, job_id, last_run_at, next_run_at, created_at`

// CreateScheduledCrawl inserts a new crawl definition -- the one
// representation of a crawl the admin sets up, whether it recurs or (see
// s.Recurring) just runs once.
func (r *Repository) CreateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error {
	seedJSON, err := json.Marshal(s.SeedURLs)
	if err != nil {
		return fmt.Errorf("encoding seed urls: %w", err)
	}
	allowedJSON, err := json.Marshal(s.AllowedDomains)
	if err != nil {
		return fmt.Errorf("encoding allowed domains: %w", err)
	}
	blockedJSON, err := json.Marshal(s.BlockedDomains)
	if err != nil {
		return fmt.Errorf("encoding blocked domains: %w", err)
	}
	insertSQL := r.ph(`INSERT INTO scheduled_crawls (`+scheduledCrawlColumns+`)
	                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29)
	_, err = r.db.ExecContext(ctx, insertSQL,
		s.ID, string(seedJSON), s.MaxPages, s.RespectRobots, s.UserAgent,
		s.Cookie, s.BasicAuthUser, s.BasicAuthPass,
		s.LinkScope, string(allowedJSON), string(blockedJSON), s.FollowIndexedDomains,
		s.UseSitemap, s.FetchTimeoutSeconds, s.MinTextLength,
		s.CrawlDelayMs, s.MaxResponseKB, s.PrioritizeUnindexed, s.Recurring,
		s.IntervalMinutes, s.MaxRuns, s.RunCount, s.Renderer, s.Enabled, s.InProgress, s.JobID,
		nullableTimeString(s.LastRunAt), s.NextRunAt.UTC().Format(crawledAtLayout), s.CreatedAt.UTC().Format(crawledAtLayout),
	)
	if err != nil {
		return fmt.Errorf("creating scheduled crawl: %w", err)
	}
	return nil
}

// GetScheduledCrawl returns the single schedule with the given id, or
// ports.ErrScheduledCrawlNotFound if none exists -- backs the admin
// schedule-detail/edit subpage's initial load.
func (r *Repository) GetScheduledCrawl(ctx context.Context, id string) (domain.ScheduledCrawl, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT `+scheduledCrawlColumns+` FROM scheduled_crawls WHERE id = %s`, 1), id)
	s, err := scanScheduledCrawl(row)
	if err == sql.ErrNoRows {
		return domain.ScheduledCrawl{}, ports.ErrScheduledCrawlNotFound
	}
	if err != nil {
		return domain.ScheduledCrawl{}, fmt.Errorf("querying scheduled crawl (%s): %w", id, err)
	}
	return s, nil
}

// ListScheduledCrawls lists every schedule, soonest next run first.
func (r *Repository) ListScheduledCrawls(ctx context.Context) ([]domain.ScheduledCrawl, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+scheduledCrawlColumns+` FROM scheduled_crawls ORDER BY next_run_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying scheduled crawls: %w", err)
	}
	defer rows.Close()

	var out []domain.ScheduledCrawl
	for rows.Next() {
		s, err := scanScheduledCrawl(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning scheduled crawl: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateScheduledCrawl replaces s's editable fields (everything but
// CreatedAt, LastRunAt and RunCount, which only CreateScheduledCrawl and
// MarkScheduledCrawlRun touch) -- MaxRuns itself is editable (an admin can
// raise, lower, or clear the cap), just not how many runs have already
// counted against it.
func (r *Repository) UpdateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error {
	seedJSON, err := json.Marshal(s.SeedURLs)
	if err != nil {
		return fmt.Errorf("encoding seed urls: %w", err)
	}
	allowedJSON, err := json.Marshal(s.AllowedDomains)
	if err != nil {
		return fmt.Errorf("encoding allowed domains: %w", err)
	}
	blockedJSON, err := json.Marshal(s.BlockedDomains)
	if err != nil {
		return fmt.Errorf("encoding blocked domains: %w", err)
	}
	updateSQL := r.ph(`UPDATE scheduled_crawls SET
	                      seed_urls = %s, max_pages = %s, respect_robots = %s, user_agent = %s,
	                      cookie = %s, basic_auth_user = %s, basic_auth_pass = %s,
	                      link_scope = %s, allowed_domains = %s, blocked_domains = %s, follow_indexed_domains = %s,
	                      use_sitemap = %s, fetch_timeout_seconds = %s,
	                      min_text_length = %s, crawl_delay_ms = %s, max_response_kb = %s,
	                      prioritize_unindexed = %s, recurring = %s, interval_minutes = %s, max_runs = %s,
	                      renderer = %s, enabled = %s, next_run_at = %s
	                    WHERE id = %s`, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24)
	res, err := r.db.ExecContext(ctx, updateSQL,
		string(seedJSON), s.MaxPages, s.RespectRobots, s.UserAgent,
		s.Cookie, s.BasicAuthUser, s.BasicAuthPass,
		s.LinkScope, string(allowedJSON), string(blockedJSON), s.FollowIndexedDomains,
		s.UseSitemap, s.FetchTimeoutSeconds,
		s.MinTextLength, s.CrawlDelayMs, s.MaxResponseKB,
		s.PrioritizeUnindexed, s.Recurring, s.IntervalMinutes, s.MaxRuns, s.Renderer, s.Enabled,
		s.NextRunAt.UTC().Format(crawledAtLayout), s.ID,
	)
	if err != nil {
		return fmt.Errorf("updating scheduled crawl (%s): %w", s.ID, err)
	}
	return requireRowsAffected(res, s.ID)
}

func (r *Repository) DeleteScheduledCrawl(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM scheduled_crawls WHERE id = %s`, 1), id)
	if err != nil {
		return fmt.Errorf("deleting scheduled crawl (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
}

// requireRowsAffected turns a zero-rows-affected result into
// ErrScheduledCrawlNotFound, so callers can tell "nothing to do" apart from
// "that ID doesn't exist".
func requireRowsAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking result for %s: %w", id, err)
	}
	if n == 0 {
		return ports.ErrScheduledCrawlNotFound
	}
	return nil
}

// DueScheduledCrawls lists every enabled, not-already-in-progress schedule
// whose next_run_at is at or before now, soonest-due first -- in_progress
// = false excludes an entry whose previously-triggered run hasn't finished.
func (r *Repository) DueScheduledCrawls(ctx context.Context, now time.Time) ([]domain.ScheduledCrawl, error) {
	query := r.ph(`SELECT `+scheduledCrawlColumns+` FROM scheduled_crawls
	               WHERE enabled = %s AND in_progress = %s AND next_run_at <= %s ORDER BY next_run_at ASC`, 1, 2, 3)
	rows, err := r.db.QueryContext(ctx, query, true, false, now.UTC().Format(crawledAtLayout))
	if err != nil {
		return nil, fmt.Errorf("querying due scheduled crawls: %w", err)
	}
	defer rows.Close()

	var out []domain.ScheduledCrawl
	for rows.Next() {
		s, err := scanScheduledCrawl(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning scheduled crawl: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkScheduledCrawlRun records a schedule's trigger or finish --
// TriggerDueCrawls calls this twice per run: once provisionally with
// inProgress=true and jobID set to the run's domain.CrawlJob, again on
// completion with inProgress=false and jobID="". enabled (the admin's
// toggle) and inProgress (what blocks double-triggering) are deliberately
// separate.
func (r *Repository) MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, enabled, inProgress bool, runCount int, jobID string) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET last_run_at = %s, next_run_at = %s, enabled = %s, in_progress = %s, run_count = %s, job_id = %s WHERE id = %s`, 1, 2, 3, 4, 5, 6, 7)
	res, err := r.db.ExecContext(ctx, updateSQL,
		lastRunAt.UTC().Format(crawledAtLayout), nextRunAt.UTC().Format(crawledAtLayout), enabled, inProgress, runCount, jobID, id)
	if err != nil {
		return fmt.Errorf("marking scheduled crawl run (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
}

// RunScheduledCrawlNow marks a schedule due immediately, discovered on the
// ticker's next tick -- conditional on the schedule not already being
// in_progress (see ports.ScheduledCrawlStore's doc comment for why forcing
// this while a crawl is genuinely still running is unsafe). If the WHERE
// clause matches nothing, a follow-up read distinguishes "doesn't exist"
// from "exists but already running" so the caller gets the right error.
func (r *Repository) RunScheduledCrawlNow(ctx context.Context, id string, now time.Time) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET next_run_at = %s, enabled = %s WHERE id = %s AND in_progress = %s`, 1, 2, 3, 4)
	res, err := r.db.ExecContext(ctx, updateSQL, now.UTC().Format(crawledAtLayout), true, id, false)
	if err != nil {
		return fmt.Errorf("running scheduled crawl now (%s): %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking result for %s: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	existing, getErr := r.GetScheduledCrawl(ctx, id)
	if getErr != nil {
		return getErr
	}
	if existing.InProgress {
		return ports.ErrScheduledCrawlInProgress
	}
	// Matched no rows despite existing and not being in_progress: a benign
	// race against another concurrent write to this same row between the
	// UPDATE and this re-check -- report not-found rather than silently
	// succeeding on a stale read.
	return ports.ErrScheduledCrawlNotFound
}

func (r *Repository) SetScheduledCrawlEnabled(ctx context.Context, id string, enabled bool) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET enabled = %s WHERE id = %s`, 1, 2)
	res, err := r.db.ExecContext(ctx, updateSQL, enabled, id)
	if err != nil {
		return fmt.Errorf("setting scheduled crawl %s enabled=%v: %w", id, enabled, err)
	}
	return requireRowsAffected(res, id)
}

// ResetStaleInProgress clears in_progress for every schedule stuck true
// whose job_id does NOT correspond to a still-queued/running crawl_jobs
// row, run once at crawl-server startup before the ticker's first tick and
// always after RecoverInterruptedCrawls (whose resumed jobs must already
// be reflected in crawl_jobs' status by the time this query runs).
//
// This is deliberately NOT "clear every stuck-true row" -- an earlier
// version of this method did exactly that, on the reasoning that "nothing
// can genuinely be in-progress the instant this process starts, since the
// in-memory closure that would clear it died with whatever process set
// it." That reasoning has a real gap: RecoverInterruptedCrawls, which runs
// moments before this in the same startup sequence, can RESUME a job that
// was queued/running when the process died -- that job (and the schedule
// that triggered it) really is in progress again, right now, in this very
// process. Blindly clearing in_progress for it let the very next scheduler
// tick trigger a SECOND, duplicate crawl of the same site while the
// resumed one was still running (confirmed in production against
// cnn.com). job_id NOT IN (a still-active crawl_jobs row) correctly
// distinguishes "this schedule's job is genuinely gone" (job_id is ” from
// before this column existed, the job was deleted, or it reached a
// terminal status) from "this schedule's job was just resumed and is
// still actually running." Returns how many rows were reset (0 is the
// healthy case).
func (r *Repository) ResetStaleInProgress(ctx context.Context) (int, error) {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET in_progress = %s, job_id = %s
	                    WHERE in_progress = %s
	                      AND job_id NOT IN (SELECT id FROM crawl_jobs WHERE status = %s OR status = %s)`,
		1, 2, 3, 4, 5)
	res, err := r.db.ExecContext(ctx, updateSQL, false, "", true, string(domain.CrawlJobQueued), string(domain.CrawlJobRunning))
	if err != nil {
		return 0, fmt.Errorf("resetting stale in_progress flags: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting reset in_progress flags: %w", err)
	}
	return int(n), nil
}

const embeddingEndpointColumns = `id, name, base_url, api_key, model, dimensions, rate_limit_per_second, enabled, chunk_size_tokens, tokenize_url, created_at`

// CreateEmbeddingEndpoint inserts a new admin-configured HTTP embedding
// endpoint (see domain.EmbeddingHTTPEndpoint).
func (r *Repository) CreateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error {
	insertSQL := r.ph(`INSERT INTO embedding_http_endpoints (`+embeddingEndpointColumns+`)
	                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	_, err := r.db.ExecContext(ctx, insertSQL,
		e.ID, e.Name, e.BaseURL, e.APIKey, e.Model, e.Dimensions, e.RateLimitPerSecond, e.Enabled,
		e.ChunkSizeTokens, e.TokenizeURL,
		e.CreatedAt.UTC().Format(crawledAtLayout),
	)
	if err != nil {
		return fmt.Errorf("creating embedding endpoint: %w", err)
	}
	return nil
}

// GetEmbeddingEndpoint returns the single endpoint with the given id, or
// ports.ErrEmbeddingEndpointNotFound if none exists.
func (r *Repository) GetEmbeddingEndpoint(ctx context.Context, id string) (domain.EmbeddingHTTPEndpoint, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT `+embeddingEndpointColumns+` FROM embedding_http_endpoints WHERE id = %s`, 1), id)
	e, err := scanEmbeddingEndpoint(row)
	if err == sql.ErrNoRows {
		return domain.EmbeddingHTTPEndpoint{}, ports.ErrEmbeddingEndpointNotFound
	}
	if err != nil {
		return domain.EmbeddingHTTPEndpoint{}, fmt.Errorf("querying embedding endpoint (%s): %w", id, err)
	}
	return e, nil
}

// ListEmbeddingEndpoints lists every configured endpoint, oldest first.
func (r *Repository) ListEmbeddingEndpoints(ctx context.Context) ([]domain.EmbeddingHTTPEndpoint, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+embeddingEndpointColumns+` FROM embedding_http_endpoints ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying embedding endpoints: %w", err)
	}
	defer rows.Close()

	var out []domain.EmbeddingHTTPEndpoint
	for rows.Next() {
		e, err := scanEmbeddingEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning embedding endpoint: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateEmbeddingEndpoint replaces e's editable fields (everything but
// ID/CreatedAt, which never change after creation).
func (r *Repository) UpdateEmbeddingEndpoint(ctx context.Context, e domain.EmbeddingHTTPEndpoint) error {
	updateSQL := r.ph(`UPDATE embedding_http_endpoints SET
	                      name = %s, base_url = %s, api_key = %s, model = %s,
	                      dimensions = %s, rate_limit_per_second = %s, enabled = %s,
	                      chunk_size_tokens = %s, tokenize_url = %s
	                    WHERE id = %s`, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	res, err := r.db.ExecContext(ctx, updateSQL,
		e.Name, e.BaseURL, e.APIKey, e.Model, e.Dimensions, e.RateLimitPerSecond, e.Enabled,
		e.ChunkSizeTokens, e.TokenizeURL, e.ID,
	)
	if err != nil {
		return fmt.Errorf("updating embedding endpoint (%s): %w", e.ID, err)
	}
	return requireEmbeddingEndpointRowsAffected(res, e.ID)
}

// DeleteEmbeddingEndpoint removes an endpoint's config only -- its stored
// document_embeddings rows and ANN column are left in place, unused, the
// same non-destructive convention as disabling a provider.
func (r *Repository) DeleteEmbeddingEndpoint(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM embedding_http_endpoints WHERE id = %s`, 1), id)
	if err != nil {
		return fmt.Errorf("deleting embedding endpoint (%s): %w", id, err)
	}
	return requireEmbeddingEndpointRowsAffected(res, id)
}

func requireEmbeddingEndpointRowsAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking result for %s: %w", id, err)
	}
	if n == 0 {
		return ports.ErrEmbeddingEndpointNotFound
	}
	return nil
}

func scanEmbeddingEndpoint(row scanner) (domain.EmbeddingHTTPEndpoint, error) {
	var e domain.EmbeddingHTTPEndpoint
	var createdAt string
	if err := row.Scan(&e.ID, &e.Name, &e.BaseURL, &e.APIKey, &e.Model,
		&e.Dimensions, &e.RateLimitPerSecond, &e.Enabled,
		&e.ChunkSizeTokens, &e.TokenizeURL, &createdAt); err != nil {
		return domain.EmbeddingHTTPEndpoint{}, err
	}
	e.CreatedAt = parseCrawledAt(createdAt)
	return e, nil
}

// chatEndpointRowID is the fixed sentinel row id chat_endpoint's single row
// always uses -- chat only ever has one active configuration (unlike
// embedding_http_endpoints, a list of many blended providers), so
// Get/SetChatEndpoint address one known row rather than a caller-supplied
// key.
const chatEndpointRowID = "default"

const chatEndpointColumns = "base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, system_prompt, updated_at"

// GetChatEndpoint returns the single admin-configured chat endpoint, or
// ports.ErrChatEndpointNotConfigured if it has never been saved.
func (r *Repository) GetChatEndpoint(ctx context.Context) (domain.ChatEndpoint, error) {
	query := r.ph(`SELECT `+chatEndpointColumns+` FROM chat_endpoint WHERE id = %s`, 1)
	row := r.db.QueryRowContext(ctx, query, chatEndpointRowID)
	e, err := scanChatEndpoint(row)
	if err == sql.ErrNoRows {
		return domain.ChatEndpoint{}, ports.ErrChatEndpointNotConfigured
	}
	if err != nil {
		return domain.ChatEndpoint{}, fmt.Errorf("querying chat endpoint: %w", err)
	}
	return e, nil
}

// SetChatEndpoint upserts the single chat_endpoint sentinel row (id =
// chatEndpointRowID) with e's fields, replacing whatever was saved before --
// there is only ever one row, so this is a create on first call and an
// in-place replace on every call after.
func (r *Repository) SetChatEndpoint(ctx context.Context, e domain.ChatEndpoint) error {
	_, err := r.db.ExecContext(ctx, r.dialect.UpsertChatEndpointSQL(),
		// false, 0: rag_enabled/rag_result_count are orphaned columns --
		// domain.ChatEndpoint no longer has a RAG concept, but the columns
		// stay (no schema migration needed) so the statement's
		// column/placeholder count is unchanged.
		chatEndpointRowID, e.BaseURL, e.APIKey, e.Model, e.Enabled, false, 0,
		e.MaxContextTokens, e.WebSearchEnabled, e.WebSearchBaseURL, e.WebSearchResultCount,
		e.SystemPrompt, e.UpdatedAt.UTC().Format(crawledAtLayout),
	)
	if err != nil {
		return fmt.Errorf("setting chat endpoint: %w", err)
	}
	return nil
}

func scanChatEndpoint(row scanner) (domain.ChatEndpoint, error) {
	var e domain.ChatEndpoint
	var updatedAt string
	// ragEnabledUnused/ragResultCountUnused: rag_enabled/rag_result_count are
	// orphaned columns -- domain.ChatEndpoint no longer has a RAG concept,
	// but the columns stay (no schema migration needed), so these two
	// throwaway locals just absorb the Scan positionally.
	var ragEnabledUnused bool
	var ragResultCountUnused int
	if err := row.Scan(&e.BaseURL, &e.APIKey, &e.Model, &e.Enabled, &ragEnabledUnused,
		&ragResultCountUnused, &e.MaxContextTokens, &e.WebSearchEnabled, &e.WebSearchBaseURL,
		&e.WebSearchResultCount, &e.SystemPrompt, &updatedAt); err != nil {
		return domain.ChatEndpoint{}, err
	}
	e.UpdatedAt = parseCrawledAt(updatedAt)
	return e, nil
}

const chatHookColumns = "id, name, pattern, script, enabled, prompt, gated_by_web_search"

// ListChatHooks lists every configured hook, ordered by name for a stable,
// human-friendly admin table order (chat_hooks has no created_at column to
// order by insertion, unlike embedding_http_endpoints).
func (r *Repository) ListChatHooks(ctx context.Context) ([]domain.ChatHook, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+chatHookColumns+` FROM chat_hooks ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying chat hooks: %w", err)
	}
	defer rows.Close()

	var out []domain.ChatHook
	for rows.Next() {
		h, err := scanChatHook(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning chat hook: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CreateChatHook inserts a new admin-configured chat hook (see
// domain.ChatHook).
func (r *Repository) CreateChatHook(ctx context.Context, h domain.ChatHook) error {
	insertSQL := r.ph(`INSERT INTO chat_hooks (`+chatHookColumns+`) VALUES (%s, %s, %s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5, 6, 7)
	if _, err := r.db.ExecContext(ctx, insertSQL, h.ID, h.Name, h.Pattern, h.Script, h.Enabled, h.Prompt, h.GatedByWebSearch); err != nil {
		return fmt.Errorf("creating chat hook: %w", err)
	}
	return nil
}

// UpdateChatHook replaces h's editable fields (everything but ID, which
// never changes after creation), returning ports.ErrChatHookNotFound if no
// hook with h.ID exists.
func (r *Repository) UpdateChatHook(ctx context.Context, h domain.ChatHook) error {
	updateSQL := r.ph(`UPDATE chat_hooks SET name = %s, pattern = %s, script = %s, enabled = %s, prompt = %s, gated_by_web_search = %s WHERE id = %s`, 1, 2, 3, 4, 5, 6, 7)
	res, err := r.db.ExecContext(ctx, updateSQL, h.Name, h.Pattern, h.Script, h.Enabled, h.Prompt, h.GatedByWebSearch, h.ID)
	if err != nil {
		return fmt.Errorf("updating chat hook (%s): %w", h.ID, err)
	}
	return requireChatHookRowsAffected(res, h.ID)
}

// DeleteChatHook removes a hook's config, returning ports.ErrChatHookNotFound
// if no hook with id exists.
func (r *Repository) DeleteChatHook(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM chat_hooks WHERE id = %s`, 1), id)
	if err != nil {
		return fmt.Errorf("deleting chat hook (%s): %w", id, err)
	}
	return requireChatHookRowsAffected(res, id)
}

func requireChatHookRowsAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking result for %s: %w", id, err)
	}
	if n == 0 {
		return ports.ErrChatHookNotFound
	}
	return nil
}

func scanChatHook(row scanner) (domain.ChatHook, error) {
	var h domain.ChatHook
	if err := row.Scan(&h.ID, &h.Name, &h.Pattern, &h.Script, &h.Enabled, &h.Prompt, &h.GatedByWebSearch); err != nil {
		return domain.ChatHook{}, err
	}
	return h, nil
}

func nullableTimeString(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(crawledAtLayout), Valid: true}
}

// parseCrawledAt parses s (expected in crawledAtLayout) into a time.Time,
// silently leaving the zero value on a parse error -- the same
// "malformed/missing timestamp isn't worth failing the whole row over"
// convention every timestamp column in this package already follows.
func parseCrawledAt(s string) time.Time {
	t, _ := time.Parse(crawledAtLayout, s)
	return t
}

// parseNullableCrawledAt is parseCrawledAt's counterpart for an optional
// timestamp column (started_at, finished_at, last_run_at): a NULL/invalid
// value yields a nil *time.Time rather than a zero-value one, matching
// domain's own convention for "this hasn't happened yet."
func parseNullableCrawledAt(ns sql.NullString) *time.Time {
	if !ns.Valid {
		return nil
	}
	if t, err := time.Parse(crawledAtLayout, ns.String); err == nil {
		return &t
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows, so
// scanScheduledCrawl works for either a single-row Get or a multi-row List.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanScheduledCrawl(row scanner) (domain.ScheduledCrawl, error) {
	var s domain.ScheduledCrawl
	var seedJSON string
	var allowedJSON, blockedJSON sql.NullString
	var lastRunAt sql.NullString
	var nextRunAt, createdAt string
	if err := row.Scan(&s.ID, &seedJSON, &s.MaxPages, &s.RespectRobots, &s.UserAgent,
		&s.Cookie, &s.BasicAuthUser, &s.BasicAuthPass,
		&s.LinkScope, &allowedJSON, &blockedJSON, &s.FollowIndexedDomains,
		&s.UseSitemap, &s.FetchTimeoutSeconds, &s.MinTextLength,
		&s.CrawlDelayMs, &s.MaxResponseKB, &s.PrioritizeUnindexed, &s.Recurring,
		&s.IntervalMinutes, &s.MaxRuns, &s.RunCount, &s.Renderer, &s.Enabled, &s.InProgress, &s.JobID,
		&lastRunAt, &nextRunAt, &createdAt); err != nil {
		return domain.ScheduledCrawl{}, err
	}
	if err := json.Unmarshal([]byte(seedJSON), &s.SeedURLs); err != nil {
		return domain.ScheduledCrawl{}, fmt.Errorf("decoding seed urls: %w", err)
	}
	// allowed_domains/blocked_domains predate this feature on a database
	// that hasn't been migrated past it yet -- a NULL, empty, or malformed
	// value there just means "no list set" rather than a reason to fail the
	// whole row, same tolerant convention parseCrawledAt already uses for a
	// malformed timestamp.
	if allowedJSON.Valid && allowedJSON.String != "" {
		_ = json.Unmarshal([]byte(allowedJSON.String), &s.AllowedDomains)
	}
	if blockedJSON.Valid && blockedJSON.String != "" {
		_ = json.Unmarshal([]byte(blockedJSON.String), &s.BlockedDomains)
	}
	s.LastRunAt = parseNullableCrawledAt(lastRunAt)
	s.NextRunAt = parseCrawledAt(nextRunAt)
	s.CreatedAt = parseCrawledAt(createdAt)
	return s, nil
}

func (r *Repository) ph(template string, positions ...int) string {
	args := make([]interface{}, len(positions))
	for i, pos := range positions {
		args[i] = r.dialect.Placeholder(pos)
	}
	return fmt.Sprintf(template, args...)
}
