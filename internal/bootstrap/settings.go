package bootstrap

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// settingsPollInterval is how often SyncSettings re-reads the settings
// store looking for edits made (via the admin UI) by another process --
// "roughly 10 seconds" is the propagation delay every admin settings/
// overrides page tells the user to expect.
const settingsPollInterval = 10 * time.Second

// PoolConfigurer is the narrow slice of *sqlrepo.Repository that
// SyncSettings needs to re-apply connection-pool limits -- kept as a local
// interface rather than importing sqlrepo, since bootstrap's settings
// syncing has no other reason to depend on that package. A nil
// PoolConfigurer (e.g. a test, or a caller with no pool to manage) is
// simply skipped.
type PoolConfigurer interface {
	ConfigurePool(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration)
}

// SyncSettings applies whatever tuning/operational/overrides blobs store
// currently holds to the given instances immediately, then keeps
// re-applying the latest stored values every ~10s for as long as ctx stays
// alive -- so an admin edit saved to the shared database (by any process)
// reaches this one too, not just the process the edit was made on. tuning,
// op and overrides may each be nil for a process that has no use for that
// kind of setting (crawl-server has neither tuning nor overrides, for
// instance); each one that's already been constructed with its hardcoded
// default is simply left as-is when the store has no value for it yet. pool
// (typically the same *sqlrepo.Repository the process opened via OpenDB)
// has op's current DBMaxOpenConns/DBMaxIdleConns/DBConnMaxLifetime
// re-applied to the live database connection on every call -- including
// this first one, which is a harmless no-op re-application of whatever
// sqlrepo.New already set at construction, but which also picks up any
// admin-configured value at every later poll tick without needing its own
// separate change-detection. pool may be nil for a caller with no
// connection pool to manage (e.g. a test that only cares about tuning).
func SyncSettings(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides, pool PoolConfigurer) {
	pollRefresh(ctx, settingsPollInterval, func() { applySettingsOnce(ctx, store, tuning, op, overrides, pool) })
}

func applySettingsOnce(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides, pool PoolConfigurer) {
	if tuning != nil {
		var v domain.TuningValues
		if loadSetting(ctx, store, ports.SettingsKeyTuning, &v) {
			tuning.SetValues(v)
		}
	}
	if op != nil {
		var v domain.OperationalSettingsValues
		if loadSetting(ctx, store, ports.SettingsKeyOperational, &v) {
			op.Set(v)
		}
		if pool != nil {
			cur := op.Get()
			pool.ConfigurePool(cur.DBMaxOpenConns, cur.DBMaxIdleConns, cur.DBConnMaxLifetime)
		}
	}
	if overrides != nil {
		var v domain.RankingOverridesValues
		if loadSetting(ctx, store, ports.SettingsKeyOverrides, &v) {
			overrides.Set(v)
		}
	}
}

// loadSetting reads key from store and decodes it into out, reporting
// whether out was actually populated -- false (with no error logged beyond
// a read/decode failure) for a key nothing has saved yet, so the caller's
// existing value is left untouched rather than zeroed out.
func loadSetting(ctx context.Context, store ports.SettingsStore, key string, out interface{}) bool {
	raw, found, err := store.GetSetting(ctx, key)
	if err != nil {
		log.Printf("loading %s setting: %v", key, err)
		return false
	}
	if !found {
		return false
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		log.Printf("decoding %s setting: %v", key, err)
		return false
	}
	return true
}
