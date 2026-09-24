package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"searchengine/internal/domain"
)

// annDDLTimeout bounds ensureVectorColumn's own transaction -- ALTER TABLE
// ADD COLUMN is normally near-instant (metadata-only, no default value to
// backfill), so this exists purely against lock contention, not real
// migration work. A real incident: this exact ALTER TABLE competed for an
// ACCESS EXCLUSIVE lock against an unrelated, long-running batch UPDATE (a
// PageRank recompute) elsewhere in the same database and blocked for over
// 15 minutes, taking the ENTIRE server down with it, since EnableANN runs
// synchronously in main() before ListenAndServe. Every caller up the
// chain already treats a failure here as non-fatal -- logged, falls back
// to brute-force semantic search for this provider for the process's
// lifetime (see enableANNForProvider) -- so timing out and moving on is
// strictly better than hanging indefinitely; a later restart gets another
// chance once whatever held the lock has cleared.
//
// Deliberately NOT applied to ensureVectorIndex's CREATE INDEX ... USING
// hnsw below -- unlike this, that one does genuine, corpus-size-proportional
// work (building the actual HNSW graph) on its first real run, so a short
// timeout there would risk spuriously failing a legitimate slow build on
// a large corpus rather than only catching lock contention.
const annDDLTimeout = 15 * time.Second

// annState tracks per-provider pgvector ANN availability and shard count,
// set once by EnableANN and never retried -- a failed provider falls back
// to brute-force SampleEmbeddings for this process's lifetime, others
// unaffected.
type annState struct {
	mu        sync.RWMutex
	available map[string]bool
	shards    map[string]int
}

func (a *annState) isAvailable(provider string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.available[provider]
}

// shardsFor returns provider's shard count (see vectorShardCount) once
// EnableANN has succeeded for it; 1 (the pre-sharding default) if it
// hasn't been recorded, so a caller that forgot to check isAvailable first
// still gets a sane single-column assumption rather than 0.
func (a *annState) shardsFor(provider string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if n, ok := a.shards[provider]; ok {
		return n
	}
	return 1
}

// markAvailable records availability and provider's shard count -- a size
// mismatch on either axis is caught separately, by ensureVectorColumn's
// own catalog check on each EnableANN run.
func (a *annState) markAvailable(provider string, shards int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.available == nil {
		a.available = make(map[string]bool)
		a.shards = make(map[string]int)
	}
	a.available[provider] = true
	a.shards[provider] = shards
}

// maxHalfvecIndexDims is pgvector's hard ceiling on how many dimensions a
// single halfvec column can have and still be indexed (HNSW or IVFFlat --
// both share this limit, it's a per-index-page constraint of the storage
// type itself, not an HNSW-specific one). A provider whose embedding
// exceeds this (e.g. a 4096-dim model) can't be ANN-indexed as one column
// at all -- see vectorShardCount.
const maxHalfvecIndexDims = 4000

// vectorShardCount reports how many indexed halfvec columns a dims-length
// embedding needs: 1 unless dims exceeds pgvector's per-index dimension
// ceiling, in which case it's split evenly across enough shards to fit.
// Each shard is ANN-searched independently and the candidate sets unioned
// (see TopSemanticMatches) -- verified empirically (not just in theory) to
// recover true full-vector top-K with full recall at a modest per-shard
// candidate count, since a real embedding's similarity signal is spread
// across both halves rather than concentrated in one; final scoring is
// always done from the full, unsplit vector (document_embeddings' bytea
// column), so splitting only affects which candidates ANN surfaces, never
// the precision of a score.
func vectorShardCount(dims int) int {
	if dims <= maxHalfvecIndexDims {
		return 1
	}
	return (dims + maxHalfvecIndexDims - 1) / maxHalfvecIndexDims
}

// vectorShardBounds splits a dims-length vector into shards even-as-possible
// pieces: shard i covers vec[bounds[i]:bounds[i+1]]. len(bounds) == shards+1.
func vectorShardBounds(dims, shards int) []int {
	bounds := make([]int, shards+1)
	base, rem := dims/shards, dims%shards
	for i := 0; i < shards; i++ {
		size := base
		if i < rem {
			size++
		}
		bounds[i+1] = bounds[i] + size
	}
	return bounds
}

// vectorColumnNameFor and vectorIndexNameFor name one shard of a
// provider's pgvector column/HNSW index. provider is always
// domain.EmbeddingProviderHash or an endpoint ID matching
// ^[a-z0-9_]{1,20}$, so direct concatenation is safe. shards==1 (the
// overwhelmingly common case, and every column created before sharding
// existed) keeps the original unsuffixed name -- only a provider that
// actually needs multiple shards gets the "_<shard>" suffix, so no
// existing single-shard provider's column/index needs renaming/migrating.
func vectorColumnNameFor(provider string, shard, shards int) string {
	if shards <= 1 {
		return "embedding_vector_" + provider
	}
	return fmt.Sprintf("embedding_vector_%s_%d", provider, shard)
}

