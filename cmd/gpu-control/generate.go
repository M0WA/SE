package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ltxT2VWorkflowTemplate is ComfyUI's own "Text to Video (LTX-2.5)"
// workflow template, already flattened into ComfyUI's executable API
// prompt format (node id -> {class_type, inputs}) -- extracted from a
// running ComfyUI instance's own frontend (which alone knows how to
// expand the template's subgraph and resolve every widget/link into that
// shape) via a headless-browser session calling its exposed
// `app.graphToPrompt()`, not hand-converted. Matches the exact models
// installed on gpu.mo-sys.de's ComfyUI (see packaging/gpu-control/README.md
// for the model list and how to re-extract this file if the template or
// installed models ever change).
//
//go:embed ltx_t2v_workflow.json
var ltxT2VWorkflowTemplate []byte

// positivePromptNodeID/positivePromptInputKey, seedNodeID/seedInputKey,
// aspectRatioNodeID/aspectRatioInputKey, durationNodeID/durationInputKey,
// megapixelsNodeID/megapixelsInputKey, negativePromptNodeID/
// negativePromptInputKey, and enhancePromptNodeID/enhancePromptInputKey
// name exactly which node+input this file overrides per request -- every
// other parameter (model, sampler, sigmas schedule) comes from the
// template's own fixed defaults. aspectRatioNodeID/megapixelsNodeID are
// the same ResolutionSelector node ("Resolution Selector" in the ComfyUI
// UI) -- aspect_ratio picks the shape, megapixels the actual output size.
// durationNodeID is the PrimitiveInt node titled "Duration" feeding the
// frame-count math node (frames = duration_seconds * frame_rate + 1,
// frame_rate itself fixed at the template's own 24fps). negativePromptNodeID
// is the CLIPTextEncode node holding the "what to avoid" text.
// enhancePromptNodeID is a PrimitiveBoolean gating a real switch node in
// the graph between the raw prompt and an LLM-rewritten version of it
// (TextGenerateLTX2Prompt) -- a genuine template feature, not something
// this file implements itself. saveVideoNodeID is the template's
// terminal SaveVideo node, whose entry in ComfyUI's own /history response
// names the resulting file.
const (
	positivePromptNodeID   = "405:376"
	positivePromptInputKey = "value"
	seedNodeID             = "405:339"
	seedInputKey           = "noise_seed"
	aspectRatioNodeID      = "409"
	aspectRatioInputKey    = "aspect_ratio"
	megapixelsNodeID       = "409"
	megapixelsInputKey     = "megapixels"
	durationNodeID         = "405:362"
	durationInputKey       = "value"
	negativePromptNodeID   = "405:373"
	negativePromptInputKey = "text"
	enhancePromptNodeID    = "405:383"
	enhancePromptInputKey  = "value"
	saveVideoNodeID        = "75"
)

// defaultAspectRatio/defaultDurationSeconds/defaultMegapixels match the
// template's own original baked-in values -- used whenever a caller
// leaves the corresponding field unset, so an old/minimal request body
// keeps behaving exactly as before this file gained per-request
// overrides. minMegapixels/maxMegapixels are deliberately narrower than
// ComfyUI's own ResolutionSelector range (0.1-16.0, confirmed live via
// GET /object_info/ResolutionSelector) -- this is video, not a single
// image, so every extra megapixel multiplies cost by the frame count
// too; 1.5 already roughly doubles the template's own 0.9 default.
const (
	defaultAspectRatio     = "16:9 (Widescreen)"
	defaultDurationSeconds = 5
	minDurationSeconds     = 3
	maxDurationSeconds     = 10
	defaultMegapixels      = 0.9
	minMegapixels          = 0.3
	maxMegapixels          = 1.5
)

// visionAspectRatios lists exactly the enum values ComfyUI's own
// ResolutionSelector node accepts for its aspect_ratio widget --
// confirmed live against gpu.mo-sys.de's ComfyUI via
// GET /object_info/ResolutionSelector. Anything else is rejected here
// rather than forwarded, since ComfyUI's own error for an invalid combo
// value is far less clear than this package's own.
var visionAspectRatios = map[string]bool{
	"1:1 (Square)":               true,
	"2:3 (Portrait Photo)":       true,
	"3:2 (Photo)":                true,
	"3:4 (Portrait Standard)":    true,
	"4:3 (Standard)":             true,
	"9:16 (Portrait Widescreen)": true,
	"16:9 (Widescreen)":          true,
	"21:9 (Ultrawide)":           true,
}

