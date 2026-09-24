package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
func connectedTestServer(t *testing.T, runner *dockersandbox.Runner, network bool, files *filesClient) *mcp.ClientSession {
	t.Helper()
	server := newServer(runner, network, files)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
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
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), true, nil)
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

// TestListTools_PackagesParamOnlyPresentWhenNetworkEnabled proves the
// "packages" input-schema property (and the "Pass \"packages\"..."
// description sentence) exist only for a network-enabled server -- never
// exposed-but-silently-ignored (see runArgsWithPackages' own doc comment).
func TestListTools_PackagesParamOnlyPresentWhenNetworkEnabled(t *testing.T) {
	requireDockerTests(t)

	withoutNetwork := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, nil)
	listWithout, err := withoutNetwork.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listing tools (no network): %v", err)
	}
	for _, tool := range listWithout.Tools {
		if strings.Contains(tool.Description, "packages") {
			t.Errorf("expected no mention of packages in %q's description without -network, got %q", tool.Name, tool.Description)
		}
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if _, ok := props["packages"]; ok {
			t.Errorf("expected no \"packages\" input property on %q without -network, got schema %+v", tool.Name, tool.InputSchema)
		}
	}

	withNetwork := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), true, nil)
	listWith, err := withNetwork.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("listing tools (with network): %v", err)
	}
	for _, tool := range listWith.Tools {
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		if _, ok := props["packages"]; !ok {
			t.Errorf("expected a \"packages\" input property on %q with -network, got schema %+v", tool.Name, tool.InputSchema)
		}
	}
}

// TestRunPythonTool_PackagesInstalledEndToEnd proves the "packages"
// argument actually reaches dockersandbox and gets installed -- "six" is
// tiny/pure-Python, chosen only to keep this test fast (mirrors
// dockersandbox's own TestRun_PythonPackagesInstalledWhenNetworkEnabled,
// one layer up through the real MCP tool-call path instead of calling
// Runner.Run directly).
func TestRunPythonTool_PackagesInstalledEndToEnd(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{Timeout: 30 * time.Second}), true, nil)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "run_python",
		Arguments: map[string]any{
			"code":     "import six\nprint('six version', six.__version__)\n",
			"packages": []string{"six"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got runResult
	if err := json.Unmarshal([]byte(textContent(t, result)), &got); err != nil {
		t.Fatalf("decoding tool result: %v", err)
	}
	if got.ExitCode != 0 || !strings.Contains(got.Stdout, "six version") {
		t.Errorf("expected six installed and importable via the packages argument, got %+v", got)
	}
}

func TestRunPythonTool_TimeoutReported(t *testing.T) {
	requireDockerTests(t)
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{Timeout: 2 * time.Second}), false, nil)
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

