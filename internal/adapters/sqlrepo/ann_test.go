package sqlrepo_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
)

// TestEnableANN_NonPostgresIsNoop verifies ANN stays permanently
// unavailable on SQLite (and, by the same dialect-name check inside
// EnableANN, MySQL) -- the default local/dev/CI test path must never
// attempt any pgvector-specific DDL at all. Deliberately always SQLite
// (sqlrepo.New with "sqlite" directly, not newTestRepo, which -- like the
// rest of this package's tests -- switches to Postgres whenever
// TEST_POSTGRES_DSN is set) since this test is specifically about the
// non-Postgres dialect branch.
func TestEnableANN_NonPostgresIsNoop(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testannnoop%d?mode=memory&cache=shared", n)
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	repo.EnableANN(context.Background(), 128)
	if repo.ANNAvailable() {
		t.Error("expected ANN to stay unavailable on SQLite")
	}
	matches, ok, err := repo.TopSemanticMatches(context.Background(), []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false when ANN was never enabled")
	}
	if matches != nil {
		t.Errorf("expected a nil match map when ANN is unavailable, got %+v", matches)
	}
}

// TestEnableANN_NonFatalWhenCreateExtensionFails proves EnableANN's core
// robustness requirement without needing a real Postgres server missing
// the pgvector extension: NewWithDB(db, "postgres") forces the dialect to
// Postgres (so EnableANN doesn't bail out on the dialect check) while the
// underlying connection is actually SQLite (mirroring
// TestConfigurePool_NonSQLiteAppliesGivenValues's identical trick) --
// "CREATE EXTENSION IF NOT EXISTS vector" against a SQLite connection
// fails with a real, unclassified error, exactly the shape of failure a
// Postgres server without pgvector installed would produce. EnableANN must
// not panic, must not return an error (it has no return value to return
// one with), and must leave ANNAvailable() false -- confirming the
// extension-enable failure is non-fatal and permanently falls back rather
// than crashing or half-applying the migration.
func TestEnableANN_NonFatalWhenCreateExtensionFails(t *testing.T) {
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:testannfail%d?mode=memory&cache=shared", n)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := sqlrepo.NewWithDB(db, "postgres")
	repo.EnableANN(context.Background(), 128) // must not panic

	if repo.ANNAvailable() {
		t.Error("expected ANN to remain unavailable when CREATE EXTENSION fails")
	}
	// The permanent-fallback contract: a subsequent TopSemanticMatches call
	// must report unavailable too, never attempting (and failing on) the
	// pgvector-specific query against a database that was never actually
	// migrated for it.
	_, ok, err := repo.TopSemanticMatches(context.Background(), []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false after a failed EnableANN")
	}
}

// TestEnableANN_ZeroOrNegativeDimsIsNoop guards against ever attempting
// pgvector DDL with a nonsensical column width.
func TestEnableANN_ZeroOrNegativeDimsIsNoop(t *testing.T) {
	repo := newTestRepo(t)
	repo.EnableANN(context.Background(), 0)
	repo.EnableANN(context.Background(), -1)
	if repo.ANNAvailable() {
		t.Error("expected ANN to stay unavailable for a non-positive dims value")
	}
}

// requirePostgresANN skips the calling test unless TEST_POSTGRES_DSN is set
// AND the server's pgvector extension is actually usable -- the CI image
// is expected to have it (see .github/workflows/ci.yml), but a local run
// against a plain postgres:16 (say) must degrade gracefully rather than
// failing the suite.
func requirePostgresANN(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	if os.Getenv(testPostgresDSNEnv) == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping pgvector ANN test")
	}
	repo := newTestRepo(t)
	repo.EnableANN(context.Background(), 2)
	if !repo.ANNAvailable() {
		t.Skip("pgvector extension not available on this Postgres server; skipping ANN test")
	}
	return repo
}

// TestEnableANN_SucceedsAgainstRealPostgres exercises the full migration
// (extension, column, HNSW index) against a real Postgres+pgvector server.
func TestEnableANN_SucceedsAgainstRealPostgres(t *testing.T) {
	repo := requirePostgresANN(t)
	if !repo.ANNAvailable() {
		t.Fatal("expected ANN to be available after a successful EnableANN")
	}
}

// TestEnableANN_BackfillsVectorColumnForPreExistingDocuments guards against
// a real production gap: a document saved before ANN was ever enabled for
// this process must not be silently excluded from every future ANN query
// forever. Save a document against a repo with ANN not yet enabled (so
// embedding_vector stays NULL, exactly like every document that existed
// before this feature shipped), then enable ANN and confirm
// TopSemanticMatches finds it -- proving EnableANN backfills the column
// for pre-existing rows rather than only ever populating it going forward
// from SaveDocument.
func TestEnableANN_BackfillsVectorColumnForPreExistingDocuments(t *testing.T) {
	if os.Getenv(testPostgresDSNEnv) == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping pgvector ANN test")
	}
	repo := newTestRepo(t)
	ctx := context.Background()

	// Saved before EnableANN is ever called -- embedding_vector must stay
	// NULL at this point, the same as every document crawled before this
	// process ever turned ANN on.
	doc := domain.Document{ID: "pre-existing", URL: "https://example.com/pre-existing", Title: "pre-existing", Text: "pre-existing"}
	if err := repo.SaveDocument(ctx, doc, []float32{1, 0}); err != nil {
		t.Fatalf("saving pre-existing document: %v", err)
	}

	repo.EnableANN(ctx, 2)
	if !repo.ANNAvailable() {
		t.Skip("pgvector extension not available on this Postgres server; skipping ANN test")
	}

	matches, ok, err := repo.TopSemanticMatches(ctx, []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true once ANN is available")
	}
	if _, found := matches["pre-existing"]; !found {
		t.Errorf("expected the pre-existing document to be backfilled and found via ANN, got %+v", matches)
	}
}

