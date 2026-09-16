package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const crawledAtLayout = time.RFC3339Nano

// newDocumentPlaceholderPageRank is the pagerank a brand-new document row
// gets at insert time, before application.RunPageRankJob has ever had a
// chance to score it -- see SaveDocument's sql.ErrNoRows branch. Order of
// magnitude only: a fixed value in the same ballpark as 1/N for a
// mid-sized (order-10,000-document) corpus, strictly positive (never the
// column's bare-0 default) and cheap to produce (no query needed at all).
// Any crawled corpus this is ever slightly too big or small for gets
// corrected the moment RunPageRankJob next runs, exactly like this
// codebase already treats backfillPageRank's and domain.PageRank's own
// neutral placeholder values.
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
	// The very first touch of a brand new sqlite file -- this Ping -- can
	// itself race with another process's own concurrent open/create
	// (SQLITE_BUSY), before there's even been a connection yet to set
	// busy_timeout on (that happens further down, once dialect-specific
	// setup starts) -- observed in practice: search-server's Ping failing
	// this way while admin-server and crawl-server started at the same
	// moment against a brand new file. retrySQLiteBusy is a harmless
	// no-op for every other dialect (isSQLiteBusyError never matches
	// their error text) and for sqlite once the file already exists (the
	// overwhelmingly common case after the very first start).
	if err := retrySQLiteBusy(ctx, func() error { return db.PingContext(ctx) }); err != nil {
		return nil, fmt.Errorf("DB ping (%s): %w", driverName, err)
	}

	repo := &Repository{db: db, dialect: NewDialect(driverName)}
	repo.applyDefaultPoolSettings()
	// search-server, admin-server and crawl-server each open (and migrate)
	// the same SQLite file independently at startup, with no coordination
	// between them -- SQLite's own file-level locking, not this process,
	// is what has to serialize their concurrent CREATE TABLE/INDEX
	// statements. Without a busy timeout, SQLite fails a migration outright
	// the instant it finds the file locked (SQLITE_BUSY) rather than
	// waiting the few milliseconds another process's migration actually
	// takes; the isAlreadyExistsError handling elsewhere only covers
	// one specific step of that same race, not the table-creation
	// statements that run first. This is a no-op for every other dialect.
	if repo.dialect.Name() == "sqlite" {
		if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			return nil, fmt.Errorf("setting busy_timeout (sqlite): %w", err)
		}
		// search-server, admin-server and crawl-server are three separate OS
		// processes, each opening its own *sql.DB (clamped to a single
		// connection by ConfigurePool) against the same search.db file.
		// Under SQLite's default rollback-journal mode, a writer's
		// transaction (crawl-server's SaveDocument -- select/archive/prune/
		// upsert/rewrite postings+links, all in one commit) takes a
		// RESERVED/EXCLUSIVE lock that blocks every concurrent reader
		// (search-server's PostingsForTerms/CorpusStats) until COMMIT. WAL
		// mode removes that: readers proceed against the last-committed
		// snapshot in the WAL file while a single writer appends to it, so
		// readers never block behind the writer. synchronous=NORMAL is
		// WAL's documented standard pairing -- it skips one of the two
		// fsyncs FULL performs per commit (safe under WAL: a crash can lose
		// the most recent commit but never corrupts the database, unlike
		// under rollback-journal mode where NORMAL can permit corruption).
		// The very first time WAL mode is set up on a brand new database
		// file -- e.g. all three binaries starting for the first time at
		// once, or in CI's install smoke test -- this PRAGMA can return
		// SQLITE_BUSY immediately rather than honoring busy_timeout's own
		// retry window (observed in practice both locally and in CI: the
		// error surfaces within milliseconds, not after waiting out the
		// 5-second busy_timeout above). retrySQLiteBusy is cheap, short-
		// lived insurance against that one-time startup race; once the
		// file is already in WAL mode (every subsequent process start,
		// overwhelmingly the common case), this is a fast no-op that never
		// retries at all.
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

