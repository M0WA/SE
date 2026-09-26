package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeUnits is a scriptable unitRunner: each unit name maps to whether
// Start/Stop should fail, and IsActive results are seeded directly.
type fakeUnits struct {
	mu sync.Mutex

	startErr map[string]error
	stopErr  map[string]error
	active   map[string]bool

	started []string
	stopped []string
}

func newFakeUnits() *fakeUnits {
	return &fakeUnits{
		startErr: map[string]error{},
		stopErr:  map[string]error{},
		active:   map[string]bool{},
	}
}

func (f *fakeUnits) Start(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, unit)
	if err := f.startErr[unit]; err != nil {
		return err
	}
	f.active[unit] = true
	return nil
}

func (f *fakeUnits) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, unit)
	if err := f.stopErr[unit]; err != nil {
		return err
	}
	f.active[unit] = false
	return nil
}

func (f *fakeUnits) IsActive(_ context.Context, unit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[unit]
}

// fakeReady reports ready for a url once callsUntilReady calls have been
// made against it (0 means ready immediately); a url absent from the map
// is never ready.
type fakeReady struct {
	mu              sync.Mutex
	callsUntilReady map[string]int
	calls           map[string]int
}

func newFakeReady() *fakeReady {
	return &fakeReady{callsUntilReady: map[string]int{}, calls: map[string]int{}}
}

func (f *fakeReady) Ready(_ context.Context, url string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	need, ok := f.callsUntilReady[url]
	if !ok {
		return false
	}
	f.calls[url]++
	return f.calls[url] > need
}

func newTestController(units unitRunner, ready readinessChecker) *Controller {
	return &Controller{
		mode:          ModeUnknown,
		units:         units,
		ready:         ready,
		now:           time.Now,
		chatUnit:      "vllm-chat.service",
		comfyUnit:     "comfyui.service",
		comfyReadyURL: "http://comfy/ready",
		vllmReadyURL:  "http://vllm/ready",
		switchTimeout: 2 * time.Second,
		pollInterval:  time.Millisecond,
	}
}

func waitForNotInProgress(t *testing.T, c *Controller) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := c.Status()
		if !st.InProgress {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for switch to finish")
	return Status{}
}

func TestSwitch_ChatToVisionSucceeds(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady()
	ready.callsUntilReady["http://comfy/ready"] = 0

	c := newTestController(units, ready)
	c.mode = ModeChat

	st, code, err := c.Switch(ModeVision)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code)
	}
	if !st.InProgress || st.Target != ModeVision {
		t.Fatalf("expected in-progress switch to vision, got %+v", st)
	}

	final := waitForNotInProgress(t, c)
	if final.Mode != ModeVision {
		t.Fatalf("expected final mode vision, got %+v", final)
	}
	if final.Detail != "" {
		t.Fatalf("expected empty detail on success, got %q", final.Detail)
	}
}

func TestSwitch_VisionToChatSucceeds(t *testing.T) {
	units := newFakeUnits()
	units.active["comfyui.service"] = true
	ready := newFakeReady()
	ready.callsUntilReady["http://vllm/ready"] = 0

	c := newTestController(units, ready)
	c.mode = ModeVision

	_, code, err := c.Switch(ModeChat)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code)
	}

	final := waitForNotInProgress(t, c)
	if final.Mode != ModeChat {
		t.Fatalf("expected final mode chat, got %+v", final)
	}
}

func TestSwitch_AlreadyInTargetModeReturns200WithoutStarting(t *testing.T) {
	units := newFakeUnits()
	ready := newFakeReady()
	c := newTestController(units, ready)
	c.mode = ModeChat

	st, code, err := c.Switch(ModeChat)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if st.InProgress {
		t.Fatalf("expected not in progress, got %+v", st)
	}
	if len(units.started) != 0 || len(units.stopped) != 0 {
		t.Fatalf("expected no unit calls, got started=%v stopped=%v", units.started, units.stopped)
	}
}

