package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"searchengine/internal/adapters/sqlrepo"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

var dsnCounter int64

func newTestRepo(t *testing.T) *sqlrepo.Repository {
	t.Helper()
	n := atomic.AddInt64(&dsnCounter, 1)
	dsn := fmt.Sprintf("file:bootstraptestdb%d?mode=memory&cache=shared", n)
	repo, err := sqlrepo.New(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to create test repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// syncContext returns a context whose cancellation is deferred to test
// cleanup, so SyncSettings' background poll goroutine (which only ever
// exits via ctx.Done()) doesn't leak past the end of the test -- while
// still being live for SyncSettings' own synchronous initial load, which
// runs before that goroutine is even spawned.
func syncContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func TestSyncSettings_AppliesStoredTuningOnStartup(t *testing.T) {
	repo := newTestRepo(t)
	data, _ := json.Marshal(domain.TuningValues{Alpha: 0.9, K1: 2.0, B: 0.3})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil)

	alpha, k1, b := tuning.Get()
	if alpha != 0.9 || k1 != 2.0 || b != 0.3 {
		t.Errorf("expected the stored tuning values to be applied, got (%v, %v, %v)", alpha, k1, b)
	}
}

func TestSyncSettings_AppliesStoredOperationalOnStartup(t *testing.T) {
	repo := newTestRepo(t)
	stored := domain.OperationalSettingsValues{
		FetchTimeout: 3, UserAgent: "stored-agent", DefaultMaxPages: 42,
		MinTextLength: 5, DefaultTopK: 3, SessionTTL: 1, CrawlDelayMs: 10, MaxResponseBytes: 2048,
	}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil)

	got := op.Get()
	if got.UserAgent != "stored-agent" || got.DefaultMaxPages != 42 || got.CrawlDelayMs != 10 {
		t.Errorf("expected the stored operational values to be applied, got %+v", got)
	}
}

func TestSyncSettings_AppliesStoredOverridesOnStartup(t *testing.T) {
	repo := newTestRepo(t)
	stored := domain.RankingOverridesValues{BlockedTerms: []string{"casino"}}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOverrides, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	overrides := domain.DefaultRankingOverrides()
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, overrides)

	got := overrides.Get()
	if len(got.BlockedTerms) != 1 || got.BlockedTerms[0] != "casino" {
		t.Errorf("expected the stored overrides to be applied, got %+v", got)
	}
}

func TestSyncSettings_LeavesDefaultsWhenNothingStored(t *testing.T) {
	repo := newTestRepo(t)
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)
	op := domain.DefaultOperationalSettings()
	overrides := domain.DefaultRankingOverrides()

	bootstrap.SyncSettings(syncContext(t), repo, tuning, op, overrides)

	alpha, k1, b := tuning.Get()
	if alpha != 0.5 || k1 != 1.2 || b != 0.75 {
		t.Errorf("expected tuning defaults untouched, got (%v, %v, %v)", alpha, k1, b)
	}
	if op.Get().UserAgent == "" {
		t.Errorf("expected operational defaults untouched")
	}
	if len(overrides.Get().BlockedTerms) != 0 {
		t.Errorf("expected overrides defaults untouched")
	}
}

func TestSyncSettings_NilInstancesAreSkipped(t *testing.T) {
	repo := newTestRepo(t)
	data, _ := json.Marshal(domain.TuningValues{Alpha: 0.9, K1: 2.0, B: 0.3})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Passing nil for every instance must not panic (e.g. crawl-server has
	// no tuning/overrides instance at all).
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, nil)
}

func TestSyncSettings_ReReadsOnEachCall(t *testing.T) {
	repo := newTestRepo(t)
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)

	first, _ := json.Marshal(domain.TuningValues{Alpha: 0.6, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(first)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.6 {
		t.Fatalf("expected alpha 0.6 after first sync, got %v", alpha)
	}

	second, _ := json.Marshal(domain.TuningValues{Alpha: 0.7, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.7 {
		t.Errorf("expected a later SyncSettings call (simulating the next poll tick) to pick up the new value, got %v", alpha)
	}
}
