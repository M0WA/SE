package restapi

import (
	"testing"
	"time"
)

func TestFileTokenStore_IssueThenUserIDForSucceeds(t *testing.T) {
	s := newFileTokenStore()
	token := s.issue("alice")
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	userID, ok := s.userIDFor(token)
	if !ok {
		t.Fatal("expected a freshly issued token to resolve")
	}
	if userID != "alice" {
		t.Errorf("expected userID %q, got %q", "alice", userID)
	}
}

func TestFileTokenStore_UserIDForUnknownTokenReportsFalse(t *testing.T) {
	s := newFileTokenStore()
	_, ok := s.userIDFor("never-issued")
	if ok {
		t.Error("expected an unknown token to not resolve")
	}
}

// TestFileTokenStore_ExpiredTokenReportsFalseAndIsForgotten proves
// userIDFor's expiry branch, mirroring sessionStore's own
// TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten (auth_internal_test.go).
func TestFileTokenStore_ExpiredTokenReportsFalseAndIsForgotten(t *testing.T) {
	s := newFileTokenStore()
	token := "tok-expired"
	s.tokens[token] = fileTokenRecord{userID: "alice", expiresAt: time.Now().Add(-time.Second)}

	_, ok := s.userIDFor(token)
	if ok {
		t.Error("expected an expired token to not resolve")
	}
	s.mu.Lock()
	_, stillPresent := s.tokens[token]
	s.mu.Unlock()
	if stillPresent {
		t.Error("expected the expired token to be removed from the store")
	}
}

func TestFileTokenStore_IssueProducesDistinctTokens(t *testing.T) {
	s := newFileTokenStore()
	a := s.issue("alice")
	b := s.issue("alice")
	if a == b {
		t.Error("expected two calls to produce distinct tokens")
	}
}
