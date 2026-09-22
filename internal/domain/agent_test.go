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

// TestAgent_AllowsServer_EmptyScopeAllowsNothing proves there is no
// "unscoped" state -- an Agent with no MCPServerIDs set allows zero
// servers, not every one. Whether "no agent active at all" should instead
// allow everything is a decision made one layer up, by
// application.ChatService.Chat's own agentActive check -- AllowsServer
// itself only ever reasons about this one Agent value's own scope.
func TestAgent_AllowsServer_EmptyScopeAllowsNothing(t *testing.T) {
	a := domain.Agent{ID: "researcher"}
	if a.AllowsServer("web") {
		t.Error("expected an agent with no MCPServerIDs to allow no servers")
	}
}

func TestAgent_AllowsServer_ListedIDAllowed(t *testing.T) {
	a := domain.Agent{ID: "researcher", MCPServerIDs: []string{"web", "datetime"}}
	if !a.AllowsServer("web") {
		t.Error("expected a listed server id to be allowed")
	}
	if !a.AllowsServer("datetime") {
		t.Error("expected a listed server id to be allowed")
	}
}

func TestAgent_AllowsServer_UnlistedIDNotAllowed(t *testing.T) {
	a := domain.Agent{ID: "researcher", MCPServerIDs: []string{"web"}}
	if a.AllowsServer("datetime") {
		t.Error("expected an unlisted server id to be disallowed")
	}
}