// applyDefaultPoolSettings applies the built-in operational-settings
// connection-pool defaults immediately at construction time, before any
// admin-configured value (if one is ever loaded) is applied via
// ConfigurePool -- so a freshly started process never runs with Go's
// unbounded-open/2-idle database/sql defaults, even for the brief window
// before bootstrap.SyncSettings's first poll completes.
func (r *Repository) applyDefaultPoolSettings() {
	d := domain.DefaultOperationalSettings().Get()
	r.ConfigurePool(d.DBMaxOpenConns, d.DBMaxIdleConns, d.DBConnMaxLifetime)
}

// ConfigurePool applies connection-pool limits to the live database
// connection: at most maxOpenConns open connections, up to maxIdleConns of
// them kept idle rather than closed between requests, and each connection
// recycled after connMaxLifetime regardless of use (0 means never). Called
// once at construction with the built-in defaults, and again by
// bootstrap.SyncSettings whenever the admin-configured operational
// settings change, so a production Postgres/MySQL deployment always keeps
// a bounded, warm pool instead of churning connections open and closed
// past database/sql's default of only 2 cached idle connections.
//
// For SQLite, maxOpenConns (and, if larger, maxIdleConns) is always
// clamped down to 1 regardless of what's requested: SQLite serializes
// writers at the file level, so more than one open connection doesn't add
// real concurrency and only risks "database is locked" errors under
// concurrent writes.
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
	for _, stmt := range r.dialect.CreateSchemaSQL() {
		if _, err := r.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	if err := r.migrateDocumentColumns(ctx); err != nil {
		return err
	}
	if err := r.migrateScheduledCrawlColumns(ctx); err != nil {
		return err
	}
	if err := r.ensureHostIndex(ctx); err != nil {
		return err
	}
	return r.ensureCrawledAtIndex(ctx)
}

// migrateScheduledCrawlColumns adds the per-crawl override columns (fetch
// timeout, minimum text length, crawl delay, max response size,
// prioritize-unindexed) to a scheduled_crawls table that predates them --
// CREATE TABLE IF NOT EXISTS above only shapes a fresh table. A
// pre-existing schedule defaults to 0/false for all five, same as a
// one-off crawl leaving them blank: "use whatever's configured on the
// Tuning page."
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
	// place, unused, on a database that has it -- this project's
	// migrations only ever add columns). Defaulting a pre-existing row to
	// '' (inherit the Tuning page's global default, domain.LinkScopeDomain)
	// rather than translating its old boolean is a deliberate behavior
	// change for any schedule that predates this column, same tradeoff as
	// every other breaking change in this app: downwards compatibility
	// isn't a concern here.
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
	return addColumn("in_progress", "in_progress BOOLEAN NOT NULL DEFAULT false")
}

// ensureHostIndex and ensureCrawledAtIndex both run after
// migrateDocumentColumns, since on a database that predates the host
// column, an index on it can't be created any earlier -- CreateSchemaSQL's
// CREATE TABLE IF NOT EXISTS is a no-op against an existing table, so the
// column wouldn't exist yet if this were part of that same statement list
// (crawled_at itself has been part of the base schema since before its
// index existed, so for it this is purely about the missing index, not a
// missing column). Both delegate to ensureIndex, which handles MySQL's
// lack of CREATE INDEX IF NOT EXISTS (a best-effort statement whose
// "already exists" error is expected and ignored on every startup after
// the first) and the concurrent-migration race on Postgres/SQLite
// (isAlreadyExistsError).
func (r *Repository) ensureHostIndex(ctx context.Context) error {
	return r.ensureIndex(ctx, "idx_documents_host", "host")
}

func (r *Repository) ensureCrawledAtIndex(ctx context.Context) error {
	return r.ensureIndex(ctx, "idx_documents_crawled_at", "crawled_at")
}

// ensureIndex creates a single-column index on documents(column) if it
// doesn't already exist, tolerating both MySQL's lack of IF NOT EXISTS and
// the benign concurrent-creation race the other dialects can hit when
// search/admin/crawl all migrate on startup at once (see
// isAlreadyExistsError).
func (r *Repository) ensureIndex(ctx context.Context, indexName, column string) error {
	if r.dialect.Name() == "mysql" {
		_, _ = r.db.ExecContext(ctx, "CREATE INDEX "+indexName+" ON documents("+column+")")
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+indexName+" ON documents("+column+")"); err != nil && !isAlreadyExistsError(err) {
		return fmt.Errorf("creating %s: %w", indexName, err)
	}
	return nil
}

