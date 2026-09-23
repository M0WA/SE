package sqlrepo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newChatVisionSettings() domain.ChatVisionSettings {
	return domain.ChatVisionSettings{
		SimilarityEnabled: true, SimilarityProviderID: "h200_gte_qwen2",
		CaptionEnabled: true, CaptionBaseURL: "http://10.7.226.11:9000/v1",
		CaptionAPIKey: "sk-test", CaptionModel: "some-vl-chat-model",
		UpdatedAt: time.Now().UTC(),
	}
}

func TestGetChatVisionSettings_NotConfigured(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetChatVisionSettings(context.Background())
	if !errors.Is(err, ports.ErrChatVisionSettingsNotConfigured) {
		t.Errorf("expected ErrChatVisionSettingsNotConfigured, got %v", err)
	}
}

func TestSetChatVisionSettings_ThenGetRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	v := newChatVisionSettings()

	if err := repo.SetChatVisionSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetChatVisionSettings(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.SimilarityEnabled || got.SimilarityProviderID != "h200_gte_qwen2" {
		t.Errorf("expected similarity fields to round trip, got %+v", got)
	}
	if !got.CaptionEnabled || got.CaptionBaseURL != v.CaptionBaseURL || got.CaptionAPIKey != v.CaptionAPIKey || got.CaptionModel != v.CaptionModel {
		t.Errorf("expected caption fields to round trip, got %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt to round trip, got %+v", got)
	}
}

func TestSetChatVisionSettings_SecondCallOverwritesRatherThanDuplicating(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	v := newChatVisionSettings()
	if err := repo.SetChatVisionSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	v.SimilarityEnabled = false
	v.SimilarityProviderID = "other_provider"
	v.CaptionEnabled = false
	v.CaptionBaseURL = "http://new.example/v1"
	v.CaptionAPIKey = "sk-rotated"
	v.CaptionModel = "new-model"
	v.UpdatedAt = v.UpdatedAt.Add(time.Hour)
	if err := repo.SetChatVisionSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error on second SetChatVisionSettings: %v", err)
	}

	got, err := repo.GetChatVisionSettings(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SimilarityEnabled || got.SimilarityProviderID != "other_provider" {
		t.Errorf("expected updated similarity fields to replace the original, got %+v", got)
	}
	if got.CaptionEnabled || got.CaptionBaseURL != "http://new.example/v1" || got.CaptionAPIKey != "sk-rotated" || got.CaptionModel != "new-model" {
		t.Errorf("expected updated caption fields to replace the original, got %+v", got)
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["chat_vision_settings"] != 1 {
		t.Errorf("expected exactly one chat_vision_settings row after two SetChatVisionSettings calls, got %d", counts["chat_vision_settings"])
	}
}
