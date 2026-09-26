package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeGPUModeStore struct {
	cfg       domain.GPUModeSettings
	notConfig bool
	err       error
}

func (f *fakeGPUModeStore) GetGPUModeSettings(ctx context.Context) (domain.GPUModeSettings, error) {
	if f.notConfig {
		return domain.GPUModeSettings{}, ports.ErrGPUModeSettingsNotConfigured
	}
	if f.err != nil {
		return domain.GPUModeSettings{}, f.err
	}
	return f.cfg, nil
}

func (f *fakeGPUModeStore) SetGPUModeSettings(ctx context.Context, v domain.GPUModeSettings) error {
	f.cfg = v
	return nil
}

type fakeGPUModeController struct {
	status    domain.GPUModeStatus
	statusErr error
	switchErr error
	heartErr  error

	switchCalls int
	lastTarget  domain.GPUMode
}

func (f *fakeGPUModeController) Status(ctx context.Context, cfg domain.GPUModeSettings) (domain.GPUModeStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeGPUModeController) Switch(ctx context.Context, cfg domain.GPUModeSettings, target domain.GPUMode) (domain.GPUModeStatus, error) {
	f.switchCalls++
	f.lastTarget = target
	return f.status, f.switchErr
}

func (f *fakeGPUModeController) Heartbeat(ctx context.Context, cfg domain.GPUModeSettings) error {
	return f.heartErr
}

func newTestGPUModeService(store *fakeGPUModeStore, controller *fakeGPUModeController) *GPUModeService {
	s := NewGPUModeService(store, controller)
	s.now = time.Now
	return s
}

func TestGPUModeService_Status_NotConfiguredMeansDisabledNotError(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	st, enabled, err := s.Status(context.Background())
	if err != nil || enabled || st != (domain.GPUModeStatus{}) {
		t.Fatalf("expected disabled/no-error, got st=%+v enabled=%v err=%v", st, enabled, err)
	}
}

func TestGPUModeService_Status_ExplicitlyDisabledMeansDisabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: false}}, &fakeGPUModeController{})
	_, enabled, err := s.Status(context.Background())
	if err != nil || enabled {
		t.Fatalf("expected disabled/no-error, got enabled=%v err=%v", enabled, err)
	}
}

func TestGPUModeService_Status_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	_, _, err := s.Status(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Status_EnabledCallsControllerAndCaches(t *testing.T) {
	want := domain.GPUModeStatus{Mode: domain.GPUModeChat}
	s := newTestGPUModeService(
		&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{status: want},
	)
	st, enabled, err := s.Status(context.Background())
	if err != nil || !enabled || st != want {
		t.Fatalf("unexpected result: st=%+v enabled=%v err=%v", st, enabled, err)
	}
	cached, cacheEnabled := s.CachedMode()
	if !cacheEnabled || cached != want {
		t.Fatalf("expected Status to populate the cache, got cached=%+v enabled=%v", cached, cacheEnabled)
	}
}

func TestGPUModeService_Status_ControllerErrorStillReportsEnabled(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(
		&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}},
		&fakeGPUModeController{statusErr: wantErr},
	)
	_, enabled, err := s.Status(context.Background())
	if !enabled {
		t.Fatal("expected enabled=true even when the live call fails")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}

func TestGPUModeService_RefreshCache_DisabledClearsCache(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}})
	s.RefreshCache(context.Background())
	if _, enabled := s.CachedMode(); !enabled {
		t.Fatal("expected cache populated after first refresh")
	}

	s.settings = &fakeGPUModeStore{notConfig: true}
	s.RefreshCache(context.Background())
	if _, enabled := s.CachedMode(); enabled {
		t.Fatal("expected cache cleared once disabled")
	}
}

func TestGPUModeService_RefreshCache_StoreErrorLeavesCacheUntouched(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeVision}})
	s.RefreshCache(context.Background())

	s.settings = &fakeGPUModeStore{err: errors.New("db down")}
	s.RefreshCache(context.Background())
	cached, enabled := s.CachedMode()
	if !enabled || cached.Mode != domain.GPUModeVision {
		t.Fatalf("expected stale cache preserved on store error, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_RefreshCache_LiveErrorLeavesCacheUntouched(t *testing.T) {
	controller := &fakeGPUModeController{status: domain.GPUModeStatus{Mode: domain.GPUModeChat}}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)
	s.RefreshCache(context.Background())

	controller.statusErr = errors.New("timeout")
	s.RefreshCache(context.Background())
	cached, enabled := s.CachedMode()
	if !enabled || cached.Mode != domain.GPUModeChat {
		t.Fatalf("expected stale cache preserved on live error, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_CachedMode_DefaultsToDisabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if _, enabled := s.CachedMode(); enabled {
		t.Fatal("expected a fresh service to report disabled")
	}
}

func TestGPUModeService_Switch_NotEnabledReturnsErrGPUModeNotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_Switch_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Switch_Success(t *testing.T) {
	want := domain.GPUModeStatus{Mode: domain.GPUModeChat, Target: domain.GPUModeVision, InProgress: true}
	controller := &fakeGPUModeController{status: want}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)

	st, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if st != want {
		t.Fatalf("unexpected status: %+v", st)
	}
	if controller.switchCalls != 1 || controller.lastTarget != domain.GPUModeVision {
		t.Fatalf("expected exactly one Switch(vision) call, got calls=%d target=%s", controller.switchCalls, controller.lastTarget)
	}
	if cached, enabled := s.CachedMode(); !enabled || cached != want {
		t.Fatalf("expected a successful Switch to populate the cache, got %+v enabled=%v", cached, enabled)
	}
}