// isAlreadyExistsError reports whether err is the benign race where two of
// search/admin/crawl migrate the same not-yet-upgraded table at once (each
// opens its own DB connection and migrates on startup): both see a column
// or index missing, both try to add it, and the loser gets an
// already-exists/duplicate error from the database instead of a clean
// no-op, even though the column/index ends up created either way. Without
// tolerating this, that race crashes the losing process outright. Covers
// Postgres ("already exists" for both CREATE INDEX and ADD COLUMN, plus
// the unique-violation phrasing CREATE INDEX CONCURRENTLY's catalog race
// can produce) and MySQL (which phrases a duplicate column as "Duplicate
// column name" rather than "already exists").
func isAlreadyExistsError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "duplicate key value violates unique constraint") ||
		strings.Contains(msg, "Duplicate column name")
}

// isSQLiteBusyError reports whether err is modernc.org/sqlite's
// SQLITE_BUSY, phrased as "database is locked (5)" (plain busy) or
// "(261)"/"(517)" (the WAL-mode and RESERVED-lock variants SQLITE_BUSY
// carries as extended result codes) -- all three show up when two
// processes race to convert a brand new database file to WAL mode at
// once, see retrySQLiteBusy.
func isSQLiteBusyError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// retrySQLiteBusy runs fn, retrying with a short backoff if it fails with
// SQLITE_BUSY. PRAGMA busy_timeout only governs SQLite's own internal wait
// loop for a lock held by another connection's in-flight statement; the
// one-time conversion of a brand new file to WAL mode involves SQLite
// re-opening the database's shared-memory (-shm) file, which can hand back
// SQLITE_BUSY immediately, before busy_timeout's retry logic ever engages
// -- see the call sites in New(). A handful of short retries is enough to
// let the winning process finish that one-time setup.
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
// documents table that predates them (CREATE TABLE IF NOT EXISTS above only
// shapes a fresh table) and backfills host/norm_embedding for any
// pre-existing row, so an upgrade never requires a manual migration step.
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
	if err := r.backfillHost(ctx); err != nil {
		return err
	}
	if err := r.backfillNormEmbedding(ctx); err != nil {
		return err
	}
	return r.backfillPageRank(ctx)
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

// backfillNormEmbedding fills in norm_embedding for any row saved before
// that column existed (it defaults to 0, indistinguishable from a
// genuinely all-zero embedding -- recomputing a zero-vector's norm as 0
// again is harmless, just a no-op). Computed once here per pre-existing
// row rather than left to be recomputed from scratch on every future
// search request that scores the document.
func (r *Repository) backfillNormEmbedding(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, r.ph(`SELECT id, embedding FROM documents WHERE norm_embedding = %s`, 1), 0)
	if err != nil {
		return fmt.Errorf("finding rows needing a norm_embedding backfill: %w", err)
	}
	type idEmbedding struct {
		id      string
		embBlob []byte
	}
	var pending []idEmbedding
	for rows.Next() {
		var ie idEmbedding
		if err := rows.Scan(&ie.id, &ie.embBlob); err != nil {
			rows.Close()
			return fmt.Errorf("scanning row: %w", err)
		}
		pending = append(pending, ie)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	updateSQL := r.ph(`UPDATE documents SET norm_embedding = %s WHERE id = %s`, 1, 2)
	for _, ie := range pending {
		vec, err := DecodeEmbedding(ie.embBlob)
		if err != nil {
			return fmt.Errorf("deserializing embedding for norm backfill (%s): %w", ie.id, err)
		}
		norm := domain.VectorNorm(vec)
		if norm == 0 {
			continue // already 0; nothing to update
		}
		if _, err := r.db.ExecContext(ctx, updateSQL, norm, ie.id); err != nil {
			return fmt.Errorf("backfilling norm_embedding for %s: %w", ie.id, err)
		}
	}
	return nil
}

