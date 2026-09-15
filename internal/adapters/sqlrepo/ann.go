package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"searchengine/internal/domain"
)

// annState tracks whether Postgres pgvector-backed approximate
// nearest-neighbor semantic search is available for this process's
// lifetime. It's discovered exactly once, at startup, by EnableANN --
// never re-attempted per request -- so a missing extension (or any other
// migration failure) permanently and safely degrades this process to the
// existing bounded-sample brute-force path (SampleEmbeddings) rather than
// retrying (and potentially failing) on every subsequent search or crawl.
type annState struct {
	mu        sync.RWMutex
	available bool
	dims      int
}

func (a *annState) isAvailable() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.available
}

func (a *annState) markAvailable(dims int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.available = true
	a.dims = dims
}

// vectorIndexName and vectorColumnName name the pgvector column and its
// HNSW index, added to documents only on Postgres once EnableANN succeeds.
const (
	vectorColumnName = "embedding_vector"
	vectorIndexName  = "idx_documents_embedding_vector_hnsw"
)

// maxHNSWEfSearch is pgvector's own hard ceiling on the hnsw.ef_search GUC
// (see pgvector's hnsw.c: DefineCustomIntVariable clamps it to [1, 1000]) --
// setting anything higher fails with "invalid value for parameter
// \"hnsw.ef_search\"". SemanticCandidatePoolSize (the value TopSemanticMatches
// is called with as limit) is an admin-configurable knob with no upper bound
// of its own (see domain.OperationalSettingsValues), so this clamp is what
// keeps an unusually large configured pool size from turning every ANN query
// into a hard Postgres error instead of merely capping recall quality at
// pgvector's own ceiling.
const maxHNSWEfSearch = 1000

// EnableANN attempts to turn on Postgres pgvector-backed ANN semantic
// search for this process's lifetime: enabling the pgvector extension,
// adding a vector(dims) column sized to the embedder's Dimensions()
// (dims), and building a cosine-distance HNSW index over it. Every
// process that searches or writes documents (cmd/search, cmd/admin,
// cmd/crawl) calls this once at startup, right after constructing its own
// embedder -- each opens its own *sql.DB/Repository, so each independently
// discovers (and logs) ANN availability for itself.
//
// This never returns an error and never crashes the process. For any
// dialect other than Postgres (SQLite/MySQL) it's a silent no-op, since
// ANN is Postgres-only. On Postgres, any failure along the way --
// pgvector's extension not being installed at the OS/server level (a
// distinct problem from merely lacking permission to install it -- this
// surfaces as something like "could not open extension control file"), a
// permission error, or any other unexpected fault -- is logged once as a
// clear warning, after which ANNAvailable() stays false and every caller
// (TopSemanticMatches, SaveDocument) permanently uses the brute-force
// fallback for this process's lifetime instead. It's never retried
// per-request. A concurrent-migration race between the three processes
// (all calling EnableANN against the same database at startup) is treated
// as success, not failure, exactly like ensureHostIndex/ensureCrawledAtIndex
// already do for their own indexes.
func (r *Repository) EnableANN(ctx context.Context, dims int) {
	if r.dialect.Name() != "postgres" || dims <= 0 {
		return
	}
	if err := r.enablePgVectorExtension(ctx); err != nil {
		if isMissingExtensionError(err) {
			log.Printf("sqlrepo: pgvector extension is not installed on this Postgres server (%v) -- falling back to brute-force semantic search for this process's lifetime", err)
		} else {
			log.Printf("sqlrepo: could not enable pgvector extension (%v) -- falling back to brute-force semantic search for this process's lifetime", err)
		}
		return
	}
	if err := r.ensureVectorColumn(ctx, dims); err != nil {
		log.Printf("sqlrepo: could not add pgvector column (%v) -- falling back to brute-force semantic search for this process's lifetime", err)
		return
	}
	if err := r.ensureVectorIndex(ctx); err != nil {
		log.Printf("sqlrepo: could not build pgvector HNSW index (%v) -- falling back to brute-force semantic search for this process's lifetime", err)
		return
	}
	if err := r.backfillVectorColumn(ctx); err != nil {
		log.Printf("sqlrepo: could not backfill pgvector column for pre-existing documents (%v) -- falling back to brute-force semantic search for this process's lifetime", err)
		return
	}
	r.ann.markAvailable(dims)
}

