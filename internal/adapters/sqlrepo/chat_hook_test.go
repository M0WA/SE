package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newChatHook(id string) domain.ChatHook {
	return domain.ChatHook{
		ID: id, Name: "web_search",
		Pattern: `\[\[search:(.+?)\]\]`, Script: "web_search.sh",
		Enabled: true,
	}
}

func TestCreateChatHook_ThenListRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	h := newChatHook("hook1")

	if err := repo.CreateChatHook(ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(got))
	}
	g := got[0]
	if g.ID != "hook1" || g.Name != "web_search" || g.Pattern != h.Pattern || g.Script != "web_search.sh" || !g.Enabled {
		t.Errorf("unexpected round trip: %+v", g)
	}
}

func TestListChatHooks_MultipleOrderedByName(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	b := newChatHook("b")
	b.Name = "zeta"
	a := newChatHook("a")
	a.Name = "alpha"
	if err := repo.CreateChatHook(ctx, b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.CreateChatHook(ctx, a); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Errorf("expected hooks ordered by name, got %+v", got)
	}
}

func TestListChatHooks_EmptyWhenNoneConfigured(t *testing.T) {
	repo := newTestRepo(t)
	got, err := repo.ListChatHooks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no hooks, got %+v", got)
	}
}

func TestUpdateChatHook_ReplacesEditableFields(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	h := newChatHook("hook1")
	if err := repo.CreateChatHook(ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	h.Name = "renamed"
	h.Pattern = `\[\[other:(.+?)\]\]`
	h.Script = "other.sh"
	h.Enabled = false
	if err := repo.UpdateChatHook(ctx, h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(got))
	}
	g := got[0]
	if g.Name != "renamed" || g.Pattern != h.Pattern || g.Script != "other.sh" || g.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", g)
	}
}

func TestUpdateChatHook_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.UpdateChatHook(context.Background(), newChatHook("missing"))
	if !errors.Is(err, ports.ErrChatHookNotFound) {
		t.Errorf("expected ErrChatHookNotFound, got %v", err)
	}
}

func TestDeleteChatHook_Success(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.CreateChatHook(ctx, newChatHook("hook1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.DeleteChatHook(ctx, "hook1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no hooks after delete, got %+v", got)
	}
}

func TestDeleteChatHook_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.DeleteChatHook(context.Background(), "missing")
	if !errors.Is(err, ports.ErrChatHookNotFound) {
		t.Errorf("expected ErrChatHookNotFound, got %v", err)
	}
}

// TestMigrateChatEndpointColumns_UpgradesPreExistingTable_SystemPrompt is a
// real-upgrade regression test for the system_prompt column specifically: a
// chat_endpoint table created before it existed (the pre-migration schema
// as of the max_context_tokens/web_search_* migration, built here by hand
// via a raw connection) must gain the column, defaulting a pre-existing row
// to "" (no persistent prompt injected, its previous behavior), without
// erroring, and the table must still work normally (get/set) afterward.
func TestMigrateChatEndpointColumns_UpgradesPreExistingTable_SystemPrompt(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-migration shape: every column up through web_search_result_count,
	// but no system_prompt.
	if _, err := db.Exec(`CREATE TABLE chat_endpoint (
		id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
		api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
		rag_result_count INTEGER NOT NULL DEFAULT 0,
		max_context_tokens INTEGER NOT NULL DEFAULT 0,
		web_search_enabled BOOLEAN NOT NULL DEFAULT false,
		web_search_base_url TEXT NOT NULL DEFAULT '',
		web_search_result_count INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy-shape table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_endpoint
		(id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, updated_at)
		VALUES ('default', 'https://example.com/v1', 'sk-test', 'llama-3', true, true, 5, 6000, true, 'http://127.0.0.1:8888', 5, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("failed to seed a pre-existing row: %v", err)
	}

	ctx := context.Background()
	repo := reopenSQLiteTestRepo(t, dsn) // migrate() runs here, including migrateChatEndpointColumns

	pre, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error reading the pre-existing row after migration: %v", err)
	}
	if pre.SystemPrompt != "" {
		t.Errorf("expected a pre-existing row to default to no system prompt (\"\"), got %+v", pre)
	}
	if pre.Model != "llama-3" || pre.MaxContextTokens != 6000 || !pre.WebSearchEnabled {
		t.Errorf("expected every pre-existing field otherwise untouched, got %+v", pre)
	}

	// The table must still work normally for a fresh write afterward too.
	fresh := newChatEndpoint()
	fresh.SystemPrompt = "You are a helpful search assistant."
	if err := repo.SetChatEndpoint(ctx, fresh); err != nil {
		t.Fatalf("unexpected error writing after migration: %v", err)
	}
	got, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SystemPrompt != fresh.SystemPrompt {
		t.Errorf("expected a fresh write's SystemPrompt to round trip, got %+v", got)
	}
}
