package sqlrepo_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newMCPServer(id string) domain.MCPServer {
	return domain.MCPServer{
		ID: id, Name: "web", Transport: "stdio",
		Command: "/usr/bin/searchengine-mcp-web", Args: []string{"--flag", "value"},
		Enabled: true,
		Prompt:  "Prefer the top 3 results.", GatedByWebSearch: true,
	}
}

func TestCreateMCPServer_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newMCPServer("srv1")

	if err := repo.CreateMCPServer(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got))
	}
	g := got[0]
	if g.ID != "srv1" || g.Name != "web" || g.Transport != "stdio" || g.Command != s.Command || !g.Enabled {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if len(g.Args) != 2 || g.Args[0] != "--flag" || g.Args[1] != "value" {
		t.Errorf("expected Args to round trip, got %+v", g.Args)
	}
	if g.Prompt != s.Prompt || !g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch to round trip, got %+v", g)
	}
}

func TestCreateMCPServer_HTTPTransportFieldsRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := domain.MCPServer{
		ID: "srv1", Name: "remote", Transport: "http",
		BaseURL: "https://example.com/mcp", APIKey: "sk-test", Enabled: true,
	}
	if err := repo.CreateMCPServer(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got))
	}
	g := got[0]
	if g.Transport != "http" || g.BaseURL != s.BaseURL || g.APIKey != s.APIKey {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if len(g.Args) != 0 {
		t.Errorf("expected an empty Args slice for an http server, got %+v", g.Args)
	}
}

func TestListMCPServers_MultipleOrderedByName(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	b := newMCPServer("b")
	b.Name = "zeta"
	a := newMCPServer("a")
	a.Name = "alpha"
	if err := repo.CreateMCPServer(ctx, b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateMCPServer(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("expected servers ordered by name, got %+v", got)
	}
}

func TestListMCPServers_EmptyWhenNoneConfigured(t *testing.T) {
	repo := newTestRepo(t)
	got, err := repo.ListMCPServers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no servers, got %+v", got)
	}
}

func TestUpdateMCPServer_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	s := newMCPServer("srv1")
	if err := repo.CreateMCPServer(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s.Name = "renamed"
	s.Transport = "http"
	s.Command = ""
	s.Args = nil
	s.BaseURL = "https://example.com/mcp"
	s.APIKey = "sk-new"
	s.Enabled = false
	s.Prompt = "renamed prompt"
	s.GatedByWebSearch = false
	if err := repo.UpdateMCPServer(ctx, s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 server, got %d", len(got))
	}
	g := got[0]
	if g.Name != "renamed" || g.Transport != "http" || g.BaseURL != s.BaseURL || g.APIKey != s.APIKey || g.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", g)
	}
	if g.Prompt != "renamed prompt" || g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch replaced too, got %+v", g)
	}
}

func TestUpdateMCPServer_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateMCPServer(context.Background(), newMCPServer("missing"))
	if !errors.Is(err, ports.ErrMCPServerNotFound) {
		t.Errorf("expected ErrMCPServerNotFound, got %v", err)
	}
}

func TestDeleteMCPServer_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateMCPServer(ctx, newMCPServer("srv1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteMCPServer(ctx, "srv1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no servers after delete, got %+v", got)
	}
}

func TestDeleteMCPServer_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteMCPServer(context.Background(), "missing")
	if !errors.Is(err, ports.ErrMCPServerNotFound) {
		t.Errorf("expected ErrMCPServerNotFound, got %v", err)
	}
}
