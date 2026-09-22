package sqlrepo_test

import (
	"context"
	"testing"
)

func TestSeedDefaultAgents_PopulatesAFreshEmptyDatabase(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if err := repo.SeedDefaultAgents(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 seeded default agents, got %d: %+v", len(got), got)
	}
	for _, a := range got {
		if a.ID == "" || a.Name == "" || a.SystemPrompt == "" {
			t.Errorf("expected every seeded agent to have an ID/Name/SystemPrompt, got %+v", a)
		}
		if !a.Enabled {
			t.Errorf("expected every seeded agent to start Enabled, got %+v", a)
		}
		if len(a.MCPServerIDs) != 0 {
			t.Errorf("expected every seeded agent to start with an empty MCPServerIDs scope (deployment-specific server IDs can't be safely guessed), got %+v", a)
		}
	}
}

// TestSeedDefaultAgents_NoOpWhenAnyAgentAlreadyExists proves seeding never
// fires once ANY agent row exists -- whether from a prior seed or an
// admin's own creation -- not just "never re-inserts the same IDs twice".
func TestSeedDefaultAgents_NoOpWhenAnyAgentAlreadyExists(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateAgent(ctx, newAgent("my-own-agent")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.SeedDefaultAgents(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected seeding to be skipped entirely with a pre-existing agent, got %d: %+v", len(got), got)
	}
}

// TestSeedDefaultAgents_DeletingOneDefaultThenReseedingLeavesItDeleted is
// the essential non-regression check: after deleting ONE seeded default
// (leaving the other four in place), running SeedDefaultAgents again (e.g.
// a process restart) must NOT bring it back -- an admin's delete has to
// stick. This is exactly the behavior a naive per-row "INSERT ... ON
// CONFLICT (id) DO NOTHING" would get wrong (it would resurrect that exact
// row every time); the count-based check here only ever looks at whether
// the table is completely empty right now, so as long as at least one
// agent remains, seeding stays a no-op.
//
// Caveat this test deliberately does NOT cover: deleting EVERY seeded
// default (down to zero rows) is indistinguishable from "a genuinely fresh
// database" by the COUNT(*) == 0 check alone, so a restart after that would
// re-seed all five. Accepted as an edge case too narrow to design around --
// see SeedDefaultAgents' own doc comment.
func TestSeedDefaultAgents_DeletingOneDefaultThenReseedingLeavesItDeleted(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SeedDefaultAgents(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.DeleteAgent(ctx, "quick_answer"); err != nil {
		t.Fatalf("unexpected error deleting a seeded default: %v", err)
	}

	// A second SeedDefaultAgents call (as a restart would trigger) must be a
	// no-op: the table isn't empty (4 defaults remain), so nothing reseeds.
	if err := repo.SeedDefaultAgents(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected the deleted default to stay deleted (4 remaining), got %d: %+v", len(got), got)
	}
	for _, a := range got {
		if a.ID == "quick_answer" {
			t.Fatalf("expected \"quick_answer\" to stay deleted, but it was resurrected: %+v", got)
		}
	}
}
