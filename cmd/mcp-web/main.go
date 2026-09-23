// Command mcp-web is a first-party MCP server exposing "web_search"
// (proxies to a self-hosted SearXNG instance) and "web_fetch" (fetches a
// URL's text, guarded against SSRF). Spawned as a stdio subprocess by
// internal/adapters/mcpclient, not a systemd service.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// searchTimeout bounds a single SearXNG proxy call.
const searchTimeout = 10 * time.Second

// maxSearchResponseBytes caps how much of a SearXNG response is ever
// read -- a safety bound against a misbehaving instance.
const maxSearchResponseBytes = 1 << 20

type searchArgs struct {
	Query string `json:"query" jsonschema:"the search query"`
}

type fetchArgs struct {
	URL string `json:"url" jsonschema:"the URL to fetch"`
}

func main() {
	// WEB_SEARCH_BASE_URL is set by mcpclient.Provider on every spawned
	// stdio server, sourced from domain.ChatEndpoint.WebSearchBaseURL.
	// Falls back to 127.0.0.1:8888 if unset (e.g. standalone testing).
	searxBaseURL := bootstrap.GetEnv("WEB_SEARCH_BASE_URL", "http://127.0.0.1:8888")
	// WEB_SEARCH_RESULT_COUNT/WEB_FETCH_USER_AGENT are set the same way.
	// Both optional: unset/unparseable result count means no cap (0), and
	// an unset user agent leaves the fetcher's configured default in place.
	resultCount, _ := strconv.Atoi(bootstrap.GetEnv("WEB_SEARCH_RESULT_COUNT", "0"))
	userAgent := bootstrap.GetEnv("WEB_FETCH_USER_AGENT", "")
	fetcher := httpfetcher.New(domain.DefaultOperationalSettings())
	server := newServer(searxBaseURL, resultCount, userAgent, fetcher)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the mcp.Server exposing "web_search"/"web_fetch",
// factored out of main so a test can connect via an in-memory transport
// instead of a real stdio subprocess.
func newServer(searxBaseURL string, resultCount int, userAgent string, fetcher *httpfetcher.Fetcher) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-web", Version: "1"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "web_search",
		Description: "Search the web for current information on a topic. Use this when the user asks about " +
			"something that could have changed -- current events, prices, versions, schedules, who holds a " +
			"position, or anything time-sensitive -- even if you feel confident. The returned snippets are " +
			"unverified and must never be trusted or cited on their own -- always fetch the actual page(s) " +
			"with web_fetch before answering, and cross-check the information across multiple independent " +
			"results when possible, since search snippets can be outdated, truncated, or simply wrong.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, any, error) {
		text, err := webSearch(ctx, searxBaseURL, args.Query, resultCount)
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "web_fetch",
		Description: "Fetch the text content of a specific URL. Use this when the user asks about a specific " +
			"URL or its content, even if you feel confident or were given unrelated search results -- those " +
			"are not the page itself.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args fetchArgs) (*mcp.CallToolResult, any, error) {
		text, err := fetcher.FetchWithOptions(ctx, args.URL, ports.FetchOptions{UserAgent: userAgent})
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})

	return server
}

// webSearch proxies query to the configured SearXNG instance's JSON search
// API (GET {base}/search?q=...&format=json over loopback -- see
// packaging/searxng/README.md), returning the response body as text. When
// resultCount is positive, "results" is truncated to that many entries
// first (see capResults) -- SearXNG's API has no param for this itself.
func webSearch(ctx context.Context, baseURL, query string, resultCount int) (string, error) {
	u := strings.TrimRight(baseURL, "/") + "/search?" + url.Values{
		"q":      {query},
		"format": {"json"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("building search request: %w", err)
	}
	client := &http.Client{Timeout: searchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling SearXNG: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchResponseBytes))
	if err != nil {
		return "", fmt.Errorf("reading SearXNG response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("SearXNG returned status %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return capResults(body, resultCount), nil
}

// capResults truncates body's top-level "results" array to at most
// resultCount entries, returning body unmodified when resultCount isn't
// positive or it doesn't parse as the expected shape.
func capResults(body []byte, resultCount int) string {
	if resultCount <= 0 {
		return string(body)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err != nil {
		return string(body)
	}
	rawResults, ok := parsed["results"]
	if !ok {
		return string(body)
	}
	var results []json.RawMessage
	if err := json.Unmarshal(rawResults, &results); err != nil {
		return string(body)
	}
	if len(results) <= resultCount {
		return string(body)
	}
	capped, err := json.Marshal(results[:resultCount])
	if err != nil {
		return string(body)
	}
	parsed["results"] = capped
	out, err := json.Marshal(parsed)
	if err != nil {
		return string(body)
	}
	return string(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "... (truncated)"
}