func TestSwitch_ConcurrentSwitchToDifferentTargetReturns409(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady()
	// Never becomes ready, so the first switch stays in-progress for the
	// duration of this test.
	c := newTestController(units, ready)
	c.mode = ModeChat
	c.switchTimeout = 5 * time.Second

	_, code, err := c.Switch(ModeVision)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("expected first switch accepted, got code=%d err=%v", code, err)
	}

	_, code2, err2 := c.Switch(ModeChat)
	if err2 == nil {
		t.Fatal("expected an error for a conflicting concurrent switch")
	}
	if code2 != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code2)
	}
}

func TestSwitch_ConcurrentSwitchToSameTargetIsIdempotent202(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady() // never ready -- stays in progress
	c := newTestController(units, ready)
	c.mode = ModeChat
	c.switchTimeout = 5 * time.Second

	_, code, err := c.Switch(ModeVision)
	if err != nil || code != http.StatusAccepted {
		t.Fatalf("expected first switch accepted, got code=%d err=%v", code, err)
	}
	st, code2, err2 := c.Switch(ModeVision)
	if err2 != nil {
		t.Fatalf("expected no error for same-target re-request, got %v", err2)
	}
	if code2 != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", code2)
	}
	if !st.InProgress || st.Target != ModeVision {
		t.Fatalf("expected still in progress toward vision, got %+v", st)
	}
}

func TestSwitch_StopUnitFailurePropagates(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	units.stopErr["vllm-chat.service"] = errors.New("systemctl: connection refused")
	ready := newFakeReady()

	c := newTestController(units, ready)
	c.mode = ModeChat

	_, _, err := c.Switch(ModeVision)
	if err != nil {
		t.Fatalf("Switch itself should not error synchronously: %v", err)
	}
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a failed switch, got %+v", final)
	}
	if final.Detail == "" {
		t.Fatal("expected a non-empty failure detail")
	}
}

func TestSwitch_StartUnitFailurePropagates(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	units.startErr["comfyui.service"] = errors.New("unit not found")
	ready := newFakeReady()

	c := newTestController(units, ready)
	c.mode = ModeChat

	c.Switch(ModeVision)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a failed start, got %+v", final)
	}
}

func TestSwitch_ChatSwitchStopComfyFailurePropagates(t *testing.T) {
	units := newFakeUnits()
	units.active["comfyui.service"] = true
	units.stopErr["comfyui.service"] = errors.New("systemctl: connection refused")
	ready := newFakeReady()

	c := newTestController(units, ready)
	c.mode = ModeVision

	c.Switch(ModeChat)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a failed stop, got %+v", final)
	}
}

func TestSwitch_ChatSwitchStartChatFailurePropagates(t *testing.T) {
	units := newFakeUnits()
	units.active["comfyui.service"] = true
	units.startErr["vllm-chat.service"] = errors.New("unit not found")
	ready := newFakeReady()

	c := newTestController(units, ready)
	c.mode = ModeVision

	c.Switch(ModeChat)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a failed start, got %+v", final)
	}
}

func TestSwitch_ChatSwitchReadinessTimeout(t *testing.T) {
	units := newFakeUnits()
	units.active["comfyui.service"] = true
	ready := newFakeReady() // never ready

	c := newTestController(units, ready)
	c.mode = ModeVision
	c.switchTimeout = 30 * time.Millisecond
	c.pollInterval = time.Millisecond

	c.Switch(ModeChat)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a readiness timeout, got %+v", final)
	}
}

func TestSwitch_ReadinessArrivesAfterSomePolling(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady()
	ready.callsUntilReady["http://comfy/ready"] = 2 // not ready immediately, ready on the 3rd check

	c := newTestController(units, ready)
	c.mode = ModeChat
	c.switchTimeout = 2 * time.Second
	c.pollInterval = time.Millisecond

	c.Switch(ModeVision)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeVision {
		t.Fatalf("expected final mode vision, got %+v", final)
	}
}

