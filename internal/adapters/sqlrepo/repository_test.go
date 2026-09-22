package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver used when testPostgresDSNEnv is set
	_ "modernc.org/sqlite"             // registers the "sqlite" driver used for the default in-memory test DB

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

var dsnCounter int64

// testPostgresDSNEnv names the environment variable that, when set, points
// this package's whole test suite at a real Postgres server (e.g. the
// postgres:16 service container CI runs) instead of in-memory SQLite. Left
// unset -- the default for a local `go test ./...` -- every test stays
// exactly as fast and dependency-free as before this suite learned to also
// run against Postgres.
const testPostgresDSNEnv = "TEST_POSTGRES_DSN"

// newTestRepo gives each test its own isolated database. Normally that's an
// in-memory SQLite database (a shared cache keyed by a unique name, so the
// pooled *sql.DB connections within one test all see the same schema/data
// without leaking into other tests). When TEST_POSTGRES_DSN is set, it
// instead runs the same test against that real Postgres server, in a fresh
// schema created just for this test so concurrent tests never collide.
func newTestRepo(t testing.TB) *sqlrepo.Repository {
	t.Helper()
	if dsn := os.Getenv(testPostgresDSNEnv); dsn != "" {
		return newPostgresTestRepo(t, dsn)
	}
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testdb%d?mode=memory&cache=shared", n)
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// newPostgresTestRepo points the calling test at baseDSN's Postgres server,
// scoped to a throwaway schema (named after a monotonic counter plus the
// wall clock, so two tests can never collide even across separate `go test`
// invocations against the same long-lived server). The schema -- and every
// table in it -- is dropped in t.Cleanup, so a re-run against the same
// server always starts clean rather than accumulating schemas over time.
func newPostgresTestRepo(t testing.TB, baseDSN string) *sqlrepo.Repository {
	t.Helper()
	repo, _ := newPostgresTestRepoAndRawDB(t, baseDSN)
	return repo
}

// newPostgresTestRepoAndRawDB does exactly what newPostgresTestRepo does,
// but also hands back the raw *sql.DB it opens against the same scoped
// schema/search_path -- for a test that needs to inject state (or inspect
// catalog state) via side-channel SQL the exported Repository API has no
// way to express, the same pattern TestMergeDocuments_CascadesLoserRows
// already uses against SQLite. newPostgresTestRepo itself just discards
// the second value, so every existing call site is unaffected.
func newPostgresTestRepoAndRawDB(t testing.TB, baseDSN string) (*sqlrepo.Repository, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	n := atomic.AddInt64(&dsnCounter, 1)
	schema := fmt.Sprintf("sqlrepo_test_%d_%d", time.Now().UnixNano(), n)

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("failed to open postgres admin connection: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("failed to create postgres test schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		cleanupConn, err := sql.Open("pgx", baseDSN)
		if err != nil {
			t.Logf("failed to open postgres connection to drop schema %s: %v", schema, err)
			return
		}
		defer cleanupConn.Close()
		if _, err := cleanupConn.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Logf("failed to drop postgres test schema %s: %v", schema, err)
		}
	})

	scopedDSN, err := dsnWithSearchPath(baseDSN, schema)
	if err != nil {
		t.Fatalf("failed to scope postgres DSN to test schema: %v", err)
	}
	repo, err := sqlrepo.New(ctx, "pgx", scopedDSN)
	if err != nil {
		t.Fatalf("failed to create postgres test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	rawDB, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatalf("failed to open raw postgres connection: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	return repo, rawDB
}

// dsnWithSearchPath sets baseDSN's search_path query parameter to schema --
// pgx forwards search_path as a Postgres startup parameter on every physical
// connection it opens (not just the first), so this keeps every connection
// database/sql pools for the returned DSN scoped to that one schema.
func dsnWithSearchPath(baseDSN, schema string) (string, error) {
	u, err := url.Parse(baseDSN)
	if err != nil {
		return "", fmt.Errorf("parsing DSN: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestPing_Success(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.Ping(context.Background()); err != nil {
		t.Errorf("expected Ping to succeed on an open connection, got %v", err)
	}
}

func TestNew_MigratesSchemaAndConnects(t *testing.T) {
	repo := newTestRepo(t)
	totalDocs, avgDocLen, err := repo.CorpusStats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 0 || avgDocLen != 1 {
		t.Errorf("expected an empty fresh DB (0 docs, avgDocLen=1), got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestNew_UnknownDriverErrors(t *testing.T) {
	_, err := sqlrepo.New(context.Background(), "not-a-real-driver", "whatever")
	if err == nil {
		t.Fatal("expected an error for an unregistered driver")
	}
}

func TestNewWithDB_UsesGivenConnectionAndDialect(t *testing.T) {
	db, err := sql.Open("sqlite", "file:testnewwithdb?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := sqlrepo.NewWithDB(db, "sqlite")
	// NewWithDB doesn't migrate, so exercise the connection directly first.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	if _, _, err := repo.CorpusStats(context.Background()); err != nil {
		t.Errorf("unexpected error using NewWithDB-constructed repository: %v", err)
	}
}

// TestNew_AppliesDialectAwareDefaultPoolSettings confirms sqlrepo.New sets a
// sane connection-pool baseline immediately at construction, before any
// admin-configured value is ever loaded: SQLite (this test's default,
// TEST_POSTGRES_DSN unset) is clamped to a single connection, since it
// serializes writers at the file level; a real Postgres server (when
// TEST_POSTGRES_DSN points the whole suite at one) gets the full default
// pool instead.
func TestNew_AppliesDialectAwareDefaultPoolSettings(t *testing.T) {
	repo := newTestRepo(t)
	stats := repo.PoolStats()
	if os.Getenv(testPostgresDSNEnv) != "" {
		if stats.MaxOpenConnections != 25 {
			t.Errorf("expected the default of 25 max open connections against a real Postgres server, got %d", stats.MaxOpenConnections)
		}
		return
	}
	if stats.MaxOpenConnections != 1 {
		t.Errorf("expected SQLite to default to a single open connection, got %d", stats.MaxOpenConnections)
	}
}

// TestConfigurePool_SQLiteAlwaysClampsToOneConnection confirms that even an
// admin (or test) requesting a large pool against a SQLite-backed
// repository never actually gets more than one open connection -- SQLite's
// single-writer locking model means a larger pool doesn't add concurrency
// and only risks "database is locked" errors.
func TestConfigurePool_SQLiteAlwaysClampsToOneConnection(t *testing.T) {
	if os.Getenv(testPostgresDSNEnv) != "" {
		t.Skip("this test specifically exercises the SQLite clamp")
	}
	repo := newTestRepo(t)
	repo.ConfigurePool(50, 50, time.Hour)
	stats := repo.PoolStats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("expected MaxOpenConnections clamped to 1 for sqlite regardless of the requested 50, got %d", stats.MaxOpenConnections)
	}
}

// TestConfigurePool_NonSQLiteAppliesGivenValues exercises the clamping
// logic itself against a repository constructed with a non-sqlite dialect
// name (the underlying connection is still an in-memory SQLite database --
// ConfigurePool only ever calls the stdlib pool-limit setters, never a
// dialect-specific query, so this is a faithful unit test of the "not
// sqlite" branch without needing a live Postgres/MySQL server).
func TestConfigurePool_NonSQLiteAppliesGivenValues(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	db, err := sql.Open("sqlite", fmt.Sprintf("file:testconfigurepool%d?mode=memory&cache=shared", n))
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := sqlrepo.NewWithDB(db, "postgres")
	repo.ConfigurePool(7, 4, 90*time.Second)
	stats := repo.PoolStats()
	if stats.MaxOpenConnections != 7 {
		t.Errorf("expected MaxOpenConnections=7 for a non-sqlite dialect, got %d", stats.MaxOpenConnections)
	}
}

// TestNewWithDB_AppliesDefaultPoolSettings confirms NewWithDB (used by
// callers that already have their own *sql.DB) applies the same
// dialect-aware default pool baseline as New, not just an un-pooled
// passthrough.
func TestNewWithDB_AppliesDefaultPoolSettings(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	db, err := sql.Open("sqlite", fmt.Sprintf("file:testnewwithdbpool%d?mode=memory&cache=shared", n))
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := sqlrepo.NewWithDB(db, "sqlite")
	if stats := repo.PoolStats(); stats.MaxOpenConnections != 1 {
		t.Errorf("expected NewWithDB to default sqlite to a single open connection, got %d", stats.MaxOpenConnections)
	}

	pgRepo := sqlrepo.NewWithDB(db, "postgres")
	if stats := pgRepo.PoolStats(); stats.MaxOpenConnections != 25 {
		t.Errorf("expected NewWithDB to default a non-sqlite dialect to 25 max open connections, got %d", stats.MaxOpenConnections)
	}
}

func TestSaveDocument_ThenRetrieveEverywhere(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets indeed"}
	embedding := []float32{0.1, 0.2, 0.3}

	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: embedding}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}

	assertDocumentFetchable(t, repo, ctx, doc)
	assertEmbeddingsRoundTrip(t, repo, ctx, embedding)
	assertDocumentListingAndSearch(t, repo, ctx, doc)
}

// assertDocumentFetchable checks that a just-saved document comes back
// unchanged from DocumentsByIDs, including a populated CrawledAt.
func assertDocumentFetchable(t *testing.T, repo *sqlrepo.Repository, ctx context.Context, doc domain.Document) {
	t.Helper()
	fetchedDocs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"})
	if err != nil {
		t.Fatalf("unexpected error loading document: %v", err)
	}
	got, ok := fetchedDocs["doc-1"]
	if !ok {
		t.Fatalf("expected doc-1 back, got %+v", fetchedDocs)
	}
	if got.URL != doc.URL || got.Title != doc.Title || got.Text != doc.Text {
		t.Errorf("expected saved document back, got %+v", got)
	}
	if got.CrawledAt.IsZero() || time.Since(got.CrawledAt) > time.Minute {
		t.Errorf("expected CrawledAt to be populated with a recent timestamp, got %v", got.CrawledAt)
	}
}

// assertEmbeddingsRoundTrip checks that a just-saved embedding, and its
// precomputed norm, come back the same from both EmbeddingsForDocs and
// SampleEmbeddings.
func assertEmbeddingsRoundTrip(t *testing.T, repo *sqlrepo.Repository, ctx context.Context, embedding []float32) {
	t.Helper()
	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error loading embeddings: %v", err)
	}
	if len(embeddings["doc-1"].Vector) != 3 || embeddings["doc-1"].Vector[0] != 0.1 {
		t.Errorf("expected saved embedding back, got %v", embeddings["doc-1"])
	}
	wantNorm := domain.VectorNorm(embedding)
	if got := embeddings["doc-1"].Norm; got < wantNorm-1e-9 || got > wantNorm+1e-9 {
		t.Errorf("expected precomputed norm %v, got %v", wantNorm, got)
	}

	sampled, err := repo.SampleEmbeddings(ctx, 10, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error sampling embeddings: %v", err)
	}
	if len(sampled["doc-1"].Vector) != 3 || sampled["doc-1"].Vector[0] != 0.1 {
		t.Errorf("expected saved embedding back from sample, got %v", sampled["doc-1"])
	}
	if got := sampled["doc-1"].Norm; got < wantNorm-1e-9 || got > wantNorm+1e-9 {
		t.Errorf("expected precomputed norm %v from sample, got %v", wantNorm, got)
	}
}

// assertDocumentListingAndSearch checks that a just-saved document shows up
// correctly in batch lookup, listing, BM25 postings, and corpus stats.
func assertDocumentListingAndSearch(t *testing.T, repo *sqlrepo.Repository, ctx context.Context, doc domain.Document) {
	t.Helper()
	docsByID, err := repo.DocumentsByIDs(ctx, []string{"doc-1", "does-not-exist"})
	if err != nil {
		t.Fatalf("unexpected error batch-loading documents: %v", err)
	}
	if len(docsByID) != 1 || docsByID["doc-1"].Title != doc.Title {
		t.Errorf("expected only the existing document back, got %+v", docsByID)
	}

	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error listing documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "doc-1" || docs[0].DocLength == 0 {
		t.Errorf("expected the saved document in the listing, got %+v", docs)
	}

	postings, err := repo.PostingsForTerm(ctx, "cats", 100)
	if err != nil {
		t.Fatalf("unexpected error querying postings: %v", err)
	}
	// "cats" appears 3 times: 2 (the titleWeight passed to SaveDocument
	// above) copies of the title ("Cats" twice) plus once in the body text
	// (SaveDocument tokenizes title repeated titleWeight times, then text,
	// giving the title a little more weight).
	if len(postings) != 1 || postings[0].DocID != "doc-1" || postings[0].TermFreq != 3 {
		t.Errorf("expected one posting for 'cats' with freq 3, got %+v", postings)
	}
	if postings[0].TotalDocs != 1 {
		t.Errorf("expected TotalDocs=1, got %d", postings[0].TotalDocs)
	}

	totalDocs, avgDocLen, err := repo.CorpusStats(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalDocs != 1 || avgDocLen <= 0 {
		t.Errorf("expected corpus stats to reflect the saved doc, got (%d, %v)", totalDocs, avgDocLen)
	}
}

func TestAllDocumentIDs_ReturnsEveryIDOrdered(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-b", "doc-a", "doc-c"} {
		doc := domain.Document{ID: id, URL: "http://" + id, Title: "t", Text: "x"}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{0.1}}, 10, 1); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}
	ids, err := repo.AllDocumentIDs(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"doc-a", "doc-b", "doc-c"}; len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Errorf("expected ids ordered %v, got %v", want, ids)
	}
}

func TestAllDocumentIDs_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	ids, err := repo.AllDocumentIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected no ids for an empty corpus, got %v", ids)
	}
}