var (
	errEmptyPrompt        = errors.New("prompt must not be empty")
	errNotInVisionMode    = errors.New("the GPU is not in vision mode -- switch to vision first")
	errInvalidAspectRatio = errors.New("aspect_ratio must be one of the supported values")
	errInvalidDuration    = fmt.Errorf("duration_seconds must be between %d and %d", minDurationSeconds, maxDurationSeconds)
	errInvalidMegapixels  = fmt.Errorf("megapixels must be between %g and %g", minMegapixels, maxMegapixels)
)

// generateHTTPTimeout bounds the two short calls Generate/GenerateResult
// make to ComfyUI's own API (submitting a job, polling its history) --
// neither waits on the generation itself finishing, only on ComfyUI
// accepting the request or answering a status poll.
const generateHTTPTimeout = 15 * time.Second

func (c *Controller) generateHTTPClient() *http.Client {
	if c.genHTTP != nil {
		return c.genHTTP
	}
	return &http.Client{Timeout: generateHTTPTimeout}
}

type comfyPromptRequest struct {
	Prompt map[string]any `json:"prompt"`
}

type comfyPromptResponse struct {
	PromptID   string                     `json:"prompt_id"`
	NodeErrors map[string]json.RawMessage `json:"node_errors"`
}

// GenerateParams is Controller.Generate's own request shape -- a local
// mirror of domain.VisionGenerateOptions' fields (this binary never
// imports internal/domain -- see requireToken's own doc comment on why),
// kept in sync by convention the same way generateRequest/generateResponse
// already are between this package and internal/adapters/httpgpumode.
type GenerateParams struct {
	Prompt          string
	AspectRatio     string
	DurationSeconds int
	Megapixels      float64
	NegativePrompt  string
	EnhancePrompt   bool
}

// Generate submits a new text-to-video generation job to ComfyUI, using
// ltxT2VWorkflowTemplate with params' fields (plus a fresh random seed)
// substituted in. Every zero-valued field falls back to this file's own
// default* constant (the template's own original fixed values), so an
// older caller that only ever sent Prompt keeps getting identical
// behavior. Only valid while the controller's own mode is already
// ModeVision -- ComfyUI isn't even running otherwise.
func (c *Controller) Generate(ctx context.Context, params GenerateParams) (string, error) {
	if strings.TrimSpace(params.Prompt) == "" {
		return "", errEmptyPrompt
	}
	aspectRatio := params.AspectRatio
	if aspectRatio == "" {
		aspectRatio = defaultAspectRatio
	}
	if !visionAspectRatios[aspectRatio] {
		return "", errInvalidAspectRatio
	}
	durationSeconds := params.DurationSeconds
	if durationSeconds == 0 {
		durationSeconds = defaultDurationSeconds
	}
	if durationSeconds < minDurationSeconds || durationSeconds > maxDurationSeconds {
		return "", errInvalidDuration
	}
	megapixels := params.Megapixels
	if megapixels == 0 {
		megapixels = defaultMegapixels
	}
	if megapixels < minMegapixels || megapixels > maxMegapixels {
		return "", errInvalidMegapixels
	}
	c.mu.Lock()
	mode := c.mode
	c.mu.Unlock()
	if mode != ModeVision {
		return "", errNotInVisionMode
	}

	var graph map[string]any
	if err := json.Unmarshal(ltxT2VWorkflowTemplate, &graph); err != nil {
		return "", fmt.Errorf("decoding workflow template: %w", err)
	}
	if err := setNodeInput(graph, positivePromptNodeID, positivePromptInputKey, params.Prompt); err != nil {
		return "", err
	}
	if err := setNodeInput(graph, seedNodeID, seedInputKey, rand.Int64N(1<<62)); err != nil {
		return "", err
	}
	if err := setNodeInput(graph, aspectRatioNodeID, aspectRatioInputKey, aspectRatio); err != nil {
		return "", err
	}
	if err := setNodeInput(graph, durationNodeID, durationInputKey, durationSeconds); err != nil {
		return "", err
	}
	if err := setNodeInput(graph, megapixelsNodeID, megapixelsInputKey, megapixels); err != nil {
		return "", err
	}
	if params.NegativePrompt != "" {
		if err := setNodeInput(graph, negativePromptNodeID, negativePromptInputKey, params.NegativePrompt); err != nil {
			return "", err
		}
	}
	if err := setNodeInput(graph, enhancePromptNodeID, enhancePromptInputKey, params.EnhancePrompt); err != nil {
		return "", err
	}

	origin, err := comfyOrigin(c.comfyReadyURL)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(comfyPromptRequest{Prompt: graph})
	if err != nil {
		return "", fmt.Errorf("encoding prompt request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.String()+"/prompt", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.generateHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("calling ComfyUI: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ComfyUI returned %d: %s", resp.StatusCode, respBody)
	}
	var parsed comfyPromptResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("decoding ComfyUI response: %w", err)
	}
	if len(parsed.NodeErrors) > 0 {
		return "", fmt.Errorf("ComfyUI rejected the workflow: %s", mustMarshal(parsed.NodeErrors))
	}
	if parsed.PromptID == "" {
		return "", fmt.Errorf("ComfyUI did not return a prompt id")
	}
	return parsed.PromptID, nil
}

