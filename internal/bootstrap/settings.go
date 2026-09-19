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

// PoolConfigurer is the narrow slice of *sqlrepo.Repository SyncSettings
// needs to re-apply connection-pool limits -- a local interface so
// bootstrap need not import sqlrepo. Nil (e.g. in a test) is skipped.
type PoolConfigurer interface {
	ConfigurePool(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration)
}

// SyncSettings applies whatever tuning/operational/overrides blobs store
// holds immediately, then re-applies them every ~10s while ctx stays alive,
// so an admin edit on any process reaches this one too. Any param may be
// nil if unused; pool gets DB pool settings re-applied each tick,
// embeddingEndpoints self-heals weights against disabled/deleted providers.
func SyncSettings(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides, pool PoolConfigurer, embeddingEndpoints ports.EmbeddingEndpointStore) {
	pollRefresh(ctx, settingsPollInterval, func() { applySettingsOnce(ctx, store, tuning, op, overrides, pool, embeddingEndpoints) })
}

func applySettingsOnce(ctx context.Context, store ports.SettingsStore, tuning *domain.TuningSettings, op *domain.OperationalSettings, overrides *domain.RankingOverrides, pool PoolConfigurer, embeddingEndpoints ports.EmbeddingEndpointStore) {
	if tuning != nil {
		var v domain.TuningValues
		if loadSetting(ctx, store, ports.SettingsKeyTuning, &v) {
			tuning.SetValues(v)
		}
	}
	if op != nil {
		// Seeded with the built-in defaults (not a zero-valued struct)
		// before unmarshaling: json.Unmarshal only overwrites fields present
		// in the stored blob, so a field added to OperationalSettingsValues
		// after this instance's blob was last saved -- most importantly a
		// bool like ContentDedupEnabled/URLAliasWWWEnabled, which
		// OperationalSettings.Set() deliberately never self-heals, since a
		// real `false` is indistinguishable from "omitted" -- keeps its
		// sensible default instead of silently regressing to Go's zero
		// value (false) forever, until an admin happens to re-save the full
		// settings form.
		v := domain.DefaultOperationalSettings().Get()
		// EmbeddingSearchWeights is excluded from the defaults seed above:
		// the legacy-migration check just below needs to tell "the stored
		// blob never had this key" (nil/empty) apart from "explicitly set,"
		// which the seeded default map would otherwise mask.
		v.EmbeddingSearchWeights = nil
		if loadSetting(ctx, store, ports.SettingsKeyOperational, &v) {
			// Upgrade migration: a blob saved before EmbeddingSearchWeights
			// existed still decodes the deprecated EmbeddingProvider field --
			// seed the new map from it once, so it carries over as weight 1.
			if len(v.EmbeddingSearchWeights) == 0 && v.EmbeddingProvider != "" {
				v.EmbeddingSearchWeights = map[string]float64{v.EmbeddingProvider: 1}
			}
			if embeddingEndpoints != nil {
				if endpoints, err := embeddingEndpoints.ListEmbeddingEndpoints(ctx); err == nil {
					v.EmbeddingSearchWeights = domain.ReconcileSearchWeights(v.EmbeddingSearchWeights, v.EmbeddingHashEnabled, endpoints)
				} else {
					log.Printf("listing embedding endpoints: %v", err)
				}
			}
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
