package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

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
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, overrides, nil)

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

	bootstrap.SyncSettings(syncContext(t), repo, tuning, op, overrides, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, nil, nil)
}

// fakePoolConfigurer records every ConfigurePool call it receives, so tests
// can assert on the (maxOpen, maxIdle, lifetime) triple SyncSettings applied
// without needing a real *sql.DB.
type fakePoolConfigurer struct {
	calls []poolConfigCall
}

type poolConfigCall struct {
	maxOpenConns, maxIdleConns int
	connMaxLifetime            time.Duration
}

func (f *fakePoolConfigurer) ConfigurePool(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) {
	f.calls = append(f.calls, poolConfigCall{maxOpenConns, maxIdleConns, connMaxLifetime})
}

func TestSyncSettings_AppliesPoolSettingsOnStartup(t *testing.T) {
	repo := newTestRepo(t)
	stored := domain.OperationalSettingsValues{
		DefaultTopK: 3, DBMaxOpenConns: 7, DBMaxIdleConns: 4, DBConnMaxLifetime: 2 * time.Minute,
	}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	pool := &fakePoolConfigurer{}
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool)

	if len(pool.calls) != 1 {
		t.Fatalf("expected exactly one ConfigurePool call, got %d", len(pool.calls))
	}
	got := pool.calls[0]
	if got.maxOpenConns != 7 || got.maxIdleConns != 4 || got.connMaxLifetime != 2*time.Minute {
		t.Errorf("expected pool configured with the stored values, got %+v", got)
	}
}

func TestSyncSettings_ReappliesPoolSettingsOnLaterPoll(t *testing.T) {
	repo := newTestRepo(t)
	op := domain.DefaultOperationalSettings()
	pool := &fakePoolConfigurer{}
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool)
	if len(pool.calls) != 1 {
		t.Fatalf("expected one ConfigurePool call after startup, got %d", len(pool.calls))
	}

	updated := domain.OperationalSettingsValues{DBMaxOpenConns: 12, DBMaxIdleConns: 6, DBConnMaxLifetime: 90 * time.Second}
	data, _ := json.Marshal(updated)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool)

	if len(pool.calls) != 2 {
		t.Fatalf("expected a second ConfigurePool call simulating the next poll tick, got %d", len(pool.calls))
	}
	got := pool.calls[1]
	if got.maxOpenConns != 12 || got.maxIdleConns != 6 || got.connMaxLifetime != 90*time.Second {
		t.Errorf("expected the second call to carry the newly stored values, got %+v", got)
	}
}

func TestSyncSettings_NilPoolConfigurerIsSkipped(t *testing.T) {
	repo := newTestRepo(t)
	op := domain.DefaultOperationalSettings()
	// Must not panic when no pool is supplied.
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil)
}

func TestSyncSettings_ReReadsOnEachCall(t *testing.T) {
	repo := newTestRepo(t)
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)

	first, _ := json.Marshal(domain.TuningValues{Alpha: 0.6, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(first)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.6 {
		t.Fatalf("expected alpha 0.6 after first sync, got %v", alpha)
	}

	second, _ := json.Marshal(domain.TuningValues{Alpha: 0.7, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.7 {
		t.Errorf("expected a later SyncSettings call (simulating the next poll tick) to pick up the new value, got %v", alpha)
	}
}
