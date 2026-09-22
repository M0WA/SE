package sqlrepo_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"searchengine/internal/ports"
)

func TestSaveFile_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")

	f, err := repo.SaveFile(ctx, "alice", "", "notes.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ID == "" {
		t.Fatal("expected SaveFile to mint a non-empty ID")
	}
	if f.OwnerUserID != "alice" || f.Filename != "notes.txt" || f.ContentType != "text/plain" || f.Size != 5 {
		t.Errorf("unexpected saved file: %+v", f)
	}
	if f.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}

	got, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got))
	}
	if got[0].ID != f.ID || got[0].Filename != f.Filename {
		t.Errorf("unexpected round trip: %+v", got[0])
	}
}

func TestSaveFile_MintsDistinctIDsForEachCall(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")

	a, err := repo.SaveFile(ctx, "alice", "", "a.txt", "text/plain", []byte("a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := repo.SaveFile(ctx, "alice", "", "a.txt", "text/plain", []byte("b"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("expected distinct IDs for two uploads with the same filename, got %q twice", a.ID)
	}
}

func TestListFiles_ScopedToOwner(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")

	if _, err := repo.SaveFile(ctx, "alice", "", "a.txt", "text/plain", []byte("a")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.SaveFile(ctx, "bob", "", "b.txt", "text/plain", []byte("b")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	aliceFiles, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aliceFiles) != 1 || aliceFiles[0].Filename != "a.txt" {
		t.Errorf("expected alice to see only her own file, got %+v", aliceFiles)
	}
}

func TestListFiles_EmptyWhenNoneUploaded(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	got, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no files, got %+v", got)
	}
}

func TestListFiles_MostRecentFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	if _, err := repo.SaveFile(ctx, "alice", "", "first.txt", "text/plain", []byte("1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.SaveFile(ctx, "alice", "", "second.txt", "text/plain", []byte("2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Filename != "second.txt" || got[1].Filename != "first.txt" {
		t.Errorf("expected most-recently-uploaded file first, got %+v", got)
	}
}

func TestGetFile_ReturnsContent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	saved, err := repo.SaveFile(ctx, "alice", "", "notes.txt", "text/plain", []byte("hello world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f, data, err := repo.GetFile(ctx, "alice", saved.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Filename != "notes.txt" {
		t.Errorf("unexpected metadata: %+v", f)
	}
	if !bytes.Equal(data, []byte("hello world")) {
		t.Errorf("expected content %q, got %q", "hello world", data)
	}
}

func TestGetFile_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	_, _, err := repo.GetFile(ctx, "alice", "missing")
	if !errors.Is(err, ports.ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

// TestGetFile_WrongOwnerNotFound proves fetching another user's file
// (right ID, wrong owner) is indistinguishable from the ID not existing at
// all -- the ownership scoping IS the access control here, not a separate
// check layered on top.
func TestGetFile_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	saved, err := repo.SaveFile(ctx, "alice", "", "secret.txt", "text/plain", []byte("shh"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, _, err = repo.GetFile(ctx, "bob", saved.ID)
	if !errors.Is(err, ports.ErrFileNotFound) {
		t.Errorf("expected bob reading alice's file to report ErrFileNotFound, got %v", err)
	}
}

func TestDeleteFile_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	saved, err := repo.SaveFile(ctx, "alice", "", "notes.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteFile(ctx, "alice", saved.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no files after delete, got %+v", got)
	}
}

func TestDeleteFile_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	err := repo.DeleteFile(ctx, "alice", "missing")
	if !errors.Is(err, ports.ErrFileNotFound) {
		t.Errorf("expected ErrFileNotFound, got %v", err)
	}
}

// TestDeleteFile_WrongOwnerNotFound is DeleteFile's counterpart to
// TestGetFile_WrongOwnerNotFound.
func TestDeleteFile_WrongOwnerNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	createTestUser(t, repo, "alice")
	createTestUser(t, repo, "bob")
	saved, err := repo.SaveFile(ctx, "alice", "", "notes.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = repo.DeleteFile(ctx, "bob", saved.ID)
	if !errors.Is(err, ports.ErrFileNotFound) {
		t.Errorf("expected bob deleting alice's file to report ErrFileNotFound, got %v", err)
	}

	got, err := repo.ListFiles(ctx, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected alice's file to survive bob's failed delete, got %+v", got)
	}
}
