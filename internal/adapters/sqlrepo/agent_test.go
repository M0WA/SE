package sqlrepo_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newAgent(id string) domain.Agent {
	return domain.Agent{
		ID: id, Name: "researcher", Description: "Digs up sources for a claim.",
		SystemPrompt: "You are a careful researcher.", MCPServerIDs: []string{"mcp1", "mcp2"},
		Enabled: true,
	}
}

func TestCreateAgent_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	a := newAgent("agent1")

	if err := repo.CreateAgent(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(got))
	}
	g := got[0]
	if g.ID != "agent1" || g.Name != "researcher" || g.Description != a.Description || !g.Enabled {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if g.SystemPrompt != a.SystemPrompt {
		t.Errorf("expected SystemPrompt to round trip, got %+v", g)
	}
	if len(g.MCPServerIDs) != 2 || g.MCPServerIDs[0] != "mcp1" || g.MCPServerIDs[1] != "mcp2" {
		t.Errorf("expected MCPServerIDs to round trip, got %+v", g.MCPServerIDs)
	}
}

func TestCreateAgent_EmptyMCPServerIDsRoundTripsAsEmptySlice(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	a := domain.Agent{ID: "agent1", Name: "generalist", Enabled: true}
	if err := repo.CreateAgent(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(got))
	}
	if len(got[0].MCPServerIDs) != 0 {
		t.Errorf("expected an empty MCPServerIDs slice, got %+v", got[0].MCPServerIDs)
	}
}

func TestListAgents_MultipleOrderedByName(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	b := newAgent("b")
	b.Name = "zeta"
	a := newAgent("a")
	a.Name = "alpha"
	if err := repo.CreateAgent(ctx, b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateAgent(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("expected agents ordered by name, got %+v", got)
	}
}

func TestListAgents_EmptyWhenNoneConfigured(t *testing.T) {
	repo := newTestRepo(t)
	got, err := repo.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no agents, got %+v", got)
	}
}

func TestUpdateAgent_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	a := newAgent("agent1")
	if err := repo.CreateAgent(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a.Name = "renamed"
	a.Description = "renamed description"
	a.SystemPrompt = "renamed prompt"
	a.MCPServerIDs = []string{"mcp3"}
	a.Enabled = false
	if err := repo.UpdateAgent(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(got))
	}
	g := got[0]
	if g.Name != "renamed" || g.Description != "renamed description" || g.SystemPrompt != "renamed prompt" || g.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", g)
	}
	if len(g.MCPServerIDs) != 1 || g.MCPServerIDs[0] != "mcp3" {
		t.Errorf("expected MCPServerIDs replaced too, got %+v", g.MCPServerIDs)
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateAgent(context.Background(), newAgent("missing"))
	if !errors.Is(err, ports.ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got %v", err)
	}
}

func TestDeleteAgent_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateAgent(ctx, newAgent("agent1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteAgent(ctx, "agent1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListAgents(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no agents after delete, got %+v", got)
	}
}

func TestDeleteAgent_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteAgent(context.Background(), "missing")
	if !errors.Is(err, ports.ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got %v", err)
	}
}
