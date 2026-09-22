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

func newChatEndpoint() domain.ChatEndpoint {
	return domain.ChatEndpoint{
		BaseURL: "https://openai.inference.de-txl.ionos.com/v1",
		APIKey:  "sk-test", Model: "meta-llama/Llama-3.3-70B-Instruct",
		Enabled: true, MaxContextTokens: 6000,
		WebSearchEnabled: true, WebSearchBaseURL: "http://127.0.0.1:8888",
		SystemPrompt:   "You are a helpful assistant.",
		DefaultAgentID: "researcher",
		UpdatedAt:      time.Now().UTC(),
	}
}

func TestGetChatEndpoint_NotConfigured(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetChatEndpoint(context.Background())
	if !errors.Is(err, ports.ErrChatEndpointNotConfigured) {
		t.Errorf("expected ErrChatEndpointNotConfigured, got %v", err)
	}
}

func TestSetChatEndpoint_ThenGetRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	e := newChatEndpoint()

	if err := repo.SetChatEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BaseURL != e.BaseURL || got.APIKey != e.APIKey || got.Model != e.Model {
		t.Errorf("unexpected round trip: %+v", got)
	}
	if !got.Enabled {
		t.Errorf("unexpected option round trip: %+v", got)
	}
	if got.MaxContextTokens != 6000 {
		t.Errorf("expected MaxContextTokens to round trip, got %+v", got)
	}
	if !got.WebSearchEnabled || got.WebSearchBaseURL != "http://127.0.0.1:8888" {
		t.Errorf("expected web search fields to round trip, got %+v", got)
	}
	if got.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("expected SystemPrompt to round trip, got %+v", got)
	}
	if got.DefaultAgentID != "researcher" {
		t.Errorf("expected DefaultAgentID to round trip, got %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt to round trip, got %+v", got)
	}
}

