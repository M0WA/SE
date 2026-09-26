package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// gpuModeSwitchDwell is the minimum gap between two accepted switches,
// regardless of account -- the actual defense against thrashing the
// shared GPU, since ports.GPUModeController's own conflict handling only
// covers a switch already in flight, not two switches accepted back to
// back once each finishes quickly.
const gpuModeSwitchDwell = 120 * time.Second

// gpuModeSwitchRateLimit/gpuModeRateLimitWindow bound how often one
// account may switch at all, independent of the global dwell above --
// gpuModeSwitchRateLimit switches per gpuModeRateLimitWindow.
const (
	gpuModeSwitchRateLimit = 6
	gpuModeRateLimitWindow = time.Hour
)

// ErrGPUModeNotEnabled is returned by Status/Switch/Heartbeat when the
// admin's GPUModeSettings.Enabled master switch is off (or never
// configured) -- the caller (restapi) turns this into a 404, matching the
// design's "the capability does not exist at all" behavior.
var ErrGPUModeNotEnabled = errors.New("gpu mode is not enabled")

// ErrGPUModeSwitchTooSoon/ErrGPUModeRateLimited are Switch's own gate
// failures -- distinct from ErrGPUModeNotEnabled (404) since these mean
// the feature IS enabled, just not switchable right now; restapi turns
// these into 429.
var (
	ErrGPUModeSwitchTooSoon = errors.New("switched too recently -- wait before switching again")
	ErrGPUModeRateLimited   = errors.New("too many switches from this account recently")
)

// GPUModeService is the application-layer use case behind
// GET/POST /vision/api/mode, POST /vision/api/heartbeat, and handleChat's
// own availability check. It owns the security gate beyond the admin
// master switch and session auth (both enforced by restapi itself): a
// global minimum dwell between accepted switches and a per-account rate
// limit, on top of ports.GPUModeController, which only talks to
// cmd/gpu-control.
type GPUModeService struct {
	settings   ports.GPUModeStore
	controller ports.GPUModeController
	now        func() time.Time

	mu                   sync.Mutex
	cacheEnabled         bool
	cachedStatus         domain.GPUModeStatus
	lastSwitchAcceptedAt time.Time
	accountSwitches      map[string][]time.Time
}

// NewGPUModeService wires the real dependencies -- tests construct a
// GPUModeService literal directly to override now.
func NewGPUModeService(settings ports.GPUModeStore, controller ports.GPUModeController) *GPUModeService {
	return &GPUModeService{
		settings: settings, controller: controller, now: time.Now,
		accountSwitches: make(map[string][]time.Time),
	}
}

// loadEnabledConfig returns (cfg, true, nil) when the feature is
// configured and enabled; (zero, false, nil) when it's off or never
// configured (never an error -- "off" is the ordinary, default state);
// and (zero, false, err) only for a genuine store failure.
func (s *GPUModeService) loadEnabledConfig(ctx context.Context) (domain.GPUModeSettings, bool, error) {
	cfg, err := s.settings.GetGPUModeSettings(ctx)
	if errors.Is(err, ports.ErrGPUModeSettingsNotConfigured) {
		return domain.GPUModeSettings{}, false, nil
	}
	if err != nil {
		return domain.GPUModeSettings{}, false, err
	}
	if !cfg.Enabled {
		return domain.GPUModeSettings{}, false, nil
	}
	return cfg, true, nil
}

// RefreshCache is meant to be driven by bootstrap.PollRefresh from
// cmd/search's own startup, keeping CachedMode() cheap (no network call)
// for handleChat's per-turn check. A live-status failure just leaves the
// previous cached value in place rather than clearing it, so a transient
// cmd/gpu-control blip doesn't spuriously make chat look unavailable;
// "feature disabled" is the one case that always clears the cache
// immediately, since that's a fast, reliable DB read, not a flaky network
// call.
func (s *GPUModeService) RefreshCache(ctx context.Context) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		log.Printf("gpu mode: refreshing cached status: %v", err)
		return
	}
	if !enabled {
		s.mu.Lock()
		s.cacheEnabled = false
		s.mu.Unlock()
		return
	}
	st, err := s.controller.Status(ctx, cfg)
	if err != nil {
		log.Printf("gpu mode: fetching live status: %v", err)
		return
	}
	s.mu.Lock()
	s.cacheEnabled = true
	s.cachedStatus = st
	s.mu.Unlock()
}