// TestTopSemanticMatches_ReturnsNearestNeighborsInSaneOrder verifies the
// actual pgvector ORDER BY ... <=> $1 LIMIT $2 query against a small
// synthetic embedding set: of three 2-D unit vectors, the one identical to
// the query direction must rank first, and the one orthogonal to it must
// rank last.
func TestTopSemanticMatches_ReturnsNearestNeighborsInSaneOrder(t *testing.T) {
	repo := requirePostgresANN(t)
	ctx := context.Background()

	docs := []struct {
		id  string
		vec []float32
	}{
		{"same-direction", []float32{1, 0}},
		{"close-direction", []float32{0.9, 0.1}},
		{"orthogonal", []float32{0, 1}},
	}
	for _, d := range docs {
		doc := domain.Document{ID: d.id, URL: "https://example.com/" + d.id, Title: d.id, Text: d.id}
		if err := repo.SaveDocument(ctx, doc, d.vec); err != nil {
			t.Fatalf("saving document %s: %v", d.id, err)
		}
	}

	matches, ok, err := repo.TopSemanticMatches(ctx, []float32{1, 0}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true once ANN is available")
	}
	if len(matches) != 3 {
		t.Fatalf("expected all 3 saved documents back, got %d: %+v", len(matches), matches)
	}
	// Order isn't reported by TopSemanticMatches itself (it returns a map),
	// but cosine similarity computed from the returned vectors must rank
	// same-direction highest and orthogonal lowest -- proving the ANN path
	// actually finds a sane semantic ordering, not an arbitrary one.
	sim := func(id string) float64 {
		return domain.CosineSimilarity([]float32{1, 0}, matches[id].Vector)
	}
	if sim("same-direction") <= sim("close-direction") || sim("close-direction") <= sim("orthogonal") {
		t.Errorf("expected same-direction > close-direction > orthogonal by cosine similarity, got %v, %v, %v",
			sim("same-direction"), sim("close-direction"), sim("orthogonal"))
	}
}

// TestTopSemanticMatches_LimitBoundsResultCount verifies the LIMIT clause
// is actually applied, not just a hint.
func TestTopSemanticMatches_LimitBoundsResultCount(t *testing.T) {
	repo := requirePostgresANN(t)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		doc := domain.Document{ID: id, URL: "https://example.com/" + id, Title: id, Text: id}
		if err := repo.SaveDocument(ctx, doc, []float32{1, 0}); err != nil {
			t.Fatalf("saving document %s: %v", id, err)
		}
	}

	matches, ok, err := repo.TopSemanticMatches(ctx, []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true once ANN is available")
	}
	if len(matches) != 2 {
		t.Errorf("expected exactly 2 matches (LIMIT 2), got %d", len(matches))
	}
}

// TestTopSemanticMatches_NonPositiveLimitReturnsUnavailableWithoutQuerying
// mirrors SampleEmbeddings' own non-positive-limit contract.
func TestTopSemanticMatches_NonPositiveLimitReturnsUnavailableWithoutQuerying(t *testing.T) {
	repo := requirePostgresANN(t)
	matches, ok, err := repo.TopSemanticMatches(context.Background(), []float32{1, 0}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || matches != nil {
		t.Errorf("expected ok=false and a nil map for a non-positive limit, got ok=%v matches=%+v", ok, matches)
	}
}

// TestSaveDocument_PopulatesVectorColumnWhenANNAvailable verifies
// SaveDocument writes the pgvector column (not just the JSON embedding
// column) once ANN is available for this process -- otherwise
// TopSemanticMatches' "WHERE embedding_vector IS NOT NULL" filter would
// silently exclude every document.
func TestSaveDocument_PopulatesVectorColumnWhenANNAvailable(t *testing.T) {
	repo := requirePostgresANN(t)
	ctx := context.Background()
	doc := domain.Document{ID: "doc-1", URL: "https://example.com/doc-1", Title: "Doc", Text: "hello world"}
	if err := repo.SaveDocument(ctx, doc, []float32{1, 0}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	matches, ok, err := repo.TopSemanticMatches(ctx, []float32{1, 0}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true once ANN is available")
	}
	if _, found := matches["doc-1"]; !found {
		t.Errorf("expected doc-1's pgvector column to be populated and found via TopSemanticMatches, got %+v", matches)
	}
}
