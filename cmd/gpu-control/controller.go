package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// Mode is one of the two GPU workloads this control service switches
// between. ModeUnknown is the honest starting value before the first
// DetectInitialMode probe (or after a restart mid-switch, which this
// process never persists across).
type Mode string

const (
	ModeChat    Mode = "chat"
	ModeVision  Mode = "vision"
	ModeUnknown Mode = "unknown"
)

// Status is the wire shape GET/POST /gpu/api/mode both return. Target/
// Since/ExpiresAt are only meaningful while InProgress.
type Status struct {
	Mode       Mode      `json:"mode"`
	Target     Mode      `json:"target,omitempty"`
	InProgress bool      `json:"in_progress"`
	Since      time.Time `json:"since,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// unitRunner starts/stops/queries a fixed set of systemd units -- an
// interface so tests never shell out to a real systemctl. realUnitRunner
// is the only production implementation. Unit names are always compile-
// time constants passed in by the caller, never taken from an HTTP
// request -- see this package's own doc comment on why.
type unitRunner interface {
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	IsActive(ctx context.Context, unit string) bool
}

// realUnitRunner shells out to systemctl with a fixed argv, never a
// shell -- the only thing an HTTP request can ever select is which of
// two hardcoded transitions to run, never a unit name or arbitrary
// command.
type realUnitRunner struct{}

func (realUnitRunner) Start(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "start", unit).Run()
}

func (realUnitRunner) Stop(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "stop", unit).Run()
}

// IsActive swallows a systemctl invocation error into false ("not
// confirmed active") rather than propagating it -- used only for the
// best-effort startup mode probe, where "can't tell" and "not active"
// should behave the same (fall back to ModeUnknown).
func (realUnitRunner) IsActive(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

// readinessChecker reports whether a service's own HTTP endpoint is
// responding yet -- an interface so tests never make a real HTTP call.
type readinessChecker interface {
	Ready(ctx context.Context, url string) bool
}

// httpReadinessChecker is the only production implementation: a bare
// GET, any 2xx counts as ready.
type httpReadinessChecker struct {
	client *http.Client
}

func (h httpReadinessChecker) Ready(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// ControllerConfig is NewController's fixed wiring -- every field is a
// compile-time constant or admin/env-configured value, never anything an
// HTTP request supplies.
type ControllerConfig struct {
	ChatUnit          string
	ComfyUnit         string
	ComfyReadyURL     string
	VLLMReadyURL      string
	SwitchTimeout     time.Duration
	IdleRevertMinutes int
	PollInterval      time.Duration
}

// Controller owns the single, global GPU mode state machine. One process
// per GPU host, so a single in-memory mutex-guarded struct is enough --
// nothing here is persisted to disk; a restart starts at ModeUnknown
// until DetectInitialMode resolves it.
type Controller struct {
	mu sync.Mutex

	mode       Mode
	target     Mode
	inProgress bool
	since      time.Time
	detail     string

	lastHeartbeat time.Time

	units unitRunner
	ready readinessChecker
	now   func() time.Time

	chatUnit          string
	comfyUnit         string
	comfyReadyURL     string
	vllmReadyURL      string
	switchTimeout     time.Duration
	idleRevertMinutes int
	pollInterval      time.Duration
}

// NewController wires a Controller against real systemctl calls and real
// HTTP readiness checks -- the only production constructor. Tests build
// a Controller literal directly so they can inject fakes.
func NewController(cfg ControllerConfig) *Controller {
	return &Controller{
		mode:              ModeUnknown,
		units:             realUnitRunner{},
		ready:             httpReadinessChecker{client: &http.Client{Timeout: 5 * time.Second}},
		now:               time.Now,
		chatUnit:          cfg.ChatUnit,
		comfyUnit:         cfg.ComfyUnit,
		comfyReadyURL:     cfg.ComfyReadyURL,
		vllmReadyURL:      cfg.VLLMReadyURL,
		switchTimeout:     cfg.SwitchTimeout,
		idleRevertMinutes: cfg.IdleRevertMinutes,
		pollInterval:      cfg.PollInterval,
	}
}

// DetectInitialMode probes which of the two units is actually active and
// sets the starting mode accordingly -- called once at startup so a
// gpu-control restart doesn't report "unknown" when vllm-chat is already
// the running, healthy default (the common case). Falls back to
// ModeUnknown if neither, or both (an inconsistent state), is confirmed
// active.
func (c *Controller) DetectInitialMode(ctx context.Context) {
	chatActive := c.units.IsActive(ctx, c.chatUnit)
	comfyActive := c.units.IsActive(ctx, c.comfyUnit)
	c.mu.Lock()
	switch {
	case chatActive && !comfyActive:
		c.mode = ModeChat
		c.lastHeartbeat = c.now()
	case comfyActive && !chatActive:
		c.mode = ModeVision
		c.lastHeartbeat = c.now()
	default:
		c.mode = ModeUnknown
	}
	c.mu.Unlock()
}

// Status returns the current mode/switch state.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Controller) statusLocked() Status {
	st := Status{Mode: c.mode, InProgress: c.inProgress, Detail: c.detail}
	if c.inProgress {
		st.Target = c.target
		st.Since = c.since
		st.ExpiresAt = c.since.Add(c.effectiveTimeout())
	}
	return st
}

// Switch begins moving to target in the background and returns
// immediately: 200 if already there and idle, 202 if a switch was just
// started (or one to the same target was already running), 409 if a
// switch to a *different* target is already in flight. The actual work
// runs on a detached context.Background() goroutine -- this repo's
// fire-and-forget convention -- so an HTTP client disconnecting never
// leaves the GPU half-switched.
func (c *Controller) Switch(target Mode) (Status, int, error) {
	c.mu.Lock()
	if c.mode == target && !c.inProgress {
		st := c.statusLocked()
		c.mu.Unlock()
		return st, http.StatusOK, nil
	}
	if c.inProgress {
		st := c.statusLocked()
		if c.target != target {
			c.mu.Unlock()
			return st, http.StatusConflict, fmt.Errorf("a switch to %s is already in progress", c.target)
		}
		c.mu.Unlock()
		return st, http.StatusAccepted, nil
	}
	c.inProgress = true
	c.target = target
	c.since = c.now()
	c.detail = "starting switch"
	st := c.statusLocked()
	c.mu.Unlock()

	go c.runSwitch(target)

	return st, http.StatusAccepted, nil
}

// Heartbeat resets the idle-revert timer -- called while a Vision-mode
// client keeps the panel open/active.
func (c *Controller) Heartbeat() {
	c.mu.Lock()
	c.lastHeartbeat = c.now()
	c.mu.Unlock()
}

func (c *Controller) setDetail(d string) {
	c.mu.Lock()
	c.detail = d
	c.mu.Unlock()
}

func (c *Controller) effectiveTimeout() time.Duration {
	if c.switchTimeout > 0 {
		return c.switchTimeout
	}
	return defaultSwitchTimeout
}

func (c *Controller) runSwitch(target Mode) {
	ctx, cancel := context.WithTimeout(context.Background(), c.effectiveTimeout())
	defer cancel()

	var err error
	switch target {
	case ModeVision:
		err = c.switchToVision(ctx)
	case ModeChat:
		err = c.switchToChat(ctx)
	default:
		err = fmt.Errorf("unknown target mode %q", target)
	}

	c.mu.Lock()
	c.inProgress = false
	if err != nil {
		c.detail = "switch failed: " + err.Error()
		c.mode = ModeUnknown
	} else {
		c.mode = target
		c.detail = ""
		c.lastHeartbeat = c.now()
	}
	c.mu.Unlock()
}

// switchToVision stops the chat model (freeing most of the GPU's VRAM --
// vllm-embed.service is deliberately never touched here, so search/
// indexing keeps working the whole time) then starts ComfyUI and waits
// for it to answer.
func (c *Controller) switchToVision(ctx context.Context) error {
	c.setDetail("stopping chat model")
	if err := c.units.Stop(ctx, c.chatUnit); err != nil {
		return fmt.Errorf("stopping %s: %w", c.chatUnit, err)
	}
	c.setDetail("starting ComfyUI")
	if err := c.units.Start(ctx, c.comfyUnit); err != nil {
		return fmt.Errorf("starting %s: %w", c.comfyUnit, err)
	}
	c.setDetail("waiting for ComfyUI to become ready")
	if !c.waitReady(ctx, c.comfyReadyURL) {
		return fmt.Errorf("ComfyUI did not become ready within %s", c.effectiveTimeout())
	}
	return nil
}

// switchToChat is switchToVision's mirror image -- always ends with the
// chat model, never ComfyUI, left running (comfyui.service itself stays
// systemd-disabled throughout, so a host reboot can never race it against
// vllm-chat.service on its own).
func (c *Controller) switchToChat(ctx context.Context) error {
	c.setDetail("stopping ComfyUI")
	if err := c.units.Stop(ctx, c.comfyUnit); err != nil {
		return fmt.Errorf("stopping %s: %w", c.comfyUnit, err)
	}
	c.setDetail("starting chat model")
	if err := c.units.Start(ctx, c.chatUnit); err != nil {
		return fmt.Errorf("starting %s: %w", c.chatUnit, err)
	}
	c.setDetail("waiting for the chat model to become ready")
	if !c.waitReady(ctx, c.vllmReadyURL) {
		return fmt.Errorf("chat model did not become ready within %s", c.effectiveTimeout())
	}
	return nil
}

func (c *Controller) effectivePollInterval() time.Duration {
	if c.pollInterval > 0 {
		return c.pollInterval
	}
	return defaultPollInterval
}

// waitReady polls url until it answers 2xx or ctx is done (the caller's
// own switch-timeout deadline).
func (c *Controller) waitReady(ctx context.Context, url string) bool {
	if c.ready.Ready(ctx, url) {
		return true
	}
	ticker := time.NewTicker(c.effectivePollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if c.ready.Ready(ctx, url) {
				return true
			}
		}
	}
}

// RunIdleRevertLoop blocks, periodically checking whether Vision mode
// has gone idle long enough to auto-revert to chat -- meant to run in
// its own goroutine for the process's lifetime. A non-positive
// idleRevertMinutes (the "0 disables the revert" convention) returns
// immediately without looping.
func (c *Controller) RunIdleRevertLoop(ctx context.Context, interval time.Duration) {
	if c.idleRevertMinutes <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.maybeRevert()
		}
	}
}

func (c *Controller) maybeRevert() {
	c.mu.Lock()
	shouldRevert := !c.inProgress && c.mode == ModeVision &&
		c.now().Sub(c.lastHeartbeat) >= time.Duration(c.idleRevertMinutes)*time.Minute
	if shouldRevert {
		c.inProgress = true
		c.target = ModeChat
		c.since = c.now()
		c.detail = "idle revert to chat"
	}
	c.mu.Unlock()
	if shouldRevert {
		go c.runSwitch(ModeChat)
	}
}
