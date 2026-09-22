package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectedTestServer wires newServer's mcp.Server to a real mcp.Client
// over an in-memory transport, mirroring cmd/mcp-web/cmd/mcp-sandbox's own
// test helper -- exercises each tool exactly as a real chat turn would,
// through CallTool, not by calling client's own methods directly.
func connectedTestServer(t *testing.T, c *client) *mcp.ClientSession {
	t.Helper()
	server := newServer(c)
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
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

func testClient(baseURL, token string) *client {
	return &client{baseURL: baseURL, token: token, http: http.DefaultClient}
}

func TestListFilesTool_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/api/files" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("expected bearer token, got %q", got)
		}
		w.Write([]byte(`[{"id":"f1","filename":"notes.txt","size":5}]`))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	if got := textContent(t, result); got != `[{"id":"f1","filename":"notes.txt","size":5}]` {
		t.Errorf("unexpected content: %s", got)
	}
}

func TestListFilesTool_NoTokenIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:0", ""))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when no token is configured")
	}
}

func TestListFilesTool_ServerErrorIsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a non-200 response")
	}
}

func TestReadFileTool_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/api/files/f1" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	if got := textContent(t, result); got != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", got)
	}
}

func TestReadFileTool_EmptyFileIDIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:0", "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"file_id": ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an empty file_id")
	}
}

func TestReadFileTool_BinaryContentIsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{0xff, 0xfe, 0x00, 0x01, 0x80})
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for non-UTF8 binary content")
	}
}

func TestReadFileTool_NotFoundIsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "file not found", http.StatusNotFound)
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"file_id": "missing"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a 404")
	}
}

func TestWriteFileTool_Success(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/api/files" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"f2","filename":"report.txt","size":11}`))
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_file", Arguments: map[string]any{"filename": "report.txt", "content": "hello world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %s", textContent(t, result))
	}
	if got := textContent(t, result); got != `{"id":"f2","filename":"report.txt","size":11}` {
		t.Errorf("unexpected content: %s", got)
	}
	if gotContentType == "" || len(gotBody) == 0 {
		t.Errorf("expected a real multipart body to reach the server, content-type=%q body-len=%d", gotContentType, len(gotBody))
	}
}

func TestWriteFileTool_EmptyFilenameIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:0", "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_file", Arguments: map[string]any{"filename": "", "content": "x"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an empty filename")
	}
}

func TestWriteFileTool_ServerErrorIsToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "too large", http.StatusBadRequest)
	}))
	defer srv.Close()

	cs := connectedTestServer(t, testClient(srv.URL, "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_file", Arguments: map[string]any{"filename": "x.txt", "content": "y"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a non-201 response")
	}
}

func TestWriteFileTool_NoTokenIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:0", ""))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_file", Arguments: map[string]any{"filename": "x.txt", "content": "y"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when no token is configured")
	}
}

func TestReadFileTool_UnreachableServerIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:1", "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the server is unreachable")
	}
}

// TestListFilesTool_InvalidRequestIsToolError covers do's own
// http.NewRequestWithContext error branch: a control character in the
// base URL makes the resulting request URL unparseable.
func TestListFilesTool_InvalidRequestIsToolError(t *testing.T) {
	cs := connectedTestServer(t, testClient("http://127.0.0.1:8080/\x7f", "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an unbuildable request")
	}
}

func TestClient_UnreachableServerIsToolError(t *testing.T) {
	// Port 0 on loopback never accepts a connection -- a real
	// infrastructure failure (connection refused), distinct from an
	// application-level error response.
	cs := connectedTestServer(t, testClient("http://127.0.0.1:1", "tok-123"))
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the server is unreachable")
	}
}
