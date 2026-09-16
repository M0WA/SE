package sqlrepo_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// uniqueSQLiteDSN returns a fresh in-memory SQLite DSN (cache=shared, so
// every connection opened against the same returned string shares the one
// underlying database for as long as any of them stays open) -- reusing
// the package's shared dsnCounter so it can never collide with a DSN
// newTestRepo mints elsewhere in this package's test run.
func uniqueSQLiteDSN(t *testing.T) string {
	t.Helper()
	n := atomic.AddInt64(&dsnCounter, 1)
	return fmt.Sprintf("file:testembeddingendpointmigration%d?mode=memory&cache=shared", n)
}

// seedLegacyOperationalSettings writes jsonBlob directly under the
// "operational" settings key via a raw connection, bypassing sqlrepo.New
// entirely -- simulating an old-version binary's app_settings row that
// already existed before this process ever started, rather than a value
// this same process wrote through its own (now-migration-guarded) code
// path. The raw connection is kept open for the rest of the test (via
// t.Cleanup) so dsn's in-memory database survives until every
// sqlrepo.New call against it is done.
func seedLegacyOperationalSettings(t *testing.T, dsn, jsonBlob string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS app_settings (setting_key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("failed to create app_settings table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO app_settings (setting_key, value, updated_at) VALUES (?, ?, ?)`,
		ports.SettingsKeyOperational, jsonBlob, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("failed to seed legacy operational settings: %v", err)
	}
}

func newEmbeddingEndpoint(id string) domain.EmbeddingHTTPEndpoint {
	return domain.EmbeddingHTTPEndpoint{
		ID: id, Name: "IONOS bge-m3", BaseURL: "https://openai.inference.de-txl.ionos.com/v1",
		APIKey: "sk-test", Model: "BAAI/bge-m3", Dimensions: 1024,
		RateLimitPerSecond: 5, Enabled: true, CreatedAt: time.Now().UTC(),
	}
}

func TestCreateEmbeddingEndpoint_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	e := newEmbeddingEndpoint("ionos")

	if err := repo.CreateEmbeddingEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListEmbeddingEndpoints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(got))
	}
	g := got[0]
	if g.ID != "ionos" || g.Name != "IONOS bge-m3" || g.BaseURL != e.BaseURL || g.APIKey != "sk-test" {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if g.Model != "BAAI/bge-m3" || g.Dimensions != 1024 || g.RateLimitPerSecond != 5 || !g.Enabled {
		t.Errorf("unexpected option round trip: %+v", g)
	}
	if g.CreatedAt.IsZero() {
		t.Errorf("expected CreatedAt to round trip, got %+v", g)
	}
}

func TestGetEmbeddingEndpoint_ReturnsMatchingEndpoint(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateEmbeddingEndpoint(ctx, newEmbeddingEndpoint("ionos")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetEmbeddingEndpoint(ctx, "ionos")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "ionos" || got.Dimensions != 1024 {
		t.Errorf("unexpected endpoint: %+v", got)
	}
}

func TestGetEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetEmbeddingEndpoint(context.Background(), "missing")
	if !errors.Is(err, ports.ErrEmbeddingEndpointNotFound) {
		t.Errorf("expected ErrEmbeddingEndpointNotFound, got %v", err)
	}
}

func TestListEmbeddingEndpoints_MultipleRoundTripIndependently(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	a := newEmbeddingEndpoint("a")
	a.Name, a.Dimensions = "Endpoint A", 768
	b := newEmbeddingEndpoint("b")
	b.Name, b.Dimensions, b.Enabled = "Endpoint B", 384, false
	if err := repo.CreateEmbeddingEndpoint(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateEmbeddingEndpoint(ctx, b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListEmbeddingEndpoints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(got))
	}
	byID := map[string]domain.EmbeddingHTTPEndpoint{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID["a"].Dimensions != 768 || !byID["a"].Enabled {
		t.Errorf("unexpected endpoint a: %+v", byID["a"])
	}
	if byID["b"].Dimensions != 384 || byID["b"].Enabled {
		t.Errorf("unexpected endpoint b: %+v", byID["b"])
	}
}

func TestUpdateEmbeddingEndpoint_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	e := newEmbeddingEndpoint("ionos")
	if err := repo.CreateEmbeddingEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	e.Name = "Renamed"
	e.BaseURL = "https://new.example/v1"
	e.APIKey = "sk-rotated"
	e.Model = "new-model"
	e.Dimensions = 512
	e.RateLimitPerSecond = 10
	e.Enabled = false
	if err := repo.UpdateEmbeddingEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetEmbeddingEndpoint(ctx, "ionos")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "Renamed" || got.BaseURL != "https://new.example/v1" || got.APIKey != "sk-rotated" ||
		got.Model != "new-model" || got.Dimensions != 512 || got.RateLimitPerSecond != 10 || got.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", got)
	}
}

func TestUpdateEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateEmbeddingEndpoint(context.Background(), newEmbeddingEndpoint("missing"))
	if !errors.Is(err, ports.ErrEmbeddingEndpointNotFound) {
		t.Errorf("expected ErrEmbeddingEndpointNotFound, got %v", err)
	}
}

func TestDeleteEmbeddingEndpoint_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateEmbeddingEndpoint(ctx, newEmbeddingEndpoint("ionos")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteEmbeddingEndpoint(ctx, "ionos"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListEmbeddingEndpoints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no endpoints after delete, got %+v", got)
	}
}

