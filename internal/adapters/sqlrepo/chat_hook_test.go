package sqlrepo_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newChatHook(id string) domain.ChatHook {
	return domain.ChatHook{
		ID: id, Name: "web_search", Description: "Search the web for current information.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Script:     "web_search.sh",
		Enabled:    true,
		Prompt:     "Prefer the top 3 results.", GatedByWebSearch: true,
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
	if g.ID != "hook1" || g.Name != "web_search" || g.Description != h.Description || string(g.Parameters) != string(h.Parameters) || g.Script != "web_search.sh" || !g.Enabled {
		t.Errorf("unexpected round trip: %+v", g)
	}
	if g.Prompt != h.Prompt || !g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch to round trip, got %+v", g)
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
	h.Description = "A renamed tool."
	h.Parameters = json.RawMessage(`{"type":"object","properties":{"other":{"type":"string"}},"required":["other"]}`)
	h.Script = "other.sh"
	h.Enabled = false
	h.Prompt = "renamed prompt"
	h.GatedByWebSearch = false
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
	if g.Name != "renamed" || g.Description != h.Description || string(g.Parameters) != string(h.Parameters) || g.Script != "other.sh" || g.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", g)
	}
	if g.Prompt != "renamed prompt" || g.GatedByWebSearch {
		t.Errorf("expected Prompt/GatedByWebSearch replaced too, got %+v", g)
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

// TestMigrateChatHookColumns_UpgradesPreExistingTable is a real-upgrade
// regression test proving se.mo-sys.de's own live chat_hooks rows (real
// "web_search" and "web_fetch" hooks, both pre-dating the native
// tool-calling migration's Description/Parameters columns) won't break on
// the next deploy: a chat_hooks table already at the previous live shape
// (id/name/pattern/script/enabled/prompt/gated_by_web_search), seeded by
// hand via a raw connection the same way
// TestMigrateChatEndpointColumns_UpgradesPreExistingTable_SystemPrompt seeds
// a pre-migration chat_endpoint table, must gain both new columns without
// erroring, AND the two well-known hook names must get backfilled with a
// sane default Parameters/Description (see backfillWebToolParameters) so
// they keep working without the admin having to hand-reconfigure them --
// while a differently-named row (an admin's own custom hook) is left at the
// bare default, never silently clobbered. The table must still work
// normally (create/list/update) for a fresh write afterward too.
func TestMigrateChatHookColumns_UpgradesPreExistingTable(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-migration shape: chat_hooks as it exists on se.mo-sys.de
	// today (pattern/prompt/gated_by_web_search already migrated in), but
	// no parameters or description yet.
	if _, err := db.Exec(`CREATE TABLE chat_hooks (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, pattern TEXT NOT NULL,
		script TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT true,
		prompt TEXT NOT NULL DEFAULT '', gated_by_web_search BOOLEAN NOT NULL DEFAULT false
	)`); err != nil {
		t.Fatalf("failed to create legacy-shape table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_hooks (id, name, pattern, script, enabled, prompt, gated_by_web_search)
		VALUES ('web_search', 'web_search', '\[\[search:(.+?)\]\]', 'web_search.sh', true, 'old prompt', true)`); err != nil {
		t.Fatalf("failed to seed a pre-existing row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_hooks (id, name, pattern, script, enabled, prompt, gated_by_web_search)
		VALUES ('web_fetch', 'web_fetch', '\[\[fetch:(.+?)\]\]', 'web_fetch.sh', true, 'old prompt', true)`); err != nil {
		t.Fatalf("failed to seed a second pre-existing row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_hooks (id, name, pattern, script, enabled, prompt, gated_by_web_search)
		VALUES ('custom', 'my_custom_tool', '\[\[custom:(.+?)\]\]', 'custom.sh', true, '', false)`); err != nil {
		t.Fatalf("failed to seed a third pre-existing row: %v", err)
	}

	ctx := context.Background()
	repo := reopenSQLiteTestRepo(t, dsn) // migrate() runs here, including migrateChatHookColumns

	pre, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error listing pre-existing rows after migration: %v", err)
	}
	if len(pre) != 3 {
		t.Fatalf("expected all 3 pre-existing rows to survive migration, got %+v", pre)
	}
	byID := make(map[string]domain.ChatHook, len(pre))
	for _, h := range pre {
		byID[h.ID] = h
	}

	ws := byID["web_search"]
	if name, err := ws.SingleParameterName(); err != nil || name != "query" {
		t.Errorf("expected web_search backfilled with a single 'query' parameter, got %+v (err=%v)", ws, err)
	}
	if ws.Description == "" {
		t.Errorf("expected web_search backfilled with a non-empty description, got %+v", ws)
	}
	if ws.Prompt != "old prompt" || !ws.GatedByWebSearch {
		t.Errorf("expected pre-existing prompt/gated_by_web_search left untouched, got %+v", ws)
	}

	wf := byID["web_fetch"]
	if name, err := wf.SingleParameterName(); err != nil || name != "url" {
		t.Errorf("expected web_fetch backfilled with a single 'url' parameter, got %+v (err=%v)", wf, err)
	}

	custom := byID["custom"]
	if string(custom.Parameters) != "{}" {
		t.Errorf("expected a non-web_search/web_fetch row's parameters left at the bare default, got %+v", custom)
	}
	if custom.Description != "" {
		t.Errorf("expected a non-web_search/web_fetch row's description left empty, got %+v", custom)
	}

	// The table must still work normally for a fresh write afterward too.
	fresh := newChatHook("fresh_hook")
	if err := repo.CreateChatHook(ctx, fresh); err != nil {
		t.Fatalf("unexpected error creating after migration: %v", err)
	}
	got, err := repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found domain.ChatHook
	for _, h := range got {
		if h.ID == "fresh_hook" {
			found = h
		}
	}
	if found.Description != fresh.Description || string(found.Parameters) != string(fresh.Parameters) {
		t.Errorf("expected a fresh write's Description/Parameters to round trip, got %+v", found)
	}

	fresh.Prompt = "updated prompt"
	fresh.GatedByWebSearch = false
	if err := repo.UpdateChatHook(ctx, fresh); err != nil {
		t.Fatalf("unexpected error updating after migration: %v", err)
	}
	got, err = repo.ListChatHooks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found = domain.ChatHook{}
	for _, h := range got {
		if h.ID == "fresh_hook" {
			found = h
		}
	}
	if found.Prompt != "updated prompt" || found.GatedByWebSearch {
		t.Errorf("expected an updated write's Prompt/GatedByWebSearch to round trip, got %+v", found)
	}
}