// backfillVectorColumn populates embedding_vector for any row saved before
// ANN was ever enabled -- without this, TopSemanticMatches' "WHERE
// embedding_vector IS NOT NULL" filter would exclude every pre-existing
// document, so ANN would report itself available yet return zero
// candidates for the entire corpus until each document happened to be
// re-crawled. Only ANN's own caller (EnableANN) runs this, and only once
// the column exists -- mirrors backfillNormEmbedding's shape exactly
// (parse the existing packed-binary embedding column, write the derived
// value),
// just writing to embedding_vector instead of norm_embedding. Run before
// r.ann.markAvailable, so TopSemanticMatches is never used against a
// corpus that hasn't actually been backfilled yet.
func (r *Repository) backfillVectorColumn(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT id, embedding FROM documents WHERE `+vectorColumnName+` IS NULL`)
	if err != nil {
		return fmt.Errorf("finding rows needing an embedding_vector backfill: %w", err)
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

	updateSQL := r.ph(`UPDATE documents SET `+vectorColumnName+` = %s::vector WHERE id = %s`, 1, 2)
	for _, ie := range pending {
		vec, err := DecodeEmbedding(ie.embBlob)
		if err != nil {
			return fmt.Errorf("deserializing embedding for vector backfill (%s): %w", ie.id, err)
		}
		if len(vec) == 0 {
			continue // nothing meaningful to backfill for an empty embedding
		}
		if _, err := r.db.ExecContext(ctx, updateSQL, formatPgVectorLiteral(vec), ie.id); err != nil {
			return fmt.Errorf("backfilling embedding_vector for %s: %w", ie.id, err)
		}
	}
	return nil
}

// ANNAvailable reports whether this process's pgvector-backed ANN semantic
// search is currently available -- true only after EnableANN has
// succeeded. Search callers don't need this directly (TopSemanticMatches
// already reports availability via its own ok return); it's exposed for
// diagnostics and tests.
func (r *Repository) ANNAvailable() bool {
	return r.ann.isAvailable()
}

// enablePgVectorExtension runs CREATE EXTENSION IF NOT EXISTS vector,
// tolerating the same concurrent-migration race ensureHostIndex/
// ensureCrawledAtIndex already tolerate for their own indexes (three
// processes racing to enable the extension at startup can hit a
// catalog unique-violation on the loser even though the extension ends up
// enabled either way).
func (r *Repository) enablePgVectorExtension(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// ensureVectorColumn adds documents.embedding_vector sized to dims, the
// same way migrateDocumentColumns adds host/version/crawled_at/etc to a
// documents table that predates them -- except this one is Postgres-only
// (pgvector's "vector" type doesn't exist on SQLite/MySQL) and needs the
// embedder's Dimensions() at call time, so it can't be part of the static,
// dialect-branched CreateSchemaSQL list every dialect already returns.
// "ADD COLUMN IF NOT EXISTS" plus this function's own tolerance for an
// "already exists" race covers the same concurrent-startup scenario
// ensureHostIndex/ensureCrawledAtIndex guard against.
func (r *Repository) ensureVectorColumn(ctx context.Context, dims int) error {
	existingDims, found, err := r.vectorColumnDimensions(ctx)
	if err != nil {
		return err
	}
	if found && existingDims != dims {
		// The embedding provider/model changed since this column was
		// first created (see application.RunEmbeddingRecomputeJob, the
		// whole point of which is letting that happen without a
		// re-crawl) -- pgvector's vector(N) type is fixed per column,
		// so the old column can't just be widened/narrowed in place.
		// Drop it (and its now-mismatched index) and let the ADD COLUMN
		// below recreate it fresh at the new size; backfillVectorColumn
		// repopulates every row from the blob `embedding` column, which
		// SaveDocument/UpdateEmbedding always keep current regardless
		// of this column's own state -- so nothing is actually lost.
		if _, err := r.db.ExecContext(ctx, `DROP INDEX IF EXISTS `+vectorIndexName); err != nil {
			return fmt.Errorf("dropping stale pgvector index: %w", err)
		}
		if _, err := r.db.ExecContext(ctx, `ALTER TABLE documents DROP COLUMN IF EXISTS `+vectorColumnName); err != nil {
			return fmt.Errorf("dropping mismatched-dimension pgvector column: %w", err)
		}
	}
	ddl := fmt.Sprintf(`ALTER TABLE documents ADD COLUMN IF NOT EXISTS %s vector(%d)`, vectorColumnName, dims)
	if _, err := r.db.ExecContext(ctx, ddl); err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// vectorColumnDimensions reports the dimension embedding_vector was
// actually created with, by asking Postgres's own catalog rather than
// trusting this process's in-memory dims -- necessary because another
// process (or an earlier run of this one, before a provider/model change)
// may have created the column at a different size. found is false when
// the column doesn't exist yet at all (a fresh database, or ANN never
// having been enabled before), which is not an error.
func (r *Repository) vectorColumnDimensions(ctx context.Context) (dims int, found bool, err error) {
	const q = `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = 'documents'::regclass
		  AND a.attname = $1
		  AND NOT a.attisdropped`
	var formatted string
	if err := r.db.QueryRowContext(ctx, q, vectorColumnName).Scan(&formatted); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("checking existing pgvector column dimensions: %w", err)
	}
	// formatted looks like "vector(128)" -- pull the integer out from
	// between the parens rather than assuming any particular prefix, so
	// this doesn't silently misparse if a future pgvector version changes
	// format_type's exact spelling.
	open, close := strings.IndexByte(formatted, '('), strings.LastIndexByte(formatted, ')')
	if open < 0 || close <= open {
		return 0, false, fmt.Errorf("unexpected pgvector column type format %q", formatted)
	}
	dims, err = strconv.Atoi(formatted[open+1 : close])
	if err != nil {
		return 0, false, fmt.Errorf("parsing pgvector column dimensions from %q: %w", formatted, err)
	}
	return dims, true, nil
}

// ensureVectorIndex builds the HNSW index TopSemanticMatches' "ORDER BY
// embedding_vector <=> $1 LIMIT $2" query needs for cosine-distance ANN
// search, run after ensureVectorColumn the same way ensureHostIndex runs
// after migrateDocumentColumns adds the host column it indexes -- a column
// an index is built over must exist first. "CREATE INDEX IF NOT EXISTS"
// plus isAlreadyExistsError tolerates the identical concurrent-startup
// race ensureHostIndex/ensureCrawledAtIndex already tolerate.
func (r *Repository) ensureVectorIndex(ctx context.Context) error {
	ddl := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON documents USING hnsw (%s vector_cosine_ops)`, vectorIndexName, vectorColumnName)
	if _, err := r.db.ExecContext(ctx, ddl); err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// isMissingExtensionError reports whether err is Postgres's specific