// fakeFilesServer simulates search-server's GET /account/api/files/{id} --
// enough to exercise filesClient.fetch/resolveFiles without a real
// search-server: checks the bearer token, sets Content-Disposition (the
// real handler's own filename-carrying mechanism), and serves fixed bytes.
func fakeFilesServer(t *testing.T, wantToken, filename string, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFilesClient_Fetch_ReturnsFilenameFromContentDispositionAndData(t *testing.T) {
	srv := fakeFilesServer(t, "tok123", "notes.txt", []byte("hello"))
	fc := &filesClient{baseURL: srv.URL, token: "tok123", http: srv.Client()}

	name, data, err := fc.fetch(context.Background(), "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "notes.txt" || string(data) != "hello" {
		t.Errorf("got (%q, %q), want (\"notes.txt\", \"hello\")", name, data)
	}
}

func TestFilesClient_Fetch_NonOKStatusIsAnError(t *testing.T) {
	srv := fakeFilesServer(t, "tok123", "notes.txt", []byte("hello"))
	fc := &filesClient{baseURL: srv.URL, token: "wrong-token", http: srv.Client()}

	if _, _, err := fc.fetch(context.Background(), "f1"); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestFilesClient_Fetch_FallsBackToFileIDWhenContentDispositionIsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(srv.Close)
	fc := &filesClient{baseURL: srv.URL, token: "tok123", http: srv.Client()}

	name, data, err := fc.fetch(context.Background(), "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "f1" || string(data) != "hello" {
		t.Errorf("expected the fileID itself as a filename fallback, got (%q, %q)", name, data)
	}
}

func TestFilesClient_Fetch_FileTooLargeIsAnError(t *testing.T) {
	srv := fakeFilesServer(t, "tok123", "big.bin", make([]byte, maxSandboxInputFileBytes+1))
	fc := &filesClient{baseURL: srv.URL, token: "tok123", http: srv.Client()}

	if _, _, err := fc.fetch(context.Background(), "f1"); err == nil {
		t.Fatal("expected an error for a file over maxSandboxInputFileBytes")
	}
}

func TestResolveFiles_EmptyFileIDsIsANoOp(t *testing.T) {
	got, err := resolveFiles(context.Background(), nil, nil)
	if err != nil || got != nil {
		t.Fatalf("expected (nil, nil) for no file_ids, got (%v, %v)", got, err)
	}
}

func TestResolveFiles_NoTokenReturnsClearError(t *testing.T) {
	_, err := resolveFiles(context.Background(), &filesClient{}, []string{"f1"})
	if err == nil || err != errNoToken {
		t.Fatalf("expected errNoToken, got %v", err)
	}
}

func TestResolveFiles_NilFilesClientReturnsClearError(t *testing.T) {
	_, err := resolveFiles(context.Background(), nil, []string{"f1"})
	if err == nil || err != errNoToken {
		t.Fatalf("expected errNoToken, got %v", err)
	}
}

func TestResolveFiles_WrapsAFetchFailureWithTheOffendingFileID(t *testing.T) {
	srv := fakeFilesServer(t, "tok123", "notes.txt", []byte("hello"))
	fc := &filesClient{baseURL: srv.URL, token: "wrong-token", http: srv.Client()}

	_, err := resolveFiles(context.Background(), fc, []string{"f1"})
	if err == nil || !strings.Contains(err.Error(), `"f1"`) {
		t.Fatalf("expected an error naming the offending file_id \"f1\", got %v", err)
	}
}

func TestResolveFiles_FetchesEachRequestedFileKeyedByFilename(t *testing.T) {
	srv := fakeFilesServer(t, "tok123", "input.csv", []byte("a,b\n1,2\n"))
	fc := &filesClient{baseURL: srv.URL, token: "tok123", http: srv.Client()}

	got, err := resolveFiles(context.Background(), fc, []string{"f1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got["input.csv"]) != "a,b\n1,2\n" {
		t.Errorf("unexpected resolved files: %v", got)
	}
}

func TestRunPythonTool_FileIDsMakesAnUploadedFileReadable(t *testing.T) {
	requireDockerTests(t)
	srv := fakeFilesServer(t, "tok123", "input.csv", []byte("a,b\n1,2\n"))
	fc := &filesClient{baseURL: srv.URL, token: "tok123", http: srv.Client()}
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, fc)

	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "print(open('input.csv').read())", "file_ids": []string{"f1"}},
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
	if got.ExitCode != 0 || strings.TrimSpace(got.Stdout) != "a,b\n1,2" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestRunPythonTool_FileIDsWithoutASignedInUserIsAToolError(t *testing.T) {
	// No docker needed: resolveFiles's no-token check fails before runInSandbox ever calls
	// runner.Run, so this never actually touches Docker.
	cs := connectedTestServer(t, dockersandbox.New(dockersandbox.Limits{}), false, &filesClient{})
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "run_python",
		Arguments: map[string]any{"code": "print('unused')", "file_ids": []string{"f1"}},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected a tool error for file_ids with no signed-in user")
	}
	if !strings.Contains(textContent(t, result), "no signed-in user") {
		t.Errorf("expected a clear no-signed-in-user message, got %q", textContent(t, result))
	}
}
