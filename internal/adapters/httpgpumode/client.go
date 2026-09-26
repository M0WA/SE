// Package httpgpumode implements ports.GPUModeController by calling
// cmd/gpu-control's own small HTTP API (GET/POST /gpu/api/mode, POST
// /gpu/api/heartbeat) over the private LAN, mirroring httpchat's own
// house style for calling an admin-configured HTTP endpoint.
package httpgpumode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"searchengine/internal/adapters/netguard"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// requestTimeout bounds a plain status/heartbeat call -- short, since
// these never wait on the switch itself (POST /gpu/api/mode returns as
// soon as cmd/gpu-control accepts the request, not once the switch
// finishes -- see that package's own Controller.Switch doc comment).
const requestTimeout = 10 * time.Second

// maxResponseBytes caps how much of the HTTP response body is ever read
// -- a safety bound against a misbehaving endpoint, not a real limit in
// practice for these small JSON responses.
const maxResponseBytes = 1 << 16

// Client calls cmd/gpu-control's HTTP API. Implements ports.GPUModeController.
type Client struct {
	HTTPClient *http.Client
}

// New returns a Client with a sane default timeout/transport when
// HTTPClient is left nil by the caller.
func New() *Client {
	return &Client{HTTPClient: defaultHTTPClient()}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: requestTimeout, Transport: netguard.ConfiguredEndpointTransport()}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient()
}

// checkEndpointURL rejects a ControlBaseURL-derived URL that
// netguard.ConfiguredEndpointURLAllowed blocks (link-local, multicast, or
// unspecified) -- ControlBaseURL is admin-trusted, but this still guards
// against it ever pointing somewhere it shouldn't, by mistake or a
// compromised admin session.
func checkEndpointURL(rawURL string) error {
	if !netguard.ConfiguredEndpointURLAllowed(rawURL) {
		return fmt.Errorf("httpgpumode: endpoint URL is not allowed: %s", rawURL)
	}
	return nil
}

// wireStatus is cmd/gpu-control's own Status wire shape.
type wireStatus struct {
	Mode       domain.GPUMode `json:"mode"`
	Target     domain.GPUMode `json:"target,omitempty"`
	InProgress bool           `json:"in_progress"`
	Since      time.Time      `json:"since,omitempty"`
	ExpiresAt  time.Time      `json:"expires_at,omitempty"`
	Detail     string         `json:"detail,omitempty"`
}

func (w wireStatus) toDomain() domain.GPUModeStatus {
	return domain.GPUModeStatus{
		Mode: w.Mode, Target: w.Target, InProgress: w.InProgress,
		Since: w.Since, ExpiresAt: w.ExpiresAt, Detail: w.Detail,
	}
}

// do sends method/path against cfg.ControlBaseURL with the shared
// X-Internal-Token header and, for a non-nil body, a JSON payload --
// shared by every call below. acceptedCodes lists every status this
// particular call treats as success (POST /gpu/api/mode legitimately
// returns 200/202/409, each meaningful, not just 2xx) -- every caller
// passes at least one.
func (c *Client) do(ctx context.Context, cfg domain.GPUModeSettings, method, path string, body any, acceptedCodes ...int) (wireStatus, int, error) {
	url := strings.TrimRight(cfg.ControlBaseURL, "/") + path
	if err := checkEndpointURL(url); err != nil {
		return wireStatus{}, 0, err
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return wireStatus{}, 0, fmt.Errorf("httpgpumode: encoding request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return wireStatus{}, 0, fmt.Errorf("httpgpumode: building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Internal-Token", cfg.ControlAPIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return wireStatus{}, 0, fmt.Errorf("httpgpumode: calling control service: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return wireStatus{}, resp.StatusCode, fmt.Errorf("httpgpumode: reading response body: %w", err)
	}

	accepted := false
	for _, code := range acceptedCodes {
		if resp.StatusCode == code {
			accepted = true
			break
		}
	}
	if !accepted {
		return wireStatus{}, resp.StatusCode, fmt.Errorf("httpgpumode: control service returned status %d: %s",
			resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(respBody), cfg.ControlAPIKey), 500))
	}
	if len(respBody) == 0 {
		return wireStatus{}, resp.StatusCode, nil
	}
	var parsed wireStatus
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return wireStatus{}, resp.StatusCode, fmt.Errorf("httpgpumode: decoding response: %w", err)
	}
	return parsed, resp.StatusCode, nil
}

// Status GETs /gpu/api/mode.
func (c *Client) Status(ctx context.Context, cfg domain.GPUModeSettings) (domain.GPUModeStatus, error) {
	st, _, err := c.do(ctx, cfg, http.MethodGet, "/gpu/api/mode", nil, http.StatusOK)
	if err != nil {
		return domain.GPUModeStatus{}, err
	}
	return st.toDomain(), nil
}

type modeRequest struct {
	Mode domain.GPUMode `json:"mode"`
}

// Switch POSTs /gpu/api/mode -- 200 (already there) and 202 (accepted,
// possibly already in flight toward the same target) both return the
// status with no error; 409 (busy with a different target) returns
// ports.ErrGPUModeSwitchConflict (wrapped) alongside the status
// describing what it's busy with.
func (c *Client) Switch(ctx context.Context, cfg domain.GPUModeSettings, target domain.GPUMode) (domain.GPUModeStatus, error) {
	st, code, err := c.do(ctx, cfg, http.MethodPost, "/gpu/api/mode", modeRequest{Mode: target},
		http.StatusOK, http.StatusAccepted, http.StatusConflict)
	if err != nil {
		return domain.GPUModeStatus{}, err
	}
	if code == http.StatusConflict {
		return st.toDomain(), fmt.Errorf("httpgpumode: a switch to %s is already in progress: %w", st.Target, ports.ErrGPUModeSwitchConflict)
	}
	return st.toDomain(), nil
}

// Heartbeat POSTs /gpu/api/heartbeat, resetting cmd/gpu-control's own
// idle-revert timer.
func (c *Client) Heartbeat(ctx context.Context, cfg domain.GPUModeSettings) error {
	_, _, err := c.do(ctx, cfg, http.MethodPost, "/gpu/api/heartbeat", nil, http.StatusNoContent)
	return err
}
