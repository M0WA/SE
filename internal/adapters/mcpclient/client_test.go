package mcpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/mcpclient"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// TestHelperMCPServer is not a real test -- it's a stdio MCP server this
// file's own tests spawn as a SUBPROCESS by re-executing the compiled test
// binary itself (os.Args[0]) with "-test.run=TestHelperMCPServer" and the
// MCP_TEST_STDIO_HELPER=1 marker env var (see stdioServerCommand). Every
// normal `go test` run skips this immediately, since that env var is unset.
// Mirrors the standard library's own os/exec "TestHelperProcess" pattern --
// the only realistic way to exercise mcpclient's real "stdio" transport
// (spawn a real child process and speak MCP over its stdin/stdout) without
// depending on a separately-built binary.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv("MCP_TEST_STDIO_HELPER") != "1" {
		t.Skip("not invoked as the stdio helper subprocess")
	}

	type echoArgs struct {
		Message string `json:"message" jsonschema:"message to echo back"`
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test-helper", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echoes the message back, with a configured env var appended."},
		func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			text := args.Message + "|" + os.Getenv("MCP_TEST_ENV_VALUE")
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "always_error", Description: "Always returns a tool-level error."},
		func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}}, nil, nil
		})

	// server.Run blocks until stdin is closed (the client disconnecting) --
	// os.Exit afterward skips the normal go test summary line, which would
	// otherwise corrupt the JSON-RPC stream on stdout if printed first.
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

// stdioServerCommand returns the Command/Args that make mcpclient spawn
// THIS test binary and land in TestHelperMCPServer's real branch --
// env["MCP_TEST_STDIO_HELPER"]="1" (merged into the child's environment by
// Provider.Open/connect, same as any other admin-configured env) is what
// actually selects that branch.
func stdioServerCommand() (string, []string) {
	return os.Args[0], []string{"-test.run=TestHelperMCPServer"}
}

func TestProvider_Open_StdioTool_CallSucceeds(t *testing.T) {
	command, args := stdioServerCommand()
	server := domain.MCPServer{ID: "s1", Name: "helper", Transport: "stdio", Command: command, Args: args, Enabled: true}

	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{server}, map[string]string{
		"MCP_TEST_STDIO_HELPER": "1",
		"MCP_TEST_ENV_VALUE":    "hello-env",
	})
	t.Cleanup(session.Close)

	if len(tools) != 2 {
		t.Fatalf("expected 2 discovered tools, got %+v", tools)
	}
	var found bool
	for _, tool := range tools {
		if tool.Name == "echo" {
			found = true
			if tool.ServerID != "s1" || tool.ServerName != "helper" {
				t.Errorf("expected the tool tagged with its origin server, got %+v", tool)
			}
		}
	}
	if !found {
		t.Fatalf("expected an 'echo' tool discovered, got %+v", tools)
	}

	out, err := session.CallTool(context.Background(), "echo", `{"message":"hi"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hi|hello-env" {
		t.Errorf("expected the echoed message plus the env var value, got %q", out)
	}
}

func TestProvider_Open_Disabled_SkippedEntirely(t *testing.T) {
	command, args := stdioServerCommand()
	server := domain.MCPServer{ID: "s1", Name: "helper", Transport: "stdio", Command: command, Args: args, Enabled: false}

	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{server}, nil)
	t.Cleanup(session.Close)

	if len(tools) != 0 {
		t.Fatalf("expected no tools discovered for a disabled server, got %+v", tools)
	}
}

func TestProvider_Open_UnknownTransport_SkippedNotFatal(t *testing.T) {
	server := domain.MCPServer{ID: "s1", Name: "bad", Transport: "carrier-pigeon", Enabled: true}

	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{server}, nil)
	t.Cleanup(session.Close)

	if session == nil {
		t.Fatal("expected a non-nil session even when every server fails to connect")
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools for an unknown transport, got %+v", tools)
	}
}

func TestProvider_Open_ConnectionFailure_SkippedNotFatal(t *testing.T) {
	server := domain.MCPServer{ID: "s1", Name: "missing", Transport: "stdio", Command: "/no/such/binary-xyz", Enabled: true}

	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{server}, nil)
	t.Cleanup(session.Close)

	if len(tools) != 0 {
		t.Fatalf("expected no tools when the command can't even be spawned, got %+v", tools)
	}
}

// TestProvider_Open_ToolNameCollision_LaterServerSkipped proves two active
// servers exposing the SAME tool name never silently misroute -- the
// later-discovered one is skipped (logged), keeping the first server's tool.
func TestProvider_Open_ToolNameCollision_LaterServerSkipped(t *testing.T) {
	command, args := stdioServerCommand()
	first := domain.MCPServer{ID: "s1", Name: "first", Transport: "stdio", Command: command, Args: args, Enabled: true}
	second := domain.MCPServer{ID: "s2", Name: "second", Transport: "stdio", Command: command, Args: args, Enabled: true}

	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{first, second}, map[string]string{
		"MCP_TEST_STDIO_HELPER": "1",
	})
	t.Cleanup(session.Close)

	echoCount := 0
	var owner string
	for _, tool := range tools {
		if tool.Name == "echo" {
			echoCount++
			owner = tool.ServerID
		}
	}
	if echoCount != 1 {
		t.Fatalf("expected the colliding tool listed exactly once, got %d", echoCount)
	}
	if owner != "s1" {
		t.Errorf("expected the FIRST server's tool kept on collision, got owner %q", owner)
	}
}

func TestSession_CallTool_UnknownToolName(t *testing.T) {
	p := mcpclient.New()
	session, _ := p.Open(context.Background(), nil, nil)
	t.Cleanup(session.Close)

	_, err := session.CallTool(context.Background(), "no_such_tool", "")
	if err == nil {
		t.Fatal("expected an error for an unrouteable tool name")
	}
}

func TestSession_CallTool_ToolLevelErrorSurfaced(t *testing.T) {
	command, args := stdioServerCommand()
	server := domain.MCPServer{ID: "s1", Name: "helper", Transport: "stdio", Command: command, Args: args, Enabled: true}

	p := mcpclient.New()
	session, _ := p.Open(context.Background(), []domain.MCPServer{server}, map[string]string{"MCP_TEST_STDIO_HELPER": "1"})
	t.Cleanup(session.Close)

	_, err := session.CallTool(context.Background(), "always_error", `{"message":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the tool's own error text surfaced, got %v", err)
	}
}

