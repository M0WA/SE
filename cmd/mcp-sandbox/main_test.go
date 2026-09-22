package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"searchengine/internal/adapters/dockersandbox"
)

// requireDockerTests mirrors internal/adapters/dockersandbox's own gate --
// these tests spawn real containers, so they're opt-in the same way.
func requireDockerTests(t *testing.T) {
	t.Helper()
	if os.Getenv("SE_DOCKER_TESTS") == "" {
		t.Skip("skipping: set SE_DOCKER_TESTS=1 to run real-Docker sandbox tests")
	}
}

// connectedTestServer wires newServer's mcp.Server to a real mcp.Client
// over an in-memory transport, mirroring cmd/mcp-web/cmd/mcp-datetime's
// own test helper -- exercises each tool exactly as a real chat turn
// would, through CallTool, not by calling runInSandbox directly.
func connectedTestServer(t *testing.T, runner *dockersandbox.Runner, network bool) *mcp.ClientSession {
	t.Helper()
	server := newServer(runner, network)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d: %+v", len(result.Content), result.Content)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return tc.Text
}

func TestRunPythonTool_Success(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "print('hi')"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.ExitCode != 0 || got.Stdout != "hi\n" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestRunGoTool_Success(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false)
	code := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_go",
		Arguments: map[string]any{"code": code},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.ExitCode != 0 || got.Stdout != "hi\n" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestRunPythonTool_NonZeroExitIsNotAToolError(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "import sys\nsys.exit(3)\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected a non-zero exit to be reported as ordinary content, not a tool error: %s", textContent(t, result))
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.ExitCode != 3 {
		t.Errorf("expected exit_code 3, got %+v", got)
	}
}

func TestRunPythonTool_InfrastructureFailureIsToolError(t *testing.T) {
	requireDockerTests(t)
	t.Setenv("PATH", t.TempDir())
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "print(1)"},
	})
	if err != nil {
		t.Fatalf("unexpected transport-level error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an infrastructure failure (docker missing) to be a tool error, got: %s", textContent(t, result))
	}
}

func TestRunPythonTool_NetworkBlockedByDefault(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "run_python",
		Arguments: map[string]any{"code": "import urllib.request\ntry:\n" +
			"    urllib.request.urlopen('http://example.com', timeout=3)\n" +
			"    print('reached')\n" +
			"except Exception:\n" +
			"    print('blocked')\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.Stdout != "blocked\n" {
		t.Errorf("expected network blocked by default, got %+v", got)
	}
}

func TestRunPythonTool_NetworkAllowedWhenServerConfiguredWithIt(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), true)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "run_python",
		Arguments: map[string]any{"code": "import urllib.request\n" +
			"r = urllib.request.urlopen('http://example.com', timeout=5)\n" +
			"print('status', r.status)\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.ExitCode != 0 {
		t.Errorf("expected a successful network fetch when this server is configured with -network, got %+v", got)
	}
}

func TestRunPythonTool_TimeoutReported(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{Timeout: 2 * time.Second}), false)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "import time\ntime.sleep(30)\n"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if !got.TimedOut {
		t.Errorf("expected timed_out=true, got %+v", got)
	}
}

func TestSplitNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", nil},
		{"single value", "8.8.8.8", []string{"8.8.8.8"}},
		{"multiple values", "8.8.8.8,1.1.1.1", []string{"8.8.8.8", "1.1.1.1"}},
		{"trims whitespace around each value", " 8.8.8.8 , 1.1.1.1 ", []string{"8.8.8.8", "1.1.1.1"}},
		{"drops empty entries from stray/trailing commas", "8.8.8.8,,1.1.1.1,", []string{"8.8.8.8", "1.1.1.1"}},
		{"only commas and whitespace yields nil", " , , ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitNonEmpty(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}
