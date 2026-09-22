package sqlrepo_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// newUserMCPServer mirrors newMCPServer, transport forced to "http" --
// unlike the admin-configured mcp_servers table, this one's real callers
// (restapi's self-service validation) only ever allow "http" here; see
// user_mcp_servers' own table comment in dialect.go for why.
func newUserMCPServer(id string) domain.MCPServer {
	return domain.MCPServer{
		ID: id, Name: "personal tools", Transport: "http",
		BaseURL: "https://example.com/mcp", APIKey: "sk-test",
		Enabled: true, Prompt: "Be terse.", GatedByWebSearch: false,
	}
}

// createTestUser is a small helper every user_mcp_servers test needs first
// -- the table's user_id column carries a real FK to users(id), enforced by
// Postgres/MySQL (SQLite doesn't enforce FKs by default, but the row should
// still exist for the test to mean anything).
func createTestUser(t *testing.T, repo interface {
	CreateUser(ctx context.Context, u domain.User) error
}, id string) {
	t.Helper()
	if err := repo.CreateUser(context.Background(), newUser(id, id)); err != nil {
		t.Fatalf("creating test user %q: %v", id, err)
	}
}

func TestCreateUserMCPServer_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	s := newUserMCPServer("srv1")

	if err := repo.CreateUserMCPServer(ctx, "alice", s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got))
	}
	g := got[0]
	if g.ID != "srv1" || g.Name != s.Name || g.Transport != "http" || g.BaseURL != s.BaseURL || g.APIKey != s.APIKey || !g.Enabled {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if g.Prompt != s.Prompt || g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch to round trip, got %+v", g)
	}
}

func TestListUserMCPServers_ScopedToOwner(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")

	if err := repo.CreateUserMCPServer(ctx, "alice", newUserMCPServer("shared-name")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateUserMCPServer(ctx, "bob", newUserMCPServer("shared-name")); err != nil {
		t.Fatalf("unexpected error creating bob's own same-ID server: %v", err)
	}

	aliceServers, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aliceServers) != 1 {
		t.Fatalf("expected alice to see only her own server, got %+v", aliceServers)
	}

	bobServers, err := repo.ListUserMCPServers(ctx, "bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bobServers) != 1 {
		t.Fatalf("expected bob to see only his own server, got %+v", bobServers)
	}
}

func TestListUserMCPServers_MultipleOrderedByName(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	b := newUserMCPServer("b")
	b.Name = "zeta"
	a := newUserMCPServer("a")
	a.Name = "alpha"
	if err := repo.CreateUserMCPServer(ctx, "alice", b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateUserMCPServer(ctx, "alice", a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("expected servers ordered by name, got %+v", got)
	}
}

func TestListUserMCPServers_EmptyWhenNoneConfigured(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no servers, got %+v", got)
	}
}

func TestUpdateUserMCPServer_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	s := newUserMCPServer("srv1")
	if err := repo.CreateUserMCPServer(ctx, "alice", s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s.Name = "renamed"
	s.BaseURL = "https://renamed.example.com/mcp"
	s.APIKey = "sk-new"
	s.Enabled = false
	s.Prompt = "renamed prompt"
	s.GatedByWebSearch = true
	if err := repo.UpdateUserMCPServer(ctx, "alice", s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got))
	}
	g := got[0]
	if g.Name != "renamed" || g.BaseURL != s.BaseURL || g.APIKey != s.APIKey || g.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", g)
	}
	if g.Prompt != "renamed prompt" || !g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch replaced too, got %+v", g)
	}
}

func TestUpdateUserMCPServer_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	err := repo.UpdateUserMCPServer(ctx, "alice", newUserMCPServer("missing"))
	if !errors.Is(err, ports.ErrUserMCPServerNotFound) {
		t.Errorf("expected ErrUserMCPServerNotFound, got %v", err)
	}
}

// TestUpdateUserMCPServer_WrongOwnerNotFound proves updating another user's
// server (right ID, wrong owner) is indistinguishable from the ID not
// existing at all -- the ownership scoping IS the access control here, not
// a separate check layered on top.
func TestUpdateUserMCPServer_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	if err := repo.CreateUserMCPServer(ctx, "alice", newUserMCPServer("srv1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := repo.UpdateUserMCPServer(ctx, "bob", newUserMCPServer("srv1"))
	if !errors.Is(err, ports.ErrUserMCPServerNotFound) {
		t.Errorf("expected bob updating alice's server to report ErrUserMCPServerNotFound, got %v", err)
	}
}

func TestDeleteUserMCPServer_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	if err := repo.CreateUserMCPServer(ctx, "alice", newUserMCPServer("srv1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteUserMCPServer(ctx, "alice", "srv1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no servers after delete, got %+v", got)
	}
}

func TestDeleteUserMCPServer_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	err := repo.DeleteUserMCPServer(ctx, "alice", "missing")
	if !errors.Is(err, ports.ErrUserMCPServerNotFound) {
		t.Errorf("expected ErrUserMCPServerNotFound, got %v", err)
	}
}

// TestDeleteUserMCPServer_WrongOwnerNotFound is DeleteUserMCPServer's
// counterpart to TestUpdateUserMCPServer_WrongOwnerNotFound.
func TestDeleteUserMCPServer_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	if err := repo.CreateUserMCPServer(ctx, "alice", newUserMCPServer("srv1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := repo.DeleteUserMCPServer(ctx, "bob", "srv1")
	if !errors.Is(err, ports.ErrUserMCPServerNotFound) {
		t.Errorf("expected bob deleting alice's server to report ErrUserMCPServerNotFound, got %v", err)
	}

	got, err := repo.ListUserMCPServers(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected alice's server to survive bob's failed delete, got %+v", got)
	}
}