// TestUpdateEmbedding_OverwritesEmbeddingWithoutTouchingText proves
// UpdateEmbedding is the narrow write application.RunEmbeddingRecomputeJob
// needs: a document's embedding (and precomputed norm) changes, but its
// text, postings, and version history don't -- unlike SaveDocument, which
// would archive a new document_versions row and rebuild postings for any
// text change (irrelevant here, since the text hasn't changed).
func TestUpdateEmbedding_OverwritesEmbeddingWithoutTouchingText(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets indeed"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{0.1, 0.2, 0.3}}, 10, 1); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}

	newEmbedding := []float32{0.9, 0.8, 0.7}
	if err := repo.UpdateEmbedding(ctx, "doc-1", map[string][]float32{domain.EmbeddingProviderHash: newEmbedding}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := embeddings["doc-1"]
	if len(got.Vector) != 3 || got.Vector[0] != 0.9 || got.Vector[1] != 0.8 || got.Vector[2] != 0.7 {
		t.Errorf("expected the new embedding back, got %v", got.Vector)
	}
	wantNorm := domain.VectorNorm(newEmbedding)
	if got.Norm < wantNorm-1e-9 || got.Norm > wantNorm+1e-9 {
		t.Errorf("expected norm recomputed for the new embedding %v, got %v", wantNorm, got.Norm)
	}

	fetchedDocs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetchedDocs["doc-1"].Text != doc.Text || fetchedDocs["doc-1"].Title != doc.Title {
		t.Errorf("expected text/title untouched, got %+v", fetchedDocs["doc-1"])
	}

	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected no archived versions from an embedding-only update, got %+v", versions)
	}

	postings, err := repo.PostingsForTerm(ctx, "cats", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 1 || postings[0].DocID != "doc-1" {
		t.Errorf("expected postings untouched by an embedding-only update, got %+v", postings)
	}
}

func TestUpdateEmbedding_UnknownIDIsNotAnError(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.UpdateEmbedding(context.Background(), "does-not-exist", map[string][]float32{domain.EmbeddingProviderHash: []float32{0.1}}); err != nil {
		t.Errorf("expected updating a nonexistent document's embedding to be a harmless no-op, got: %v", err)
	}
}

// TestSaveDocument_StoresBothProvidersIndependently proves
// document_embeddings genuinely keys on (doc_id, provider) rather than one
// provider silently overwriting another's row for the same document --
// the whole point of storing both hash and http embeddings simultaneously.
func TestSaveDocument_StoresBothProvidersIndependently(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets"}
	embeddings := map[string][]float32{
		domain.EmbeddingProviderHash: {1, 0, 0},
		"http":                       {0, 1, 0, 0},
	}
	if err := repo.SaveDocument(ctx, doc, embeddings, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hashResult, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := hashResult["doc-1"].Vector; len(got) != 3 || got[0] != 1 {
		t.Errorf("expected the hash provider's own vector back, got %v", got)
	}

	httpResult, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := httpResult["doc-1"].Vector; len(got) != 4 || got[1] != 1 {
		t.Errorf("expected the http provider's own (differently-shaped) vector back, got %v", got)
	}
}

// TestSaveDocument_UpdatingOneProviderLeavesTheOtherUntouched proves a
// second SaveDocument call that only supplies one provider (e.g. after
// disabling the other) never clobbers the other provider's
// already-stored row -- each provider's document_embeddings row is
// upserted independently, never as a delete-then-reinsert-all.
func TestSaveDocument_UpdatingOneProviderLeavesTheOtherUntouched(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{
		domain.EmbeddingProviderHash: {1, 0},
		"http":                       {0, 1},
	}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Re-save with only the hash provider present (e.g. content unchanged,
	// but the http provider was disabled between crawls).
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{
		domain.EmbeddingProviderHash: {2, 0},
	}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	httpResult, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := httpResult["doc-1"].Vector; len(got) != 2 || got[1] != 1 {
		t.Errorf("expected the http provider's earlier row to survive untouched, got %v", got)
	}
}

// TestUpdateEmbedding_UpdatesOnlyTheGivenProviders proves UpdateEmbedding
// (the recompute job's narrow write) only ever touches the providers
// present in its argument, never every provider a document happens to
// have stored.
func TestUpdateEmbedding_UpdatesOnlyTheGivenProviders(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "Cats are great pets"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{
		domain.EmbeddingProviderHash: {1, 0},
		"http":                       {0, 1},
	}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.UpdateEmbedding(ctx, "doc-1", map[string][]float32{domain.EmbeddingProviderHash: {9, 9}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hashResult, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := hashResult["doc-1"].Vector; len(got) != 2 || got[0] != 9 {
		t.Errorf("expected the hash provider's vector updated, got %v", got)
	}
	httpResult, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, "http")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := httpResult["doc-1"].Vector; len(got) != 2 || got[1] != 1 {
		t.Errorf("expected the http provider's vector left untouched, got %v", got)
	}
}

// titleRepeatCountForTest is the titleWeight this test passes to
// SaveDocument -- kept as a named constant purely so the assertion below
// reads as "however many times we asked for", not a bare magic number.
const titleRepeatCountForTest = 2

// TestSaveDocument_TitleTermsCountedExtraForModestBoost is the direct
// regression test for SaveDocument's titleWeight parameter: the exact same
// word contributes more to term_freq when it's in the title than when it's
// only in the body, giving a title match a little more weight in BM25
// scoring without needing a separate per-field formula.
func TestSaveDocument_TitleTermsCountedExtraForModestBoost(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	titleMatch := domain.Document{ID: "doc-title", URL: "http://a", Title: "Widget", Text: "This product is great"}
	bodyMatch := domain.Document{ID: "doc-body", URL: "http://b", Title: "Gadget", Text: "This widget is great"}
	for _, d := range []domain.Document{titleMatch, bodyMatch} {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, titleRepeatCountForTest); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerm(ctx, "widget", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("expected postings for both documents, got %+v", postings)
	}
	byDoc := map[string]int{}
	for _, p := range postings {
		byDoc[p.DocID] = p.TermFreq
	}
	if byDoc["doc-title"] != titleRepeatCountForTest {
		t.Errorf("expected 'widget' in the title counted %d times, got %d", titleRepeatCountForTest, byDoc["doc-title"])
	}
	if byDoc["doc-body"] != 1 {
		t.Errorf("expected 'widget' in the body only counted once, got %d", byDoc["doc-body"])
	}
	if byDoc["doc-title"] <= byDoc["doc-body"] {
		t.Errorf("expected the title mention to count for more than the body-only mention, got title=%d body=%d", byDoc["doc-title"], byDoc["doc-body"])
	}
}

// TestSaveDocument_TitleWeightIsConfigurable proves titleWeight isn't just
// wired through but actually changes the resulting term_freq -- a higher
// weight passed for the same document produces a proportionally higher
// count for its title term.
func TestSaveDocument_TitleWeightIsConfigurable(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "Gizmo", Text: "unrelated body"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	postings, err := repo.PostingsForTerm(ctx, "gizmo", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 1 || postings[0].TermFreq != 5 {
		t.Errorf("expected titleWeight=5 to produce TermFreq=5 for a title-only term, got %+v", postings)
	}
}

// TestSaveDocument_NonPositiveTitleWeightStillIndexesTitleOnce proves a
// non-positive titleWeight (a caller that passed a zero value rather than
// a real setting) doesn't silently drop the title from the token stream
// entirely -- it falls back to counting it once, the same as any
// unweighted term.
func TestSaveDocument_NonPositiveTitleWeightStillIndexesTitleOnce(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "Sprocket", Text: "unrelated body"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	postings, err := repo.PostingsForTerm(ctx, "sprocket", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 1 || postings[0].TermFreq != 1 {
		t.Errorf("expected titleWeight<=0 to still index the title once, got %+v", postings)
	}
}

func TestSaveDocument_UpsertReplacesPostings(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	first := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "cats everywhere"}
	if err := repo.SaveDocument(ctx, first, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}

	second := domain.Document{ID: "doc-1", URL: "http://a", Title: "Dogs", Text: "dogs everywhere"}
	if err := repo.SaveDocument(ctx, second, map[string][]float32{domain.EmbeddingProviderHash: []float32{2}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on upsert save: %v", err)
	}

	catsPostings, err := repo.PostingsForTerm(ctx, "cats", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(catsPostings) != 0 {
		t.Errorf("expected the old 'cats' posting to be gone after upsert, got %+v", catsPostings)
	}

	dogsPostings, err := repo.PostingsForTerm(ctx, "dogs", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dogsPostings) != 1 {
		t.Errorf("expected a 'dogs' posting after upsert, got %+v", dogsPostings)
	}

	docs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if docs["doc-1"].Title != "Dogs" {
		t.Errorf("expected upserted title, got %q", docs["doc-1"].Title)
	}
}

func TestPostingsForTerm_NoMatches(t *testing.T) {
	repo := newTestRepo(t)
	postings, err := repo.PostingsForTerm(context.Background(), "nonexistent", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postings != nil {
		t.Errorf("expected nil postings for an unindexed term, got %+v", postings)
	}
}

func TestPostingsForTerm_AcrossMultipleDocuments(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared term here"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared term appears twice, shared term"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerm(ctx, "shared", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("expected postings from both documents, got %+v", postings)
	}
	for _, p := range postings {
		if p.DocFreq != 2 {
			t.Errorf("expected DocFreq=2 (term appears in 2 docs), got %d", p.DocFreq)
		}
	}
}

// TestPostingsForTerm_RespectsLimitAndOrdering verifies a limit narrower than
// the term's true doc_freq still returns the strongest matches (highest
// term_freq first), not an arbitrary subset -- otherwise the vocabulary
// term-detail view's default limit would silently hide the very pages an
// admin would want to see first.
func TestPostingsForTerm_RespectsLimitAndOrdering(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-weak", URL: "http://a", Title: "A", Text: "shared once"},
		{ID: "doc-strong", URL: "http://b", Title: "B", Text: "shared shared shared shared"},
		{ID: "doc-mid", URL: "http://c", Title: "C", Text: "shared shared"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerm(ctx, "shared", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("expected the limit to cap the result at 2, got %+v", postings)
	}
	if postings[0].DocID != "doc-strong" || postings[1].DocID != "doc-mid" {
		t.Errorf("expected the two highest-term_freq postings in descending order, got %+v", postings)
	}
	if postings[0].DocFreq != 3 {
		t.Errorf("expected DocFreq to reflect all 3 matching docs regardless of the limit, got %d", postings[0].DocFreq)
	}
}

func TestPostingsForTerms_EmptyTermsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	postings, err := repo.PostingsForTerms(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 0 {
		t.Errorf("expected an empty result for no terms, got %+v", postings)
	}
}

func TestPostingsForTerms_NoMatches(t *testing.T) {
	repo := newTestRepo(t)
	postings, err := repo.PostingsForTerms(context.Background(), []string{"nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings["nonexistent"]) != 0 {
		t.Errorf("expected no postings for an unindexed term, got %+v", postings)
	}
}

// TestPostingsForTerms_BatchesMultipleTermsInOneCall verifies the fix for
// hybrid search's "one query per query term" problem: a single
// PostingsForTerms call for several terms returns each term's own postings
// (with per-term DocFreq computed from the returned rows), keyed
// separately -- not conflated with each other, and without one query per
// term.
func TestPostingsForTerms_BatchesMultipleTermsInOneCall(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "cats and dogs"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "cats everywhere, cats"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerms(ctx, []string{"cats", "dogs", "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	catsPostings := postings["cats"]
	if len(catsPostings) != 2 {
		t.Fatalf("expected 'cats' postings from both documents, got %+v", catsPostings)
	}
	for _, p := range catsPostings {
		if p.DocFreq != 2 {
			t.Errorf("expected DocFreq=2 for 'cats' (appears in 2 docs), got %d", p.DocFreq)
		}
		if p.TotalDocs != 0 || p.AvgDocLen != 0 {
			t.Errorf("expected PostingsForTerms to leave TotalDocs/AvgDocLen unset (caller fills them in from its own corpus-wide stats), got %+v", p)
		}
	}
	for _, p := range catsPostings {
		if p.DocID == "doc-2" && p.TermFreq != 2 {
			t.Errorf("expected doc-2's 'cats' term_freq=2, got %d", p.TermFreq)
		}
	}

	dogsPostings := postings["dogs"]
	if len(dogsPostings) != 1 || dogsPostings[0].DocID != "doc-1" || dogsPostings[0].DocFreq != 1 {
		t.Errorf("expected a single 'dogs' posting for doc-1 with DocFreq=1, got %+v", dogsPostings)
	}

	if len(postings["nonexistent"]) != 0 {
		t.Errorf("expected no postings for an unindexed term, got %+v", postings["nonexistent"])
	}
}

func TestVocabularyStats_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	vocabSize, matched, topTerms, err := repo.VocabularyStats(context.Background(), 10, 0, "", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vocabSize != 0 || matched != 0 || topTerms != nil {
		t.Errorf("expected an empty vocabulary, got (%d, %d, %+v)", vocabSize, matched, topTerms)
	}
}

func vocabularyTestCorpus(t *testing.T, repo *sqlrepo.Repository, ctx context.Context) {
	t.Helper()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common rare"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared common common"},
		{ID: "doc-3", URL: "http://c", Title: "C", Text: "shared unique"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}
}

func TestVocabularyStats_ReportsSizeAndTopTermsByDocFreq(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	vocabularyTestCorpus(t, repo, ctx)

	vocabSize, matched, topTerms, err := repo.VocabularyStats(ctx, 2, 0, "", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// distinct terms across all three docs: shared, common, rare, unique.
	if vocabSize != 4 {
		t.Errorf("expected vocabulary size 4, got %d", vocabSize)
	}
	if matched != 4 {
		t.Errorf("expected matchedCount=4 (no search filter, so it equals vocabSize), got %d", matched)
	}
	if len(topTerms) != 2 {
		t.Fatalf("expected limit=2 to be respected, got %+v", topTerms)
	}
	if topTerms[0].Term != "shared" || topTerms[0].DocFreq != 3 || topTerms[0].TotalFreq != 3 {
		t.Errorf("expected 'shared' first with doc_freq=3, total_freq=3, got %+v", topTerms[0])
	}
	if topTerms[1].Term != "common" || topTerms[1].DocFreq != 2 || topTerms[1].TotalFreq != 3 {
		t.Errorf("expected 'common' second with doc_freq=2, total_freq=3, got %+v", topTerms[1])
	}
}

// TestVocabularyStats_OffsetPagesPastTheFirstLimit proves offset actually
// advances the page rather than being silently ignored: the same query
// with offset=2 picks up right where a limit=2/offset=0 page left off.
func TestVocabularyStats_OffsetPagesPastTheFirstLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	vocabularyTestCorpus(t, repo, ctx)

	firstPage, _, page1, err := repo.VocabularyStats(ctx, 2, 0, "", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, page2, err := repo.VocabularyStats(ctx, 2, 2, "", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if firstPage != 4 || len(page1) != 2 || len(page2) != 2 {
		t.Fatalf("expected two full pages of 2 out of vocabSize=4, got page1=%+v page2=%+v", page1, page2)
	}
	seen := map[string]bool{}
	for _, s := range append(append([]domain.TermStat{}, page1...), page2...) {
		if seen[s.Term] {
			t.Errorf("expected offset=2 to page past page1 without repeating %q", s.Term)
		}
		seen[s.Term] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected the two pages together to cover all 4 distinct terms, got %+v", seen)
	}
}

// TestVocabularyStats_SortByTermAscending proves sortBy="term" orders
// alphabetically rather than by frequency -- unlike the doc_freq default,
// this ordering also holds across the whole vocabulary, not just within
// whatever the frequency-based top page happened to include.
func TestVocabularyStats_SortByTermAscending(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	vocabularyTestCorpus(t, repo, ctx)

	_, _, terms, err := repo.VocabularyStats(ctx, 10, 0, "", "term", "asc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"common", "rare", "shared", "unique"}
	if len(terms) != len(want) {
		t.Fatalf("expected %d terms, got %+v", len(want), terms)
	}
	for i, w := range want {
		if terms[i].Term != w {
			t.Errorf("expected terms[%d]=%q, got %q (full: %+v)", i, w, terms[i].Term, terms)
		}
	}
}

// TestVocabularyStats_SortByTotalFreqDescending proves sortBy="total_freq"
// orders by the (potentially different) total-occurrence count rather than
// doc_freq -- "common" occurs 3 times total (twice in doc-2) but only in 2
// documents, so it outranks "shared" (3 distinct documents, 3 occurrences)
// under this sort even though "shared" has the higher doc_freq.
func TestVocabularyStats_SortByTotalFreqDescending(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	vocabularyTestCorpus(t, repo, ctx)

	_, _, terms, err := repo.VocabularyStats(ctx, 1, 0, "", "total_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(terms) != 1 || terms[0].TotalFreq != 3 {
		t.Fatalf("expected a single top result with total_freq=3 (a 3-way tie broken by term ascending), got %+v", terms)
	}
}

// TestVocabularyStats_SearchFiltersTermsAndReportsMatchedCount verifies the
// search-filtered listing only includes terms containing the search
// string, matchedCount reflects that filtered total (not vocabSize), and
// vocabSize itself keeps reporting the whole corpus's distinct-term count
// regardless of the filter.
func TestVocabularyStats_SearchFiltersTermsAndReportsMatchedCount(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	vocabularyTestCorpus(t, repo, ctx)

	vocabSize, matched, topTerms, err := repo.VocabularyStats(ctx, 10, 0, "rare", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vocabSize != 4 {
		t.Errorf("expected vocabSize to stay at the whole corpus's 4 regardless of the filter, got %d", vocabSize)
	}
	if matched != 1 {
		t.Errorf("expected matchedCount=1 for a search matching only 'rare', got %d", matched)
	}
	if len(topTerms) != 1 || topTerms[0].Term != "rare" {
		t.Errorf("expected only 'rare' to match search %q, got %+v", "rare", topTerms)
	}
}

func TestVocabularyStats_SearchWithNoMatchesReturnsEmptyTopTerms(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving doc: %v", err)
	}

	_, matched, topTerms, err := repo.VocabularyStats(ctx, 10, 0, "zzz-no-such-term", "doc_freq", "desc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched != 0 {
		t.Errorf("expected matchedCount=0 for a search with no matches, got %d", matched)
	}
	if len(topTerms) != 0 {
		t.Errorf("expected no terms to match, got %+v", topTerms)
	}
}

func TestAllTerms_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	terms, err := repo.AllTerms(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(terms) != 0 {
		t.Errorf("expected an empty vocabulary, got %+v", terms)
	}
}

// TestAllTerms_ReturnsEveryTermUnbounded verifies AllTerms reports the whole
// vocabulary -- unlike VocabularyStats' topN-bounded listing, every distinct
// term appears regardless of how many there are, since domain.NearestTerm
// needs the full vocabulary to check a mistyped query term against.
func TestAllTerms_ReturnsEveryTermUnbounded(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common rare"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared common common"},
		{ID: "doc-3", URL: "http://c", Title: "C", Text: "shared unique"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	terms, err := repo.AllTerms(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byTerm := make(map[string]domain.TermStat, len(terms))
	for _, s := range terms {
		byTerm[s.Term] = s
	}
	if len(byTerm) != 4 {
		t.Fatalf("expected all 4 distinct terms, got %+v", terms)
	}
	if s := byTerm["shared"]; s.DocFreq != 3 || s.TotalFreq != 3 {
		t.Errorf("expected 'shared' doc_freq=3, total_freq=3, got %+v", s)
	}
	if s := byTerm["common"]; s.DocFreq != 2 || s.TotalFreq != 3 {
		t.Errorf("expected 'common' doc_freq=2, total_freq=3, got %+v", s)
	}
	if s, ok := byTerm["rare"]; !ok || s.DocFreq != 1 || s.TotalFreq != 1 {
		t.Errorf("expected 'rare' (a bottom-frequency term VocabularyStats' topN would omit) doc_freq=1, total_freq=1, got %+v (present=%v)", s, ok)
	}
}

func TestDeleteDocument_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "cats are great"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	if err := repo.DeleteDocument(ctx, "doc-1"); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}

	if docs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"}); err != nil || len(docs) != 0 {
		t.Errorf("expected the document to be gone after delete, got docs=%+v err=%v", docs, err)
	}
	postings, err := repo.PostingsForTerm(ctx, "cats", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(postings) != 0 {
		t.Errorf("expected postings to cascade-delete with the document, got %+v", postings)
	}
}

func TestDeleteDocument_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteDocument(context.Background(), "does-not-exist")
	if !errors.Is(err, ports.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

// TestRecordDocumentAlias_UpsertsRow proves a basic alias round-trips.
func TestRecordDocumentAlias_UpsertsRow(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testalias%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.RecordDocumentAlias(ctx, "https://www.example.com/x", "doc-canonical", domain.DocumentAliasReasonCanonicalTag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var canonicalID, reason string
	if err := raw.QueryRow(`SELECT canonical_id, reason FROM document_aliases WHERE alias_url = 'https://www.example.com/x'`).Scan(&canonicalID, &reason); err != nil {
		t.Fatalf("unexpected error querying the alias row: %v", err)
	}
	if canonicalID != "doc-canonical" || reason != domain.DocumentAliasReasonCanonicalTag {
		t.Errorf("expected canonical_id=doc-canonical, reason=%s, got canonical_id=%s, reason=%s",
			domain.DocumentAliasReasonCanonicalTag, canonicalID, reason)
	}
}

// TestRecordDocumentAlias_ToleratesForwardDeclaredCanonicalID proves the
// whole reason document_aliases has no foreign key on canonical_id: the
// canonical target may not be crawled yet when its alias is discovered.
func TestRecordDocumentAlias_ToleratesForwardDeclaredCanonicalID(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.RecordDocumentAlias(context.Background(), "https://www.example.com/x", "doc-not-yet-crawled", domain.DocumentAliasReasonCanonicalTag)
	if err != nil {
		t.Errorf("expected no error recording an alias for a not-yet-existing canonical document, got %v", err)
	}
}

// TestRecordDocumentAlias_UpsertReplacesExisting proves recording the same
// alias URL again (e.g. a re-crawl finding a different canonical target)
// replaces the previous mapping rather than erroring or duplicating.
func TestRecordDocumentAlias_UpsertReplacesExisting(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testaliasupsert%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.RecordDocumentAlias(ctx, "https://www.example.com/x", "doc-old", domain.DocumentAliasReasonCanonicalTag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://www.example.com/x", "doc-new", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM document_aliases WHERE alias_url = 'https://www.example.com/x'`).Scan(&count); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one row (upserted, not duplicated), got %d", count)
	}
	var canonicalID string
	if err := raw.QueryRow(`SELECT canonical_id FROM document_aliases WHERE alias_url = 'https://www.example.com/x'`).Scan(&canonicalID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if canonicalID != "doc-new" {
		t.Errorf("expected the second call's canonical_id to win, got %q", canonicalID)
	}
}

func TestAllDocumentFingerprints_ReturnsEveryDocument(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-a", URL: "https://a.example/x", Title: "A", Text: "hello world"},
		{ID: "doc-b", URL: "https://b.example/y", Title: "B", Text: "goodbye world"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	fingerprints, err := repo.AllDocumentFingerprints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fingerprints) != 2 {
		t.Fatalf("expected 2 fingerprints, got %d: %+v", len(fingerprints), fingerprints)
	}
	byID := make(map[string]domain.DocumentFingerprint, len(fingerprints))
	for _, f := range fingerprints {
		byID[f.ID] = f
	}
	a, ok := byID["doc-a"]
	if !ok {
		t.Fatalf("expected doc-a present, got %+v", fingerprints)
	}
	if a.URL != "https://a.example/x" || a.Host != "a.example" {
		t.Errorf("expected doc-a's URL/Host populated, got %+v", a)
	}
	if a.ContentHash != domain.ContentHash("hello world") {
		t.Errorf("expected doc-a's ContentHash to match domain.ContentHash, got %q", a.ContentHash)
	}
	if a.SimHash != domain.EncodeSimHash64(domain.SimHash64("hello world")) {
		t.Errorf("expected doc-a's SimHash to match domain.SimHash64, got %q", a.SimHash)
	}
	if a.CrawledAt.IsZero() {
		t.Error("expected doc-a's CrawledAt to be set")
	}
}

// TestMergeDocuments_CascadesLoserRows proves a merged-away loser's row,
// and everything referencing it by doc_id (postings, document_versions,
// document_embeddings, links), are all gone -- and that a document_aliases
// row survives mapping its URL to the canonical document.
func TestMergeDocuments_CascadesLoserRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmerge%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	canonical := domain.Document{ID: "doc-canonical", URL: "https://canonical.example/", Title: "C", Text: "shared content"}
	loser := domain.Document{
		ID: "doc-loser", URL: "https://loser.example/", Title: "L", Text: "shared content",
		Links: []string{"https://elsewhere.example/z"},
	}
	for _, d := range []domain.Document{canonical, loser} {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	if err := repo.MergeDocuments(ctx, "doc-canonical", []string{"doc-loser"}, domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var docCount int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM documents WHERE id = 'doc-loser'`).Scan(&docCount); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if docCount != 0 {
		t.Error("expected the loser's documents row removed")
	}
	for _, table := range []string{"postings", "document_versions", "document_embeddings", "links"} {
		var count int
		col := "doc_id"
		if table == "links" {
			col = "from_id"
		}
		if err := raw.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE ` + col + ` = 'doc-loser'`).Scan(&count); err != nil {
			t.Fatalf("unexpected error querying %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("expected no %s rows left for the merged-away loser, got %d", table, count)
		}
	}

	var canonicalID, reason string
	if err := raw.QueryRow(`SELECT canonical_id, reason FROM document_aliases WHERE alias_url = 'https://loser.example/'`).Scan(&canonicalID, &reason); err != nil {
		t.Fatalf("unexpected error querying the alias row: %v", err)
	}
	if canonicalID != "doc-canonical" || reason != domain.DocumentAliasReasonContentExact {
		t.Errorf("expected an alias to doc-canonical with reason=content_exact, got canonical_id=%s reason=%s", canonicalID, reason)
	}

	got, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "doc-canonical" {
		t.Errorf("expected only the canonical document to remain indexed, got %+v", got)
	}
}

// TestMergeDocuments_RepointsExistingAliasOfLoser proves path compression:
// an alias that already pointed at the loser (e.g. a rel=canonical alias
// recorded before this document was itself found to be a content
// duplicate) is repointed straight to the surviving canonical document,
// never left pointing at a now-deleted ID.
func TestMergeDocuments_RepointsExistingAliasOfLoser(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, d := range []domain.Document{
		{ID: "doc-canonical", URL: "https://canonical.example/", Title: "C", Text: "shared content"},
		{ID: "doc-loser", URL: "https://loser.example/", Title: "L", Text: "shared content"},
	} {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}
	if err := repo.RecordDocumentAlias(ctx, "https://old-alias.example/", "doc-loser", domain.DocumentAliasReasonCanonicalTag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.MergeDocuments(ctx, "doc-canonical", []string{"doc-loser"}, domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	groups, total, err := repo.ListDocumentAliasGroups(ctx, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected exactly one canonical group (both aliases repointed to the same survivor), got total=%d groups=%+v", total, groups)
	}
	if len(groups) != 1 || groups[0].CanonicalID != "doc-canonical" {
		t.Fatalf("expected the one group's canonical to be doc-canonical, got %+v", groups)
	}
	wantAliases := map[string]bool{"https://loser.example/": true, "https://old-alias.example/": true}
	if len(groups[0].Aliases) != 2 {
		t.Fatalf("expected 2 aliases (the loser's own URL, plus the repointed pre-existing alias), got %+v", groups[0].Aliases)
	}
	for _, a := range groups[0].Aliases {
		if !wantAliases[a.URL] {
			t.Errorf("unexpected alias URL %q", a.URL)
		}
	}
}

// TestMergeDocuments_SkipsLoserEqualToCanonical proves a (defensive,
// shouldn't-happen-in-practice) loserIDs entry naming the canonical itself
// is simply skipped, not an error.
func TestMergeDocuments_SkipsLoserEqualToCanonical(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "content"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.MergeDocuments(ctx, "doc-a", []string{"doc-a"}, domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected the document untouched, got %+v", got)
	}
}

// TestMergeDocuments_SkipsAlreadyRemovedLoser proves a loserIDs entry
// naming a document that no longer exists (removed by a concurrent run)
// is silently skipped rather than erroring the whole merge.
func TestMergeDocuments_SkipsAlreadyRemovedLoser(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "content"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := repo.MergeDocuments(ctx, "doc-a", []string{"doc-never-existed"}, domain.DocumentAliasReasonContentExact)
	if err != nil {
		t.Errorf("expected no error for a loser that no longer exists, got %v", err)
	}
}

func TestListDocumentAliasGroups_EmptyWhenNoAliasesExist(t *testing.T) {
	repo := newTestRepo(t)
	groups, total, err := repo.ListDocumentAliasGroups(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || len(groups) != 0 {
		t.Errorf("expected no groups on a fresh DB, got total=%d groups=%+v", total, groups)
	}
}

// TestListDocumentAliasGroups_ForwardDeclaredCanonicalHasEmptyURL proves a
// group whose canonical document hasn't actually been crawled yet (a
// forward-declared alias) reports an empty CanonicalURL rather than
// erroring -- see RecordDocumentAlias's own doc comment.
func TestListDocumentAliasGroups_ForwardDeclaredCanonicalHasEmptyURL(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.RecordDocumentAlias(ctx, "https://alias.example/", "doc-not-yet-crawled", domain.DocumentAliasReasonCanonicalTag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	groups, total, err := repo.ListDocumentAliasGroups(ctx, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(groups) != 1 {
		t.Fatalf("expected exactly one group, got total=%d groups=%+v", total, groups)
	}
	if groups[0].CanonicalURL != "" {
		t.Errorf("expected an empty CanonicalURL for a not-yet-crawled canonical, got %q", groups[0].CanonicalURL)
	}
	if len(groups[0].Aliases) != 1 || groups[0].Aliases[0].URL != "https://alias.example/" {
		t.Errorf("expected the one alias URL listed, got %+v", groups[0].Aliases)
	}
	if groups[0].Aliases[0].Reason != domain.DocumentAliasReasonCanonicalTag {
		t.Errorf("expected the alias's own reason surfaced, got %q", groups[0].Aliases[0].Reason)
	}
}

// TestListDocumentAliasGroups_PaginatesByCanonicalID proves limit/offset
// page over distinct canonical groups, not raw alias rows.
func TestListDocumentAliasGroups_PaginatesByCanonicalID(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, canonicalID := range []string{"doc-1", "doc-2", "doc-3"} {
		if err := repo.RecordDocumentAlias(ctx, "https://alias-"+canonicalID+".example/", canonicalID, domain.DocumentAliasReasonCanonicalTag); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	groups, total, err := repo.ListDocumentAliasGroups(ctx, 2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total=3 regardless of the page size, got %d", total)
	}
	if len(groups) != 2 {
		t.Errorf("expected exactly 2 groups on a page of size 2, got %d: %+v", len(groups), groups)
	}

	page2, total2, err := repo.ListDocumentAliasGroups(ctx, 2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total2 != 3 || len(page2) != 1 {
		t.Errorf("expected the second page to hold the remaining 1 group, got total=%d groups=%+v", total2, page2)
	}
}

// TestClearContent_DeletesEveryContentTableButNoSettingsTable proves
// ClearContent removes documents (and everything derived from it via
// cascade), document_aliases, and crawl_jobs -- but leaves every settings
// table untouched.
func TestClearContent_DeletesEveryContentTableButNoSettingsTable(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	doc := domain.Document{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "content", Links: []string{"https://a.example/b"}}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://alias.example/", "doc-a", domain.DocumentAliasReasonCanonicalTag); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveSetting(ctx, ports.SettingsKeyOperational, `{"UserAgent":"kept"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.ClearContent(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, table := range []string{"documents", "postings", "links", "document_versions", "document_embeddings", "crawl_jobs"} {
		if counts[table] != 0 {
			t.Errorf("expected %s empty after ClearContent, got %d rows", table, counts[table])
		}
	}
	if _, total, err := repo.ListDocumentAliasGroups(ctx, 10, 0); err != nil || total != 0 {
		t.Errorf("expected document_aliases empty after ClearContent, got total=%d err=%v", total, err)
	}

	raw, found, err := repo.GetSetting(ctx, ports.SettingsKeyOperational)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || raw != `{"UserAgent":"kept"}` {
		t.Errorf("expected app_settings left untouched by ClearContent, got found=%v raw=%q", found, raw)
	}
}

// TestClearSettings_DeletesEverySettingsTableButNoContentTable proves
// ClearSettings removes app_settings, chat_endpoint,
// embedding_http_endpoints, and scheduled_crawls -- but leaves crawled
// content untouched.
func TestClearSettings_DeletesEverySettingsTableButNoContentTable(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	doc := domain.Document{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "content"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveSetting(ctx, ports.SettingsKeyOperational, `{"UserAgent":"gone"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SetChatEndpoint(ctx, domain.ChatEndpoint{BaseURL: "http://x", Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateEmbeddingEndpoint(ctx, domain.EmbeddingHTTPEndpoint{ID: "e1", Name: "E1", BaseURL: "http://x", Model: "m", Dimensions: 4}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateScheduledCrawl(ctx, newScheduledCrawl("sched-1", 5, time.Now())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.ClearSettings(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, table := range []string{"app_settings", "chat_endpoint", "embedding_http_endpoints", "scheduled_crawls"} {
		if counts[table] != 0 {
			t.Errorf("expected %s empty after ClearSettings, got %d rows", table, counts[table])
		}
	}

	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("expected crawled content left untouched by ClearSettings, got %d documents", len(docs))
	}
}

func TestTryAcquireContentDedupLock_SucceedsWhenFree(t *testing.T) {
	repo := newTestRepo(t)
	acquired, err := repo.TryAcquireContentDedupLock(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !acquired {
		t.Error("expected the lock to be free on a fresh database")
	}
}

// TestTryAcquireContentDedupLock_FailsWhileAlreadyHeld proves a second
// acquire attempt is refused while the first caller still holds it -- the
// exact scenario that let two content-dedup runs interleave in production
// before this lock existed.
func TestTryAcquireContentDedupLock_FailsWhileAlreadyHeld(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	first, err := repo.TryAcquireContentDedupLock(ctx)
	if err != nil || !first {
		t.Fatalf("expected the first acquire to succeed, got acquired=%v err=%v", first, err)
	}
	second, err := repo.TryAcquireContentDedupLock(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second {
		t.Error("expected the second acquire to fail while the first still holds the lock")
	}
}

func TestReleaseContentDedupLock_AllowsReacquiring(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if _, err := repo.TryAcquireContentDedupLock(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.ReleaseContentDedupLock(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reacquired, err := repo.TryAcquireContentDedupLock(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reacquired {
		t.Error("expected the lock to be acquirable again after being released")
	}
}

// TestReleaseContentDedupLock_IdempotentWhenAlreadyFree proves releasing a
// lock nobody holds is a harmless no-op, not an error -- a deferred release
// after a failed/short-circuited acquire must never itself need handling.
func TestReleaseContentDedupLock_IdempotentWhenAlreadyFree(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.ReleaseContentDedupLock(context.Background()); err != nil {
		t.Fatalf("expected releasing a free lock to be a no-op, got %v", err)
	}
}

func TestListDocuments_RespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2", "doc-3"} {
		doc := domain.Document{ID: id, URL: "http://" + id, Title: id, Text: "content for " + id}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}

	docs, err := repo.ListDocuments(ctx, 2, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected limit=2 to be respected, got %d documents", len(docs))
	}
}

func TestNew_MigrationFailureErrors(t *testing.T) {
	// A read-only DB file: Open and Ping succeed, but CREATE TABLE fails.
	path := t.TempDir() + "/readonly.db"
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create temp db file: %v", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("failed to make temp db file read-only: %v", err)
	}

	_, err = sqlrepo.New(context.Background(), "sqlite", "file:"+path+"?mode=ro")
	if err == nil {
		t.Fatal("expected a migration error against a read-only database")
	}
}

func TestNew_PingFailureErrors(t *testing.T) {
	// A file DSN under a directory that doesn't exist: sql.Open succeeds
	// (it's lazy), but PingContext fails trying to actually open the file.
	_, err := sqlrepo.New(context.Background(), "sqlite", "file:/nonexistent-dir-xyz/test.db")
	if err == nil {
		t.Fatal("expected an error when the DB can't actually be reached")
	}
}

// closedRepo returns a repository whose underlying connection is already
// closed, to exercise the DB-error branches of every method with a real
// (not mocked) failure: any query against a closed *sql.DB errors.
func closedRepo(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	repo := newTestRepo(t)
	if err := repo.Close(); err != nil {
		t.Fatalf("failed to close repository: %v", err)
	}
	return repo
}

func TestRepository_MethodsErrorOnClosedConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("Ping", func(t *testing.T) {
		if err := closedRepo(t).Ping(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CorpusStats", func(t *testing.T) {
		if _, _, err := closedRepo(t).CorpusStats(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("EmbeddingsForDocs", func(t *testing.T) {
		if _, err := closedRepo(t).EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SampleEmbeddings", func(t *testing.T) {
		if _, err := closedRepo(t).SampleEmbeddings(ctx, 10, domain.EmbeddingProviderHash); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentsByIDs", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentsByIDs(ctx, []string{"doc-1"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentsByIDsSortedByCrawledAt", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentsByIDsSortedByCrawledAt(ctx, []string{"doc-1"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("VocabularyStats", func(t *testing.T) {
		if _, _, _, err := closedRepo(t).VocabularyStats(ctx, 10, 0, "", "doc_freq", "desc"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PostingsForTerm", func(t *testing.T) {
		if _, err := closedRepo(t).PostingsForTerm(ctx, "term", 100); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PostingsForTerms", func(t *testing.T) {
		if _, err := closedRepo(t).PostingsForTerms(ctx, []string{"term"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PageRankDistribution", func(t *testing.T) {
		if _, _, _, err := closedRepo(t).PageRankDistribution(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("TableRowCounts", func(t *testing.T) {
		if _, err := closedRepo(t).TableRowCounts(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DeleteDocument", func(t *testing.T) {
		if err := closedRepo(t).DeleteDocument(ctx, "doc-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ListDocuments", func(t *testing.T) {
		if _, err := closedRepo(t).ListDocuments(ctx, 10, ""); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SaveDocument", func(t *testing.T) {
		doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}
		if err := closedRepo(t).SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SearchDomains", func(t *testing.T) {
		if _, err := closedRepo(t).SearchDomains(ctx, "example", 10); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentVersions", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentVersions(ctx, "doc-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentsOverview", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentsOverview(ctx, 5); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobOutcomes", func(t *testing.T) {
		if _, err := closedRepo(t).CrawlJobOutcomes(ctx, time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DailyFetchOutcomes", func(t *testing.T) {
		if _, err := closedRepo(t).DailyFetchOutcomes(ctx, time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentsIndexedByDay", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentsIndexedByDay(ctx, time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DailyFetchDuration", func(t *testing.T) {
		if _, err := closedRepo(t).DailyFetchDuration(ctx, time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PageRankHistogram", func(t *testing.T) {
		if _, _, _, err := closedRepo(t).PageRankHistogram(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CreateScheduledCrawl", func(t *testing.T) {
		s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 5}
		if err := closedRepo(t).CreateScheduledCrawl(ctx, s); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ListScheduledCrawls", func(t *testing.T) {
		if _, err := closedRepo(t).ListScheduledCrawls(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("UpdateScheduledCrawl", func(t *testing.T) {
		s := domain.ScheduledCrawl{ID: "sched-1", SeedURLs: []string{"http://a"}, IntervalMinutes: 5}
		if err := closedRepo(t).UpdateScheduledCrawl(ctx, s); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DeleteScheduledCrawl", func(t *testing.T) {
		if err := closedRepo(t).DeleteScheduledCrawl(ctx, "sched-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DueScheduledCrawls", func(t *testing.T) {
		if _, err := closedRepo(t).DueScheduledCrawls(ctx, time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("MarkScheduledCrawlRun", func(t *testing.T) {
		if err := closedRepo(t).MarkScheduledCrawlRun(ctx, "sched-1", time.Now(), time.Now(), true, false, 1, "job-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("RunScheduledCrawlNow", func(t *testing.T) {
		if err := closedRepo(t).RunScheduledCrawlNow(ctx, "sched-1", time.Now()); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SetScheduledCrawlEnabled", func(t *testing.T) {
		if err := closedRepo(t).SetScheduledCrawlEnabled(ctx, "sched-1", true); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ResetStaleInProgress", func(t *testing.T) {
		if _, err := closedRepo(t).ResetStaleInProgress(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("AllTerms", func(t *testing.T) {
		if _, err := closedRepo(t).AllTerms(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DocumentIDsByHost", func(t *testing.T) {
		if _, err := closedRepo(t).DocumentIDsByHost(ctx, []string{"example.com"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("LinkGraph", func(t *testing.T) {
		if _, err := closedRepo(t).LinkGraph(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("UpdatePageRanks", func(t *testing.T) {
		if err := closedRepo(t).UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.5}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("GetSetting", func(t *testing.T) {
		if _, _, err := closedRepo(t).GetSetting(ctx, "some-key"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SaveSetting", func(t *testing.T) {
		if err := closedRepo(t).SaveSetting(ctx, "some-key", "value"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_Create", func(t *testing.T) {
		if _, err := closedRepo(t).Create(ctx, domain.CrawlJobRequest{SeedURLs: []string{"http://a"}}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_MarkRunning", func(t *testing.T) {
		if err := closedRepo(t).MarkRunning(ctx, "job-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_AppendPage", func(t *testing.T) {
		if err := closedRepo(t).AppendPage(ctx, "job-1", domain.CrawlPageEvent{URL: "http://a", Status: domain.CrawlPageIndexed}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_MarkDone", func(t *testing.T) {
		if err := closedRepo(t).MarkDone(ctx, "job-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_MarkFailed", func(t *testing.T) {
		if err := closedRepo(t).MarkFailed(ctx, "job-1", errors.New("boom")); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_Get", func(t *testing.T) {
		if _, err := closedRepo(t).Get(ctx, "job-1"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_List", func(t *testing.T) {
		if _, err := closedRepo(t).List(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CrawlJobs_PruneCrawlJobs", func(t *testing.T) {
		if err := closedRepo(t).PruneCrawlJobs(ctx, 10); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CreateSession", func(t *testing.T) {
		if err := closedRepo(t).CreateSession(ctx, "tok", time.Now().Add(time.Hour), "admin", ""); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ValidSession", func(t *testing.T) {
		if _, _, _, err := closedRepo(t).ValidSession(ctx, "tok"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("RevokeSession", func(t *testing.T) {
		if err := closedRepo(t).RevokeSession(ctx, "tok"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("ListUsers", func(t *testing.T) {
		if _, err := closedRepo(t).ListUsers(ctx); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("GetUser", func(t *testing.T) {
		if _, err := closedRepo(t).GetUser(ctx, "id"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("GetUserByUsername", func(t *testing.T) {
		if _, err := closedRepo(t).GetUserByUsername(ctx, "name"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("CreateUser", func(t *testing.T) {
		if err := closedRepo(t).CreateUser(ctx, domain.User{ID: "id", Username: "name"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("UpdateUser", func(t *testing.T) {
		if err := closedRepo(t).UpdateUser(ctx, domain.User{ID: "id", Username: "name"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("DeleteUser", func(t *testing.T) {
		if err := closedRepo(t).DeleteUser(ctx, "id"); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestHostOf_InvalidURLReturnsEmpty(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	// A control character makes url.Parse fail outright, exercising
	// hostOf's error branch (a merely relative/schemeless URL still parses
	// fine and just yields an empty Hostname(), which isn't this branch).
	doc := domain.Document{ID: "doc-1", URL: "http://\x7f", Title: "A", Text: "some text"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].Host != "" {
		t.Errorf("expected an unparseable URL to yield an empty host, got %+v", docs)
	}
}

func TestSampleEmbeddings_EmptyWhenNoDocuments(t *testing.T) {
	repo := newTestRepo(t)
	embeddings, err := repo.SampleEmbeddings(context.Background(), 10, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected no embeddings, got %v", embeddings)
	}
}

func TestSampleEmbeddings_ZeroLimitReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}
	embeddings, err := repo.SampleEmbeddings(ctx, 0, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected a non-positive limit to yield no embeddings, got %v", embeddings)
	}
}

func TestSampleEmbeddings_BoundedByLimitRegardlessOfCorpusSize(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("doc-%d", i)
		if err := repo.SaveDocument(ctx, domain.Document{ID: id, URL: "http://" + id, Title: "A", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{float32(i)}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving document %s: %v", id, err)
		}
	}
	embeddings, err := repo.SampleEmbeddings(ctx, 2, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 2 {
		t.Errorf("expected exactly 2 sampled embeddings out of 5 saved documents, got %d: %v", len(embeddings), embeddings)
	}
}

func TestEmbeddingsForDocs_OnlyReturnsRequestedIDs(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-2", URL: "http://b", Title: "B", Text: "other text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{2}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1", "does-not-exist"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 1 || embeddings["doc-1"].Vector[0] != 1 {
		t.Errorf("expected only doc-1's embedding, got %v", embeddings)
	}
}

func TestEmbeddingsForDocs_EmptyIDsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	embeddings, err := repo.EmbeddingsForDocs(context.Background(), nil, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected no embeddings for an empty ID list, got %v", embeddings)
	}
}

func TestDocumentsByIDs_EmptyIDsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	docs, err := repo.DocumentsByIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected no documents for an empty ID list, got %v", docs)
	}
}

func TestDocumentsByIDs_MissingIDsAreOmittedNotErrored(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.DocumentsByIDs(ctx, []string{"doc-1", "ghost-doc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("expected only the existing document, got %+v", docs)
	}
	if _, ok := docs["ghost-doc"]; ok {
		t.Errorf("expected a missing ID to be silently omitted, not present as a zero value")
	}
}

func TestSaveDocument_SetsHostFromURL(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "https://example.com/page", Title: "A", Text: "some text"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].Host != "example.com" {
		t.Errorf("expected host=example.com, got %+v", docs)
	}
	if docs[0].Version != 1 {
		t.Errorf("expected version=1 for a new document, got %d", docs[0].Version)
	}
	if docs[0].CrawledAt.IsZero() {
		t.Error("expected CrawledAt to be set")
	}
}

// TestSaveDocument_ComputesContentFingerprints proves SaveDocument always
// writes content_hash/simhash, regardless of whether the content-dedup
// feature is enabled -- see domain.ContentHash/SimHash64. Uses a direct
// sqlite connection (rather than newTestRepo, which also transparently
// runs this suite against Postgres) since verifying these columns needs
// raw SQL access this port doesn't otherwise expose -- the pure Go
// fingerprint functions themselves are dialect-independent, so this is
// only ever testing SaveDocument's own wiring, not dialect-specific SQL.
func TestSaveDocument_ComputesContentFingerprints(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testfingerprint%d?mode=memory&cache=shared", n)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	const text = "Some Freshly Crawled Text Content"
	doc := domain.Document{ID: "doc-1", URL: "https://example.com/page", Title: "A", Text: text}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: {1}}, 5, 1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var contentHash, simhash string
	if err := raw.QueryRow(`SELECT content_hash, simhash FROM documents WHERE id = 'doc-1'`).Scan(&contentHash, &simhash); err != nil {
		t.Fatalf("unexpected error querying fingerprints: %v", err)
	}
	if want := domain.ContentHash(text); contentHash != want {
		t.Errorf("expected content_hash=%q, got %q", want, contentHash)
	}
	if want := domain.EncodeSimHash64(domain.SimHash64(text)); simhash != want {
		t.Errorf("expected simhash=%q, got %q", want, simhash)
	}
}

func TestSaveDocument_UnchangedContentKeepsVersion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "same text"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on re-save: %v", err)
	}
	docs, _ := repo.ListDocuments(ctx, 10, "")
	if len(docs) != 1 || docs[0].Version != 1 {
		t.Errorf("expected version to stay at 1 for unchanged content, got %+v", docs)
	}
	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected no archived versions for unchanged content, got %+v", versions)
	}
}

func TestSaveDocument_ChangedContentArchivesPreviousVersion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	v1 := domain.Document{ID: "doc-1", URL: "http://a", Title: "Old Title", Text: "old text"}
	if err := repo.SaveDocument(ctx, v1, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	v2 := domain.Document{ID: "doc-1", URL: "http://a", Title: "New Title", Text: "new text"}
	if err := repo.SaveDocument(ctx, v2, map[string][]float32{domain.EmbeddingProviderHash: []float32{2}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on second save: %v", err)
	}
	v3 := domain.Document{ID: "doc-1", URL: "http://a", Title: "Newer Title", Text: "newer text"}
	if err := repo.SaveDocument(ctx, v3, map[string][]float32{domain.EmbeddingProviderHash: []float32{3}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on third save: %v", err)
	}

	docs, _ := repo.ListDocuments(ctx, 10, "")
	if len(docs) != 1 || docs[0].Version != 3 {
		t.Errorf("expected version=3 after two content changes, got %+v", docs)
	}

	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 archived versions, got %d: %+v", len(versions), versions)
	}
	if versions[0].Version != 2 || versions[0].Title != "New Title" {
		t.Errorf("expected most recent archived version first (v2, New Title), got %+v", versions[0])
	}
	if versions[1].Version != 1 || versions[1].Title != "Old Title" {
		t.Errorf("expected oldest archived version last (v1, Old Title), got %+v", versions[1])
	}
}

// TestSaveDocument_PrunesArchivedVersionsBeyondMaxVersions saves the same
// document five times with changing content and maxVersions=2 (meaning
// keep the current row plus 1 archived one), and confirms only the single
// most recent archived version survives -- the rest are pruned in the same
// write that archives each new one, not accumulated until some later
// sweep.
func TestSaveDocument_PrunesArchivedVersionsBeyondMaxVersions(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		doc := domain.Document{ID: "doc-1", URL: "http://a", Title: fmt.Sprintf("Title %d", i), Text: fmt.Sprintf("text version %d", i)}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{float32(i)}}, 2, 2); err != nil {
			t.Fatalf("unexpected error on save %d: %v", i, err)
		}
	}
	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly 1 archived version to survive (maxVersions=2 keeps current + 1), got %d: %+v", len(versions), versions)
	}
	if versions[0].Version != 4 {
		t.Errorf("expected the most recently superseded version (4) to survive, got %+v", versions[0])
	}
	docs, _ := repo.ListDocuments(ctx, 10, "")
	if len(docs) != 1 || docs[0].Version != 5 {
		t.Errorf("expected the current row to be at version 5, got %+v", docs)
	}
}

// TestSaveDocument_MaxVersionsOfOneKeepsNoArchivedHistory covers the
// keep=0 edge case (maxVersions<=1): every archived version is pruned,
// leaving only the current row.
func TestSaveDocument_MaxVersionsOfOneKeepsNoArchivedHistory(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		doc := domain.Document{ID: "doc-1", URL: "http://a", Title: fmt.Sprintf("Title %d", i), Text: fmt.Sprintf("text version %d", i)}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{float32(i)}}, 1, 2); err != nil {
			t.Fatalf("unexpected error on save %d: %v", i, err)
		}
	}
	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected no archived versions with maxVersions=1, got %d: %+v", len(versions), versions)
	}
}

func TestDocumentVersions_EmptyForNeverModifiedDocument(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "text"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	versions, err := repo.DocumentVersions(ctx, "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected no versions, got %+v", versions)
	}
}

func TestListDocuments_FiltersByHost(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a.example/1", Title: "A1", Text: "text"},
		{ID: "doc-2", URL: "http://a.example/2", Title: "A2", Text: "text"},
		{ID: "doc-3", URL: "http://b.example/1", Title: "B1", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	got, err := repo.ListDocuments(ctx, 10, "a.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 documents for a.example, got %d: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Host != "a.example" {
			t.Errorf("expected only a.example documents, got %+v", d)
		}
	}
}

// TestSearchDomains_EmptyQueryReturnsEveryDomain proves a blank q returns
// every domain (most-documents-first, still capped at limit) rather than
// nothing -- the admin Documents page fetches this broad batch once an
// admin starts searching, then matches q as a regex against it client-side.
func TestSearchDomains_EmptyQueryReturnsEveryDomain(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a.example/1", Title: "A", Text: "text"},
		{ID: "doc-2", URL: "http://b.example/1", Title: "B", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}
	domains, err := repo.SearchDomains(ctx, "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(domains) != 2 {
		t.Errorf("expected both domains returned for a blank query, got %+v", domains)
	}
}

// TestSearchDomains_EmptyQueryStillRespectsLimit proves the blank-query
// "return everything" path is still bounded by limit, not a genuinely
// unbounded fetch.
func TestSearchDomains_EmptyQueryStillRespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a.example/1", Title: "A", Text: "text"},
		{ID: "doc-2", URL: "http://b.example/1", Title: "B", Text: "text"},
		{ID: "doc-3", URL: "http://c.example/1", Title: "C", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}
	domains, err := repo.SearchDomains(ctx, "", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(domains) != 2 {
		t.Errorf("expected the blank-query result capped at limit=2, got %+v", domains)
	}
}

func TestSearchDomains_MatchesSubstringOrderedByCount(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://shop.example.com/1", Title: "A", Text: "text"},
		{ID: "doc-2", URL: "http://shop.example.com/2", Title: "B", Text: "text"},
		{ID: "doc-3", URL: "http://blog.example.com/1", Title: "C", Text: "text"},
		{ID: "doc-4", URL: "http://other.org/1", Title: "D", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	domains, err := repo.SearchDomains(ctx, "example", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(domains) != 2 {
		t.Fatalf("expected 2 matching domains, got %+v", domains)
	}
	if domains[0].Host != "shop.example.com" || domains[0].DocCount != 2 {
		t.Errorf("expected shop.example.com (2 docs) first, got %+v", domains[0])
	}
	if domains[1].Host != "blog.example.com" || domains[1].DocCount != 1 {
		t.Errorf("expected blog.example.com (1 doc) second, got %+v", domains[1])
	}
}

func TestDocumentsOverview_TopDomainsAndAgeBuckets(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a.example/1", Title: "A", Text: "text"},
		{ID: "doc-2", URL: "http://a.example/2", Title: "A2", Text: "text"},
		{ID: "doc-3", URL: "http://b.example/1", Title: "B", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	overview, err := repo.DocumentsOverview(ctx, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(overview.TopDomains) != 2 || overview.TopDomains[0].Host != "a.example" || overview.TopDomains[0].DocCount != 2 {
		t.Errorf("expected a.example (2 docs) first, got %+v", overview.TopDomains)
	}
	if len(overview.AgeBuckets) != 4 {
		t.Fatalf("expected 4 age buckets, got %d: %+v", len(overview.AgeBuckets), overview.AgeBuckets)
	}
	total := 0
	for _, b := range overview.AgeBuckets {
		total += b.Count
	}
	if total != 3 {
		t.Errorf("expected age buckets to account for all 3 documents, got total=%d (%+v)", total, overview.AgeBuckets)
	}
	if overview.AgeBuckets[0].Label != "last 24h" || overview.AgeBuckets[0].Count != 3 {
		t.Errorf("expected all 3 freshly-saved documents in the 'last 24h' bucket, got %+v", overview.AgeBuckets[0])
	}
	if overview.TotalDomains != 2 {
		t.Errorf("expected 2 distinct domains (a.example, b.example), got %d", overview.TotalDomains)
	}
	if len(overview.VersionCounts) != 1 || overview.VersionCounts[0].Version != 1 || overview.VersionCounts[0].Count != 3 {
		t.Errorf("expected all 3 freshly-saved documents at version 1, got %+v", overview.VersionCounts)
	}
	if len(overview.StoredVersionCounts) != 1 || overview.StoredVersionCounts[0].StoredVersions != 1 || overview.StoredVersionCounts[0].DocCount != 3 {
		t.Errorf("expected all 3 freshly-saved (never-changed) documents to have exactly 1 version stored, got %+v", overview.StoredVersionCounts)
	}
}

// TestDocumentsOverview_VersionCountsGroupsByVersion proves a re-crawled
// page (its version bumped by a second SaveDocument with changed text)
// lands in a separate version bucket from a page that's only ever been
// saved once.
func TestDocumentsOverview_VersionCountsGroupsByVersion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	once := domain.Document{ID: "doc-1", URL: "http://a.example/1", Title: "A", Text: "genuegend inhalt text fuer diese seite bitte danke"}
	if err := repo.SaveDocument(ctx, once, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recrawled := domain.Document{ID: "doc-2", URL: "http://b.example/1", Title: "B", Text: "genuegend inhalt text fuer diese andere seite bitte danke"}
	if err := repo.SaveDocument(ctx, recrawled, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recrawled.Text = "genuegend inhalt text fuer diese andere seite bitte danke, jetzt geaendert"
	if err := repo.SaveDocument(ctx, recrawled, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error re-saving: %v", err)
	}

	overview, err := repo.DocumentsOverview(ctx, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[int]int{}
	for _, v := range overview.VersionCounts {
		got[v.Version] = v.Count
	}
	if got[1] != 1 || got[2] != 1 {
		t.Errorf("expected version 1 count=1 (doc-1) and version 2 count=1 (doc-2, re-crawled once), got %+v", overview.VersionCounts)
	}

	gotStored := map[int]int{}
	for _, s := range overview.StoredVersionCounts {
		gotStored[s.StoredVersions] = s.DocCount
	}
	if gotStored[1] != 1 || gotStored[2] != 1 {
		t.Errorf("expected 1 document with 1 version stored (doc-1, never changed) and 1 with 2 stored (doc-2, changed once), got %+v", overview.StoredVersionCounts)
	}
}

// TestDocumentsOverview_StoredVersionCountsReflectsPruningNotVersionNumber
// proves StoredVersionCounts and VersionCounts diverge once
// MaxDocumentVersions has actually pruned something: a document changed 4
// times (version number 5) but saved with maxVersions=2 has only 2 rows of
// history retained (the current one plus 1 archived), not 5.
func TestDocumentsOverview_StoredVersionCountsReflectsPruningNotVersionNumber(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		doc := domain.Document{ID: "doc-1", URL: "http://a.example/1", Title: fmt.Sprintf("Title %d", i), Text: fmt.Sprintf("text version %d", i)}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{float32(i)}}, 2, 2); err != nil {
			t.Fatalf("unexpected error on save %d: %v", i, err)
		}
	}
	overview, err := repo.DocumentsOverview(ctx, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(overview.VersionCounts) != 1 || overview.VersionCounts[0].Version != 5 || overview.VersionCounts[0].Count != 1 {
		t.Errorf("expected the document's version number to reach 5 regardless of pruning, got %+v", overview.VersionCounts)
	}
	if len(overview.StoredVersionCounts) != 1 || overview.StoredVersionCounts[0].StoredVersions != 2 || overview.StoredVersionCounts[0].DocCount != 1 {
		t.Errorf("expected only 2 versions actually stored (maxVersions=2), got %+v", overview.StoredVersionCounts)
	}
}

func TestDocumentsOverview_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	overview, err := repo.DocumentsOverview(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(overview.TopDomains) != 0 {
		t.Errorf("expected no top domains for an empty corpus, got %+v", overview.TopDomains)
	}
	if overview.TotalDomains != 0 {
		t.Errorf("expected 0 distinct domains for an empty corpus, got %d", overview.TotalDomains)
	}
	if len(overview.VersionCounts) != 0 {
		t.Errorf("expected no version counts for an empty corpus, got %+v", overview.VersionCounts)
	}
	if len(overview.StoredVersionCounts) != 0 {
		t.Errorf("expected no stored version counts for an empty corpus, got %+v", overview.StoredVersionCounts)
	}
}

func TestMigrateDocumentColumns_BackfillsHostOnPreExistingRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigrate%d?mode=memory&cache=shared", n)

	// Simulate a database created before host/version/crawled_at existed.
	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
	                       VALUES ('doc-1', 'https://old.example/page', 'Old', 'old text', 10, ?)`,
		sqlrepo.EncodeEmbedding(nil)); err != nil {
		t.Fatalf("failed to insert legacy row: %v", err)
	}
	// Keep pre open for the rest of the test: an in-memory sqlite database
	// (even with cache=shared) is destroyed once every connection to it
	// closes, and repo below opens its own separate connection pool to
	// the same DSN.
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to migrate the legacy schema without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	docs, err := repo.ListDocuments(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].Host != "old.example" {
		t.Errorf("expected the pre-existing row's host to be backfilled to old.example, got %+v", docs)
	}
	if docs[0].Version != 1 {
		t.Errorf("expected a backfilled row to default to version 1, got %d", docs[0].Version)
	}
}

// TestMigrateDocumentColumns_BackfillsContentFingerprintsOnPreExistingRows
// proves a database created before content_hash/simhash existed gets both
// computed directly from the row's already-stored text, with no re-crawl
// needed -- exactly what lets application.RunContentDedupJob find
// duplicates among content crawled before this feature ever existed.
func TestMigrateDocumentColumns_BackfillsContentFingerprintsOnPreExistingRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigratefingerprint%d?mode=memory&cache=shared", n)

	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	const legacyText = "Some Pre-Existing Crawled Text"
	if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
	                       VALUES ('doc-1', 'https://old.example/page', 'Old', ?, 10, ?)`,
		legacyText, sqlrepo.EncodeEmbedding(nil)); err != nil {
		t.Fatalf("failed to insert legacy row: %v", err)
	}
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to migrate the legacy schema without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var contentHash, simhash string
	if err := pre.QueryRow(`SELECT content_hash, simhash FROM documents WHERE id = 'doc-1'`).Scan(&contentHash, &simhash); err != nil {
		t.Fatalf("unexpected error querying backfilled fingerprints: %v", err)
	}
	if want := domain.ContentHash(legacyText); contentHash != want {
		t.Errorf("expected content_hash backfilled to %q, got %q", want, contentHash)
	}
	if want := domain.EncodeSimHash64(domain.SimHash64(legacyText)); simhash != want {
		t.Errorf("expected simhash backfilled to %q, got %q", want, simhash)
	}
}

// TestMigrateDocumentAliasColumns_BackfillsHostOnPreExistingRows proves a
// document_aliases table created before its host column existed (this
// feature's very first release) gets it backfilled from each row's own
// alias_url -- needed for DocumentIDsByHost's alias-aware site: matching
// to find rows written before this column existed.
func TestMigrateDocumentAliasColumns_BackfillsHostOnPreExistingRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigratealiashost%d?mode=memory&cache=shared", n)

	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE document_aliases (
		alias_url TEXT PRIMARY KEY, canonical_id TEXT NOT NULL,
		reason TEXT NOT NULL, created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := pre.Exec(`INSERT INTO document_aliases (alias_url, canonical_id, reason, created_at)
	                       VALUES ('https://old.example/page', 'doc-1', 'canonical_tag', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("failed to insert legacy row: %v", err)
	}
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to migrate the legacy schema without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var host string
	if err := pre.QueryRow(`SELECT host FROM document_aliases WHERE alias_url = 'https://old.example/page'`).Scan(&host); err != nil {
		t.Fatalf("unexpected error querying backfilled host: %v", err)
	}
	if host != "old.example" {
		t.Errorf("expected host backfilled to old.example, got %q", host)
	}
}

// TestMigrateDocumentColumns_LegacyEmbeddingBlobIsNotAutoMigrated documents
// a deliberate choice: documents.embedding/norm_embedding are retired now
// that every provider's vector lives in document_embeddings instead (see
// SaveDocument), and there's no automatic migration from the old blob
// column into the new table -- migrating a database that predates
// document_embeddings must still succeed without error (New() below), but
// a pre-existing row's embedding is simply absent from EmbeddingsForDocs
// until that document is next crawled or explicitly recomputed (see
// application.RunEmbeddingRecomputeJob), the same "no automatic re-embed"
// precedent already documented on OperationalSettingsValues.EmbeddingProvider.
func TestMigrateDocumentColumns_LegacyEmbeddingBlobIsNotAutoMigrated(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigratenorm%d?mode=memory&cache=shared", n)

	// Simulate a database created before document_embeddings existed, with
	// a pre-existing row carrying a non-trivial embedding in the old
	// columns.
	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
	                       VALUES ('doc-1', 'https://old.example/page', 'Old', 'old text', 10, ?)`,
		sqlrepo.EncodeEmbedding([]float32{3, 4})); err != nil {
		t.Fatalf("failed to insert legacy row: %v", err)
	}
	// Keep pre open for the rest of the test: an in-memory sqlite database
	// (even with cache=shared) is destroyed once every connection to it
	// closes, and repo below opens its own separate connection pool to
	// the same DSN.
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to migrate the legacy schema without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	embeddings, err := repo.EmbeddingsForDocs(context.Background(), []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := embeddings["doc-1"]; ok {
		t.Errorf("expected the legacy row's embedding to be absent until recomputed, got %v", embeddings)
	}
}

func TestSaveDocument_ClassifiesOutboundLinksInternalVsExternal(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{
		ID: "doc-1", URL: "https://a.example/page", Title: "A", Text: "text",
		Links: []string{
			"https://a.example/other",
			"https://a.example/third",
			"https://b.example/elsewhere",
		},
	}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].InternalLinks != 2 {
		t.Errorf("expected 2 internal links, got %d", docs[0].InternalLinks)
	}
	if docs[0].ExternalLinks != 1 {
		t.Errorf("expected 1 external link, got %d", docs[0].ExternalLinks)
	}
}

func TestSaveDocument_SelfLinkIsExcludedFromLinkCounts(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{
		ID: "doc-1", URL: "https://a.example/page", Title: "A", Text: "text",
		Links: []string{"https://a.example/page", "https://a.example/page"},
	}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, _ := repo.ListDocuments(ctx, 10, "")
	if len(docs) != 1 || docs[0].InternalLinks != 0 || docs[0].ExternalLinks != 0 {
		t.Errorf("expected a self-link to be excluded entirely, got %+v", docs)
	}
}

func TestSaveDocument_ResavingReplacesLinks(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	first := domain.Document{
		ID: "doc-1", URL: "https://a.example/page", Title: "A", Text: "text one",
		Links: []string{"https://a.example/other", "https://b.example/x"},
	}
	if err := repo.SaveDocument(ctx, first, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	second := domain.Document{
		ID: "doc-1", URL: "https://a.example/page", Title: "A", Text: "text two",
		Links: []string{"https://b.example/x"},
	}
	if err := repo.SaveDocument(ctx, second, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error on re-save: %v", err)
	}
	docs, _ := repo.ListDocuments(ctx, 10, "")
	if len(docs) != 1 || docs[0].InternalLinks != 0 || docs[0].ExternalLinks != 1 {
		t.Errorf("expected re-saving to replace the old link set, got %+v", docs)
	}
}

func TestListDocuments_ComputesBacklinksFromOtherIndexedPages(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "text",
			Links: []string{"https://c.example/target"}},
		{ID: "doc-b", URL: "https://b.example/", Title: "B", Text: "text",
			Links: []string{"https://c.example/target"}},
		{ID: "doc-c", URL: "https://c.example/target", Title: "C", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	got, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID := map[string]domain.IndexedDocument{}
	for _, d := range got {
		byID[d.ID] = d
	}
	if byID["doc-c"].Backlinks != 2 {
		t.Errorf("expected doc-c to have 2 backlinks, got %d", byID["doc-c"].Backlinks)
	}
	if byID["doc-a"].Backlinks != 0 || byID["doc-b"].Backlinks != 0 {
		t.Errorf("expected doc-a/doc-b to have 0 backlinks, got a=%d b=%d", byID["doc-a"].Backlinks, byID["doc-b"].Backlinks)
	}
}

func TestDeleteDocument_RemovesItsOutboundLinks(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "text",
			Links: []string{"https://b.example/target"}},
		{ID: "doc-b", URL: "https://b.example/target", Title: "B", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if err := repo.DeleteDocument(ctx, "doc-a"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "doc-b" {
		t.Fatalf("expected only doc-b to remain, got %+v", got)
	}
	if got[0].Backlinks != 0 {
		t.Errorf("expected doc-b's backlinks to drop to 0 once doc-a (its only linker) is deleted, got %d", got[0].Backlinks)
	}
}

func TestGetSetting_NotFoundOnFreshDB(t *testing.T) {
	repo := newTestRepo(t)
	value, found, err := repo.GetSetting(context.Background(), "tuning")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected found=false on a fresh DB, got value %q", value)
	}
}

func TestSaveSetting_ThenGetSettingRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveSetting(ctx, "tuning", `{"alpha":0.9,"k1":2,"b":0.3}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	value, found, err := repo.GetSetting(ctx, "tuning")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after saving")
	}
	if value != `{"alpha":0.9,"k1":2,"b":0.3}` {
		t.Errorf("unexpected value: %q", value)
	}
}

func TestSaveSetting_UpsertOverwritesExistingValue(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveSetting(ctx, "operational", "first"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveSetting(ctx, "operational", "second"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	value, found, err := repo.GetSetting(ctx, "operational")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || value != "second" {
		t.Errorf("expected the second save to overwrite the first, got (%q, %v)", value, found)
	}
}

func TestSaveSetting_KeysAreIndependent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveSetting(ctx, "tuning", "tuning-value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveSetting(ctx, "overrides", "overrides-value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tuning, _, err := repo.GetSetting(ctx, "tuning")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	overrides, _, err := repo.GetSetting(ctx, "overrides")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tuning != "tuning-value" || overrides != "overrides-value" {
		t.Errorf("expected independent keys, got tuning=%q overrides=%q", tuning, overrides)
	}
}

func TestDocumentsByIDsSortedByCrawledAt_EmptyIDsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	docs, err := repo.DocumentsByIDsSortedByCrawledAt(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected no documents for an empty ID list, got %v", docs)
	}
}

func TestDocumentsByIDsSortedByCrawledAt_MissingIDsAreOmittedNotErrored(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, err := repo.DocumentsByIDsSortedByCrawledAt(ctx, []string{"doc-1", "ghost-doc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != "doc-1" {
		t.Errorf("expected only the existing document, got %+v", docs)
	}
}

// TestDocumentsByIDsSortedByCrawledAt_OrdersDescendingWithDeterministicTieBreak
// proves the ordering is pushed down into SQL (idx_documents_crawled_at)
// rather than left to an in-app sort: most-recently-crawled first, ties
// broken by id ascending.
func TestDocumentsByIDsSortedByCrawledAt_OrdersDescendingWithDeterministicTieBreak(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testsortedcrawled%d?mode=memory&cache=shared", n)

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := repo.SaveDocument(ctx, domain.Document{ID: id, URL: "http://" + id, Title: id, Text: "text " + id}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}

	set := func(id string, ts time.Time) {
		if _, err := raw.ExecContext(ctx, `UPDATE documents SET crawled_at = ? WHERE id = ?`, ts.Format(time.RFC3339Nano), id); err != nil {
			t.Fatalf("failed to set crawled_at for %s: %v", id, err)
		}
	}
	newest := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	oldest := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	set("a", middle)
	set("b", newest)
	set("c", oldest)
	set("d", middle) // ties with "a" -- tie-break must be id ascending: a before d

	docs, err := repo.DocumentsByIDsSortedByCrawledAt(ctx, []string{"a", "b", "c", "d", "ghost"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 4 {
		t.Fatalf("expected 4 documents (ghost omitted), got %d: %+v", len(docs), docs)
	}
	gotOrder := []string{docs[0].ID, docs[1].ID, docs[2].ID, docs[3].ID}
	wantOrder := []string{"b", "a", "d", "c"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("expected order (crawled_at desc, ties broken by id asc) %v, got %v", wantOrder, gotOrder)
			break
		}
	}
}

func TestDocumentIDsByHost_EmptyHostsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	ids, err := repo.DocumentIDsByHost(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected no ids for an empty host list, got %v", ids)
	}
}

// TestDocumentIDsByHost_MatchesExactAndSubdomainNotUnrelated guards the
// exact bug that shipped to production: a site: filter must find every
// document on the requested host(s) -- exact match or subdomain -- via a
// direct lookup, regardless of whether those documents also happen to be a
// BM25 hit or land in hybrid search's bounded semantic sample.
func TestDocumentIDsByHost_MatchesExactAndSubdomainNotUnrelated(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "exact", URL: "https://example.com/a", Title: "t", Text: "some text"},
		{ID: "subdomain", URL: "https://www.example.com/b", Title: "t", Text: "some text"},
		{ID: "unrelated", URL: "https://notexample.com/c", Title: "t", Text: "some text"},
		{ID: "other-site", URL: "https://other.test/d", Title: "t", Text: "some text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	ids, err := repo.DocumentIDsByHost(ctx, []string{"example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["exact"] || !got["subdomain"] {
		t.Errorf("expected exact and subdomain matches, got %v", ids)
	}
	if got["unrelated"] || got["other-site"] {
		t.Errorf("expected no unrelated hosts matched, got %v", ids)
	}
	if len(ids) != 2 {
		t.Errorf("expected exactly 2 matches, got %d: %v", len(ids), ids)
	}
}

// TestDocumentIDsByHost_FindsCanonicalThroughAliasedHost proves a merged-
// away (or www-folded) document is still findable via site: through
// whichever host it was actually aliased from, even though the surviving
// canonical document's own host differs entirely.
func TestDocumentIDsByHost_FindsCanonicalThroughAliasedHost(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-canonical", URL: "https://canonical.example/x", Title: "t", Text: "text"},
		map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://aliased.example/x", "doc-canonical", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids, err := repo.DocumentIDsByHost(ctx, []string{"aliased.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "doc-canonical" {
		t.Errorf("expected site:aliased.example to resolve to the canonical document, got %v", ids)
	}
}

// TestDocumentIDsByHost_DoesNotDuplicateWhenBothDirectAndAliasMatch proves
// a document matching by its own host AND, coincidentally, by an alias
// pointing at it, is still only reported once.
func TestDocumentIDsByHost_DoesNotDuplicateWhenBothDirectAndAliasMatch(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-canonical", URL: "https://example.com/x", Title: "t", Text: "text"},
		map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://example.com/y", "doc-canonical", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids, err := repo.DocumentIDsByHost(ctx, []string{"example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "doc-canonical" {
		t.Errorf("expected doc-canonical reported exactly once, got %v", ids)
	}
}

// TestResolveAliasHosts_ReturnsCanonicalDocumentsRealHost proves the
// hybrid_search_service site:/-site: expansion fix's underlying lookup:
// given an alias host, it resolves to the actual documents.host of the
// document that alias's content now lives under -- not the alias's own
// host, and not the canonical document's ID.
func TestResolveAliasHosts_ReturnsCanonicalDocumentsRealHost(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-canonical", URL: "https://canonical.example/x", Title: "t", Text: "text"},
		map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://aliased.example/x", "doc-canonical", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hosts, err := repo.ResolveAliasHosts(ctx, []string{"aliased.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "canonical.example" {
		t.Errorf("expected [canonical.example], got %v", hosts)
	}
}

// TestResolveAliasHosts_MatchesSubdomainOfRequestedHost proves the same
// exact-or-subdomain matching rule as hostMatchConditions/DocumentIDsByHost
// applies to the alias's own host, not just an exact match.
func TestResolveAliasHosts_MatchesSubdomainOfRequestedHost(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-canonical", URL: "https://canonical.example/x", Title: "t", Text: "text"},
		map[string][]float32{domain.EmbeddingProviderHash: {1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.RecordDocumentAlias(ctx, "https://old.aliased.example/x", "doc-canonical", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hosts, err := repo.ResolveAliasHosts(ctx, []string{"aliased.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "canonical.example" {
		t.Errorf("expected [canonical.example] via old.aliased.example being a subdomain of aliased.example, got %v", hosts)
	}
}

// TestResolveAliasHosts_NoMatchingAliasReturnsEmpty proves a host with no
// document_aliases row at all (the common case -- most site: filters name a
// document's own real host, never merged or aliased) resolves to nothing,
// not an error.
func TestResolveAliasHosts_NoMatchingAliasReturnsEmpty(t *testing.T) {
	repo := newTestRepo(t)
	hosts, err := repo.ResolveAliasHosts(context.Background(), []string{"never-aliased.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("expected no resolved hosts, got %v", hosts)
	}
}

// TestResolveAliasHosts_EmptyHostsReturnsEmptyWithoutQuerying mirrors
// TestHostsIndexed_EmptyHostsReturnsEmptyWithoutQuerying/
// DocumentIDsByHost's own nil-hosts short circuit.
func TestResolveAliasHosts_EmptyHostsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	hosts, err := repo.ResolveAliasHosts(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 0 {
		t.Errorf("expected no resolved hosts, got %v", hosts)
	}
}

func TestHostsIndexed_EmptyHostsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	result, err := repo.HostsIndexed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected an empty result for an empty host list, got %v", result)
	}
}

// TestHostsIndexed_MatchesExactAndSubdomainNotUnrelated mirrors
// TestDocumentIDsByHost_MatchesExactAndSubdomainNotUnrelated's matching
// rule (exact host or a subdomain of it), proving HostsIndexed reports
// true/false per requested host rather than per document.
func TestHostsIndexed_MatchesExactAndSubdomainNotUnrelated(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "exact", URL: "https://example.com/a", Title: "t", Text: "some text"},
		{ID: "subdomain", URL: "https://www.other.example/b", Title: "t", Text: "some text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	result, err := repo.HostsIndexed(ctx, []string{"example.com", "other.example", "never-crawled.example"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result["example.com"] {
		t.Errorf("expected example.com (exact match) reported indexed, got %v", result)
	}
	if !result["other.example"] {
		t.Errorf("expected other.example (subdomain match via www.other.example) reported indexed, got %v", result)
	}
	if result["never-crawled.example"] {
		t.Errorf("expected never-crawled.example not reported indexed, got %v", result)
	}
}

// TestHostsIndexed_TruncatesToMaxBatch proves an oversized host list is
// capped at maxHostsIndexedBatch rather than building an unbounded query --
// a host beyond the cap is silently absent from the result (not indexed),
// same "safety valve, not an error" tradeoff as maxDocumentIDsByHost.
func TestHostsIndexed_TruncatesToMaxBatch(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "d1", URL: "https://kept.example/a", Title: "t", Text: "some text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hosts := make([]string, 0, 501)
	hosts = append(hosts, "kept.example")
	for i := 0; i < 500; i++ {
		hosts = append(hosts, fmt.Sprintf("filler-%d.example", i))
	}
	// "kept.example" is truncated away (only the first 500 of 501 hosts are
	// queried), so it must NOT be reported indexed despite having a document.
	hosts = append(hosts[1:], hosts[0])

	result, err := repo.HostsIndexed(ctx, hosts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["kept.example"] {
		t.Error("expected the 501st host to be truncated away, not looked up")
	}
}

// TestEnsureCrawledAtIndex_CreatedOnFreshDatabase verifies idx_documents_crawled_at
// exists after a normal New() against a brand-new database.
func TestEnsureCrawledAtIndex_CreatedOnFreshDatabase(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testcrawledidxfresh%d?mode=memory&cache=shared", n)

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var name string
	err = raw.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_documents_crawled_at'`).Scan(&name)
	if err != nil {
		t.Errorf("expected idx_documents_crawled_at to exist on a freshly migrated database: %v", err)
	}
}

// TestPostingsDocIDIndex_CreatedOnFreshDatabase guards against a real
// production incident: postings' only index besides its own (term, doc_id)
// primary key was on term alone, so "DELETE FROM postings WHERE doc_id = ?"
// (every re-crawl of an existing page, in SaveDocument) couldn't seek an
// index directly and fell back to scanning the whole primary key -- 600ms+
// on a 9.7M-row production table, confirmed via EXPLAIN ANALYZE.
func TestPostingsDocIDIndex_CreatedOnFreshDatabase(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testpostingsdocidfresh%d?mode=memory&cache=shared", n)

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var name string
	err = raw.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_postings_doc_id'`).Scan(&name)
	if err != nil {
		t.Errorf("expected idx_postings_doc_id to exist on a freshly migrated database: %v", err)
	}
}

// TestEnsureCrawledAtIndex_BackstopCreatesIndexOnPreExistingDatabase mirrors
// the existing host-index/backfill migration tests: a database that already
// has every documents column (so migrateDocumentColumns has nothing to add)
// but predates this optimization's index must still get it, via the
// ensureCrawledAtIndex backstop that runs on every New() regardless of
// whether the table was just created or already existed.
func TestEnsureCrawledAtIndex_BackstopCreatesIndexOnPreExistingDatabase(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testcrawledidxlegacy%d?mode=memory&cache=shared", n)

	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL,
		norm_embedding REAL NOT NULL DEFAULT 0,
		host TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
		crawled_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to add the missing index without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	var name string
	if err := pre.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_documents_crawled_at'`).Scan(&name); err != nil {
		t.Errorf("expected idx_documents_crawled_at to be backstopped onto a pre-existing database, got: %v", err)
	}
}

func TestSaveDocument_NewDocumentGetsNeutralPageRankDefault(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	first := domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text one"}
	if err := repo.SaveDocument(ctx, first, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving first doc: %v", err)
	}
	second := domain.Document{ID: "doc-2", URL: "https://b.example/", Title: "B", Text: "text two"}
	if err := repo.SaveDocument(ctx, second, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error saving second doc: %v", err)
	}

	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID := map[string]domain.IndexedDocument{}
	for _, d := range docs {
		byID[d.ID] = d
	}
	// Neither document has any incoming/outgoing links at all, but each
	// should still land on a sane, strictly positive default -- never the
	// bare 0 the pagerank column itself defaults to.
	if byID["doc-1"].PageRank <= 0 {
		t.Errorf("expected doc-1 to have a positive default pagerank, got %v", byID["doc-1"].PageRank)
	}
	if byID["doc-2"].PageRank <= 0 {
		t.Errorf("expected doc-2 to have a positive default pagerank, got %v", byID["doc-2"].PageRank)
	}
}

func TestSaveDocument_ResavingUnchangedContentPreservesPageRank(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.42}); err != nil {
		t.Fatalf("unexpected error updating pagerank: %v", err)
	}
	// Re-save with identical content (a re-crawl that found nothing new):
	// unchanged-content path should preserve the pagerank set above, never
	// reset it back to a neutral default.
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error re-saving: %v", err)
	}
	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].PageRank != 0.42 {
		t.Errorf("expected pagerank 0.42 preserved across an unchanged-content re-save, got %+v", docs)
	}
}

func TestSaveDocument_ResavingChangedContentPreservesPageRank(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text one"}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.77}); err != nil {
		t.Fatalf("unexpected error updating pagerank: %v", err)
	}
	changed := domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text two, now different"}
	if err := repo.SaveDocument(ctx, changed, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error re-saving changed content: %v", err)
	}
	docs, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 || docs[0].Version != 2 {
		t.Fatalf("expected a new version to have been archived, got %+v", docs)
	}
	if docs[0].PageRank != 0.77 {
		t.Errorf("expected pagerank 0.77 preserved across a changed-content re-save, got %v", docs[0].PageRank)
	}
}

// TestRepository_LinkGraph verifies LinkGraph builds a doc-ID adjacency map
// by joining links.to_url against documents.url -- a link whose target was
// never crawled/indexed (no matching document row) is simply omitted,
// since it has no document ID to report.
func TestRepository_LinkGraph(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "text",
			Links: []string{"https://c.example/target", "https://nowhere.example/unindexed"}},
		{ID: "doc-b", URL: "https://b.example/", Title: "B", Text: "text",
			Links: []string{"https://c.example/target"}},
		{ID: "doc-c", URL: "https://c.example/target", Title: "C", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	graph, err := repo.LinkGraph(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(graph["doc-a"]) != 1 || graph["doc-a"][0] != "doc-c" {
		t.Errorf("expected doc-a -> [doc-c] (the unindexed link omitted), got %v", graph["doc-a"])
	}
	if len(graph["doc-b"]) != 1 || graph["doc-b"][0] != "doc-c" {
		t.Errorf("expected doc-b -> [doc-c], got %v", graph["doc-b"])
	}
	if _, ok := graph["doc-c"]; ok {
		t.Errorf("expected doc-c (no outbound links) to have no adjacency entry, got %v", graph["doc-c"])
	}
}

// TestRepository_LinkGraph_ResolvesLinksThroughAliases proves a link to a
// URL that's since become an alias (rather than its own indexed document)
// still contributes to the alias's canonical document's inbound link
// count -- otherwise a merge would silently erase that PageRank
// contribution the moment MergeDocuments runs.
func TestRepository_LinkGraph_ResolvesLinksThroughAliases(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-a", URL: "https://a.example/", Title: "A", Text: "text",
			Links: []string{"https://merged-away.example/"}},
		{ID: "doc-canonical", URL: "https://canonical.example/", Title: "C", Text: "text"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}
	if err := repo.RecordDocumentAlias(ctx, "https://merged-away.example/", "doc-canonical", domain.DocumentAliasReasonContentExact); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	graph, err := repo.LinkGraph(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(graph["doc-a"]) != 1 || graph["doc-a"][0] != "doc-canonical" {
		t.Errorf("expected doc-a's link to the aliased URL resolved to doc-canonical, got %v", graph["doc-a"])
	}
}

func TestRepository_LinkGraph_EmptyWhenNoLinks(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text"}, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graph, err := repo.LinkGraph(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(graph) != 0 {
		t.Errorf("expected an empty graph when nothing links to anything, got %v", graph)
	}
}

// TestRepository_UpdatePageRanks_RoundTrips verifies scores written via
// UpdatePageRanks come back through ListDocuments/EmbeddingsForDocs, and
// that a document ID left out of the batch keeps its prior value rather
// than being reset.
func TestRepository_UpdatePageRanks_RoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2"} {
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: id, Text: "text " + id}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}
	before, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID := map[string]domain.IndexedDocument{}
	for _, d := range before {
		byID[d.ID] = d
	}
	doc2Before := byID["doc-2"].PageRank

	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.9}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after, err := repo.ListDocuments(ctx, 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID = map[string]domain.IndexedDocument{}
	for _, d := range after {
		byID[d.ID] = d
	}
	if byID["doc-1"].PageRank != 0.9 {
		t.Errorf("expected doc-1's pagerank updated to 0.9, got %v", byID["doc-1"].PageRank)
	}
	if byID["doc-2"].PageRank != doc2Before {
		t.Errorf("expected doc-2 (left out of the batch) to keep its prior pagerank %v, got %v", doc2Before, byID["doc-2"].PageRank)
	}

	// EmbeddingsForDocs is the path hybrid search actually reads pagerank
	// through -- verify it reflects the same updated value.
	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"}, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embeddings["doc-1"].PageRank != 0.9 {
		t.Errorf("expected EmbeddingsForDocs to report the updated pagerank 0.9, got %v", embeddings["doc-1"].PageRank)
	}
}

func TestRepository_UpdatePageRanks_EmptyIsNoop(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.UpdatePageRanks(context.Background(), map[string]float64{}); err != nil {
		t.Errorf("expected an empty batch to be a no-op, got error: %v", err)
	}
}

// TestRepository_UpdatePageRanks_SpansMultipleBatches proves the chunking
// in UpdatePageRanks (pageRankUpdateBatchSize documents per UPDATE) doesn't
// drop or miscount any document at a batch boundary -- more documents than
// one batch holds, every one of them updated correctly.
func TestRepository_UpdatePageRanks_SpansMultipleBatches(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	const n = 407 // pageRankUpdateBatchSize is 200, so this spans 3 batches, the last one partial
	scores := make(map[string]float64, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc-%d", i)
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: id, Text: "text"}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
		scores[id] = float64(i) / float64(n)
	}

	if err := repo.UpdatePageRanks(ctx, scores); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ids := make([]string, 0, n)
	for id := range scores {
		ids = append(ids, id)
	}
	docs, err := repo.DocumentsByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	embeddings, err := repo.EmbeddingsForDocs(ctx, ids, domain.EmbeddingProviderHash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != n {
		t.Fatalf("expected all %d documents back, got %d", n, len(docs))
	}
	for id, want := range scores {
		if got := embeddings[id].PageRank; got != want {
			t.Errorf("expected %s's pagerank updated to %v, got %v", id, want, got)
		}
	}
}

func TestPageRankDistribution_EmptyCorpus(t *testing.T) {
	repo := newTestRepo(t)
	minRank, maxRank, avg, err := repo.PageRankDistribution(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if minRank != 0 || maxRank != 0 || avg != 0 {
		t.Errorf("expected min=max=avg=0 for an empty corpus, got min=%v max=%v avg=%v", minRank, maxRank, avg)
	}
}

func TestPageRankDistribution_ReflectsUpdatedScores(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2", "doc-3"} {
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: id, Text: "text " + id}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.1, "doc-2": 0.5, "doc-3": 0.9}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minRank, maxRank, avg, err := repo.PageRankDistribution(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if minRank != 0.1 {
		t.Errorf("expected min=0.1, got %v", minRank)
	}
	if maxRank != 0.9 {
		t.Errorf("expected max=0.9, got %v", maxRank)
	}
	wantAvg := (0.1 + 0.5 + 0.9) / 3
	if diff := avg - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("expected avg=%v, got %v", wantAvg, avg)
	}
}

func TestTableRowCounts_ReflectsSavedDocuments(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2"} {
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: id, Text: "shared term"}
		if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
			t.Fatalf("unexpected error saving %s: %v", id, err)
		}
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["documents"] != 2 {
		t.Errorf("expected 2 documents, got %d (%+v)", counts["documents"], counts)
	}
	if counts["postings"] == 0 {
		t.Errorf("expected postings rows for the saved documents' terms, got %+v", counts)
	}
	// Every table the schema creates should be present, even if empty --
	// the admin database diagnostics page shows all of them.
	for _, table := range []string{"documents", "postings", "document_versions", "document_embeddings", "links", "app_settings", "scheduled_crawls", "crawl_jobs", "crawl_job_pages", "sessions"} {
		if _, ok := counts[table]; !ok {
			t.Errorf("expected a row count entry for table %q, got %+v", table, counts)
		}
	}
}

// TestMigrateDocumentColumns_BackfillsPageRankOnPreExistingRows mirrors the
// existing norm_embedding backfill test: a database created before the
// pagerank column existed must have every pre-existing row backfilled to a
// neutral 1/N score, not left at the column's bare 0 default.
func TestMigrateDocumentColumns_BackfillsPageRankOnPreExistingRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigratepagerank%d?mode=memory&cache=shared", n)

	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding BLOB NOT NULL,
		norm_embedding REAL NOT NULL DEFAULT 0,
		host TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
		crawled_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	for _, id := range []string{"doc-1", "doc-2"} {
		if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
		                       VALUES (?, ?, 'Old', 'old text', 10, ?)`, id, "https://old.example/"+id, sqlrepo.EncodeEmbedding(nil)); err != nil {
			t.Fatalf("failed to insert legacy row %s: %v", id, err)
		}
	}
	t.Cleanup(func() { _ = pre.Close() })

	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("expected New to migrate the legacy schema without error, got: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	docs, err := repo.ListDocuments(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	for _, d := range docs {
		if d.PageRank != 0.5 {
			t.Errorf("expected pre-existing row %s backfilled to 1/2 = 0.5, got %v", d.ID, d.PageRank)
		}
	}
}

// TestSaveDocument_ChunkedInsertsAcrossBatchBoundary verifies SaveDocument's
// postings/links rewrite -- chunked into multi-row "INSERT ... VALUES
// (...),(...),..." statements bounded by an internal batch size -- inserts
// every single row correctly (no row dropped or duplicated at a chunk
// boundary) when the term/link count is large enough to span more than one
// chunk (currently 300 rows/chunk), and that a follow-up save with far
// fewer terms/links leaves exactly the new, smaller set behind rather than
// merging with whatever the previous save's chunks touched.
func TestSaveDocument_ChunkedInsertsAcrossBatchBoundary(t *testing.T) {
	ctx := context.Background()
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testdbchunk%d?mode=memory&cache=shared", n)
	repo, err := sqlrepo.New(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	defer repo.Close()

	const termCount = 350
	const linkCount = 350
	var text strings.Builder
	for i := 0; i < termCount; i++ {
		fmt.Fprintf(&text, "chunkterm%d ", i)
	}
	links := make([]string, linkCount)
	for i := 0; i < linkCount; i++ {
		links[i] = fmt.Sprintf("https://example.com/chunklink-%d", i)
	}
	doc := domain.Document{
		// Title left empty so the only tokens produced are the termCount
		// chunktermN ones in Text -- a non-empty title would itself
		// contribute an extra posting row and throw off the exact counts
		// this test checks.
		ID: "doc-chunk", URL: "https://example.com/doc-chunk",
		Text: text.String(), Links: links,
	}
	if err := repo.SaveDocument(ctx, doc, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("opening raw connection: %v", err)
	}
	defer raw.Close()

	var postingsCount int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM postings WHERE doc_id = ?`, doc.ID).Scan(&postingsCount); err != nil {
		t.Fatalf("counting postings: %v", err)
	}
	if postingsCount != termCount {
		t.Errorf("expected %d postings rows spanning multiple insert chunks, got %d", termCount, postingsCount)
	}

	var linksCount int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM links WHERE from_id = ?`, doc.ID).Scan(&linksCount); err != nil {
		t.Fatalf("counting links: %v", err)
	}
	if linksCount != linkCount {
		t.Errorf("expected %d links rows spanning multiple insert chunks, got %d", linkCount, linksCount)
	}

	// Spot-check specific terms straddling the chunk boundary (currently
	// 300 rows/chunk: index 299 is the last row of chunk 1, 300 the first
	// of chunk 2) actually made it in with the right frequency, not just
	// that the total count matches.
	for _, i := range []int{0, 299, 300, termCount - 1} {
		term := fmt.Sprintf("chunkterm%d", i)
		postings, err := repo.PostingsForTerm(ctx, term, 10)
		if err != nil {
			t.Fatalf("PostingsForTerm(%s): %v", term, err)
		}
		if len(postings) != 1 || postings[0].DocID != doc.ID || postings[0].TermFreq != 1 {
			t.Errorf("expected exactly one posting for %q with term_freq=1, got %+v", term, postings)
		}
	}

	// Re-saving with far fewer terms/links must leave exactly the new
	// counts behind, not a union with the previous (larger, multi-chunk)
	// save's rows.
	doc2 := domain.Document{
		ID: "doc-chunk", URL: "https://example.com/doc-chunk",
		Text: "onlyterm", Links: []string{"https://example.com/onlylink"},
	}
	if err := repo.SaveDocument(ctx, doc2, map[string][]float32{domain.EmbeddingProviderHash: []float32{1}}, 100, 2); err != nil {
		t.Fatalf("SaveDocument (second): %v", err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM postings WHERE doc_id = ?`, doc.ID).Scan(&postingsCount); err != nil {
		t.Fatalf("counting postings after re-save: %v", err)
	}
	if postingsCount != 1 {
		t.Errorf("expected 1 posting row after re-save, got %d", postingsCount)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM links WHERE from_id = ?`, doc.ID).Scan(&linksCount); err != nil {
		t.Fatalf("counting links after re-save: %v", err)
	}
	if linksCount != 1 {
		t.Errorf("expected 1 link row after re-save, got %d", linksCount)
	}
}
