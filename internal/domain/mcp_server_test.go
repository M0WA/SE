package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

// TestNewMCPServerID_WiresThroughToMintSlugID is a lean smoke test -- the
// underlying slugify/dedupe/fallback logic is already exhaustively covered
// by TestNewUserID_* in user_test.go (mintSlugID is shared), this just
// proves NewMCPServerID actually calls it with the right prefix/arguments.
func TestNewMCPServerID_WiresThroughToMintSlugID(t *testing.T) {
	if got := domain.NewMCPServerID("Web Search", nil); got != "web_search" {
		t.Errorf("NewMCPServerID(%q, nil) = %q, want %q", "Web Search", got, "web_search")
	}
}

func TestNewMCPServerID_DedupesAgainstExisting(t *testing.T) {
	existing := map[string]bool{"web_search": true}
	if got := domain.NewMCPServerID("web_search", existing); got != "web_search_2" {
		t.Errorf("expected the first collision to dedupe to \"web_search_2\", got %q", got)
	}
}
