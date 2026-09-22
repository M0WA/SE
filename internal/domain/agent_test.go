package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

// TestNewAgentID_WiresThroughToMintSlugID is a lean smoke test -- the
// underlying slugify/dedupe/fallback logic is already exhaustively covered
// by TestNewUserID_* in user_test.go (mintSlugID is shared), this just
// proves NewAgentID actually calls it with the right prefix/arguments.
func TestNewAgentID_WiresThroughToMintSlugID(t *testing.T) {
	if got := domain.NewAgentID("Fact Checker", nil); got != "fact_checker" {
		t.Errorf("NewAgentID(%q, nil) = %q, want %q", "Fact Checker", got, "fact_checker")
	}
}

func TestNewAgentID_DedupesAgainstExisting(t *testing.T) {
	existing := map[string]bool{"researcher": true}
	if got := domain.NewAgentID("researcher", existing); got != "researcher_2" {
		t.Errorf("expected the first collision to dedupe to \"researcher_2\", got %q", got)
	}
}

func TestNewAgentID_NoAlphanumericFallsBackToTimestamp(t *testing.T) {
	got := domain.NewAgentID("!!!", nil)
	if got == "" {
		t.Error("expected a non-empty fallback ID for a name with no alphanumeric characters")
	}
	if !domain.SlugIDPattern.MatchString(got) {
		t.Errorf("expected fallback ID %q to match SlugIDPattern", got)
	}
}
