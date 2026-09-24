package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver used by newTestRepo

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
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, nil)

	got := op.Get()
	if got.UserAgent != "stored-agent" || got.DefaultMaxPages != 42 || got.CrawlDelayMs != 10 {
		t.Errorf("expected the stored operational values to be applied, got %+v", got)
	}
}

// TestSyncSettings_ReconcilesEmbeddingSearchWeightsAgainstLiveEndpoints
// proves the choke point domain.ReconcileSearchWeights is meant to run at:
// a stored EmbeddingSearchWeights entry naming an endpoint that's since
// been disabled (or deleted) is dropped, self-healing to hash, on the very
// next sync tick -- same as it used to be self-healed inside
// OperationalSettings.Set before provider validity depended on a
// dynamically configured table.
func TestSyncSettings_ReconcilesEmbeddingSearchWeightsAgainstLiveEndpoints(t *testing.T) {
	repo := newTestRepo(t)
	stored := domain.OperationalSettingsValues{
		EmbeddingHashEnabled:   true,
		EmbeddingSearchWeights: map[string]float64{"gone": 1},
	}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, repo)

	got := op.Get().EmbeddingSearchWeights
	if len(got) != 1 || got[domain.EmbeddingProviderHash] != 1 {
		t.Errorf("expected a weight naming a nonexistent endpoint to self-heal to {hash: 1}, got %+v", got)
	}
}

// TestSyncSettings_ReconciliationPreservesAnEnabledEndpoint proves the
// reconciliation doesn't clobber a genuinely valid, currently-enabled
// endpoint's weight.
func TestSyncSettings_ReconciliationPreservesAnEnabledEndpoint(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored := domain.OperationalSettingsValues{EmbeddingSearchWeights: map[string]float64{"ionos": 0.7}}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, repo)

	got := op.Get().EmbeddingSearchWeights
	if len(got) != 1 || got["ionos"] != 0.7 {
		t.Errorf("expected the enabled endpoint's weight preserved, got %+v", got)
	}
}

// TestSyncSettings_MigratesLegacyEmbeddingProviderToSearchWeights proves
// the upgrade-safety migration: a settings blob saved before
// EmbeddingSearchWeights existed (so it decodes to an empty map) but
// naming a real, currently-enabled provider via the deprecated
// EmbeddingProvider field carries that provider over as a weight-1 entry,
// rather than resetting to the hard-coded {hash: 1} default and silently
// discarding a real live config -- mirrors the PR #60 precedent.
func TestSyncSettings_MigratesLegacyEmbeddingProviderToSearchWeights(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.CreateEmbeddingEndpoint(context.Background(), domain.EmbeddingHTTPEndpoint{
		ID: "ionos", Name: "IONOS", BaseURL: "https://example.com", Model: "m", Dimensions: 4, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Simulate a pre-upgrade blob: EmbeddingProvider set, EmbeddingSearchWeights
	// absent entirely (decodes to nil/empty, not just zero-valued).
	stored := map[string]interface{}{"EmbeddingProvider": "ionos"}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, repo)

	got := op.Get().EmbeddingSearchWeights
	if len(got) != 1 || got["ionos"] != 1 {
		t.Errorf("expected the legacy EmbeddingProvider to migrate to {ionos: 1}, got %+v", got)
	}
}

// TestSyncSettings_MissingBoolFieldInStoredBlobKeepsItsDefault is the
// regression test for a real incident: OperationalSettings.Set() never
// self-heals a bool (a real false is indistinguishable from "omitted"), so
// a blob saved before a bool field like ContentDedupEnabled existed used to
// decode straight to Go's zero value (false) forever, even though the
// built-in default is true -- silently disabling the feature on any
// instance whose settings blob predates it, with no way to tell from the
// admin UI that anything was ever wrong. Loading onto a defaults-seeded
// struct (not a zero-valued one) means a field absent from the stored JSON
// keeps its real default instead.
func TestSyncSettings_MissingBoolFieldInStoredBlobKeepsItsDefault(t *testing.T) {
	repo := newTestRepo(t)
	// Simulate a pre-upgrade blob: some other field set, ContentDedupEnabled
	// (and every other field added after this hypothetical blob was last
	// saved) absent entirely, not just zero-valued.
	stored := map[string]interface{}{"UserAgent": "legacy-agent"}
	data, _ := json.Marshal(stored)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	op := domain.DefaultOperationalSettings()
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, nil)

	got := op.Get()
	if got.UserAgent != "legacy-agent" {
		t.Errorf("expected the one stored field to still apply, got %q", got.UserAgent)
	}
	if !got.ContentDedupEnabled {
		t.Error("expected ContentDedupEnabled to keep its true default when absent from the stored blob, got false")
	}
	if !got.PageRankEnabled {
		t.Error("expected PageRankEnabled to keep its true default when absent from the stored blob, got false")
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
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, overrides, nil, nil)

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

	bootstrap.SyncSettings(syncContext(t), repo, tuning, op, overrides, nil, nil)

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

// fakeSettingsStore lets tests drive loadSetting's two failure branches
// directly (a GetSetting error, and a stored value that fails to decode)
// without needing a real repository in a hard-to-reach state.
type fakeSettingsStore struct {
	getErr  error
	rawJSON string
	found   bool
}

func (f *fakeSettingsStore) SaveSetting(context.Context, string, string) error { return nil }
func (f *fakeSettingsStore) GetSetting(context.Context, string) (string, bool, error) {
	if f.getErr != nil {
		return "", false, f.getErr
	}
	return f.rawJSON, f.found, nil
}

// TestSyncSettings_GetSettingErrorLeavesDefaults proves loadSetting's
// "store.GetSetting failed" branch is logged and skipped, not fatal --
// tuning keeps whatever value it already had.
func TestSyncSettings_GetSettingErrorLeavesDefaults(t *testing.T) {
	store := &fakeSettingsStore{getErr: errors.New("db unavailable")}
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)

	bootstrap.SyncSettings(syncContext(t), store, tuning, nil, nil, nil, nil)

	if alpha, k1, b := tuning.Get(); alpha != 0.5 || k1 != 1.2 || b != 0.75 {
		t.Errorf("expected tuning untouched on a GetSetting error, got (%v, %v, %v)", alpha, k1, b)
	}
}