func TestSwitch_ReadinessNeverArrivesTimesOut(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady() // never ready

	c := newTestController(units, ready)
	c.mode = ModeChat
	c.switchTimeout = 30 * time.Millisecond
	c.pollInterval = time.Millisecond

	c.Switch(ModeVision)
	final := waitForNotInProgress(t, c)
	if final.Mode != ModeUnknown {
		t.Fatalf("expected mode unknown after a readiness timeout, got %+v", final)
	}
}

func TestSwitch_UnknownTargetFails(t *testing.T) {
	units := newFakeUnits()
	ready := newFakeReady()
	c := newTestController(units, ready)
	c.mode = ModeChat

	// Bypass the exported Switch (which only accepts chat/vision at the
	// handler layer) to exercise runSwitch's own default branch directly.
	c.mu.Lock()
	c.inProgress = true
	c.target = ModeUnknown
	c.since = time.Now()
	c.mu.Unlock()
	c.runSwitch(ModeUnknown)

	final := c.Status()
	if final.Mode != ModeUnknown || final.Detail == "" {
		t.Fatalf("expected a failure detail for an unknown target, got %+v", final)
	}
}

func TestDetectInitialMode(t *testing.T) {
	tests := []struct {
		name        string
		chatActive  bool
		comfyActive bool
		want        Mode
	}{
		{"chat active only", true, false, ModeChat},
		{"comfy active only", false, true, ModeVision},
		{"neither active", false, false, ModeUnknown},
		{"both active (inconsistent)", true, true, ModeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			units := newFakeUnits()
			units.active["vllm-chat.service"] = tt.chatActive
			units.active["comfyui.service"] = tt.comfyActive
			c := newTestController(units, newFakeReady())
			c.DetectInitialMode(context.Background())
			if got := c.Status().Mode; got != tt.want {
				t.Fatalf("expected mode %s, got %s", tt.want, got)
			}
		})
	}
}

func TestHeartbeat_ResetsLastHeartbeat(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.lastHeartbeat = time.Now().Add(-time.Hour)
	before := c.lastHeartbeat
	c.Heartbeat()
	c.mu.Lock()
	after := c.lastHeartbeat
	c.mu.Unlock()
	if !after.After(before) {
		t.Fatalf("expected lastHeartbeat to advance, before=%v after=%v", before, after)
	}
}

func TestRunIdleRevertLoop_DisabledWhenNonPositive(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.idleRevertMinutes = 0
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.RunIdleRevertLoop(ctx, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
		// returned immediately, as expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected RunIdleRevertLoop to return immediately when disabled")
	}
}