func vectorIndexNameFor(provider string, shard, shards int) string {
	if shards <= 1 {
		return "idx_documents_embedding_vector_" + provider + "_hnsw"
	}
	return fmt.Sprintf("idx_documents_embedding_vector_%s_%d_hnsw", provider, shard)
}

// maxHNSWEfSearch is pgvector's hard ceiling on hnsw.ef_search ([1, 1000],
// else it errors). Clamping the admin-configured pool size to it turns an
// over-large setting into a recall cap instead of a query error.
const maxHNSWEfSearch = 1000

// EnableANN turns on Postgres pgvector ANN search per provider in
// dimsByProvider: enables the extension, then adds each provider's
// halfvec(dims) column(s) + HNSW index(es) -- one of each unless dims
// exceeds pgvector's per-index ceiling (see vectorShardCount), in which
// case the embedding is sharded across several. Never errors -- a no-op
// off Postgres; a failure falls that provider back to brute-force search
// for this process.
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
	shards := vectorShardCount(dims)
	bounds := vectorShardBounds(dims, shards)
	for shard := 0; shard < shards; shard++ {
		shardDims := bounds[shard+1] - bounds[shard]
		if err := r.ensureVectorColumn(ctx, provider, shard, shards, shardDims); err != nil {
			log.Printf("sqlrepo: could not add pgvector column for %s shard %d/%d (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, shard, shards, err, provider)
			return
		}
		if err := r.ensureVectorIndex(ctx, provider, shard, shards); err != nil {
			log.Printf("sqlrepo: could not build pgvector HNSW index for %s shard %d/%d (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, shard, shards, err, provider)
			return
		}
	}
	if err := r.backfillVectorColumn(ctx, provider, shards, bounds); err != nil {
		log.Printf("sqlrepo: could not backfill pgvector column for %s (%v) -- falling back to brute-force semantic search for %s this process's lifetime", provider, err, provider)
		return
	}
	r.ann.markAvailable(provider, shards)
}

// backfillVectorColumn fills provider's pgvector column(s) for rows saved
// before ANN was enabled, else TopSemanticMatches' NOT NULL filter would
// exclude them. Must run before r.ann.markAvailable. Checks the first
// shard's column for NULL as the "needs backfill" signal -- ensureVectorColumn
// just (re)created every shard column together, so they're always NULL in
// lockstep for a row that predates this EnableANN run.
func (r *Repository) backfillVectorColumn(ctx context.Context, provider string, shards int, bounds []int) error {
	col0 := vectorColumnNameFor(provider, 0, shards)
	query := r.ph(`SELECT de.doc_id, de.embedding FROM document_embeddings de
	               JOIN documents d ON d.id = de.doc_id
	               WHERE de.provider = %s AND d.`+col0+` IS NULL`, 1)
	rows, err := r.db.QueryContext(ctx, query, provider)
	if err != nil {
		return fmt.Errorf("finding rows needing a %s backfill: %w", col0, err)
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

	setClauses := make([]string, shards)
	for shard := 0; shard < shards; shard++ {
		setClauses[shard] = vectorColumnNameFor(provider, shard, shards) + " = %s::halfvec"
	}
	positions := make([]int, shards+1)
	for i := range positions {
		positions[i] = i + 1
	}
	updateSQL := r.ph(`UPDATE documents SET `+strings.Join(setClauses, ", ")+` WHERE id = %s`, positions...)

	wantDims := bounds[shards]
	for _, ie := range pending {
		vec, err := DecodeEmbedding(ie.embBlob)
		if err != nil {
			return fmt.Errorf("deserializing embedding for %s backfill (%s): %w", col0, ie.id, err)
		}
		if len(vec) == 0 {
			continue // nothing meaningful to backfill for an empty embedding
		}
		if len(vec) != wantDims {
			// A real, expected transitional state, not a bug: this row's
			// stored document_embeddings blob predates a model/dimension
			// change (see RunEmbeddingRecomputeJob) and hasn't been
			// recomputed to the new provider's dims yet -- slicing it by
			// the NEW bounds would either panic (shorter than expected) or
			// silently drop data (longer). Leave its pgvector column(s)
			// NULL for now; saveDocumentEmbeddings backfills it correctly
			// once the recompute actually reaches this document and
			// rewrites document_embeddings with a real wantDims-length
			// vector.
			continue
		}
		args := make([]interface{}, 0, shards+1)
		for shard := 0; shard < shards; shard++ {
			args = append(args, formatPgVectorLiteral(vec[bounds[shard]:bounds[shard+1]]))
		}
		args = append(args, ie.id)
		if _, err := r.db.ExecContext(ctx, updateSQL, args...); err != nil {
			return fmt.Errorf("backfilling %s for %s: %w", col0, ie.id, err)
		}
	}
	return nil
}

// ANNAvailable reports whether ANN search is available for provider (true
// only after EnableANN succeeds for it). Search callers don't need this --
// TopSemanticMatches reports availability itself; exposed for diagnostics/tests.
func (r *Repository) ANNAvailable(provider string) bool {
	return r.ann.isAvailable(provider)
}

// enablePgVectorExtension runs CREATE EXTENSION IF NOT EXISTS vector,
// tolerating the same concurrent-startup race as ensureHostIndex/
// ensureCrawledAtIndex: racing processes can hit a unique-violation on the
// loser even though it ends up enabled.
func (r *Repository) enablePgVectorExtension(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`)
	if err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// queryRower lets vectorColumnType run on either *sql.DB or an open *sql.Tx
// (as ensureVectorColumn uses it), so its read sees the same catalog
// snapshot as the migration decision made from it.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// ensureVectorColumn adds one shard of documents.embedding_vector_<provider>
// (or _<provider>_<shard> when the provider needs more than one -- see
// vectorShardCount) sized to dims, under pg_advisory_xact_lock keyed on
// provider+shard -- not the session-level lock, which can't guarantee
// release through a pool -- since migrating a mismatched column isn't
// idempotent under concurrent EnableANN calls. Bounded by annDDLTimeout --
// see its own doc comment for why.
func (r *Repository) ensureVectorColumn(ctx context.Context, provider string, shard, shards, dims int) error {
	ctx, cancel := context.WithTimeout(ctx, annDDLTimeout)
	defer cancel()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting vector column migration transaction: %w", err)
	}
	defer tx.Rollback()

	// hashtext(...) folds provider+shard into the int key
	// pg_advisory_xact_lock needs; keying by provider+shard lets
	// independent providers/shards migrate concurrently without
	// contending on each other.
	lockKey := fmt.Sprintf("sqlrepo:vector_column:%s:%d", provider, shard)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("acquiring vector column migration lock: %w", err)
	}

	col := vectorColumnNameFor(provider, shard, shards)
	existingDims, existingType, found, err := vectorColumnType(ctx, tx, col)
	if err != nil {
		return err
	}
	if found && (existingDims != dims || existingType != "halfvec") {
		// The model changed (see RunEmbeddingRecomputeJob), its shard count
		// changed, or this predates the vector->halfvec switch -- pgvector's
		// type is fixed once created, so drop the column and stale index and
		// let ADD COLUMN recreate it; backfillVectorColumn repopulates every
		// row, nothing lost.
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+vectorIndexNameFor(provider, shard, shards)); err != nil {
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

// vectorColumnType reads col's actual dimension and pgvector type from
// Postgres's catalog, not trusted in-memory state. found is false if it
// doesn't exist yet; takes a queryRower so ensureVectorColumn can call it
// inside its own locked *sql.Tx.
func vectorColumnType(ctx context.Context, q queryRower, col string) (dims int, typeName string, found bool, err error) {
	const query = `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		WHERE a.attrelid = 'documents'::regclass
		  AND a.attname = $1
		  AND NOT a.attisdropped`
	var formatted string
	if err := q.QueryRowContext(ctx, query, col).Scan(&formatted); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", false, nil
		}
		return 0, "", false, fmt.Errorf("checking existing pgvector column type: %w", err)
	}
	// formatted looks like "halfvec(128)" (or "vector(128)" for a
	// pre-switch column) -- parse the type name and integer from around
	// the parens rather than assuming a fixed prefix, so a future
	// format_type spelling change doesn't silently misparse.
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
// needs for one shard, after ensureVectorColumn. halfvec, not vector:
// pgvector's per-row byte budget caps vector at 2000 dims but halfvec
// (float16) at 4000 -- needed even for a single-shard 3584-dim model with
// no meaningful accuracy loss, and it's this same 4000-dim ceiling that
// vectorShardCount splits a larger embedding around.
func (r *Repository) ensureVectorIndex(ctx context.Context, provider string, shard, shards int) error {
	col := vectorColumnNameFor(provider, shard, shards)
	ddl := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON documents USING hnsw (%s halfvec_cosine_ops)`, vectorIndexNameFor(provider, shard, shards), col)
	if _, err := r.db.ExecContext(ctx, ddl); err != nil && !isAlreadyExistsError(err) {
		return err
	}
	return nil
}

// isMissingExtensionError reports whether err means pgvector isn't
// installed at the OS/server level (vector.control missing), distinct from
// a permission error, so EnableANN can log a clear, specific warning.
func isMissingExtensionError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "could not open extension control file")
}