func TestGPUModeService_Switch_ControllerErrorStillConsumesDwellAndRateLimit(t *testing.T) {
	controller := &fakeGPUModeController{switchErr: errors.New("gpu-control unreachable")}
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, controller)

	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if err == nil {
		t.Fatal("expected the controller error to propagate")
	}
	// A second immediate attempt must still be blocked by dwell, proving
	// the gate is applied before the (failing) live call, not after.
	_, err2 := s.Switch(context.Background(), "acct1", domain.GPUModeChat)
	if !errors.Is(err2, ErrGPUModeSwitchTooSoon) {
		t.Fatalf("expected ErrGPUModeSwitchTooSoon on the immediate retry, got %v", err2)
	}
}

func TestGPUModeService_Switch_DwellBlocksImmediateSecondSwitch(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	fixed := time.Now()
	s.now = func() time.Time { return fixed }

	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	_, err := s.Switch(context.Background(), "acct2", domain.GPUModeChat)
	if !errors.Is(err, ErrGPUModeSwitchTooSoon) {
		t.Fatalf("expected ErrGPUModeSwitchTooSoon (dwell is global, not per-account), got %v", err)
	}
}

func TestGPUModeService_Switch_DwellClearsAfterInterval(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeChat); err != nil {
		t.Fatalf("expected the second switch to succeed once the dwell interval passes, got %v", err)
	}
}

func TestGPUModeService_Switch_PerAccountRateLimit(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		// Advance past the dwell each time so only the rate limit is
		// under test here, not the global dwell.
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("switch %d: unexpected error %v", i, err)
		}
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	_, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision)
	if !errors.Is(err, ErrGPUModeRateLimited) {
		t.Fatalf("expected ErrGPUModeRateLimited on the %dth switch, got %v", gpuModeSwitchRateLimit+1, err)
	}
}

func TestGPUModeService_Switch_RateLimitWindowSlidesAndFreesUp(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("switch %d: unexpected error %v", i, err)
		}
	}
	// Move past the rate-limit window entirely -- every earlier switch
	// should have aged out, freeing the account back up.
	current = current.Add(gpuModeRateLimitWindow + time.Minute)
	if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
		t.Fatalf("expected the account to be free again once the window slides, got %v", err)
	}
}

func TestGPUModeService_Switch_RateLimitIsPerAccount(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	current := time.Now()
	s.now = func() time.Time { return current }

	for i := 0; i < gpuModeSwitchRateLimit; i++ {
		current = current.Add(gpuModeSwitchDwell + time.Second)
		if _, err := s.Switch(context.Background(), "acct1", domain.GPUModeVision); err != nil {
			t.Fatalf("acct1 switch %d: unexpected error %v", i, err)
		}
	}
	current = current.Add(gpuModeSwitchDwell + time.Second)
	if _, err := s.Switch(context.Background(), "acct2", domain.GPUModeVision); err != nil {
		t.Fatalf("expected a different account to be unaffected by acct1's rate limit, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_NotEnabled(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{notConfig: true}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, ErrGPUModeNotEnabled) {
		t.Fatalf("expected ErrGPUModeNotEnabled, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	s := newTestGPUModeService(&fakeGPUModeStore{err: wantErr}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected store error to propagate, got %v", err)
	}
}

func TestGPUModeService_Heartbeat_Success(t *testing.T) {
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{})
	if err := s.Heartbeat(context.Background()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
}

func TestGPUModeService_Heartbeat_ControllerErrorPropagates(t *testing.T) {
	wantErr := errors.New("gpu-control unreachable")
	s := newTestGPUModeService(&fakeGPUModeStore{cfg: domain.GPUModeSettings{Enabled: true}}, &fakeGPUModeController{heartErr: wantErr})
	if err := s.Heartbeat(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("expected controller error to propagate, got %v", err)
	}
}
