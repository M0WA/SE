// Package httpchat implements ports.ChatCompleter by calling an
// OpenAI-compatible chat-completions HTTP endpoint (a local inference server
// or a hosted API), mirroring httpembed's own house style for calling an
// OpenAI-compatible endpoint.
package httpchat

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
)

// requestTimeout is Complete's default per-call deadline, used whenever
// endpoint.CompletionTimeoutSeconds is <= 0 (unset) -- generous since chat
// completions (unlike a short embeddings call) can genuinely take a while,
// especially against a CPU-only local model or a self-hosted GPU host
// under concurrent load. Raised from an original 60s after a live report
// of "context deadline exceeded (Client.Timeout exceeded while awaiting
// headers)" against a self-hosted vLLM instance during a long, genuinely
// slow generation (not a stuck/hung request) -- 60s was too tight for a
// real answer, not just a degenerate one. See mcp-vision's own
// captionCallTimeout for the same class of fix on the separate
// vision-captioning HTTP call.
//
// Raised again from 180s to match nginx's own proxy_read_timeout
// (packaging/nginx/searchengine.conf, 300s on the "/" location that
// proxies /chat) -- a single completion call being cut off at 180s while
// nginx itself would have tolerated up to 300s was a needless mismatch,
// especially now that one turn can chain multiple sequential completions
// (see completeDetectingLeakedToolCalls's own bounded retries plus the
// existing tool-calling follow-up loop) -- each individual call deserves
// the same runway nginx already grants the turn as a whole. Now also
// admin-configurable per endpoint (domain.ChatEndpoint.
// CompletionTimeoutSeconds) for a deployment whose own nginx timeout, or
// whose model's own typical latency, differs from this default.
const requestTimeout = 300 * time.Second

// maxHTTPClientTimeout is a generous safety ceiling on the shared
// *http.Client itself, well above any sane admin-configured
// CompletionTimeoutSeconds -- the actual, meaningful per-call deadline is
// always the context timeout Complete derives from the endpoint's own
// config (see completionTimeout), never this. This exists only so a
// context-wiring bug can't turn into a truly unbounded hang.
const maxHTTPClientTimeout = 30 * time.Minute

// maxResponseBytes caps how much of the HTTP response body is ever read --
// a safety bound against a misbehaving or malicious endpoint, not a real
// limit in practice for an ordinary chat completion.
const maxResponseBytes = 1 << 20

// Client calls an OpenAI-compatible POST {base_url}/chat/completions
// endpoint. Implements ports.ChatCompleter.
type Client struct {
	HTTPClient *http.Client
}

// New returns a Client with a sane default timeout when HTTPClient is left
// nil by the caller.
func New() *Client {
	return &Client{HTTPClient: defaultHTTPClient()}
}

// defaultHTTPClient is shared by New() and Complete/ModelMaxContextTokens'
// own nil-HTTPClient fallback -- its Transport routes every dial through
// netguard.ConfiguredEndpointDialContext, so even a redirect hop or a DNS
// answer that changes between check and connect can't land the connection
// on a blocked address (see the checkEndpointURL pre-request check below
// for why both layers exist).
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: maxHTTPClientTimeout, Transport: netguard.ConfiguredEndpointTransport()}
}

// completionTimeout resolves endpoint's own configured per-call deadline,
// falling back to requestTimeout when unset (<= 0).
func completionTimeout(endpoint domain.ChatEndpoint) time.Duration {
	if endpoint.CompletionTimeoutSeconds > 0 {
		return time.Duration(endpoint.CompletionTimeoutSeconds) * time.Second
	}
	return requestTimeout
}

// checkEndpointURL rejects a BaseURL-derived URL that
// netguard.AllowedConfiguredEndpointIP blocks (link-local -- e.g. cloud
// metadata services -- multicast, or unspecified). BaseURL is
// admin-trusted, but this still guards against it ever pointing at a
// cloud metadata endpoint, by mistake or a compromised admin session.
func checkEndpointURL(rawURL string) error {
	if !netguard.ConfiguredEndpointURLAllowed(rawURL) {
		return fmt.Errorf("httpchat: endpoint URL is not allowed: %s", rawURL)
	}
	return nil
}