// setNodeInput overwrites graph[nodeID].inputs[inputKey] = value,
// erroring out rather than panicking if ltxT2VWorkflowTemplate's own
// shape ever doesn't match what this file expects (e.g. after
// re-extracting it from a changed ComfyUI template).
func setNodeInput(graph map[string]any, nodeID, inputKey string, value any) error {
	nodeRaw, ok := graph[nodeID]
	if !ok {
		return fmt.Errorf("workflow template missing node %q", nodeID)
	}
	node, ok := nodeRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("workflow template node %q has an unexpected shape", nodeID)
	}
	inputsRaw, ok := node["inputs"]
	if !ok {
		return fmt.Errorf("workflow template node %q has no inputs", nodeID)
	}
	inputs, ok := inputsRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("workflow template node %q inputs have an unexpected shape", nodeID)
	}
	inputs[inputKey] = value
	return nil
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// comfyOutputFile is one entry of ComfyUI's own /history response --
// whatever list key it appears under (images/gifs/videos/... varies by
// node type), the item shape is always this.
type comfyOutputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// comfyNodeOutput is one node's entry in /history's own "outputs" map --
// keyed by output-list name (varies by node type: SaveVideo's own real
// shape, confirmed live, is "images" holding the actual file plus a
// sibling "animated" key holding a plain bool array, not a file list at
// all) -- so every value is decoded generically here and only
// individually re-decoded into comfyOutputFile where that succeeds (see
// GenerateResult below), rather than assuming every key under a node
// holds a uniform file-list shape.
type comfyNodeOutput map[string][]json.RawMessage

type comfyHistoryEntry struct {
	Outputs map[string]comfyNodeOutput `json:"outputs"`
	Status  struct {
		Completed bool   `json:"completed"`
		StatusStr string `json:"status_str"`
	} `json:"status"`
}

// GenerateResult polls ComfyUI's own GET /history/{promptID}. Returns
// status "pending" while promptID hasn't finished yet (absent from
// history, or present but not yet completed), "done" with file set once
// saveVideoNodeID's output names a real file, or "failed" (with a
// non-nil error) if ComfyUI reports the job completed without ever
// producing one.
func (c *Controller) GenerateResult(ctx context.Context, promptID string) (status string, file comfyOutputFile, err error) {
	origin, err := comfyOrigin(c.comfyReadyURL)
	if err != nil {
		return "", comfyOutputFile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return "", comfyOutputFile{}, fmt.Errorf("building request: %w", err)
	}
	resp, err := c.generateHTTPClient().Do(req)
	if err != nil {
		return "", comfyOutputFile{}, fmt.Errorf("calling ComfyUI: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", comfyOutputFile{}, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", comfyOutputFile{}, fmt.Errorf("ComfyUI returned %d: %s", resp.StatusCode, body)
	}
	var history map[string]comfyHistoryEntry
	if err := json.Unmarshal(body, &history); err != nil {
		return "", comfyOutputFile{}, fmt.Errorf("decoding history response: %w", err)
	}
	entry, ok := history[promptID]
	if !ok {
		return "pending", comfyOutputFile{}, nil
	}
	for _, items := range entry.Outputs[saveVideoNodeID] {
		for _, item := range items {
			var f comfyOutputFile
			if err := json.Unmarshal(item, &f); err == nil && f.Filename != "" {
				return "done", f, nil
			}
		}
	}
	if entry.Status.Completed {
		return "failed", comfyOutputFile{}, fmt.Errorf("generation finished without producing a video (status: %s)", entry.Status.StatusStr)
	}
	return "pending", comfyOutputFile{}, nil
}
