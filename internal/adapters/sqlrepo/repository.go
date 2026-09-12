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
}

func New(ctx context.Context, driverName, dsn string) (*Repository, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening DB (%s): %w", driverName, err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("DB ping (%s): %w", driverName, err)
	}

	repo := &Repository{db: db, dialect: NewDialect(driverName)}
	repo.applyDefaultPoolSettings()
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
	if err := r.ensureHostIndex(ctx); err != nil {
		return err
	}
	return r.ensureCrawledAtIndex(ctx)
}

// ensureHostIndex runs after migrateDocumentColumns, since on a database
// that predates the host column, an index on it can't be created any
// earlier -- CreateSchemaSQL's CREATE TABLE IF NOT EXISTS is a no-op
// against an existing table, so the column wouldn't exist yet if this
// were part of that same statement list. MySQL has no IF NOT EXISTS for
// CREATE INDEX, so there it's a best-effort statement whose "already
// exists" error is expected (and ignored) on every startup after the
// first.
func (r *Repository) ensureHostIndex(ctx context.Context) error {
	if r.dialect.Name() == "mysql" {
		_, _ = r.db.ExecContext(ctx, "CREATE INDEX idx_documents_host ON documents(host)")
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_documents_host ON documents(host)"); err != nil && !isIndexAlreadyExistsError(err) {
		return fmt.Errorf("creating host index: %w", err)
	}
	return nil
}

// ensureCrawledAtIndex mirrors ensureHostIndex exactly, for the same
// reason: crawled_at-based range queries (countDocumentsCrawled, backing
// DocumentsOverview's admin age-bucket charts) and the recency-sort
// ORDER BY crawled_at DESC query below were both running as full table
// scans with no supporting index. Runs after migrateDocumentColumns for
// the same reason ensureHostIndex does (though crawled_at itself has been
// part of the base schema since before this index existed, so this is
// purely about the missing index, not a missing column). MySQL has no IF
// NOT EXISTS for CREATE INDEX, so there it's a best-effort statement
// whose "already exists" error is expected (and ignored) on every startup
// after the first.
func (r *Repository) ensureCrawledAtIndex(ctx context.Context) error {
	if r.dialect.Name() == "mysql" {
		_, _ = r.db.ExecContext(ctx, "CREATE INDEX idx_documents_crawled_at ON documents(crawled_at)")
		return nil
	}
	if _, err := r.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_documents_crawled_at ON documents(crawled_at)"); err != nil && !isIndexAlreadyExistsError(err) {
		return fmt.Errorf("creating crawled_at index: %w", err)
	}
	return nil
}