func TestSession_CallTool_InvalidArgumentsJSON(t *testing.T) {
	command, args := stdioServerCommand()
	server := domain.MCPServer{ID: "s1", Name: "helper", Transport: "stdio", Command: command, Args: args, Enabled: true}

	p := mcpclient.New()
	session, _ := p.Open(context.Background(), []domain.MCPServer{server}, map[string]string{"MCP_TEST_STDIO_HELPER": "1"})
	t.Cleanup(session.Close)

	_, err := session.CallTool(context.Background(), "echo", "{not json")
	if err == nil {
		t.Fatal("expected an error for arguments that aren't valid JSON")
	}
}

func TestProvider_Open_HTTPTransport_BearerHeaderSent(t *testing.T) {
	var gotAuth string
	server := mcp.NewServer(&mcp.Implementation{Name: "http-helper", Version: "1"}, nil)
	type pingArgs struct{}
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "Replies pong."},
		func(ctx context.Context, req *mcp.CallToolRequest, args pingArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	mcpServer := domain.MCPServer{ID: "s1", Name: "remote", Transport: "http", BaseURL: srv.URL, APIKey: "sk-test", Enabled: true}
	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{mcpServer}, nil)
	t.Cleanup(session.Close)

	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("expected the 'ping' tool discovered over http, got %+v", tools)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("expected the configured API key sent as a Bearer token, got %q", gotAuth)
	}

	out, err := session.CallTool(context.Background(), "ping", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "pong" {
		t.Errorf("expected 'pong', got %q", out)
	}
}

func TestProvider_Open_HTTPTransport_BlockedURL_SkippedNotFatal(t *testing.T) {
	mcpServer := domain.MCPServer{ID: "s1", Name: "metadata", Transport: "http", BaseURL: "http://169.254.169.254/", Enabled: true}
	p := mcpclient.New()
	session, tools := p.Open(context.Background(), []domain.MCPServer{mcpServer}, nil)
	t.Cleanup(session.Close)

	if len(tools) != 0 {
		t.Fatalf("expected no tools discovered from a blocked (link-local/metadata) endpoint, got %+v", tools)
	}
	if _, err := session.CallTool(context.Background(), "anything", "{}"); err == nil {
		t.Error("expected an error calling a tool that was never registered")
	}
}

var _ ports.MCPToolProvider = (*mcpclient.Provider)(nil)
var _ ports.MCPSession = (*mcpclient.Session)(nil)
