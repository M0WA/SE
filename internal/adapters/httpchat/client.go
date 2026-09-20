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
	return &Client{HTTPClient: &http.Client{Timeout: requestTimeout}}
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
		client = &http.Client{Timeout: requestTimeout}
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