// backfillPageRank fills in pagerank for any row saved before that column
// existed (it defaults to 0) with a neutral 1/N score rather than leaving
// it at 0 -- a bare 0 would unfairly rank every pre-existing document dead
// last on PageRank the moment an admin turns on PageRankWeight, before
// application.RunPageRankJob has ever had a chance to compute a real
// score. A real PageRank score is always strictly positive (see
// domain.PageRank's base (1-d)/N term, added unconditionally every
// iteration), so "pagerank = 0" unambiguously means "never assigned",
// exactly like backfillNormEmbedding's use of 0 for "never computed".
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

// SaveDocument upserts doc keyed by its ID (callers derive that ID
// deterministically from the URL, so re-crawling the same page always
// lands on the same row rather than creating a duplicate). When the new
// content actually differs from what's on record, the previous version is
// archived to document_versions and the version counter advances, and any
// archived versions beyond maxVersions-1 (the current row in documents
// makes up the "+1") are pruned, oldest first, in the same transaction;
// re-confirming unchanged content just refreshes crawled_at and prunes
// nothing.
func (r *Repository) SaveDocument(ctx context.Context, doc domain.Document, embedding []float32, maxVersions, titleWeight int) error {
	// The title is counted titleWeight times before the body: postings
	// stores one merged term_freq per (term, doc) rather than a separate
	// per-field count (no BM25F-style fielded formula), so the simplest way
	// to give a title match more weight than the same word appearing in the
	// body is to make it contribute that many times more to term_freq --
	// see domain.OperationalSettingsValues.TitleWeight. A non-positive
	// value (the caller passed a zero value rather than a real setting, as
	// in an older test) is treated as 1, the same as any other unweighted
	// term, rather than dropping the title from the token stream entirely.
	// Like every other indexing-time setting (min text length, crawl
	// delay, ...), this only takes effect for documents crawled or
	// re-crawled after a change to it -- it isn't retroactively applied to
	// already-indexed content.
	if titleWeight <= 0 {
		titleWeight = 1
	}
	tokens := domain.Tokenize(strings.Repeat(doc.Title+" ", titleWeight) + doc.Text)
	embBlob := EncodeEmbedding(embedding)
	// Computed once here, at write time, and persisted alongside the
	// embedding -- so every future search request that scores this
	// document against a query reuses this norm instead of recomputing a
	// full sum-of-squares pass over the embedding from scratch.
	normEmbedding := domain.VectorNorm(embedding)
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

	if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentSQL(),
		doc.ID, doc.URL, doc.Title, doc.Text, len(tokens), embBlob, normEmbedding, pagerank, host, version, now,
	); err != nil {
		return fmt.Errorf("saving document: %w", err)
	}

	// Also populate the pgvector column used by the ANN path (see ann.go),
	// alongside (never instead of) the packed-binary embedding column above -- that
	// column stays the source of truth for SQLite/MySQL, and is a harmless
	// duplicate on Postgres. A plain UPDATE right after the upsert rather
	// than folding it into UpsertDocumentSQL, since that statement is
	// shared verbatim across all three dialects and embedding_vector only
	// exists (and only ever should be written to) on Postgres once
	// EnableANN has actually succeeded for this process.
	if r.ann.isAvailable() {
		vecSQL := r.ph(`UPDATE documents SET embedding_vector = %s::vector WHERE id = %s`, 1, 2)
		if _, err := tx.ExecContext(ctx, vecSQL, formatPgVectorLiteral(embedding), doc.ID); err != nil {
			return fmt.Errorf("saving embedding vector: %w", err)
		}
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

// saveDocumentInsertBatchSize bounds how many postings or links rows one
// multi-row "INSERT ... VALUES (...),(...),..." statement in SaveDocument
// covers, following the same batching principle UpdatePageRanks/
// updatePageRankBatch already apply to the pagerank write path -- applied
// here to the postings/links rewrite that runs on literally every crawled
// page, replacing what used to be one INSERT per term/link row. Each row
// contributes 3 placeholders, and SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER is 999 -- 300 rows/chunk (900 params) stays
// comfortably under that regardless of dialect, since Postgres/MySQL's own
// per-statement parameter limits are far higher and never the binding
// constraint.
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

// PostingsForTerms batch-fetches postings for every one of terms in a
// single "WHERE term IN (...)" query joined against documents, instead of
// hybrid search's caller running one such join (plus a separate doc-freq
// COUNT(*) query) per unique query term. Each term's DocFreq is simply the
// number of rows that came back for it -- postings has exactly one row per
// (term, doc_id) pair, so counting the grouped rows in Go is equivalent to
// (and cheaper than) a separate "SELECT COUNT(*) ... GROUP BY term" query.
// TotalDocs/AvgDocLen are deliberately left zero here -- see the
// ports.SQLRepository.PostingsForTerms doc comment -- since the caller
// fetches those once per request (or less often, from an in-memory cache)
// rather than once per term.
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
	"app_settings", "scheduled_crawls", "crawl_jobs", "crawl_job_pages", "sessions",
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

// VocabularyStats reports the total number of distinct indexed terms
// (vocabSize, always corpus-wide) plus a limit/offset page of terms
// ordered by sortBy/sortDir (see vocabularySortColumn; ties are always
// broken by term ascending, for a stable order across pages). When search
// is non-empty, both the page and matchedCount (the total this search
// matches, before limit/offset -- for the caller to compute a page count)
// are restricted to terms containing it; matchedCount equals vocabSize
// when search is empty.
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
// rows into a map, shared by EmbeddingsForDocs and SampleEmbeddings so both
// stay consistent about how the embedding column is deserialized. The norm
// and pagerank are read straight off their own columns -- computed once at
// SaveDocument time (or by the norm_embedding/pagerank backfills, or --
// for pagerank -- a later application.RunPageRankJob run) -- rather than
// recomputed here from the deserialized vector.
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

// EmbeddingsForDocs batch-fetches embeddings for exactly the given doc IDs
// (a single "WHERE id IN (...)" query), so a search only ever deserializes
// embeddings for documents it actually needs -- never the whole corpus.
func (r *Repository) EmbeddingsForDocs(ctx context.Context, ids []string) (map[string]domain.EmbeddedVector, error) {
	if len(ids) == 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT id, embedding, norm_embedding, pagerank FROM documents WHERE id IN (` + r.placeholderList(len(ids), 1) + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying embeddings for docs: %w", err)
	}
	defer rows.Close()
	return scanEmbeddingRows(rows)
}

// SampleEmbeddings returns up to limit embeddings from across the corpus
// (SQL-bounded via LIMIT, so the query cost never scales with corpus size),
// used to fill out a search's semantic candidate pool beyond its BM25 hits.
// A non-positive limit returns an empty map without touching the database.
func (r *Repository) SampleEmbeddings(ctx context.Context, limit int) (map[string]domain.EmbeddedVector, error) {
	if limit <= 0 {
		return map[string]domain.EmbeddedVector{}, nil
	}
	query := r.ph(`SELECT id, embedding, norm_embedding, pagerank FROM documents ORDER BY id LIMIT %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("sampling embeddings: %w", err)
	}
	defer rows.Close()
	return scanEmbeddingRows(rows)
}

// DocumentsByIDs batch-fetches documents for the given IDs in a single
// "WHERE id IN (...)" query, for callers (e.g. hybrid search's
// constraint/boost/recency filtering) that would otherwise need one round
// trip per candidate -- an ID with no matching row is simply absent from
// the result rather than an error, so a candidate whose document was
// deleted concurrently is silently dropped by the caller rather than
// failing the whole search.
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

// DocumentsByIDsSortedByCrawledAt batch-fetches documents for the given IDs
// exactly like DocumentsByIDs (an ID with no matching row is simply absent,
// not an error), but returns them as a slice in descending crawled_at order
// (ties broken by id ascending, for a deterministic order) -- pushed down
// into SQL and served by idx_documents_crawled_at, so the hybrid search
// service's recency-sort path never needs an in-app sort.Slice over the
// fetched candidates.
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

// DocumentIDsByHost returns the IDs of documents whose host exactly
// matches one of hosts, or is a subdomain of one (host = ? OR host LIKE
// '%.'+?), mirroring domain.ParsedQuery.SiteAllowed's matching rule.
// Served by idx_documents_host rather than a full table scan.
func (r *Repository) DocumentIDsByHost(ctx context.Context, hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	conditions := make([]string, 0, len(hosts))
	args := make([]interface{}, 0, len(hosts)*2)
	pos := 1
	for _, h := range hosts {
		conditions = append(conditions, fmt.Sprintf("(host = %s OR host LIKE %s)", r.dialect.Placeholder(pos), r.dialect.Placeholder(pos+1)))
		args = append(args, h, "%."+h)
		pos += 2
	}
	query := `SELECT id FROM documents WHERE ` + strings.Join(conditions, " OR ") +
		r.ph(` LIMIT %s`, pos)
	args = append(args, maxDocumentIDsByHost)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying document ids by host: %w", err)
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

// AllDocumentIDs lists every document ID in the corpus, ordered by id for
// stable, deterministic pagination -- used by
// application.RunEmbeddingRecomputeJob to walk the whole corpus in
// bounded-size batches (via DocumentsByIDs) rather than loading every
// document's text into memory at once. A plain "SELECT id", with none of
// ListDocuments' link-stat joins or DocumentIDsByHost's per-host filter --
// neither is relevant to just enumerating every ID once.
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

// UpdateEmbedding overwrites one document's embedding (and norm_embedding,
// recomputed to match) -- the narrow write half of SaveDocument's embedding
// handling, without touching text/postings/links/document_versions/
// pagerank/host, none of which change when a document's vector
// representation is recomputed against its own already-stored text (e.g.
// after an OperationalSettingsValues.EmbeddingProvider change -- see
// application.RunEmbeddingRecomputeJob). Also refreshes the Postgres
// pgvector column when ANN is enabled for this process, mirroring
// SaveDocument's own handling of that column.
func (r *Repository) UpdateEmbedding(ctx context.Context, id string, embedding []float32) error {
	embBlob := EncodeEmbedding(embedding)
	normEmbedding := domain.VectorNorm(embedding)
	updateSQL := r.ph(`UPDATE documents SET embedding = %s, norm_embedding = %s WHERE id = %s`, 1, 2, 3)
	// No rows-affected check: a document deleted between
	// RunEmbeddingRecomputeJob listing its ID and reaching this call is a
	// harmless no-op update, not an error worth surfacing -- unlike
	// RunScheduledCrawlNow/DeleteScheduledCrawl's use of
	// requireRowsAffected, there's no caller here for whom "the target no
	// longer exists" is meaningfully different from "nothing to do".
	if _, err := r.db.ExecContext(ctx, updateSQL, embBlob, normEmbedding, id); err != nil {
		return fmt.Errorf("updating embedding (%s): %w", id, err)
	}
	if r.ann.isAvailable() {
		vecSQL := r.ph(`UPDATE documents SET embedding_vector = %s::vector WHERE id = %s`, 1, 2)
		if _, err := r.db.ExecContext(ctx, vecSQL, formatPgVectorLiteral(embedding), id); err != nil {
			return fmt.Errorf("updating embedding vector (%s): %w", id, err)
		}
	}
	return nil
}

// DeleteDocument removes a document and every row that references it
// (postings, archived versions, outbound links). The schema also declares
// ON DELETE CASCADE for all three, but that's only a backstop here, not
// relied on: SQLite's foreign-key enforcement is off by default and is a
// per-connection PRAGMA, so a pooled connection that never ran it would
// silently leave orphaned rows behind -- deleting them explicitly is
// correct regardless of whether cascade enforcement happens to be active.
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

// ListDocuments lists indexed pages, most recent ID first, optionally
// narrowed to a single host (used by the per-domain admin subpage so it
// doesn't need to fetch and filter the whole corpus client-side).
// maxHostsIndexedBatch bounds how many hosts a single FollowIndexedDomains
// lookup batches per query -- a safety valve consistent with
// maxDocumentIDsByHost, since a crawl could otherwise discover an
// unbounded number of distinct off-scope hosts across its own pages' links.
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

// LinkGraph loads the entire crawled link graph as an adjacency map: each
// document's ID to the IDs of every other indexed document it links to.
// links only stores each outbound link's raw target URL (to_url), not a
// document ID -- a link whose target was never crawled/indexed has no
// document ID to report, so this join against documents.url on to_url
// naturally omits it. Self-links are already excluded at SaveDocument time
// (a link back to doc.URL itself is skipped there), but a document ID
// pair could still coincide here if two distinct source URLs happened to
// resolve to the same document ID; guarded against defensively anyway,
// since a self-loop is meaningless for PageRank. Loaded in one query
// rather than one row at a time.
func (r *Repository) LinkGraph(ctx context.Context) (map[string][]string, error) {
	query := `SELECT l.from_id, d.id FROM links l JOIN documents d ON d.url = l.to_url`
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

// UpdatePageRanks batch-writes every given document ID's freshly computed
// PageRank score to documents.pagerank, one UPDATE per ID within a single
// transaction. A document ID not present in scores is left untouched --
// see application.RunPageRankJob and ports.PageRankRepository.
// pageRankUpdateBatchSize bounds how many documents one UPDATE statement in
// UpdatePageRanks covers -- each document contributes 3 placeholders (a
// CASE WHEN id/THEN score pair, plus the id again in the WHERE IN list), so
// 200 keeps every batch's placeholder count comfortably under any SQL
// driver's per-statement parameter limit regardless of dialect.
const pageRankUpdateBatchSize = 200

// UpdatePageRanks writes every document's freshly computed PageRank score
// in batches of one CASE-WHEN UPDATE per pageRankUpdateBatchSize documents
// (standard SQL, portable across sqlite/postgres/mysql) rather than one
// UPDATE per document -- the same batching principle DocumentsByIDs/
// PostingsForTerms already apply to reads, applied here to a write that
// can touch the entire corpus every time application.RunPageRankJob runs.
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
// most-documents-first, capped at limit. An empty q matches every domain
// (still capped at limit, most-documents-first) rather than nothing -- the
// admin Documents page's domain search fetches this bounded-but-broad
// batch once an admin starts searching, then matches q as a regular
// expression against it client-side, since a substring LIKE can't express
// what a regex can.
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
// [since, until) -- an empty bound on either side means unbounded on that
// side. crawled_at is stored as RFC3339Nano UTC text, which sorts
// lexicographically the same as chronologically, so plain string
// comparison works across all three dialects without a native TIMESTAMP
// column or driver-specific time scanning.
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
	interval_minutes, max_runs, run_count, renderer, enabled, in_progress, last_run_at, next_run_at, created_at`

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
	                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28)
	_, err = r.db.ExecContext(ctx, insertSQL,
		s.ID, string(seedJSON), s.MaxPages, s.RespectRobots, s.UserAgent,
		s.Cookie, s.BasicAuthUser, s.BasicAuthPass,
		s.LinkScope, string(allowedJSON), string(blockedJSON), s.FollowIndexedDomains,
		s.UseSitemap, s.FetchTimeoutSeconds, s.MinTextLength,
		s.CrawlDelayMs, s.MaxResponseKB, s.PrioritizeUnindexed, s.Recurring,
		s.IntervalMinutes, s.MaxRuns, s.RunCount, s.Renderer, s.Enabled, s.InProgress,
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
// whose next_run_at is at or before now, soonest-due first. in_progress =
// false excludes an entry whose previously-triggered run hasn't finished
// yet -- see application.TriggerDueCrawls, which is the only thing that
// ever sets in_progress true, and MarkScheduledCrawlRun's onDone call,
// which is the only thing that ever clears it back to false.
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

// MarkScheduledCrawlRun records that a schedule was just triggered (or just
// finished -- see application.TriggerDueCrawls, which calls this twice per
// run: once immediately with a provisional nextRunAt and inProgress=true,
// once more when the crawl actually completes, correcting nextRunAt to
// reflect the real finish time and clearing inProgress back to false).
// enabled and inProgress are deliberately separate columns: enabled is
// purely the admin's own on/off toggle (this call only ever changes it to
// reflect a genuine end state -- a one-off entry that just ran, or a
// recurring one that just reached its MaxRuns cap -- never merely "a run
// is currently in flight"), while inProgress is what actually keeps
// DueScheduledCrawls from double-triggering an entry that's still running
// past its own interval. Conflating the two used to mean every trigger
// (including a manual "Run now") visibly, if temporarily, unchecked the
// admin's own enabled toggle in the UI -- confusing and wrong, since the
// admin never touched it.
func (r *Repository) MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time, enabled, inProgress bool, runCount int) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET last_run_at = %s, next_run_at = %s, enabled = %s, in_progress = %s, run_count = %s WHERE id = %s`, 1, 2, 3, 4, 5, 6)
	res, err := r.db.ExecContext(ctx, updateSQL,
		lastRunAt.UTC().Format(crawledAtLayout), nextRunAt.UTC().Format(crawledAtLayout), enabled, inProgress, runCount, id)
	if err != nil {
		return fmt.Errorf("marking scheduled crawl run (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
}

// RunScheduledCrawlNow marks a schedule due immediately -- next_run_at =
// now, enabled = true -- without touching last_run_at, run_count, or any
// crawl option. crawl-server's own scheduler ticker (application.
// TriggerDueCrawls) discovers it on its next tick, the same as a freshly
// created one-off crawl already does; that tick is what actually calls
// MarkScheduledCrawlRun once the triggered job finishes.
//
// Also force-clears in_progress: DueScheduledCrawls only ever selects an
// entry with in_progress = false (see its own doc comment), and nothing
// besides a triggered run's own completion callback ever clears that flag
// -- a callback that's only ever registered in-memory for the lifetime of
// the goroutine that triggered it (see crawl_internal.go's
// TriggerScheduledCrawl). A crawl-server restart while that run was still
// in flight loses that callback entirely, leaving in_progress stuck true
// forever with nothing to reset it (ResumeCrawlJob, the restart-recovery
// path, only knows about the crawl_jobs row, not the ScheduledCrawl that
// triggered it) -- silently breaking both this entry's normal recurring
// schedule and every future "Run now" against it, since neither could
// ever satisfy DueScheduledCrawls' in_progress = false condition again. An
// explicit "run now" click is exactly the moment to self-heal that: the
// admin is unambiguously asking for this to run immediately, so any stale
// "already running" state is cleared rather than left to silently block
// it forever. (cmd/crawl/main.go's own startup also resets every stale
// in_progress flag, which is the systemic fix for schedules nobody
// happens to click "Run now" on again.)
func (r *Repository) RunScheduledCrawlNow(ctx context.Context, id string, now time.Time) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET next_run_at = %s, enabled = %s, in_progress = %s WHERE id = %s`, 1, 2, 3, 4)
	res, err := r.db.ExecContext(ctx, updateSQL, now.UTC().Format(crawledAtLayout), true, false, id)
	if err != nil {
		return fmt.Errorf("running scheduled crawl now (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
}

// ResetStaleInProgress clears in_progress back to false for every schedule
// that has it stuck true -- meant to run once at crawl-server startup (see
// cmd/crawl/main.go), before the scheduler ticker's first tick. Nothing
// can genuinely still be "in progress" the instant this process starts:
// any goroutine that would eventually have cleared the flag via its
// triggered run's completion callback died with whatever process set it,
// whether that was this same restart or an earlier one this schedule
// never got "Run now" clicked on since. Returns how many rows were reset,
// for a one-line startup log -- 0 is the expected, healthy case.
func (r *Repository) ResetStaleInProgress(ctx context.Context) (int, error) {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET in_progress = %s WHERE in_progress = %s`, 1, 2)
	res, err := r.db.ExecContext(ctx, updateSQL, false, true)
	if err != nil {
		return 0, fmt.Errorf("resetting stale in_progress flags: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting reset in_progress flags: %w", err)
	}
	return int(n), nil
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
		&s.IntervalMinutes, &s.MaxRuns, &s.RunCount, &s.Renderer, &s.Enabled, &s.InProgress,
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