func TestRunIdleRevertLoop_RevertsAfterIdleTimeoutInVisionMode(t *testing.T) {
	units := newFakeUnits()
	units.active["comfyui.service"] = true
	ready := newFakeReady()
	ready.callsUntilReady["http://vllm/ready"] = 0

	c := newTestController(units, ready)
	c.mode = ModeVision
	c.idleRevertMinutes = 1
	c.lastHeartbeat = time.Now().Add(-2 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go c.RunIdleRevertLoop(ctx, 5*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status().Mode == ModeChat {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected idle revert to switch back to chat, got %+v", c.Status())
}

func TestRunIdleRevertLoop_DoesNotRevertWhileHeartbeatIsRecent(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.mode = ModeVision
	c.idleRevertMinutes = 5
	c.lastHeartbeat = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.RunIdleRevertLoop(ctx, 5*time.Millisecond)

	if c.Status().Mode != ModeVision {
		t.Fatalf("expected mode to stay vision, got %+v", c.Status())
	}
}

func TestRunIdleRevertLoop_DoesNotRevertWhileAlreadyInProgress(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.mode = ModeVision
	c.idleRevertMinutes = 1
	c.lastHeartbeat = time.Now().Add(-time.Hour)
	c.inProgress = true

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.RunIdleRevertLoop(ctx, 5*time.Millisecond)

	// Still marked in-progress from the test's own setup, and mode should
	// be untouched by maybeRevert (it must not double-fire).
	if c.Status().Mode != ModeVision {
		t.Fatalf("expected mode unchanged, got %+v", c.Status())
	}
}

func TestEffectiveTimeout_FallsBackToDefaultWhenUnset(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.switchTimeout = 0
	if got := c.effectiveTimeout(); got != defaultSwitchTimeout {
		t.Fatalf("expected default switch timeout, got %s", got)
	}
}

func TestEffectivePollInterval_FallsBackToDefaultWhenUnset(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.pollInterval = 0
	if got := c.effectivePollInterval(); got != defaultPollInterval {
		t.Fatalf("expected default poll interval, got %s", got)
	}
}

func TestHTTPReadinessChecker(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/bad", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	checker := httpReadinessChecker{client: srv.Client()}
	if !checker.Ready(context.Background(), srv.URL+"/ok") {
		t.Fatal("expected /ok to report ready")
	}
	if checker.Ready(context.Background(), srv.URL+"/bad") {
		t.Fatal("expected /bad to report not ready")
	}
	if checker.Ready(context.Background(), "://not-a-url") {
		t.Fatal("expected a malformed URL to report not ready")
	}
	if checker.Ready(context.Background(), "http://127.0.0.1:1/nothing-listens-here") {
		t.Fatal("expected an unreachable URL to report not ready")
	}
}

func TestNewController_UsesRealDependencies(t *testing.T) {
	c := NewController(ControllerConfig{ChatUnit: "a.service", ComfyUnit: "b.service"})
	if _, ok := c.units.(*realUnitRunner); !ok {
		t.Fatal("expected NewController to wire realUnitRunner")
	}
	if _, ok := c.ready.(httpReadinessChecker); !ok {
		t.Fatal("expected NewController to wire httpReadinessChecker")
	}
}

// writeFakeSystemctl installs a fake "systemctl" executable earlier on
// PATH for the duration of the test, mirroring how tests elsewhere in
// this repo (e.g. dockersandbox) verify a real exec.CommandContext
// invocation against a real (if fake) binary rather than mocking the
// call away entirely. "start"/"stop" exit 0 unless the unit name is
// listed in FAKE_SYSTEMCTL_FAIL (comma-separated); "is-active --quiet"
// exits 0 only for a unit listed in FAKE_SYSTEMCTL_ACTIVE.
func writeFakeSystemctl(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "is-active" ]; then
  unit="$3"
  case ",$FAKE_SYSTEMCTL_ACTIVE," in
    *",$unit,"*) exit 0 ;;
    *) exit 3 ;;
  esac
fi
if [ "$1" = "start" ] || [ "$1" = "stop" ]; then
  unit="$2"
  case ",$FAKE_SYSTEMCTL_FAIL," in
    *",$unit,"*) exit 1 ;;
    *) exit 0 ;;
  esac
fi
exit 0
`
	path := dir + "/systemctl"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake systemctl: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestRealUnitRunner_StartStopIsActive(t *testing.T) {
	writeFakeSystemctl(t)
	t.Setenv("FAKE_SYSTEMCTL_ACTIVE", "vllm-chat.service")
	t.Setenv("FAKE_SYSTEMCTL_FAIL", "comfyui.service")

	r := realUnitRunner{}
	ctx := context.Background()

	if !r.IsActive(ctx, "vllm-chat.service") {
		t.Fatal("expected vllm-chat.service to report active")
	}
	if r.IsActive(ctx, "comfyui.service") {
		t.Fatal("expected comfyui.service to report inactive")
	}
	if err := r.Start(ctx, "comfyui.service"); err == nil {
		t.Fatal("expected Start to fail for comfyui.service per fake script")
	}
	if err := r.Stop(ctx, "vllm-chat.service"); err != nil {
		t.Fatalf("expected Stop to succeed for vllm-chat.service, got %v", err)
	}
}

func TestRealUnitRunner_SystemctlPath_FallsBackWhenNotFoundOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty dir on PATH -- "systemctl" can't resolve
	r := &realUnitRunner{}
	if got := r.systemctlPath(); got != "systemctl" {
		t.Fatalf("expected fallback to the literal \"systemctl\", got %q", got)
	}
}