func TestDeleteEmbeddingEndpoint_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteEmbeddingEndpoint(context.Background(), "missing")
	if !errors.Is(err, ports.ErrEmbeddingEndpointNotFound) {
		t.Errorf("expected ErrEmbeddingEndpointNotFound, got %v", err)
	}
}

// reopenSQLiteTestRepo opens a second, independent *sqlrepo.Repository
// against the exact same in-memory SQLite database dsn already points at
// (relying on SQLite's cache=shared mode, kept alive by the first
// connection newTestRepo opened and t.Cleanup will eventually close) --
// simulating a process restart against the same on-disk database, which
// migrate() (and so migrateLegacyHTTPEmbeddingConfig) runs again on every
// call to sqlrepo.New. Dialect-agnostic logic like this migration doesn't
// need the package's Postgres-schema-isolating newTestRepo for this --
// each call there deliberately gets its own throwaway schema, so it can't
// simulate a second open against the same database the way this needs to.
func reopenSQLiteTestRepo(t *testing.T, dsn string) *sqlrepo.Repository {
	t.Helper()
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to reopen test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// TestMigrateLegacyHTTPEmbeddingConfig_CreatesEndpointFromPreExistingSettings
// is a real-upgrade regression test: an install that configured the old
// single-HTTP-endpoint feature (flat EmbeddingHTTPBaseURL/Model/APIKey/
// Dimensions/Enabled fields on OperationalSettingsValues, before those were
// replaced by this table) must not silently lose that live config across
// the upgrade -- see the migration's own doc comment for why this mirrors
// the PR #60 precedent (protect an existing live config across a breaking
// settings change).
func TestMigrateLegacyHTTPEmbeddingConfig_CreatesEndpointFromPreExistingSettings(t *testing.T) {
	// The legacy shape is simulated as a raw JSON blob with the old field
	// names -- domain.OperationalSettingsValues no longer has these fields
	// at all, so this can't be built via the struct.
	legacy := map[string]interface{}{
		"EmbeddingHTTPEnabled":        true,
		"EmbeddingHTTPBaseURL":        "https://openai.inference.de-txl.ionos.com/v1",
		"EmbeddingHTTPAPIKey":         "sk-legacy",
		"EmbeddingHTTPModel":          "BAAI/bge-m3",
		"EmbeddingHTTPDimensions":     1024,
		"EmbeddingRateLimitPerSecond": 5,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dsn := uniqueSQLiteDSN(t)
	ctx := context.Background()
	// Seed the legacy blob via a raw connection BEFORE the first
	// sqlrepo.New() call -- simulating an old-version binary's app_settings
	// row that already existed before this process ever started. The very
	// first sqlrepo.New() against dsn is where migrate() -- and so this
	// migration -- runs and finds it.
	seedLegacyOperationalSettings(t, dsn, string(data))

	repo := reopenSQLiteTestRepo(t, dsn)
	got, err := repo.ListEmbeddingEndpoints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one migrated endpoint, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.ID != "http" {
		t.Errorf("expected the migrated endpoint's ID to be \"http\" for continuity with pre-existing document_embeddings rows, got %q", e.ID)
	}
	if e.BaseURL != "https://openai.inference.de-txl.ionos.com/v1" || e.APIKey != "sk-legacy" ||
		e.Model != "BAAI/bge-m3" || e.Dimensions != 1024 || e.RateLimitPerSecond != 5 || !e.Enabled {
		t.Errorf("expected every legacy field carried over, got %+v", e)
	}
}

// TestMigrateLegacyHTTPEmbeddingConfig_SkipsWhenNoLegacyConfig proves a
// genuinely fresh install (no legacy blob at all) doesn't fabricate an
// endpoint out of nothing.
func TestMigrateLegacyHTTPEmbeddingConfig_SkipsWhenNoLegacyConfig(t *testing.T) {
	repo := newTestRepo(t)
	got, err := repo.ListEmbeddingEndpoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no endpoints for a fresh install, got %+v", got)
	}
}

// TestMigrateLegacyHTTPEmbeddingConfig_NeverRunsTwice proves the migration
// is guarded to run at most once: an admin who explicitly deletes the
// migrated "http" endpoint afterward must never have it resurrected by a
// later process restart.
func TestMigrateLegacyHTTPEmbeddingConfig_NeverRunsTwice(t *testing.T) {
	legacy := map[string]interface{}{
		"EmbeddingHTTPEnabled":    true,
		"EmbeddingHTTPBaseURL":    "https://example.com/v1",
		"EmbeddingHTTPModel":      "m",
		"EmbeddingHTTPDimensions": 4,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dsn := uniqueSQLiteDSN(t)
	ctx := context.Background()
	// Seed the legacy blob before the first sqlrepo.New() call, same as
	// above -- this first open is where the endpoint actually gets
	// migrated-created.
	seedLegacyOperationalSettings(t, dsn, string(data))
	repo := reopenSQLiteTestRepo(t, dsn)
	if err := repo.DeleteEmbeddingEndpoint(ctx, "http"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	repo2 := reopenSQLiteTestRepo(t, dsn)
	got, err := repo2.ListEmbeddingEndpoints(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected the deleted migrated endpoint to stay deleted across a later restart, got %+v", got)
	}
}
