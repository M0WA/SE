package httpgpumode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type generateRequest struct {
	Prompt          string  `json:"prompt"`
	AspectRatio     string  `json:"aspect_ratio,omitempty"`
	DurationSeconds int     `json:"duration_seconds,omitempty"`
	Megapixels      float64 `json:"megapixels,omitempty"`
	NegativePrompt  string  `json:"negative_prompt,omitempty"`
	EnhancePrompt   bool    `json:"enhance_prompt,omitempty"`
}

type generateResponse struct {
	PromptID string `json:"prompt_id"`
}

// Generate POSTs /gpu/api/generate.
func (c *Client) Generate(ctx context.Context, cfg domain.GPUModeSettings, opts domain.VisionGenerateOptions) (string, error) {
	url := strings.TrimRight(cfg.ControlBaseURL, "/") + "/gpu/api/generate"
	if err := checkEndpointURL(url); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(generateRequest{
		Prompt:          opts.Prompt,
		AspectRatio:     opts.AspectRatio,
		DurationSeconds: opts.DurationSeconds,
		Megapixels:      opts.Megapixels,
		NegativePrompt:  opts.NegativePrompt,
		EnhancePrompt:   opts.EnhancePrompt,
	})
	if err != nil {
		return "", fmt.Errorf("httpgpumode: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return "", fmt.Errorf("httpgpumode: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", cfg.ControlAPIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("httpgpumode: calling control service: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("httpgpumode: reading response body: %w", err)
	}
	if resp.StatusCode == http.StatusConflict {
		return "", fmt.Errorf("httpgpumode: %w", ports.ErrGPUGenerateNotInVisionMode)
	}
	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("httpgpumode: control service returned status %d: %s",
			resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), cfg.ControlAPIKey), 500))
	}
	var parsed generateResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("httpgpumode: decoding response: %w", err)
	}
	return parsed.PromptID, nil
}

type generateResultResponse struct {
	Status  string `json:"status"`
	ViewURL string `json:"view_url,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GenerateResult GETs /gpu/api/generate/{jobID}.
func (c *Client) GenerateResult(ctx context.Context, cfg domain.GPUModeSettings, jobID string) (domain.GPUGenerateResult, error) {
	url := strings.TrimRight(cfg.ControlBaseURL, "/") + "/gpu/api/generate/" + jobID
	if err := checkEndpointURL(url); err != nil {
		return domain.GPUGenerateResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return domain.GPUGenerateResult{}, fmt.Errorf("httpgpumode: building request: %w", err)
	}
	req.Header.Set("X-Internal-Token", cfg.ControlAPIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return domain.GPUGenerateResult{}, fmt.Errorf("httpgpumode: calling control service: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return domain.GPUGenerateResult{}, fmt.Errorf("httpgpumode: reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return domain.GPUGenerateResult{}, fmt.Errorf("httpgpumode: control service returned status %d: %s",
			resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), cfg.ControlAPIKey), 500))
	}
	var parsed generateResultResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return domain.GPUGenerateResult{}, fmt.Errorf("httpgpumode: decoding response: %w", err)
	}
	return domain.GPUGenerateResult{Status: parsed.Status, ViewURL: parsed.ViewURL, Error: parsed.Error}, nil
}

// comfyViewURLPrefix is the only prefix ViewAsset ever accepts for
// viewURL -- it's meant to be exactly what GenerateResult's own
// ViewURL field returned (this service's own computed value, never
// arbitrary user input), and this guards against ever fetching anything
// else even if that assumption is ever violated by a caller bug.
const comfyViewURLPrefix = "/gpu/api/view?"

// ViewAsset GETs viewURL (as returned by GenerateResult, e.g.
// "/gpu/api/view?filename=...") against cfg.ControlBaseURL, streaming
// the response straight through -- the caller must close body. Used to
// serve a finished generation's video bytes to a browser without ever
// exposing cmd/gpu-control (or ComfyUI behind it) directly.
func (c *Client) ViewAsset(ctx context.Context, cfg domain.GPUModeSettings, viewURL string) (contentType string, body io.ReadCloser, err error) {
	if !strings.HasPrefix(viewURL, comfyViewURLPrefix) {
		return "", nil, fmt.Errorf("httpgpumode: unexpected view URL %q", viewURL)
	}
	url := strings.TrimRight(cfg.ControlBaseURL, "/") + viewURL
	if err := checkEndpointURL(url); err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, fmt.Errorf("httpgpumode: building request: %w", err)
	}
	req.Header.Set("X-Internal-Token", cfg.ControlAPIKey)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("httpgpumode: calling control service: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return "", nil, fmt.Errorf("httpgpumode: control service returned status %d: %s",
			resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(errBody), cfg.ControlAPIKey), 500))
	}
	return resp.Header.Get("Content-Type"), resp.Body, nil
}
