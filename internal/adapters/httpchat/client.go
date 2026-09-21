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

// requestTimeout is the default HTTP client timeout used when Client is
// constructed via New() with no HTTPClient override -- generous since chat
// completions (unlike a short embeddings call) can genuinely take a while,
// especially against a CPU-only local model.
const requestTimeout = 60 * time.Second

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
	return &http.Client{Timeout: requestTimeout, Transport: netguard.ConfiguredEndpointTransport()}
}

// checkEndpointURL rejects a BaseURL-derived request URL that resolves to
// an address netguard.AllowedConfiguredEndpointIP blocks (link-local --
// covering every cloud provider's metadata service -- multicast, or
// unspecified). endpoint.BaseURL is admin-configured, trusted the same way
// any other stored config is, but this still guards a real self-hosted
// deployment against ever pointing it at its own cloud metadata endpoint,
// whether by admin mistake or a compromised admin session.
func checkEndpointURL(rawURL string) error {
	if !netguard.ConfiguredEndpointURLAllowed(rawURL) {
		return fmt.Errorf("httpchat: endpoint URL is not allowed: %s", rawURL)
	}
	return nil
}

type chatCompletionRequest struct {
	Model    string               `json:"model"`
	Messages []domain.ChatMessage `json:"messages"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message domain.ChatMessage `json:"message"`
	} `json:"choices"`
}

// Complete POSTs {"model": endpoint.Model, "messages": messages} to
// strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions", parses an
// OpenAI-compatible response body, and returns the first choice's message
// content. A non-2xx status or an empty choices array is an error.
func (c *Client) Complete(ctx context.Context, endpoint domain.ChatEndpoint, messages []domain.ChatMessage) (string, error) {
	reqBody, err := json.Marshal(chatCompletionRequest{Model: endpoint.Model, Messages: messages})
	if err != nil {
		return "", fmt.Errorf("httpchat: encoding request: %w", err)
	}

	url := strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions"
	if err := checkEndpointURL(url); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("httpchat: building request: %w", err)
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
		return "", fmt.Errorf("httpchat: calling chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("httpchat: reading response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("httpchat: chat completions endpoint returned status %d: %s", resp.StatusCode, domain.TruncateWithEllipsis(domain.RedactSecret(string(body), endpoint.APIKey), 500))
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("httpchat: decoding response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("httpchat: chat completions endpoint returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

type modelInfo struct {
	ID          string `json:"id"`
	MaxModelLen int    `json:"max_model_len"`
}

type modelsListResponse struct {
	Data []modelInfo `json:"data"`
}

// ModelMaxContextTokens GETs strings.TrimRight(endpoint.BaseURL, "/") +
// "/models" (the OpenAI-compatible model-listing endpoint) and returns the
// entry matching endpoint.Model's own advertised maximum context length,
// as vLLM reports it via a "max_model_len" field on each entry -- used to
// auto-fill ChatEndpoint.MaxContextTokens rather than requiring an admin to
// hand-type (and keep in sync with the model's real limit) a number.
//
// ok is false, not an error, whenever the endpoint simply doesn't report
// this (an OpenAI-compatible server that isn't vLLM, or one with no
// matching/positive max_model_len) -- this is best-effort auto-detection,
// never a hard requirement for chat to work. Falls back to the response's
// first entry if none match endpoint.Model by exact ID (a server serving
// exactly one model under a different alias still gets detected).
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
