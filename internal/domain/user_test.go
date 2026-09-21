package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNewUserID_SlugifiesUsername(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"alice", "alice"},
		{"Alice Smith", "alice_smith"},
		{"  leading and trailing  ", "leading_and_trailing"},
		{"Multiple   Spaces", "multiple_spaces"},
	}
	for _, tc := range cases {
		if got := domain.NewUserID(tc.name, nil); got != tc.want {
			t.Errorf("NewUserID(%q, nil) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNewUserID_TruncatesToPatternMaxLength(t *testing.T) {
	got := domain.NewUserID("a very long descriptive username indeed", nil)
	if len(got) > 20 {
		t.Errorf("expected ID truncated to at most 20 chars, got %q (%d chars)", got, len(got))
	}
	if !domain.UserIDPattern.MatchString(got) {
		t.Errorf("expected %q to match UserIDPattern", got)
	}
}

func TestNewUserID_NoAlphanumericFallsBackToTimestamp(t *testing.T) {
	got := domain.NewUserID("!!!", nil)
	if got == "" {
		t.Error("expected a non-empty fallback ID for a username with no alphanumeric characters")
	}
	if !domain.UserIDPattern.MatchString(got) {
		t.Errorf("expected fallback ID %q to match UserIDPattern", got)
	}
}

func TestNewUserID_DedupesAgainstExisting(t *testing.T) {
	existing := map[string]bool{"alice": true}
	got := domain.NewUserID("alice", existing)
	if got != "alice_2" {
		t.Errorf("expected the first collision to dedupe to \"alice_2\", got %q", got)
	}
}

func TestNewUserID_DedupesPastMultipleCollisions(t *testing.T) {
	existing := map[string]bool{"bob": true, "bob_2": true, "bob_3": true}
	got := domain.NewUserID("bob", existing)
	if got != "bob_4" {
		t.Errorf("expected dedupe to skip past every taken suffix, got %q", got)
	}
}

func TestRoleConstants(t *testing.T) {
	if domain.RoleAdmin != "admin" {
		t.Errorf("RoleAdmin = %q, want %q", domain.RoleAdmin, "admin")
	}
	if domain.RoleUser != "user" {
		t.Errorf("RoleUser = %q, want %q", domain.RoleUser, "user")
	}
}
