package sqlrepo

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

var sessionInternalTestDSNCounter int64

func newSessionInternalTestRepo(t *testing.T) *Repository {
	t.Helper()
	n := atomic.AddInt64(&sessionInternalTestDSNCounter, 1)
	dsn := fmt.Sprintf("file:sessioninternaltest%d?mode=memory&cache=shared", n)
	repo, err := New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestHashSessionToken_DeterministicAndDistinct(t *testing.T) {
	a := hashSessionToken("token-a")
	b := hashSessionToken("token-a")
	c := hashSessionToken("token-b")
	if a != b {
		t.Error("expected hashing the same token twice to produce the same digest")
	}
	if a == c {
		t.Error("expected hashing different tokens to produce different digests")
	}
	if len(a) != 64 { // hex-encoded SHA-256: 32 bytes -> 64 hex characters
		t.Errorf("expected a 64-character hex digest, got %d characters: %q", len(a), a)
	}
}

// TestCreateSession_StoresHashedTokenNotPlaintext queries the sessions
// table's raw token column directly (something ValidSession/RevokeSession's
// public API can't reveal either way) to prove the actual stored value is
// never the plaintext session token -- DB-only access (a backup, a read
// replica, an unrelated SQL injection) should yield a digest, not a
// directly-usable session cookie.
func TestCreateSession_StoresHashedTokenNotPlaintext(t *testing.T) {
	repo := newSessionInternalTestRepo(t)
	ctx := context.Background()
	const token = "super-secret-session-token"
	if err := repo.CreateSession(ctx, token, time.Now().Add(time.Hour), "admin", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var stored string
	err := repo.db.QueryRowContext(ctx, repo.ph(`SELECT token FROM sessions WHERE token = %s`, 1), hashSessionToken(token)).Scan(&stored)
	if err != nil {
		t.Fatalf("expected to find the session row by its hashed token, got: %v", err)
	}
	if stored == token {
		t.Error("expected the stored token to be hashed, not the plaintext value")
	}
	if stored != hashSessionToken(token) {
		t.Errorf("expected the stored value to equal hashSessionToken(token), got %q", stored)
	}

	var count int
	if err := repo.db.QueryRowContext(ctx, repo.ph(`SELECT COUNT(*) FROM sessions WHERE token = %s`, 1), token).Scan(&count); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Error("expected no row to be findable by the raw plaintext token")
	}
}
