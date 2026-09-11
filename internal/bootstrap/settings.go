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

// SyncSettings applies whatever tuning/operational/overrides blobs store
// currently holds to the given instances immediately, then keeps
// re-applying the latest stored values every ~10s for as long as ctx stays
// alive -- so an admin edit saved to the shared database (by any process)
// reaches this one too, not just the process the edit was made on. tuning,
// op and overrides may each be nil for a process that has no use for that
// kind of setting (crawl-server has neither tuning nor overrides, for
// instance); each one that's already been constructed with its hardcoded
// default is simply left as-is when the store has no value for it yet.
func SyncSettings(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides) {
	applySettingsOnce(ctx, store, tuning, op, overrides)
	go func() {
		ticker := time.NewTicker(settingsPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				applySettingsOnce(ctx, store, tuning, op, overrides)
			}
		}
	}()
}

func applySettingsOnce(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides) {
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
