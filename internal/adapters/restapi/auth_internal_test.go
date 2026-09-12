package restapi

import (
	"context"
	"testing"
	"time"
)

func TestSessionStore_CreateThenValidSucceeds(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	if err := s.CreateSession(ctx, "tok-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	valid, err := s.ValidSession(ctx, "tok-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Error("expected a freshly created session to be valid")
	}
}

func TestSessionStore_ValidUnknownTokenReportsFalse(t *testing.T) {
	s := newSessionStore()
	valid, err := s.ValidSession(context.Background(), "never-issued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected an unknown token to be invalid")
	}
}

// TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten proves
// ValidSession's expiry branch: a session past its TTL reports invalid and
// is removed from the store (a subsequent check doesn't need to
// re-discover the same expired entry every time).
func TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	token := "tok-expired"
	if err := s.CreateSession(ctx, token, time.Now().Add(-time.Second)); err != nil { // already expired
		t.Fatalf("unexpected error: %v", err)
	}

	valid, err := s.ValidSession(ctx, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected an expired session to be invalid")
	}
	s.mu.Lock()
	_, stillPresent := s.sessions[token]
	s.mu.Unlock()
	if stillPresent {
		t.Error("expected the expired session to be removed from the store")
	}
}

func TestSessionStore_RevokeInvalidatesSession(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	token := "tok-revoke"
	if err := s.CreateSession(ctx, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.RevokeSession(ctx, token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	valid, err := s.ValidSession(ctx, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected a revoked session to be invalid")
	}
}

func TestRandomToken_ProducesDistinctNonEmptyTokens(t *testing.T) {
	a := randomToken()
	b := randomToken()
	if a == "" || b == "" {
		t.Error("expected non-empty tokens")
	}
	if a == b {
		t.Error("expected two calls to produce distinct tokens")
	}
}