// wireChatMessage/wireToolCall/wireToolDef are this adapter's own wire
// shapes for the OpenAI-compatible tool-calling convention -- kept separate
// from domain.ChatMessage/ToolCall/ToolDef since the wire format nests a
// tool call's name/arguments under a "function" object the domain layer
// has no business knowing about. toWireMessages/fromWireMessage/toWireTools
// translate.
type wireToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function wireToolCallFunction `json:"function"`
}

type wireChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

func toWireMessages(messages []domain.ChatMessage) []wireChatMessage {
	out := make([]wireChatMessage, len(messages))
	for i, m := range messages {
		out[i] = wireChatMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			out[i].ToolCalls = append(out[i].ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
	}
	return out
}

func fromWireMessage(w wireChatMessage) domain.ChatMessage {
	m := domain.ChatMessage{Role: w.Role, Content: w.Content, ToolCallID: w.ToolCallID}
	for _, tc := range w.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, domain.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return m
}

type wireFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireToolDef struct {
	Type     string          `json:"type"`
	Function wireFunctionDef `json:"function"`
}

// toWireTools returns nil, not an empty slice, for an empty tools list, so
// "tools,omitempty" actually omits the field -- some OpenAI-compatible
// servers reject an empty tools array or a tool_choice with no tools.
func toWireTools(tools []domain.ToolDef) []wireToolDef {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireToolDef, len(tools))
	for i, t := range tools {
		out[i] = wireToolDef{Type: "function", Function: wireFunctionDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters}}
	}
	return out
}

type chatCompletionRequest struct {
	Model      string            `json:"model"`
	Messages   []wireChatMessage `json:"messages"`
	Tools      []wireToolDef     `json:"tools,omitempty"`
	ToolChoice string            `json:"tool_choice,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message wireChatMessage `json:"message"`
	} `json:"choices"`
}

// Complete POSTs to {BaseURL}/chat/completions, parses an
// OpenAI-compatible response, and returns the first choice's message
// (content and/or tool_calls). A non-2xx status or empty choices is an error.
func (c *Client) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage, tools []domain.ToolDef) (domain.ChatMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, completionTimeout(endpoint))
	defer cancel()

	reqPayload := chatCompletionRequest{Model: endpoint.Model, Messages: toWireMessages(messages), Tools: toWireTools(tools)}
	if len(tools) > 0 {
		reqPayload.ToolChoice = "auto"
	}
	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: encoding request: %w", err)
	}

	url := strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions"
	if err := checkEndpointURL(url); err != nil {
		return domain.ChatMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	client := c.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: calling chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: chat completions endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), endpoint.APIKey), 500))
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: decoding response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return domain.ChatMessage{}, fmt.Errorf("httpchat: chat completions endpoint returned no choices")
	}
	return fromWireMessage(parsed.Choices[0].Message), nil
}

type modelInfo struct {
	ID          string `json:"id"`
	MaxModelLen int    `json:"max_model_len"`
}

type modelsListResponse struct {
	Data []modelInfo `json:"data"`
}

// ModelMaxContextTokens GETs {BaseURL}/models and returns the entry
// matching endpoint.Model's max context length, as vLLM reports via
// "max_model_len" -- auto-fills ChatEndpoint.MaxContextTokens instead of
// requiring an admin to hand-type it.
//
// ok is false, not an error, when the endpoint just doesn't report this
// (best-effort, never required for chat to work). Falls back to the
// response's first entry if none match by exact ID.
func (c *Client) ModelMaxContextTokens(ctx context.Context, endpoint domain.ChatEndpoint) (int, bool, error) {
	url := strings.TrimRight(endpoint.BaseURL, "/") + "/models"
	if err := checkEndpointURL(url); err != nil {
		return 0, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false, fmt.Errorf("httpchat: building models request: %w", err)
	}
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	client := c.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("httpchat: calling models endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, false, fmt.Errorf("httpchat: reading models response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false, fmt.Errorf("httpchat: models endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), endpoint.APIKey), 500))
	}

	var parsed modelsListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false, fmt.Errorf("httpchat: decoding models response: %w", err)
	}
	for _, m := range parsed.Data {
		if m.ID == endpoint.Model && m.MaxModelLen > 0 {
			return m.MaxModelLen, true, nil
		}
	}
	if len(parsed.Data) > 0 && parsed.Data[0].MaxModelLen > 0 {
		return parsed.Data[0].MaxModelLen, true, nil
	}
	return 0, false, nil
}
