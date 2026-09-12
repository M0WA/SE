package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

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
func newTestRepo(t *testing.T) *sqlrepo.Repository {
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
func newPostgresTestRepo(t *testing.T, baseDSN string) *sqlrepo.Repository {
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
	return repo
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
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL
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

	if err := repo.SaveDocument(ctx, doc, embedding); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}

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

	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"})
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

	sampled, err := repo.SampleEmbeddings(ctx, 10)
	if err != nil {
		t.Fatalf("unexpected error sampling embeddings: %v", err)
	}
	if len(sampled["doc-1"].Vector) != 3 || sampled["doc-1"].Vector[0] != 0.1 {
		t.Errorf("expected saved embedding back from sample, got %v", sampled["doc-1"])
	}
	if got := sampled["doc-1"].Norm; got < wantNorm-1e-9 || got > wantNorm+1e-9 {
		t.Errorf("expected precomputed norm %v from sample, got %v", wantNorm, got)
	}

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

	postings, err := repo.PostingsForTerm(ctx, "cats")
	if err != nil {
		t.Fatalf("unexpected error querying postings: %v", err)
	}
	// "cats" appears twice: once in the title, once in the text (SaveDocument
	// tokenizes title+text together).
	if len(postings) != 1 || postings[0].DocID != "doc-1" || postings[0].TermFreq != 2 {
		t.Errorf("expected one posting for 'cats' with freq 2, got %+v", postings)
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

func TestSaveDocument_UpsertReplacesPostings(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	first := domain.Document{ID: "doc-1", URL: "http://a", Title: "Cats", Text: "cats everywhere"}
	if err := repo.SaveDocument(ctx, first, []float32{1}); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}

	second := domain.Document{ID: "doc-1", URL: "http://a", Title: "Dogs", Text: "dogs everywhere"}
	if err := repo.SaveDocument(ctx, second, []float32{2}); err != nil {
		t.Fatalf("unexpected error on upsert save: %v", err)
	}

	catsPostings, err := repo.PostingsForTerm(ctx, "cats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(catsPostings) != 0 {
		t.Errorf("expected the old 'cats' posting to be gone after upsert, got %+v", catsPostings)
	}

	dogsPostings, err := repo.PostingsForTerm(ctx, "dogs")
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
	postings, err := repo.PostingsForTerm(context.Background(), "nonexistent")
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	postings, err := repo.PostingsForTerm(ctx, "shared")
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
	vocabSize, topTerms, err := repo.VocabularyStats(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vocabSize != 0 || topTerms != nil {
		t.Errorf("expected an empty vocabulary, got (%d, %+v)", vocabSize, topTerms)
	}
}

func TestVocabularyStats_ReportsSizeAndTopTermsByDocFreq(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common rare"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared common common"},
		{ID: "doc-3", URL: "http://c", Title: "C", Text: "shared unique"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	vocabSize, topTerms, err := repo.VocabularyStats(ctx, 2, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// distinct terms across all three docs: shared, common, rare, unique.
	if vocabSize != 4 {
		t.Errorf("expected vocabulary size 4, got %d", vocabSize)
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

// TestVocabularyStats_SearchFiltersTopTermsButNotVocabSize verifies the
// search-filtered topN listing only includes terms containing the search
// string, while vocabSize keeps reporting the whole corpus's distinct-term
// count regardless of the filter.
func TestVocabularyStats_SearchFiltersTopTermsButNotVocabSize(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	docs := []domain.Document{
		{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common rare"},
		{ID: "doc-2", URL: "http://b", Title: "B", Text: "shared common common"},
		{ID: "doc-3", URL: "http://c", Title: "C", Text: "shared unique"},
	}
	for _, d := range docs {
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
			t.Fatalf("unexpected error saving %s: %v", d.ID, err)
		}
	}

	vocabSize, topTerms, err := repo.VocabularyStats(ctx, 10, "rare")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vocabSize != 4 {
		t.Errorf("expected vocabSize to stay at the whole corpus's 4 regardless of the filter, got %d", vocabSize)
	}
	if len(topTerms) != 1 || topTerms[0].Term != "rare" {
		t.Errorf("expected only 'rare' to match search %q, got %+v", "rare", topTerms)
	}
}

func TestVocabularyStats_SearchWithNoMatchesReturnsEmptyTopTerms(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "shared common"}, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving doc: %v", err)
	}

	_, topTerms, err := repo.VocabularyStats(ctx, 10, "zzz-no-such-term")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	if err := repo.DeleteDocument(ctx, "doc-1"); err != nil {
		t.Fatalf("unexpected error deleting: %v", err)
	}

	if docs, err := repo.DocumentsByIDs(ctx, []string{"doc-1"}); err != nil || len(docs) != 0 {
		t.Errorf("expected the document to be gone after delete, got docs=%+v err=%v", docs, err)
	}
	postings, err := repo.PostingsForTerm(ctx, "cats")
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

func TestListDocuments_RespectsLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{"doc-1", "doc-2", "doc-3"} {
		doc := domain.Document{ID: id, URL: "http://" + id, Title: id, Text: "content for " + id}
		if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
		if _, err := closedRepo(t).EmbeddingsForDocs(ctx, []string{"doc-1"}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("SampleEmbeddings", func(t *testing.T) {
		if _, err := closedRepo(t).SampleEmbeddings(ctx, 10); err == nil {
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
		if _, _, err := closedRepo(t).VocabularyStats(ctx, 10, ""); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PostingsForTerm", func(t *testing.T) {
		if _, err := closedRepo(t).PostingsForTerm(ctx, "term"); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("PostingsForTerms", func(t *testing.T) {
		if _, err := closedRepo(t).PostingsForTerms(ctx, []string{"term"}); err == nil {
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
		if err := closedRepo(t).SaveDocument(ctx, doc, []float32{1}); err == nil {
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
		if err := closedRepo(t).MarkScheduledCrawlRun(ctx, "sched-1", time.Now(), time.Now()); err == nil {
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
}

func TestHostOf_InvalidURLReturnsEmpty(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	// A control character makes url.Parse fail outright, exercising
	// hostOf's error branch (a merely relative/schemeless URL still parses
	// fine and just yields an empty Hostname(), which isn't this branch).
	doc := domain.Document{ID: "doc-1", URL: "http://\x7f", Title: "A", Text: "some text"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	embeddings, err := repo.SampleEmbeddings(context.Background(), 10)
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
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving document: %v", err)
	}
	embeddings, err := repo.SampleEmbeddings(ctx, 0)
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
		if err := repo.SaveDocument(ctx, domain.Document{ID: id, URL: "http://" + id, Title: "A", Text: "some text"}, []float32{float32(i)}); err != nil {
			t.Fatalf("unexpected error saving document %s: %v", id, err)
		}
	}
	embeddings, err := repo.SampleEmbeddings(ctx, 2)
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
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-2", URL: "http://b", Title: "B", Text: "other text"}, []float32{2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1", "does-not-exist"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 1 || embeddings["doc-1"].Vector[0] != 1 {
		t.Errorf("expected only doc-1's embedding, got %v", embeddings)
	}
}

func TestEmbeddingsForDocs_EmptyIDsReturnsEmptyWithoutQuerying(t *testing.T) {
	repo := newTestRepo(t)
	embeddings, err := repo.EmbeddingsForDocs(context.Background(), nil)
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
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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

func TestSaveDocument_UnchangedContentKeepsVersion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "same text"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, v1, []float32{1}); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	v2 := domain.Document{ID: "doc-1", URL: "http://a", Title: "New Title", Text: "new text"}
	if err := repo.SaveDocument(ctx, v2, []float32{2}); err != nil {
		t.Fatalf("unexpected error on second save: %v", err)
	}
	v3 := domain.Document{ID: "doc-1", URL: "http://a", Title: "Newer Title", Text: "newer text"}
	if err := repo.SaveDocument(ctx, v3, []float32{3}); err != nil {
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

func TestDocumentVersions_EmptyForNeverModifiedDocument(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "text"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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

func TestSearchDomains_EmptyQueryReturnsNothing(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "http://a.example/1", Title: "A", Text: "text"}
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	domains, err := repo.SearchDomains(ctx, "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if domains != nil {
		t.Errorf("expected an empty query to match nothing, got %+v", domains)
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
	                       VALUES ('doc-1', 'https://old.example/page', 'Old', 'old text', 10, '[]')`); err != nil {
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

func TestMigrateDocumentColumns_BackfillsNormEmbeddingOnPreExistingRows(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testmigratenorm%d?mode=memory&cache=shared", n)

	// Simulate a database created before norm_embedding existed, with a
	// pre-existing row carrying a non-trivial embedding.
	pre, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open pre-migration DB: %v", err)
	}
	if _, err := pre.Exec(`CREATE TABLE documents (
		id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT, text TEXT,
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
	                       VALUES ('doc-1', 'https://old.example/page', 'Old', 'old text', 10, '[3,4]')`); err != nil {
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

	embeddings, err := repo.EmbeddingsForDocs(context.Background(), []string{"doc-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := embeddings["doc-1"]
	if !ok {
		t.Fatalf("expected the pre-existing row back, got %v", embeddings)
	}
	if got.Norm < 4.999 || got.Norm > 5.001 {
		t.Errorf("expected the pre-existing row's norm_embedding to be backfilled to 5 (norm of [3,4]), got %v", got.Norm)
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, first, []float32{1}); err != nil {
		t.Fatalf("unexpected error on first save: %v", err)
	}
	second := domain.Document{
		ID: "doc-1", URL: "https://a.example/page", Title: "A", Text: "text two",
		Links: []string{"https://b.example/x"},
	}
	if err := repo.SaveDocument(ctx, second, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "http://a", Title: "A", Text: "some text"}, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, domain.Document{ID: id, URL: "http://" + id, Title: id, Text: "text " + id}, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL,
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
	if err := repo.SaveDocument(ctx, first, []float32{1}); err != nil {
		t.Fatalf("unexpected error saving first doc: %v", err)
	}
	second := domain.Document{ID: "doc-2", URL: "https://b.example/", Title: "B", Text: "text two"}
	if err := repo.SaveDocument(ctx, second, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.42}); err != nil {
		t.Fatalf("unexpected error updating pagerank: %v", err)
	}
	// Re-save with identical content (a re-crawl that found nothing new):
	// unchanged-content path should preserve the pagerank set above, never
	// reset it back to a neutral default.
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.UpdatePageRanks(ctx, map[string]float64{"doc-1": 0.77}); err != nil {
		t.Fatalf("unexpected error updating pagerank: %v", err)
	}
	changed := domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text two, now different"}
	if err := repo.SaveDocument(ctx, changed, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, d, []float32{1}); err != nil {
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

func TestRepository_LinkGraph_EmptyWhenNoLinks(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SaveDocument(ctx, domain.Document{ID: "doc-1", URL: "https://a.example/", Title: "A", Text: "text"}, []float32{1}); err != nil {
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
		if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	embeddings, err := repo.EmbeddingsForDocs(ctx, []string{"doc-1"})
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
		if err := repo.SaveDocument(ctx, doc, []float32{1}); err != nil {
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
	embeddings, err := repo.EmbeddingsForDocs(ctx, ids)
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
		doc_length INTEGER NOT NULL, embedding TEXT NOT NULL,
		norm_embedding REAL NOT NULL DEFAULT 0,
		host TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1,
		crawled_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	for _, id := range []string{"doc-1", "doc-2"} {
		if _, err := pre.Exec(`INSERT INTO documents (id, url, title, text, doc_length, embedding)
		                       VALUES (?, ?, 'Old', 'old text', 10, '[]')`, id, "https://old.example/"+id); err != nil {
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