func TestSetChatEndpoint_SecondCallOverwritesRatherThanDuplicating(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	e := newChatEndpoint()
	if err := repo.SetChatEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	e.BaseURL = "https://new.example/v1"
	e.APIKey = "sk-rotated"
	e.Model = "new-model"
	e.Enabled = false
	e.MaxContextTokens = 9000
	e.WebSearchEnabled = false
	e.WebSearchBaseURL = "http://new-searx.example"
	e.SystemPrompt = "You are a rotated assistant."
	e.UpdatedAt = e.UpdatedAt.Add(time.Hour)
	if err := repo.SetChatEndpoint(ctx, e); err != nil {
		t.Fatalf("unexpected error on second SetChatEndpoint: %v", err)
	}

	got, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BaseURL != "https://new.example/v1" || got.APIKey != "sk-rotated" || got.Model != "new-model" {
		t.Errorf("expected every editable field replaced, got %+v", got)
	}
	if got.Enabled {
		t.Errorf("expected updated flags to replace the original, got %+v", got)
	}
	if got.MaxContextTokens != 9000 {
		t.Errorf("expected updated MaxContextTokens to replace the original, got %+v", got)
	}
	if got.WebSearchEnabled || got.WebSearchBaseURL != "http://new-searx.example" {
		t.Errorf("expected updated web search fields to replace the original, got %+v", got)
	}
	if got.SystemPrompt != "You are a rotated assistant." {
		t.Errorf("expected updated SystemPrompt to replace the original, got %+v", got)
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["chat_endpoint"] != 1 {
		t.Errorf("expected exactly one chat_endpoint row after two SetChatEndpoint calls, got %d", counts["chat_endpoint"])
	}
}

func TestMigrateChatEndpointColumns_UpgradesPreExistingTable(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-migration shape: no max_context_tokens or web_search_* columns
	// at all.
	if _, err := db.Exec(`CREATE TABLE chat_endpoint (
		id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
		api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
		rag_result_count INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy-shape table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_endpoint
		(id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, updated_at)
		VALUES ('default', 'https://example.com/v1', 'sk-test', 'llama-3', true, true, 5, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("failed to seed a pre-existing row: %v", err)
	}

	ctx := context.Background()
	repo := reopenSQLiteTestRepo(t, dsn) // migrate() runs here, including migrateChatEndpointColumns

	pre, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error reading the pre-existing row after migration: %v", err)
	}
	if pre.MaxContextTokens != 0 {
		t.Errorf("expected a pre-existing row to default to trimming disabled (0), got %+v", pre)
	}
	if pre.WebSearchEnabled || pre.WebSearchBaseURL != "" {
		t.Errorf("expected a pre-existing row to default to web search off/unconfigured, got %+v", pre)
	}
	if pre.Model != "llama-3" {
		t.Errorf("expected every pre-existing field otherwise untouched, got %+v", pre)
	}

	// The table must still work normally for a fresh write afterward too.
	fresh := newChatEndpoint()
	if err := repo.SetChatEndpoint(ctx, fresh); err != nil {
		t.Fatalf("unexpected error writing after migration: %v", err)
	}
	got, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.MaxContextTokens != fresh.MaxContextTokens {
		t.Errorf("expected a fresh write's MaxContextTokens to round trip, got %+v", got)
	}
	if got.WebSearchEnabled != fresh.WebSearchEnabled || got.WebSearchBaseURL != fresh.WebSearchBaseURL {
		t.Errorf("expected a fresh write's web search fields to round trip, got %+v", got)
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

func TestMigrateChatEndpointColumns_UpgradesPreExistingTable_DefaultAgentID(t *testing.T) {
	dsn := uniqueSQLiteDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The pre-migration shape: every column up through system_prompt, but
	// no default_agent_id.
	if _, err := db.Exec(`CREATE TABLE chat_endpoint (
		id TEXT PRIMARY KEY, base_url TEXT NOT NULL,
		api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT false, rag_enabled BOOLEAN NOT NULL DEFAULT false,
		rag_result_count INTEGER NOT NULL DEFAULT 0,
		max_context_tokens INTEGER NOT NULL DEFAULT 0,
		web_search_enabled BOOLEAN NOT NULL DEFAULT false,
		web_search_base_url TEXT NOT NULL DEFAULT '',
		web_search_result_count INTEGER NOT NULL DEFAULT 0,
		system_prompt TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("failed to create legacy-shape table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_endpoint
		(id, base_url, api_key, model, enabled, rag_enabled, rag_result_count, max_context_tokens, web_search_enabled, web_search_base_url, web_search_result_count, system_prompt, updated_at)
		VALUES ('default', 'https://example.com/v1', 'sk-test', 'llama-3', true, true, 5, 6000, true, 'http://127.0.0.1:8888', 5, 'be helpful', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("failed to seed a pre-existing row: %v", err)
	}

	ctx := context.Background()
	repo := reopenSQLiteTestRepo(t, dsn) // migrate() runs here, including migrateChatEndpointColumns

	pre, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error reading the pre-existing row after migration: %v", err)
	}
	if pre.DefaultAgentID != "" {
		t.Errorf("expected a pre-existing row to default to no default agent (\"\"), got %+v", pre)
	}
	if pre.SystemPrompt != "be helpful" || pre.Model != "llama-3" {
		t.Errorf("expected every pre-existing field otherwise untouched, got %+v", pre)
	}

	// The table must still work normally for a fresh write afterward too.
	fresh := newChatEndpoint()
	fresh.DefaultAgentID = "fact_checker"
	if err := repo.SetChatEndpoint(ctx, fresh); err != nil {
		t.Fatalf("unexpected error writing after migration: %v", err)
	}
	got, err := repo.GetChatEndpoint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DefaultAgentID != fresh.DefaultAgentID {
		t.Errorf("expected a fresh write's DefaultAgentID to round trip, got %+v", got)
	}
}
