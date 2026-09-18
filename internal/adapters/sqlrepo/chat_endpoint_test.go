package sqlrepo_test

import (
	"context"
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
		Enabled: true, RAGEnabled: true, RAGResultCount: 5,
		UpdatedAt: time.Now().UTC(),
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

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["chat_endpoint"] != 1 {
		t.Errorf("expected exactly one chat_endpoint row after two SetChatEndpoint calls, got %d", counts["chat_endpoint"])
	}
}
