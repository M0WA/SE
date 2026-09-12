package restapi

import (
	"testing"
	"time"
)

func TestSessionStore_CreateThenValidSucceeds(t *testing.T) {
	s := newSessionStore()
	token := s.create(time.Hour)
	if !s.valid(token) {
		t.Error("expected a freshly created session to be valid")
	}
}

func TestSessionStore_ValidUnknownTokenReportsFalse(t *testing.T) {
	s := newSessionStore()
	if s.valid("never-issued") {
		t.Error("expected an unknown token to be invalid")
	}
}

// TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten proves
// valid's expiry branch: a session past its TTL reports invalid and is
// removed from the store (a subsequent check doesn't need to re-discover
// the same expired entry every time).
func TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten(t *testing.T) {
	s := newSessionStore()
	token := s.create(-time.Second) // already expired the instant it's created

	if s.valid(token) {
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
	token := s.create(time.Hour)
	s.revoke(token)
	if s.valid(token) {
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