// isIndexAlreadyExistsError reports whether err is Postgres's benign race
// where CREATE INDEX IF NOT EXISTS is run concurrently by more than one
// process (as happens when search/admin/crawl all migrate on startup at
// once): the existence check and the catalog insert aren't atomic across
// sessions, so the loser gets a unique-violation on the system catalog
// instead of a clean no-op, even though the index ends up created either
// way. Without this, that race crashes the losing process outright.
func isIndexAlreadyExistsError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "duplicate key value violates unique constraint")
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
		if _, err := r.db.ExecContext(ctx, "ALTER TABLE documents ADD COLUMN "+ddl); err != nil {
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
	if err := r.backfillHost(ctx); err != nil {
		return err
	}
	return r.backfillNormEmbedding(ctx)
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
		embJSON string
	}
	var pending []idEmbedding
	for rows.Next() {
		var ie idEmbedding
		if err := rows.Scan(&ie.id, &ie.embJSON); err != nil {
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
		var vec []float32
		if err := json.Unmarshal([]byte(ie.embJSON), &vec); err != nil {
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
// archived to document_versions and the version counter advances;
// re-confirming unchanged content just refreshes crawled_at.
func (r *Repository) SaveDocument(ctx context.Context, doc domain.Document, embedding []float32) error {
	tokens := domain.Tokenize(doc.Title + " " + doc.Text)
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("serializing embedding: %w", err)
	}
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
	var existingVersion int
	var existingText string
	selectSQL := r.ph(`SELECT version, text FROM documents WHERE id = %s`, 1)
	switch selectErr := tx.QueryRowContext(ctx, selectSQL, doc.ID).Scan(&existingVersion, &existingText); {
	case selectErr == sql.ErrNoRows:
		// new document: version stays 1, nothing to archive
	case selectErr != nil:
		return fmt.Errorf("checking existing document: %w", selectErr)
	case existingText == doc.Text:
		version = existingVersion // unchanged content: not a new version
	default:
		archiveSQL := r.ph(`INSERT INTO document_versions (doc_id, version, title, text, doc_length, crawled_at)
		                     SELECT id, version, title, text, doc_length, crawled_at FROM documents WHERE id = %s`, 1)
		if _, err := tx.ExecContext(ctx, archiveSQL, doc.ID); err != nil {
			return fmt.Errorf("archiving previous version: %w", err)
		}
		version = existingVersion + 1
	}

	if _, err := tx.ExecContext(ctx, r.dialect.UpsertDocumentSQL(),
		doc.ID, doc.URL, doc.Title, doc.Text, len(tokens), string(embJSON), normEmbedding, host, version, now,
	); err != nil {
		return fmt.Errorf("saving document: %w", err)
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM postings WHERE doc_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("deleting old postings: %w", err)
	}

	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	insertSQL := r.ph(`INSERT INTO postings (term, doc_id, term_freq) VALUES (%s, %s, %s)`, 1, 2, 3)
	for term, freq := range counts {
		if _, err := tx.ExecContext(ctx, insertSQL, term, doc.ID, freq); err != nil {
			return fmt.Errorf("saving posting: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, r.ph(`DELETE FROM links WHERE from_id = %s`, 1), doc.ID); err != nil {
		return fmt.Errorf("deleting old links: %w", err)
	}
	insertLinkSQL := r.ph(`INSERT INTO links (from_id, to_url, to_host) VALUES (%s, %s, %s)`, 1, 2, 3)
	seenLinks := make(map[string]bool)
	for _, link := range doc.Links {
		// A link back to the page itself (e.g. a logo/home link) isn't a
		// meaningful internal link or backlink -- skip it so it can't
		// inflate either count.
		if link == doc.URL || seenLinks[link] {
			continue
		}
		seenLinks[link] = true
		if _, err := tx.ExecContext(ctx, insertLinkSQL, doc.ID, link, hostOf(link)); err != nil {
			return fmt.Errorf("saving link: %w", err)
		}
	}

	return tx.Commit()
}

func (r *Repository) PostingsForTerm(ctx context.Context, term string) ([]domain.PostingStats, error) {
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
	               WHERE p.term = %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, term)
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

// VocabularyStats reports the total number of distinct indexed terms plus
// the topN terms by document frequency (ties broken by total frequency).
func (r *Repository) VocabularyStats(ctx context.Context, topN int) (int, []domain.TermStat, error) {
	var vocabSize int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT term) FROM postings`).Scan(&vocabSize); err != nil {
		return 0, nil, fmt.Errorf("querying vocabulary size: %w", err)
	}

	query := r.ph(`SELECT term, COUNT(*) AS doc_freq, SUM(term_freq) AS total_freq
	               FROM postings GROUP BY term ORDER BY doc_freq DESC, total_freq DESC LIMIT %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, topN)
	if err != nil {
		return 0, nil, fmt.Errorf("querying top terms: %w", err)
	}
	defer rows.Close()

	var out []domain.TermStat
	for rows.Next() {
		var s domain.TermStat
		if err := rows.Scan(&s.Term, &s.DocFreq, &s.TotalFreq); err != nil {
			return 0, nil, fmt.Errorf("scanning row: %w", err)
		}
		out = append(out, s)
	}
	return vocabSize, out, rows.Err()
}

// scanEmbeddingRows reads (id, embedding-JSON, norm_embedding) rows into a
// map, shared by EmbeddingsForDocs and SampleEmbeddings so both stay
// consistent about how the embedding column is deserialized. The norm is
// read straight off its own column -- computed once at SaveDocument time
// (or by the norm_embedding backfill) -- rather than recomputed here from
// the deserialized vector.
func scanEmbeddingRows(rows *sql.Rows) (map[string]domain.EmbeddedVector, error) {
	out := make(map[string]domain.EmbeddedVector)
	for rows.Next() {
		var id, embJSON string
		var norm float64
		if err := rows.Scan(&id, &embJSON, &norm); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		var vec []float32
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			return nil, fmt.Errorf("deserializing embedding (%s): %w", id, err)
		}
		out[id] = domain.EmbeddedVector{Vector: vec, Norm: norm}
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
	query := `SELECT id, embedding, norm_embedding FROM documents WHERE id IN (` + r.placeholderList(len(ids), 1) + `)`
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
	query := r.ph(`SELECT id, embedding, norm_embedding FROM documents ORDER BY id LIMIT %s`, 1)
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("sampling embeddings: %w", err)
	}
	defer rows.Close()
	return scanEmbeddingRows(rows)
}

func (r *Repository) DocumentByID(ctx context.Context, docID string) (domain.Document, error) {
	query := r.ph(`SELECT id, url, title, text, crawled_at FROM documents WHERE id = %s`, 1)
	var doc domain.Document
	var crawledAt string
	err := r.db.QueryRowContext(ctx, query, docID).Scan(&doc.ID, &doc.URL, &doc.Title, &doc.Text, &crawledAt)
	if err != nil {
		return domain.Document{}, fmt.Errorf("loading document (%s): %w", docID, err)
	}
	if t, err := time.Parse(crawledAtLayout, crawledAt); err == nil {
		doc.CrawledAt = t
	}
	return doc, nil
}

// DocumentsByIDs batch-fetches documents for the given IDs in a single
// "WHERE id IN (...)" query, for callers (e.g. hybrid search's
// constraint/boost/recency filtering) that would otherwise call
// DocumentByID once per candidate -- an ID with no matching row is simply
// absent from the result rather than an error, so a candidate whose
// document was deleted concurrently is silently dropped by the caller
// rather than failing the whole search.
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
		if t, err := time.Parse(crawledAtLayout, crawledAt); err == nil {
			doc.CrawledAt = t
		}
		out[doc.ID] = doc
	}
	return out, rows.Err()
}

