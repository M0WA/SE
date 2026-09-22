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

// annState tracks whether Postgres pgvector-backed ANN semantic search is
// available, per embedding provider, discovered once at startup by
// EnableANN and never re-attempted per request -- a migration failure for
// one provider permanently (for this process) falls back to the brute-force
// SampleEmbeddings path for just that provider, others unaffected.
type annState struct {
	mu        sync.RWMutex
	available map[string]bool
}

func (a *annState) isAvailable(provider string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.available[provider]
}

// markAvailable no longer remembers dims -- nothing ever read the map this
// used to build; a size mismatch against a pre-existing column is already
// caught independently by ensureVectorColumn's own vectorColumnType
// comparison against the DB catalog, checked fresh on every EnableANN run.
func (a *annState) markAvailable(provider string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.available == nil {
		a.available = make(map[string]bool)
	}
	a.available[provider] = true
}

// vectorColumnNameFor and vectorIndexNameFor name a provider's pgvector
// column/HNSW index, added on Postgres once EnableANN succeeds for it.
// provider is always domain.EmbeddingProviderHash or an endpoint ID
// matching domain.EmbeddingEndpointIDPattern (^[a-z0-9_]{1,20}$), so
// concatenating it directly into the name is safe.
func vectorColumnNameFor(provider string) string {
	return "embedding_vector_" + provider
}

func vectorIndexNameFor(provider string) string {
	return "idx_documents_embedding_vector_" + provider + "_hnsw"
}

// maxHNSWEfSearch is pgvector's own hard ceiling on hnsw.ef_search (see
// hnsw.c: clamped to [1, 1000], else "invalid value" errors). Clamps the
// admin-configurable, unbounded SemanticCandidatePoolSize so a large
// setting merely caps recall quality instead of erroring every ANN query.
const maxHNSWEfSearch = 1000

