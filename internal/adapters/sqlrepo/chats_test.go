package sqlrepo_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestCreateChat_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")

	c, err := repo.CreateChat(ctx, domain.PersistedChat{
		OwnerUserID: "alice", Title: "My chat", AgentID: "researcher",
		History: []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ID == "" {
		t.Fatal("expected CreateChat to mint a non-empty ID")
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Error("expected CreatedAt/UpdatedAt to be set")
	}

	got, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("expected 1 chat, got %+v", got)
	}
	if got[0].Title != "My chat" || got[0].AgentID != "researcher" {
		t.Errorf("unexpected round trip: %+v", got[0])
	}
	if len(got[0].History) != 1 || got[0].History[0].Content != "hi" {
		t.Errorf("expected history round tripped, got %+v", got[0].History)
	}
}

func TestListChats_ScopedToOwner(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")

	if _, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "a"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "bob", Title: "b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	aliceChats, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aliceChats) != 1 || aliceChats[0].Title != "a" {
		t.Errorf("expected alice to see only her own chat, got %+v", aliceChats)
	}
}

func TestListChats_MostRecentlyUpdatedFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	first, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "first"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "second"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Touch "first" so it becomes the most recently updated.
	first.Title = "first renamed"
	if err := repo.UpdateChat(ctx, first); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Title != "first renamed" {
		t.Errorf("expected the just-updated chat first, got %+v", got)
	}
}

func TestUpdateChat_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	c, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "original"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c.Title = "renamed"
	c.AgentID = "deep_research"
	c.History = []domain.ChatMessage{{Role: domain.ChatRoleUser, Content: "q"}, {Role: domain.ChatRoleAssistant, Content: "a"}}
	if err := repo.UpdateChat(ctx, c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Title != "renamed" || got[0].AgentID != "deep_research" || len(got[0].History) != 2 {
		t.Errorf("expected every editable field replaced, got %+v", got)
	}
}

func TestUpdateChat_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	err := repo.UpdateChat(ctx, domain.PersistedChat{ID: "missing", OwnerUserID: "alice", Title: "x"})
	if !errors.Is(err, ports.ErrChatNotFound) {
		t.Errorf("expected ErrChatNotFound, got %v", err)
	}
}

// TestUpdateChat_WrongOwnerNotFound proves updating another user's chat
// (right ID, wrong owner) is indistinguishable from the ID not existing.
func TestUpdateChat_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	c, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "secret"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = repo.UpdateChat(ctx, domain.PersistedChat{ID: c.ID, OwnerUserID: "bob", Title: "hijacked"})
	if !errors.Is(err, ports.ErrChatNotFound) {
		t.Errorf("expected bob updating alice's chat to report ErrChatNotFound, got %v", err)
	}
}

func TestDeleteChat_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	c, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "to delete"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteChat(ctx, "alice", c.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no chats after delete, got %+v", got)
	}
}

func TestDeleteChat_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	err := repo.DeleteChat(ctx, "alice", "missing")
	if !errors.Is(err, ports.ErrChatNotFound) {
		t.Errorf("expected ErrChatNotFound, got %v", err)
	}
}

// TestDeleteChat_WrongOwnerNotFound is DeleteChat's counterpart to
// TestUpdateChat_WrongOwnerNotFound.
func TestDeleteChat_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	c, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "safe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = repo.DeleteChat(ctx, "bob", c.ID)
	if !errors.Is(err, ports.ErrChatNotFound) {
		t.Errorf("expected bob deleting alice's chat to report ErrChatNotFound, got %v", err)
	}
	got, err := repo.ListChats(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected alice's chat to survive bob's failed delete, got %+v", got)
	}
}

// TestDeleteChat_CascadesFiles is the direct regression test for a real
// bug found while building this: SQLite only enforces a foreign key's "ON
// DELETE CASCADE" when a connection has run "PRAGMA foreign_keys = ON",
// which this package's connections never do -- without DeleteChat's own
// explicit file-deletion step, a chat's files silently survived its
// deletion under sqlite (confirmed) while the identical code would have
// cascaded correctly under Postgres, a real cross-dialect behavior gap.
func TestDeleteChat_CascadesFiles(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	c, err := repo.CreateChat(ctx, domain.PersistedChat{OwnerUserID: "alice", Title: "with files"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.SaveFile(ctx, "alice", c.ID, "notes.txt", "text/plain", []byte("hello")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.SaveFile(ctx, "alice", c.ID, "more.txt", "text/plain", []byte("world")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A file NOT attached to this chat must survive the chat's deletion.
	if _, err := repo.SaveFile(ctx, "alice", "", "unrelated.txt", "text/plain", []byte("x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteChat(ctx, "alice", c.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	files, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 || files[0].Filename != "unrelated.txt" {
		t.Errorf("expected only the unattached file to survive, got %+v", files)
	}
}