// CachedMode reports the last known GPU mode with no network call --
// enabled=false means the feature is off entirely (handleChat should
// never block a chat turn on this). Meant only for that cheap, per-turn
// check; GET /vision/api/mode uses Status below for a live, current
// answer instead.
func (s *GPUModeService) CachedMode() (status domain.GPUModeStatus, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cachedStatus, s.cacheEnabled
}

// Status is GET /vision/api/mode's live (uncached) read -- also
// opportunistically refreshes the cache, so a manual check is reflected
// in CachedMode immediately rather than waiting for the next background
// tick. enabled=false, err=nil means "feature not enabled" (404, not an
// error).
func (s *GPUModeService) Status(ctx context.Context) (domain.GPUModeStatus, bool, error) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil || !enabled {
		if !enabled && err == nil {
			s.mu.Lock()
			s.cacheEnabled = false
			s.mu.Unlock()
		}
		return domain.GPUModeStatus{}, enabled, err
	}
	st, err := s.controller.Status(ctx, cfg)
	if err != nil {
		return domain.GPUModeStatus{}, true, err
	}
	s.mu.Lock()
	s.cacheEnabled = true
	s.cachedStatus = st
	s.mu.Unlock()
	return st, true, nil
}

// Switch is POST /vision/api/mode's use case: enforces the dwell/rate-
// limit gate below, then delegates to the controller. accountID must
// already be a real, authenticated account id -- restapi's own
// requireAuthAPI gate on this route means an anonymous caller never
// reaches here at all.
func (s *GPUModeService) Switch(ctx context.Context, accountID string, target domain.GPUMode) (domain.GPUModeStatus, error) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		return domain.GPUModeStatus{}, err
	}
	if !enabled {
		return domain.GPUModeStatus{}, ErrGPUModeNotEnabled
	}

	now := s.now()
	s.mu.Lock()
	if !s.lastSwitchAcceptedAt.IsZero() {
		if wait := gpuModeSwitchDwell - now.Sub(s.lastSwitchAcceptedAt); wait > 0 {
			s.mu.Unlock()
			return domain.GPUModeStatus{}, fmt.Errorf("%w (%s)", ErrGPUModeSwitchTooSoon, wait.Round(time.Second))
		}
	}
	if !s.recordAccountSwitchLocked(accountID, now) {
		s.mu.Unlock()
		return domain.GPUModeStatus{}, ErrGPUModeRateLimited
	}
	s.lastSwitchAcceptedAt = now
	s.mu.Unlock()

	st, err := s.controller.Switch(ctx, cfg, target)
	log.Printf("gpu mode: account %s requested switch to %s: status=%+v err=%v", accountID, target, st, err)
	if err != nil {
		return st, err
	}
	s.mu.Lock()
	s.cacheEnabled = true
	s.cachedStatus = st
	s.mu.Unlock()
	return st, nil
}

// recordAccountSwitchLocked reports whether accountID may switch now,
// under gpuModeSwitchRateLimit per gpuModeRateLimitWindow -- caller must
// hold s.mu. In-memory only, never evicted between accounts -- the same
// accepted gap as restapi's own loginLimiter.
func (s *GPUModeService) recordAccountSwitchLocked(accountID string, now time.Time) bool {
	cutoff := now.Add(-gpuModeRateLimitWindow)
	kept := s.accountSwitches[accountID][:0]
	for _, t := range s.accountSwitches[accountID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= gpuModeSwitchRateLimit {
		s.accountSwitches[accountID] = kept
		return false
	}
	s.accountSwitches[accountID] = append(kept, now)
	return true
}

// Heartbeat resets cmd/gpu-control's own idle-revert timer -- called
// while a Vision-mode client keeps the panel open/active. No dwell/
// rate-limit gating (unlike Switch): a heartbeat can't itself thrash the
// GPU.
func (s *GPUModeService) Heartbeat(ctx context.Context) error {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrGPUModeNotEnabled
	}
	return s.controller.Heartbeat(ctx, cfg)
}
