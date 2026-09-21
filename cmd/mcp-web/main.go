// Command mcp-web is a first-party MCP (Model Context Protocol) server
// exposing "web_search" (proxies to a self-hosted SearXNG instance) and
// "web_fetch" (fetches a URL's text content, guarded against SSRF) as
// native tool-calling tools -- the replacement for the old
// packaging/chat-hooks/web_search.sh/web_fetch.sh shell scripts, spawned
// as a stdio subprocess by internal/adapters/mcpclient (see
// domain.MCPServer's Transport="stdio" configuration) rather than run as a
// systemd service.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/httpfetcher"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

// searchTimeout bounds a single SearXNG proxy call.
const searchTimeout = 10 * time.Second

// maxSearchResponseBytes caps how much of a SearXNG response is ever read
// -- a safety bound against a misbehaving instance, mirrors
// web_search.sh's own implicit trust in a fixed, admin-controlled local
// instance while still not reading an unbounded body.
const maxSearchResponseBytes = 1 << 20

type searchArgs struct {
	Query string `json:"query" jsonschema:"the search query"`
}

type fetchArgs struct {
	URL string `json:"url" jsonschema:"the URL to fetch"`
}

func main() {
	// WEB_SEARCH_BASE_URL is set unconditionally by mcpclient.Provider on
	// every spawned "stdio" server -- see ports.MCPToolProvider's own doc
	// comment -- sourced server-side from the SAME admin-configured
	// domain.ChatEndpoint.WebSearchBaseURL the deterministic web-search
	// context injection already uses, so the two never drift out of sync.
	// Falls back to 127.0.0.1:8888 if somehow unset (e.g. invoked
	// standalone for local testing), matching web_search.sh's own default.
	searxBaseURL := bootstrap.GetEnv("WEB_SEARCH_BASE_URL", "http://127.0.0.1:8888")
	fetcher := httpfetcher.New(domain.DefaultOperationalSettings())
	server := newServer(searxBaseURL, fetcher)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// newServer builds the mcp.Server exposing "web_search"/"web_fetch",
// factored out of main so a test can connect to it directly over an
// in-memory transport (mcp.NewInMemoryTransports) instead of exercising it
// only via a real stdio subprocess.
func newServer(searxBaseURL string, fetcher *httpfetcher.Fetcher) *mcp.Server {
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
		text, err := webSearch(ctx, searxBaseURL, args.Query)
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
		text, err := fetcher.Fetch(ctx, args.URL)
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
// packaging/searxng/README.md for how that instance is set up and why
// search.formats must include json), returning the raw JSON response body
// as text -- the model reads it directly, same as web_search.sh's raw
// stdout did.
func webSearch(ctx context.Context, baseURL, query string) (string, error) {
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
	return string(body), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "... (truncated)"
}