// EnableANN turns on Postgres pgvector-backed ANN search, once per
// provider in dimsByProvider: enables the extension, then adds each
// provider's halfvec(dims) column + HNSW index. Never errors or crashes --
// a no-op on non-Postgres dialects; a Postgres failure permanently falls
// that provider back to brute-force search for this process's lifetime.
func (r *Repository) EnableANN(ctx context.Context, dimsByProvider map[string]int) {
	if r.dialect.Name() != "postgres" {
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
	for provider, dims := range dimsByProvider {
		if dims <= 0 {
			continue
		}
		r.enableANNForProvider(ctx, provider, dims)
	}
}

func (r *Repository) enableANNForProvider(ctx context.Context, provider string, dims int) {
	if err := r.ensureVectorColumn(ctx, provider, dims); err != nil {
		log.Printf("sqlrepo: could not add pgvector column for %s (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, err, provider)
		return
	}
	if err := r.ensureVectorIndex(ctx, provider); err != nil {
		log.Printf("sqlrepo: could not build pgvector HNSW index for %s (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, err, provider)
		return
	}
	if err := r.backfillVectorColumn(ctx, provider); err != nil {
		log.Printf("sqlrepo: could not backfill pgvector column for %s (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, err, provider)
		return
	}
	r.ann.markAvailable(provider)
}

// backfillVectorColumn populates provider's pgvector column for any
// document_embeddings row saved before ANN was enabled for it -- else
// TopSemanticMatches' NOT NULL filter would exclude every pre-existing
// document until each got re-saved. Must run before r.ann.markAvailable,
// so TopSemanticMatches is never used before the backfill completes.
func (r *Repository) backfillVectorColumn(ctx context.Context, provider string) error {
	col := vectorColumnNameFor(provider)
	query := r.ph(`SELECT de.doc_id, de.embedding FROM document_embeddings de
	               JOIN documents d ON d.id = de.doc_id
	               WHERE de.provider = %s AND d.`+col+` IS NULL`, 1)
	rows, err := r.db.QueryContext(ctx, query, provider)
	if err != nil {
		return fmt.Errorf("finding rows needing a %s backfill: %w", col, err)
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

	updateSQL := r.ph(`UPDATE documents SET `+col+` = %s::halfvec WHERE id = %s`, 1, 2)
	for _, ie := range pending {
		vec, err := DecodeEmbedding(ie.embBlob)
		if err != nil {
			return fmt.Errorf("deserializing embedding for %s backfill (%s): %w", col, ie.id, err)
		}
		if len(vec) == 0 {
			continue // nothing meaningful to backfill for an empty embedding
		}
		if _, err := r.db.ExecContext(ctx, updateSQL, formatPgVectorLiteral(vec), ie.id); err != nil {
			return fmt.Errorf("backfilling %s for %s: %w", col, ie.id, err)
		}
	}
	return nil
}

// ANNAvailable reports whether this process's pgvector-backed ANN semantic
// search is currently available for provider -- true only after EnableANN
// has succeeded for it. Search callers don't need this directly
// (TopSemanticMatches already reports availability via its own ok
// return); it's exposed for diagnostics and tests.
func (r *Repository) ANNAvailable(provider string) bool {
	return r.ann.isAvailable(provider)
}

// enablePgVectorExtension runs CREATE EXTENSION IF NOT EXISTS vector,
// tolerating the same concurrent-startup race ensureHostIndex/
// ensureCrawledAtIndex tolerate: three processes racing this can hit a
// catalog unique-violation on the loser even though it ends up enabled.
func (r *Repository) enablePgVectorExtension(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// queryRower is satisfied by both *sql.DB and *sql.Tx, so
// vectorColumnType can run either standalone or (as ensureVectorColumn
// does) inside its already-open transaction -- guaranteeing the read and
// the migration decision made from it see the same catalog snapshot.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// ensureVectorColumn adds documents.embedding_vector_<provider> sized to
// dims. Migrating a mismatched pre-existing column isn't idempotent under
// 3 processes calling EnableANN concurrently, so it runs in one
// transaction under pg_advisory_xact_lock keyed on provider -- not the
// session-level variant, which can't guarantee release through a pool.
func (r *Repository) ensureVectorColumn(ctx context.Context, provider string, dims int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting vector column migration transaction: %w", err)
	}
	defer tx.Rollback()

	// hashtext(...) folds the provider string down to the int4/int8 key
	// pg_advisory_xact_lock needs; keying by provider (rather than one
	// global lock for every provider) lets independent providers migrate
	// concurrently without contending on each other's unrelated column.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('sqlrepo:vector_column:'||$1))`, provider); err != nil {
		return fmt.Errorf("acquiring vector column migration lock: %w", err)
	}

	col := vectorColumnNameFor(provider)
	existingDims, existingType, found, err := vectorColumnType(ctx, tx, provider)
	if err != nil {
		return err
	}
	if found && (existingDims != dims || existingType != "halfvec") {
		// The model changed (see RunEmbeddingRecomputeJob) or this column
		// predates the "vector"->"halfvec" switch -- pgvector's type is
		// fixed once created, so drop it (and its stale index) and let ADD
		// COLUMN below recreate it; backfillVectorColumn repopulates every
		// row from document_embeddings, so nothing is lost.
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+vectorIndexNameFor(provider)); err != nil {
			return fmt.Errorf("dropping stale pgvector index: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE documents DROP COLUMN IF EXISTS `+col); err != nil {
			return fmt.Errorf("dropping mismatched pgvector column: %w", err)
		}
	}
	ddl := fmt.Sprintf(`ALTER TABLE documents ADD COLUMN IF NOT EXISTS %s halfvec(%d)`, col, dims)
	if _, err := tx.ExecContext(ctx, ddl); err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return tx.Commit()
}

// vectorColumnType reports provider's column's actual dimension and
// pgvector type, read from Postgres's catalog rather than trusted
// in-memory state (another process may have created it differently).
// found is false if the column doesn't exist yet. Takes a queryRower so
// ensureVectorColumn can call it inside its own locked *sql.Tx.
func vectorColumnType(ctx context.Context, q queryRower, provider string) (dims int, typeName string, found bool, err error) {
	const query = `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = 'documents'::regclass
		  AND a.attname = $1
		  AND NOT a.attisdropped`
	var formatted string
	if err := q.QueryRowContext(ctx, query, vectorColumnNameFor(provider)).Scan(&formatted); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", false, nil
		}
		return 0, "", false, fmt.Errorf("checking existing pgvector column type: %w", err)
	}
	// formatted looks like "halfvec(128)" (or, for a column predating this
	// package's switch to halfvec, "vector(128)") -- pull the type name
	// and the integer out from around the parens rather than assuming any
	// particular prefix, so this doesn't silently misparse if a future
	// pgvector version changes format_type's exact spelling.
	open, closeParen := strings.IndexByte(formatted, '('), strings.LastIndexByte(formatted, ')')
	if open < 0 || closeParen <= open {
		return 0, "", false, fmt.Errorf("unexpected pgvector column type format %q", formatted)
	}
	dims, err = strconv.Atoi(formatted[open+1 : closeParen])
	if err != nil {
		return 0, "", false, fmt.Errorf("parsing pgvector column dimensions from %q: %w", formatted, err)
	}
	return dims, formatted[:open], true, nil
}

// ensureVectorIndex builds the HNSW cosine-distance index TopSemanticMatches
// needs, run after ensureVectorColumn. halfvec, not vector: pgvector's
// per-row index byte budget caps "vector" at 2000 dims but "halfvec"
// (float16) at 4000 -- needed for a 3584-dim model, no meaningful accuracy loss.
func (r *Repository) ensureVectorIndex(ctx context.Context, provider string) error {
	col := vectorColumnNameFor(provider)
	ddl := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON documents USING hnsw (%s halfvec_cosine_ops)`, vectorIndexNameFor(provider), col)
	if _, err := r.db.ExecContext(ctx, ddl); err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// isMissingExtensionError reports whether err is Postgres's "pgvector
// isn't installed at the OS/server level" failure -- distinct from a
// permission error -- so EnableANN can log a clear, specific warning for
// this real production failure mode (vector.control missing on disk).
func isMissingExtensionError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "could not open extension control file")
}

// formatPgVectorLiteral renders vec in pgvector's text input format
// ("[v1,v2,v3]"), used both to write a provider's embedding column and to
// pass the query vector to TopSemanticMatches' ORDER BY ... <=> $1 clause.
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

// TopSemanticMatches implements ports.SQLRepository's ANN search via
// pgvector's HNSW index. ok is false (nil map, nil error) when unavailable
// or limit isn't positive -- caller falls back to SampleEmbeddings. Sets
// "hnsw.ef_search" (SET LOCAL) to limit first: left at its default of 40,
// HNSW silently under-recalls a larger requested limit.
func (r *Repository) TopSemanticMatches(ctx context.Context, queryVec []float32, limit int, provider string) (map[string]domain.EmbeddedVector, bool, error) {
	if !r.ann.isAvailable(provider) || limit <= 0 {
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

	col := vectorColumnNameFor(provider)
	query := r.ph(`SELECT de.doc_id, de.embedding, de.norm_embedding, d.pagerank
	               FROM documents d JOIN document_embeddings de ON de.doc_id = d.id AND de.provider = %s
	               WHERE d.`+col+` IS NOT NULL
	               ORDER BY d.`+col+` <=> %s::halfvec LIMIT %s`, 1, 2, 3)
	rows, err := tx.QueryContext(ctx, query, provider, formatPgVectorLiteral(queryVec), limit)
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