// "the pgvector extension isn't installed at the OS/server level at all"
// failure -- distinct from a permission problem (which would instead be a
// plain "permission denied" error) -- so EnableANN can log a clear,
// specific warning for the case a real production incident traced back to
// exactly this: CREATE EXTENSION failing because vector.control isn't
// present on the server's filesystem, not because the connecting role
// lacked the privilege to run it.
func isMissingExtensionError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "could not open extension control file")
}

// formatPgVectorLiteral renders vec in pgvector's text input format
// ("[v1,v2,v3]"), accepted by Postgres wherever a ::vector cast is applied
// to a query parameter -- used both to write documents.embedding_vector in
// SaveDocument and to pass the query vector to TopSemanticMatches' ORDER
// BY ... <=> $1 clause.
func formatPgVectorLiteral(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// TopSemanticMatches implements ports.SQLRepository's ANN search: when
// available (see ANNAvailable), it finds queryVec's nearest neighbors by
// cosine distance via pgvector's HNSW index instead of SampleEmbeddings'
// bounded "ORDER BY id LIMIT limit" sample -- the same shape of result
// (scanEmbeddingRows, shared with SampleEmbeddings/EmbeddingsForDocs), just
// chosen by actual similarity to the query rather than an arbitrary ID
// order. ok is false (with a nil map and nil error) whenever ANN isn't
// available or limit isn't positive, telling the caller
// (hybridSearchService.Search) to fall back to SampleEmbeddings exactly as
// it did before ANN existed -- a real query failure, by contrast, is
// returned as a non-nil error, the same as any other repository method.
//
// Rows with a NULL embedding_vector (documents saved before ANN was ever
// enabled for this process, or before EmbeddingsForDocs -- SaveDocument
// only writes this column once r.ann.isAvailable(), see SaveDocument) are
// excluded, since ordering by cosine distance against a NULL is
// meaningless.
//
// Before the ORDER BY query, this issues "SET LOCAL hnsw.ef_search = <n>"
// (n = limit, clamped to maxHNSWEfSearch) inside the same transaction. HNSW
// query-time recall is governed entirely by hnsw.ef_search, a GUC that
// defaults to 40 and is completely independent of the query's own LIMIT --
// pgvector's docs are explicit that ef_search should be >= the requested
// LIMIT for good recall, but nothing in EnableANN's migration ever touches
// it. Left at its 40 default while LIMIT asks for (typically) 200 rows, the
// HNSW graph traversal only ever explores a 40-wide dynamic candidate list,
// so the query still returns exactly `limit` rows -- they just are not
// reliably the true top-`limit` nearest neighbors by cosine distance, a
// silent recall degradation with no visible error. SET LOCAL scopes the
// change to this transaction only (never a session-wide SET, which would
// leak the setting to whatever unrelated query the pooled connection serves
// next), so it's safe under the connection pool's concurrent connections
// each potentially wanting a different ef_search for a different limit.
func (r *Repository) TopSemanticMatches(ctx context.Context, queryVec []float32, limit int) (map[string]domain.EmbeddedVector, bool, error) {
	if !r.ann.isAvailable() || limit <= 0 {
		return nil, false, nil
	}

	efSearch := limit
	if efSearch > maxHNSWEfSearch {
		efSearch = maxHNSWEfSearch
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("starting ANN transaction: %w", err)
	}
	defer tx.Rollback()

	// SET's parameter can't be bound as a placeholder (Postgres rejects
	// "$1" there), so efSearch -- an int, clamped above, never raw user
	// input -- is formatted directly; safe from injection the same way any
	// %d-formatted int literal is.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d`, efSearch)); err != nil {
		return nil, false, fmt.Errorf("setting hnsw.ef_search: %w", err)
	}

	query := r.ph(`SELECT id, embedding, norm_embedding, pagerank FROM documents
	               WHERE `+vectorColumnName+` IS NOT NULL
	               ORDER BY `+vectorColumnName+` <=> %s::vector LIMIT %s`, 1, 2)
	rows, err := tx.QueryContext(ctx, query, formatPgVectorLiteral(queryVec), limit)
	if err != nil {
		return nil, false, fmt.Errorf("querying ANN semantic matches: %w", err)
	}
	defer rows.Close()
	out, err := scanEmbeddingRows(rows)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