// DocumentsByIDsSortedByCrawledAt batch-fetches documents for the given IDs
// exactly like DocumentsByIDs (an ID with no matching row is simply absent,
// not an error), but returns them as a slice in descending crawled_at order
// (ties broken by id ascending, for a deterministic order matching the
// tie-break domain.SortByCrawledAt used to apply in Go) -- pushed down into
// SQL and served by idx_documents_crawled_at, so the hybrid search
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
		if t, err := time.Parse(crawledAtLayout, crawledAt); err == nil {
			doc.CrawledAt = t
		}
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
func (r *Repository) ListDocuments(ctx context.Context, limit int, host string) ([]domain.IndexedDocument, error) {
	var query string
	var args []interface{}
	if host != "" {
		query = r.ph(`SELECT id, url, host, title, doc_length, version, crawled_at
		               FROM documents WHERE host = %s ORDER BY id LIMIT %s`, 1, 2)
		args = []interface{}{host, limit}
	} else {
		query = r.ph(`SELECT id, url, host, title, doc_length, version, crawled_at
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
		if err := rows.Scan(&d.ID, &d.URL, &d.Host, &d.Title, &d.DocLength, &d.Version, &crawledAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		if t, err := time.Parse(crawledAtLayout, crawledAt); err == nil {
			d.CrawledAt = t
		}
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
// most-documents-first. An empty q intentionally matches nothing -- the
// admin Documents page only shows domains once an admin searches for one,
// rather than listing every domain by default.
func (r *Repository) SearchDomains(ctx context.Context, q string, limit int) ([]domain.DomainSummary, error) {
	if q == "" {
		return nil, nil
	}
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
// recent first (the current content lives in ListDocuments/DocumentByID,
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
		if t, err := time.Parse(crawledAtLayout, crawledAt); err == nil {
			v.CrawledAt = t
		}
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
	allow_off_domain_links, use_sitemap, interval_minutes, enabled, last_run_at, next_run_at, created_at`

// CreateScheduledCrawl inserts a new recurring crawl schedule. Deliberately
// carries no credentials to persist -- ScheduledCrawl has none.
func (r *Repository) CreateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error {
	seedJSON, err := json.Marshal(s.SeedURLs)
	if err != nil {
		return fmt.Errorf("encoding seed urls: %w", err)
	}
	insertSQL := r.ph(`INSERT INTO scheduled_crawls (`+scheduledCrawlColumns+`)
	                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)`,
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)
	_, err = r.db.ExecContext(ctx, insertSQL,
		s.ID, string(seedJSON), s.MaxPages, s.RespectRobots, s.UserAgent,
		s.AllowOffDomainLinks, s.UseSitemap, s.IntervalMinutes, s.Enabled,
		nullableTimeString(s.LastRunAt), s.NextRunAt.UTC().Format(crawledAtLayout), s.CreatedAt.UTC().Format(crawledAtLayout),
	)
	if err != nil {
		return fmt.Errorf("creating scheduled crawl: %w", err)
	}
	return nil
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
// CreatedAt and LastRunAt, which only CreateScheduledCrawl and
// MarkScheduledCrawlRun touch).
func (r *Repository) UpdateScheduledCrawl(ctx context.Context, s domain.ScheduledCrawl) error {
	seedJSON, err := json.Marshal(s.SeedURLs)
	if err != nil {
		return fmt.Errorf("encoding seed urls: %w", err)
	}
	updateSQL := r.ph(`UPDATE scheduled_crawls SET
	                      seed_urls = %s, max_pages = %s, respect_robots = %s, user_agent = %s,
	                      allow_off_domain_links = %s, use_sitemap = %s, interval_minutes = %s,
	                      enabled = %s, next_run_at = %s
	                    WHERE id = %s`, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	res, err := r.db.ExecContext(ctx, updateSQL,
		string(seedJSON), s.MaxPages, s.RespectRobots, s.UserAgent,
		s.AllowOffDomainLinks, s.UseSitemap, s.IntervalMinutes, s.Enabled,
		s.NextRunAt.UTC().Format(crawledAtLayout), s.ID,
	)
	if err != nil {
		return fmt.Errorf("updating scheduled crawl (%s): %w", s.ID, err)
	}
	return requireRowsAffected(res, s.ID)
}

// SetScheduledCrawlEnabled flips a schedule's enabled flag without
// touching any of its other fields.
func (r *Repository) SetScheduledCrawlEnabled(ctx context.Context, id string, enabled bool) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET enabled = %s WHERE id = %s`, 1, 2)
	res, err := r.db.ExecContext(ctx, updateSQL, enabled, id)
	if err != nil {
		return fmt.Errorf("updating scheduled crawl enabled flag (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
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

// DueScheduledCrawls lists every enabled schedule whose next_run_at is at
// or before now, soonest-due first.
func (r *Repository) DueScheduledCrawls(ctx context.Context, now time.Time) ([]domain.ScheduledCrawl, error) {
	query := r.ph(`SELECT `+scheduledCrawlColumns+` FROM scheduled_crawls
	               WHERE enabled = %s AND next_run_at <= %s ORDER BY next_run_at ASC`, 1, 2)
	rows, err := r.db.QueryContext(ctx, query, true, now.UTC().Format(crawledAtLayout))
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

// MarkScheduledCrawlRun records that a schedule was just triggered, so the
// next tick's DueScheduledCrawls call doesn't pick it up again until its
// interval has actually elapsed.
func (r *Repository) MarkScheduledCrawlRun(ctx context.Context, id string, lastRunAt, nextRunAt time.Time) error {
	updateSQL := r.ph(`UPDATE scheduled_crawls SET last_run_at = %s, next_run_at = %s WHERE id = %s`, 1, 2, 3)
	res, err := r.db.ExecContext(ctx, updateSQL,
		lastRunAt.UTC().Format(crawledAtLayout), nextRunAt.UTC().Format(crawledAtLayout), id)
	if err != nil {
		return fmt.Errorf("marking scheduled crawl run (%s): %w", id, err)
	}
	return requireRowsAffected(res, id)
}

func nullableTimeString(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(crawledAtLayout), Valid: true}
}

// scanner is satisfied by both *sql.Row and *sql.Rows, so
// scanScheduledCrawl works for either a single-row Get or a multi-row List.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanScheduledCrawl(row scanner) (domain.ScheduledCrawl, error) {
	var s domain.ScheduledCrawl
	var seedJSON string
	var lastRunAt sql.NullString
	var nextRunAt, createdAt string
	if err := row.Scan(&s.ID, &seedJSON, &s.MaxPages, &s.RespectRobots, &s.UserAgent,
		&s.AllowOffDomainLinks, &s.UseSitemap, &s.IntervalMinutes, &s.Enabled,
		&lastRunAt, &nextRunAt, &createdAt); err != nil {
		return domain.ScheduledCrawl{}, err
	}
	if err := json.Unmarshal([]byte(seedJSON), &s.SeedURLs); err != nil {
		return domain.ScheduledCrawl{}, fmt.Errorf("decoding seed urls: %w", err)
	}
	if lastRunAt.Valid {
		if t, err := time.Parse(crawledAtLayout, lastRunAt.String); err == nil {
			s.LastRunAt = &t
		}
	}
	if t, err := time.Parse(crawledAtLayout, nextRunAt); err == nil {
		s.NextRunAt = t
	}
	if t, err := time.Parse(crawledAtLayout, createdAt); err == nil {
		s.CreatedAt = t
	}
	return s, nil
}

func (r *Repository) ph(template string, positions ...int) string {
	args := make([]interface{}, len(positions))
	for i, pos := range positions {
		args[i] = r.dialect.Placeholder(pos)
	}
	return fmt.Sprintf(template, args...)
}