// TestSyncSettings_MalformedStoredValueLeavesDefaults proves loadSetting's
// "stored value isn't valid JSON for the target type" branch is logged
// and skipped, not fatal.
func TestSyncSettings_MalformedStoredValueLeavesDefaults(t *testing.T) {
	store := &fakeSettingsStore{rawJSON: "{not valid json", found: true}
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)

	bootstrap.SyncSettings(syncContext(t), store, tuning, nil, nil, nil, nil)

	if alpha, k1, b := tuning.Get(); alpha != 0.5 || k1 != 1.2 || b != 0.75 {
		t.Errorf("expected tuning untouched on a malformed stored value, got (%v, %v, %v)", alpha, k1, b)
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
	bootstrap.SyncSettings(syncContext(t), repo, nil, nil, nil, nil, nil)
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
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool, nil)
	if len(pool.calls) != 1 {
		t.Fatalf("expected one ConfigurePool call after startup, got %d", len(pool.calls))
	}

	updated := domain.OperationalSettingsValues{DBMaxOpenConns: 12, DBMaxIdleConns: 6, DBConnMaxLifetime: 90 * time.Second}
	data, _ := json.Marshal(updated)
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyOperational, string(data)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, pool, nil)

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
	bootstrap.SyncSettings(syncContext(t), repo, nil, op, nil, nil, nil)
}

func TestSyncSettings_ReReadsOnEachCall(t *testing.T) {
	repo := newTestRepo(t)
	tuning := domain.NewTuningSettings(0.5, 1.2, 0.75)

	first, _ := json.Marshal(domain.TuningValues{Alpha: 0.6, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(first)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.6 {
		t.Fatalf("expected alpha 0.6 after first sync, got %v", alpha)
	}

	second, _ := json.Marshal(domain.TuningValues{Alpha: 0.7, K1: 1.0, B: 0.5})
	if err := repo.SaveSetting(context.Background(), ports.SettingsKeyTuning, string(second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bootstrap.SyncSettings(syncContext(t), repo, tuning, nil, nil, nil, nil)
	if alpha, _, _ := tuning.Get(); alpha != 0.7 {
		t.Errorf("expected a later SyncSettings call (simulating the next poll tick) to pick up the new value, got %v", alpha)
	}
}