// formatPgVectorLiteral renders vec in pgvector's text format ("[v1,v2,v3]"),
// used both to write an embedding column and in TopSemanticMatches' ORDER BY.
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

// TopSemanticMatches implements ANN search via pgvector's HNSW index. ok is
// false when unavailable or limit isn't positive -- caller falls back to
// SampleEmbeddings. Sets hnsw.ef_search to limit first: at its default of
// 40, HNSW silently under-recalls a larger requested limit.
//
// For a provider sharded across multiple columns (see vectorShardCount --
// needed once an embedding's dims exceed pgvector's 4000-dim per-index
// ceiling), each shard's own HNSW index is searched independently against
// the matching slice of queryVec, and the candidate doc_id sets are
// UNIONed in one query (not one round trip per shard) before joining back
// to document_embeddings for each candidate's full, unsplit vector --
// final scoring downstream always uses that full vector, never a shard on
// its own, so sharding only changes which candidates ANN surfaces, not the
// precision of any score. Verified empirically (see vectorShardCount's doc
// comment) that this recovers true full-vector top-K with full recall at a
// modest per-shard candidate count.
func (r *Repository) TopSemanticMatches(ctx context.Context, queryVec []float32, limit int, provider string) (map[string]domain.EmbeddedVector, bool, error) {
	if !r.ann.isAvailable(provider) || limit <= 0 {
		return nil, false, nil
	}
	shards := r.ann.shardsFor(provider)

	efSearch := limit
	if efSearch > maxHNSWEfSearch {
		efSearch = maxHNSWEfSearch
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("starting ANN transaction: %w", err)
	}
	defer tx.Rollback()

	// SET's parameter can't be bound as a placeholder (Postgres rejects "$1"
	// there), so efSearch -- an int clamped above, never raw user input --
	// is formatted directly; safe like any %d int literal.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d`, efSearch)); err != nil {
		return nil, false, fmt.Errorf("setting hnsw.ef_search: %w", err)
	}

	// provider reaches here only after passing a map-membership check
	// against the admin-configured embedder set at every caller
	// (hybrid_search_service.go's resolveProviderWeights,
	// restapi/vision_similarity.go's explicit h.embedders[req.Provider]
	// check) -- never raw, unvalidated user input, and its format is
	// separately constrained (see vectorColumnNameFor's own doc comment)
	// even before that. A static SQL-injection scanner can't see either
	// invariant and will flag topSemanticMatchesQuery's provider-derived
	// column name below as tainted; verified false positive, dismissed
	// with this reasoning on the corresponding CodeQL alert.
	query, args, err := topSemanticMatchesQuery(r, provider, shards, queryVec, limit)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
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

