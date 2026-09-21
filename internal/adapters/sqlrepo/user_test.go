package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newUser(id, username string) domain.User {
	now := time.Now().UTC()
	return domain.User{
		ID: id, Username: username, PasswordHash: "bcrypt-hash-placeholder",
		CustomPrompt: "", CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreateUser_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	u := newUser("user1", "alice")

	if err := repo.CreateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 user, got %d", len(got))
	}
	g := got[0]
	if g.ID != "user1" || g.Username != "alice" || g.PasswordHash != u.PasswordHash {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		t.Errorf("expected timestamps to round trip, got %+v", g)
	}
}

// TestCreateUser_CustomPromptRoundTrips proves a non-empty CustomPrompt
// survives a Create -> Get round trip, not just the default empty string
// every other test in this file implicitly exercises via newUser.
func TestCreateUser_CustomPromptRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	u := newUser("user1", "alice")
	u.CustomPrompt = "Always answer in the style of a pirate."
	if err := repo.CreateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CustomPrompt != u.CustomPrompt {
		t.Errorf("expected CustomPrompt to round trip, got %q, want %q", got.CustomPrompt, u.CustomPrompt)
	}
}

// TestUpdateUser_CustomPromptRoundTrips proves UpdateUser's wholesale
// replace persists a changed CustomPrompt (used by restapi.handleAccount's
// self-service update), and that it can be cleared back to empty too.
func TestUpdateUser_CustomPromptRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	u := newUser("user1", "alice")
	if err := repo.CreateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u.CustomPrompt = "Be concise."
	if err := repo.UpdateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CustomPrompt != "Be concise." {
		t.Errorf("expected updated CustomPrompt to round trip, got %q", got.CustomPrompt)
	}

	// Clearing it back to empty must also work -- a user legitimately
	// removing their custom prompt.
	u.CustomPrompt = ""
	if err := repo.UpdateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err = repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CustomPrompt != "" {
		t.Errorf("expected CustomPrompt cleared back to empty, got %q", got.CustomPrompt)
	}
}

func TestListUsers_MultipleOrderedByUsername(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("u1", "zeta")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateUser(ctx, newUser("u2", "alpha")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 users, got %d", len(got))
	}
	if got[0].Username != "alpha" || got[1].Username != "zeta" {
		t.Errorf("expected users ordered by username, got %+v", got)
	}
}

func TestListUsers_EmptyWhenNoneConfigured(t *testing.T) {
	repo := newTestRepo(t)
	got, err := repo.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no users, got %+v", got)
	}
}

func TestGetUser_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("user1", "alice")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("expected alice, got %+v", got)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetUser(context.Background(), "missing")
	if !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestGetUserByUsername_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("user1", "alice")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "user1" {
		t.Errorf("expected user1, got %+v", got)
	}
}

func TestGetUserByUsername_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetUserByUsername(context.Background(), "nobody")
	if !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

// TestCreateUser_DuplicateUsernameReturnsErrUsernameTaken proves the
// username UNIQUE constraint (see dialect.go's users table) is actually
// enforced at the DB layer, not just a check-then-insert race at the
// application layer.
func TestCreateUser_DuplicateUsernameReturnsErrUsernameTaken(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("user1", "alice")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err := repo.CreateUser(ctx, newUser("user2", "alice"))
	if !errors.Is(err, ports.ErrUsernameTaken) {
		t.Errorf("expected ErrUsernameTaken, got %v", err)
	}
}

func TestUpdateUser_ReplacesFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	u := newUser("user1", "alice")
	if err := repo.CreateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	u.PasswordHash = "new-hash"
	u.UpdatedAt = u.UpdatedAt.Add(time.Minute)
	if err := repo.UpdateUser(ctx, u); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Errorf("expected password hash replaced, got %+v", got)
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateUser(context.Background(), newUser("missing", "nobody"))
	if !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestDeleteUser_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("user1", "alice")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteUser(ctx, "user1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListUsers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no users after delete, got %+v", got)
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteUser(context.Background(), "missing")
	if !errors.Is(err, ports.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

// TestUsersTable_CreatedFreshByNewInstallation is a lightweight smoke test
// proving a brand new database (no pre-existing users table at all) gets
// one from CreateSchemaSQL directly, with custom_prompt already present, no
// migration needed. (custom_prompt itself DOES have a pre-existing-table
// upgrade scenario, since the users table predates it -- see
// TestMigrateUserColumns_UpgradesPreExistingTable below.)
func TestUsersTable_CreatedFreshByNewInstallation(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	repo := reopenSQLiteTestRepo(t, dsn)
	ctx := context.Background()
	if err := repo.CreateUser(ctx, newUser("user1", "alice")); err != nil {
		t.Fatalf("unexpected error on a freshly migrated database: %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("unexpected error querying the users table directly: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in the users table, got %d", count)
	}
}

// TestMigrateUserColumns_UpgradesPreExistingTable is a real-upgrade
// regression test proving se.mo-sys.de's own live users rows (pre-dating
// custom_prompt) won't break on the next deploy -- mirrors
// TestMigrateSessionColumns_UpgradesPreExistingTable's exact pattern
// (session_test.go): a users table created before custom_prompt existed,
// seeded by hand via a raw connection, must gain the column -- a
// pre-existing row defaulting to custom_prompt=” (correct: no user could
// have set one before the column existed) without erroring, and the table
// must still work normally (create/get/update) for a fresh write
// afterward too.
func TestMigrateUserColumns_UpgradesPreExistingTable(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-migration shape: no custom_prompt column.
	if _, err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy-shape table: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"user1", "alice", "bcrypt-hash-placeholder", now, now); err != nil {
		t.Fatalf("failed to seed a pre-existing row: %v", err)
	}

	ctx := context.Background()
	repo := reopenSQLiteTestRepo(t, dsn) // migrate() runs here, including migrateUserColumns

	var customPrompt string
	if err := db.QueryRowContext(ctx, `SELECT custom_prompt FROM users WHERE id = ?`, "user1").Scan(&customPrompt); err != nil {
		t.Fatalf("unexpected error reading the pre-existing row's new column after migration: %v", err)
	}
	if customPrompt != "" {
		t.Errorf("expected a pre-existing user row to default to custom_prompt=\"\", got %q", customPrompt)
	}

	// The table must still work normally for a fresh write afterward too.
	if err := repo.CreateUser(ctx, newUser("user2", "bob")); err != nil {
		t.Fatalf("unexpected error creating after migration: %v", err)
	}
	got, err := repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got.CustomPrompt = "Be terse."
	if err := repo.UpdateUser(ctx, got); err != nil {
		t.Fatalf("unexpected error updating after migration: %v", err)
	}
	got, err = repo.GetUser(ctx, "user1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CustomPrompt != "Be terse." {
		t.Errorf("expected a fresh update's CustomPrompt to round trip, got %q", got.CustomPrompt)
	}
}
