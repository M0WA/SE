package sqlrepo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func newGPUModeSettings() domain.GPUModeSettings {
	return domain.GPUModeSettings{
		Enabled: true, ControlBaseURL: "http://10.7.226.11:8002",
		ControlAPIKey: "sk-test", SwitchTimeoutSeconds: 300, IdleRevertMinutes: 15,
		UpdatedAt: time.Now().UTC(),
	}
}

func TestGetGPUModeSettings_NotConfigured(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.GetGPUModeSettings(context.Background())
	if !errors.Is(err, ports.ErrGPUModeSettingsNotConfigured) {
		t.Errorf("expected ErrGPUModeSettingsNotConfigured, got %v", err)
	}
}

func TestSetGPUModeSettings_ThenGetRoundTrips(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	v := newGPUModeSettings()

	if err := repo.SetGPUModeSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := repo.GetGPUModeSettings(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Enabled || got.ControlBaseURL != v.ControlBaseURL || got.ControlAPIKey != v.ControlAPIKey {
		t.Errorf("expected control fields to round trip, got %+v", got)
	}
	if got.SwitchTimeoutSeconds != 300 || got.IdleRevertMinutes != 15 {
		t.Errorf("expected timeout/revert fields to round trip, got %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt to round trip, got %+v", got)
	}
}

func TestSetGPUModeSettings_SecondCallOverwritesRatherThanDuplicating(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	v := newGPUModeSettings()
	if err := repo.SetGPUModeSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	v.Enabled = false
	v.ControlBaseURL = "http://new.example:8002"
	v.ControlAPIKey = "sk-rotated"
	v.SwitchTimeoutSeconds = 600
	v.IdleRevertMinutes = 30
	v.UpdatedAt = v.UpdatedAt.Add(time.Hour)
	if err := repo.SetGPUModeSettings(ctx, v); err != nil {
		t.Fatalf("unexpected error on second SetGPUModeSettings: %v", err)
	}

	got, err := repo.GetGPUModeSettings(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Enabled || got.ControlBaseURL != "http://new.example:8002" || got.ControlAPIKey != "sk-rotated" {
		t.Errorf("expected updated control fields to replace the original, got %+v", got)
	}
	if got.SwitchTimeoutSeconds != 600 || got.IdleRevertMinutes != 30 {
		t.Errorf("expected updated timeout/revert fields to replace the original, got %+v", got)
	}

	counts, err := repo.TableRowCounts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counts["gpu_mode_settings"] != 1 {
		t.Errorf("expected exactly one gpu_mode_settings row after two SetGPUModeSettings calls, got %d", counts["gpu_mode_settings"])
	}
}
