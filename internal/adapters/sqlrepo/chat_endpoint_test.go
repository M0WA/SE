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
		Enabled: true, RAGEnabled: true, RAGResultCount: 5, MaxContextTokens: 6000,
		WebSearchEnabled: true, WebSearchBaseURL: "http://127.0.0.1:8888", WebSearchResultCount: 5,
		SystemPrompt: "You are a helpful assistant.",
		UpdatedAt:    time.Now().UTC(),
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
	if !got.Enabled || !got.RAGEnabled || got.RAGResultCount != 5 {
		t.Errorf("unexpected option round trip: %+v", got)
	}
	if got.MaxContextTokens != 6000 {
		t.Errorf("expected MaxContextTokens to round trip, got %+v", got)
	}
	if !got.WebSearchEnabled || got.WebSearchBaseURL != "http://127.0.0.1:8888" || got.WebSearchResultCount != 5 {
		t.Errorf("expected web search fields to round trip, got %+v", got)
	}
	if got.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("expected SystemPrompt to round trip, got %+v", got)
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
	e.RAGEnabled = false
	e.RAGResultCount = 12
	e.MaxContextTokens = 9000
	e.WebSearchEnabled = false
	e.WebSearchBaseURL = "http://new-searx.example"
	e.WebSearchResultCount = 15
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
	if got.Enabled || got.RAGEnabled || got.RAGResultCount != 12 {
		t.Errorf("expected updated flags/count to replace the original, got %+v", got)
	}
	if got.MaxContextTokens != 9000 {
		t.Errorf("expected updated MaxContextTokens to replace the original, got %+v", got)
	}
	if got.WebSearchEnabled || got.WebSearchBaseURL != "http://new-searx.example" || got.WebSearchResultCount != 15 {
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
	if pre.WebSearchEnabled || pre.WebSearchBaseURL != "" || pre.WebSearchResultCount != 0 {
		t.Errorf("expected a pre-existing row to default to web search off/unconfigured, got %+v", pre)
	}
	if pre.Model != "llama-3" || pre.RAGResultCount != 5 {
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
	if got.WebSearchEnabled != fresh.WebSearchEnabled || got.WebSearchBaseURL != fresh.WebSearchBaseURL || got.WebSearchResultCount != fresh.WebSearchResultCount {
		t.Errorf("expected a fresh write's web search fields to round trip, got %+v", got)
	}
}
