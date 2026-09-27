package application

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	files      ports.FileStore
	now        func() time.Time

	mu                   sync.Mutex
	cacheEnabled         bool
	cachedStatus         domain.GPUModeStatus
	lastSwitchAcceptedAt time.Time
	accountSwitches      map[string][]time.Time
	generateJobs         map[string]generateJobRecord
}

// generateJobRecord remembers which account/chat a generation job
// belongs to, recorded at submit time (Generate) and consulted once by
// GenerateResult the first time it observes "done", to save the
// finished video into that account's own files -- so it shows up in
// the account's existing file list and survives a lost browser-side job
// id (e.g. after a page reload), not just a raw ComfyUI-backed URL only
// this process ever knew about. In-memory only, same trade-off as
// accountSwitches above: an in-flight job across a searchengine restart
// just never gets auto-saved -- the video is still reachable via its
// own ViewURL in the meantime, this is a best-effort convenience layered
// on top, not the only way to reach it.
type generateJobRecord struct {
	userID string
	chatID string
	saved  bool
}

// NewGPUModeService wires the real dependencies -- tests construct a
// GPUModeService literal directly to override now. files may be nil
// (e.g. in a test that doesn't care about the save-to-account-files side
// effect) -- Generate/GenerateResult simply skip that step when it is.
func NewGPUModeService(settings ports.GPUModeStore, controller ports.GPUModeController, files ports.FileStore) *GPUModeService {
	return &GPUModeService{
		settings: settings, controller: controller, files: files, now: time.Now,
		accountSwitches: make(map[string][]time.Time),
		generateJobs:    make(map[string]generateJobRecord),
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

// Generate is POST /vision/api/generate's use case: submits a new
// text-to-video job through the controller. No dwell/rate-limit gating
// here (unlike Switch) -- ComfyUI's own queue already serializes
// generation jobs one at a time, and ordinary generation traffic doesn't
// thrash the shared GPU the way a chat<->vision mode switch does.
// userID/chatID are recorded (not sent to the controller) so
// GenerateResult can later save the finished video into that account's
// own files -- chatID may be empty (an unpinned/session-only tab), in
// which case that save step is simply skipped, same as every other
// files-requires-a-pinned-chat path in this codebase.
func (s *GPUModeService) Generate(ctx context.Context, userID, chatID, prompt string) (string, error) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrGPUModeNotEnabled
	}
	jobID, err := s.controller.Generate(ctx, cfg, prompt)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.generateJobs[jobID] = generateJobRecord{userID: userID, chatID: chatID}
	s.mu.Unlock()
	return jobID, nil
}

// GenerateResult is GET /vision/api/result's use case: polls the
// controller for jobID's current status, additionally saving the
// finished video into the submitting account's own files the first time
// it observes "done" (see saveGeneratedFileOnce).
func (s *GPUModeService) GenerateResult(ctx context.Context, jobID string) (domain.GPUGenerateResult, error) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		return domain.GPUGenerateResult{}, err
	}
	if !enabled {
		return domain.GPUGenerateResult{}, ErrGPUModeNotEnabled
	}
	result, err := s.controller.GenerateResult(ctx, cfg, jobID)
	if err != nil {
		return result, err
	}
	if result.Status == "done" {
		result.FileID = s.saveGeneratedFileOnce(ctx, cfg, jobID, result)
	}
	return result, nil
}

// saveGeneratedFileOnce downloads a finished generation's own bytes and
// saves them into the owning account's files, exactly once per job id
// (guarded by the "saved" flag in generateJobs, not by whether the save
// itself succeeded -- a transient failure isn't retried on every future
// poll). Best-effort: any failure here is logged, never surfaced as an
// error from GenerateResult, since the video stays viewable via its own
// ViewURL regardless of whether this side effect succeeds. Returns the
// new file's id, or "" if nothing was (or needed to be) saved.
func (s *GPUModeService) saveGeneratedFileOnce(ctx context.Context, cfg domain.GPUModeSettings, jobID string, result domain.GPUGenerateResult) string {
	s.mu.Lock()
	rec, ok := s.generateJobs[jobID]
	alreadyHandled := !ok || rec.saved || rec.chatID == "" || s.files == nil
	if ok && !rec.saved {
		rec.saved = true
		s.generateJobs[jobID] = rec
	}
	s.mu.Unlock()
	if alreadyHandled {
		return ""
	}

	contentType, body, err := s.controller.ViewAsset(ctx, cfg, result.ViewURL)
	if err != nil {
		log.Printf("gpu mode: saving generated video for job %s: fetching asset: %v", jobID, err)
		return ""
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		log.Printf("gpu mode: saving generated video for job %s: reading asset: %v", jobID, err)
		return ""
	}
	if contentType == "" {
		contentType = "video/mp4"
	}
	f, err := s.files.SaveFile(ctx, rec.userID, rec.chatID, "vision-"+jobID+".mp4", contentType, data)
	if err != nil {
		log.Printf("gpu mode: saving generated video for job %s: %v", jobID, err)
		return ""
	}
	return f.ID
}

// ViewAsset streams a finished generation's own bytes -- the caller must
// close the returned io.ReadCloser.
func (s *GPUModeService) ViewAsset(ctx context.Context, viewURL string) (contentType string, body io.ReadCloser, err error) {
	cfg, enabled, err := s.loadEnabledConfig(ctx)
	if err != nil {
		return "", nil, err
	}
	if !enabled {
		return "", nil, ErrGPUModeNotEnabled
	}
	return s.controller.ViewAsset(ctx, cfg, viewURL)
}