// topSemanticMatchesQuery builds TopSemanticMatches' query for an
// arbitrary shard count: a single shard is the original direct
// "ORDER BY <col> <=> query LIMIT n" query unchanged; multiple shards
// become a UNION of one such per-shard candidate-id subquery, joined back
// to documents/document_embeddings for each candidate's full vector.
func topSemanticMatchesQuery(r *Repository, provider string, shards int, queryVec []float32, limit int) (string, []interface{}, error) {
	if shards <= 1 {
		col := vectorColumnNameFor(provider, 0, 1)
		query := r.ph(`SELECT de.doc_id, de.embedding, de.norm_embedding, d.pagerank
		               FROM documents d JOIN document_embeddings de ON de.doc_id = d.id AND de.provider = %s
		               WHERE d.`+col+` IS NOT NULL
		               ORDER BY d.`+col+` <=> %s::halfvec LIMIT %s`, 1, 2, 3)
		return query, []interface{}{provider, formatPgVectorLiteral(queryVec), limit}, nil
	}

	bounds := vectorShardBounds(len(queryVec), shards)
	subqueries := make([]string, shards)
	args := make([]interface{}, 0, shards+2)
	pos := 1
	for shard := 0; shard < shards; shard++ {
		col := vectorColumnNameFor(provider, shard, shards)
		subqueries[shard] = fmt.Sprintf(`(SELECT d.id AS doc_id FROM documents d WHERE d.%s IS NOT NULL ORDER BY d.%s <=> %s::halfvec LIMIT %s)`,
			col, col, r.dialect.Placeholder(pos), r.dialect.Placeholder(pos+1))
		args = append(args, formatPgVectorLiteral(queryVec[bounds[shard]:bounds[shard+1]]), limit)
		pos += 2
	}
	query := fmt.Sprintf(`WITH candidates AS (%s)
	SELECT de.doc_id, de.embedding, de.norm_embedding, d.pagerank
	FROM (SELECT DISTINCT doc_id FROM candidates) c
	JOIN documents d ON d.id = c.doc_id
	JOIN document_embeddings de ON de.doc_id = d.id AND de.provider = %s`,
		strings.Join(subqueries, " UNION "), r.dialect.Placeholder(pos))
	args = append(args, provider)
	return query, args, nil
}
