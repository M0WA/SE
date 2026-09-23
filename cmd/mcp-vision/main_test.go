package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectedTestServer wires newServer's mcp.Server to a real mcp.Client
// over an in-memory transport, mirroring cmd/mcp-files' own test helper.
func connectedTestServer(t *testing.T, similarity *similarityTool, caption *captionTool) *mcp.ClientSession {
	t.Helper()
	server := newServer(similarity, caption)
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

func testFilesClient(baseURL, token string) *filesClient {
	return &filesClient{baseURL: baseURL, token: token, http: http.DefaultClient}
}

func disabledSimilarityTool() *similarityTool {
	return &similarityTool{files: testFilesClient("http://unused.invalid", "tok-123"), enabled: false, http: http.DefaultClient}
}

func disabledCaptionTool() *captionTool {
	return &captionTool{files: testFilesClient("http://unused.invalid", "tok-123"), enabled: false, http: http.DefaultClient}
}

// --- vision_similarity ---

func TestVisionSimilarityTool_NotConfiguredIsToolError(t *testing.T) {
	cs := connectedTestServer(t, disabledSimilarityTool(), disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when similarity is disabled")
	}
}

func TestVisionSimilarityTool_NoTokenIsToolError(t *testing.T) {
	similarity := &similarityTool{files: testFilesClient("http://unused.invalid", ""), enabled: true, http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result with no signed-in user")
	}
}

func TestVisionSimilarityTool_FileFetchErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "file not found", http.StatusNotFound)
	}))
	defer filesSrv.Close()

	similarity := &similarityTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the file fetch fails")
	}
}

func TestVisionSimilarityTool_Success(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer filesSrv.Close()

	var gotPath, gotKey string
	var gotBody visionSimilarityRequest
	simSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Internal-API-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"matches":[{"url":"https://a.example","title":"A","score":0.9}]}`))
	}))
	defer simSrv.Close()

	similarity := &similarityTool{
		files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, provider: "h200_gte_qwen2",
		baseURL: simSrv.URL, apiKey: "internal-secret", http: http.DefaultClient,
	}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1", "limit": 3},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", textContent(t, result))
	}
	if gotPath != "/search/api/vision-similarity" {
		t.Errorf("expected the internal vision-similarity path, got %q", gotPath)
	}
	if gotKey != "internal-secret" {
		t.Errorf("expected the internal API key header, got %q", gotKey)
	}
	if gotBody.Provider != "h200_gte_qwen2" || gotBody.Limit != 3 || gotBody.MimeType != "image/png" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
	if gotBody.Base64 == "" {
		t.Error("expected the image base64-encoded in the request")
	}
	if textContent(t, result) == "" {
		t.Error("expected the raw match JSON returned as the tool's text result")
	}
}

func TestVisionSimilarityTool_UpstreamErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1, 2, 3})
	}))
	defer filesSrv.Close()
	simSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such provider", http.StatusBadRequest)
	}))
	defer simSrv.Close()

	similarity := &similarityTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: simSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the internal endpoint fails")
	}
}

func TestVisionSimilarityTool_InvalidInternalBaseURLIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	similarity := &similarityTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: "://bad-url", http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an invalid internal base URL")
	}
}

func TestVisionSimilarityTool_NetworkErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	similarity := &similarityTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: "http://127.0.0.1:1", http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result connecting to an unreachable address")
	}
}

func TestVisionSimilarityTool_BodyReadErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	simSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer simSrv.Close()
	similarity := &similarityTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: simSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the response body can't be fully read")
	}
}

// --- vision_caption ---

func TestVisionCaptionTool_NotConfiguredIsToolError(t *testing.T) {
	cs := connectedTestServer(t, disabledSimilarityTool(), disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when captioning is disabled")
	}
}

func TestVisionCaptionTool_NoTokenIsToolError(t *testing.T) {
	caption := &captionTool{files: testFilesClient("http://unused.invalid", ""), enabled: true, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result with no signed-in user")
	}
}

func TestVisionCaptionTool_Success(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	}))
	defer filesSrv.Close()

	var gotAuth string
	var gotBody captionRequest
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"A red square."}}]}`))
	}))
	defer capSrv.Close()

	caption := &captionTool{
		files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true,
		baseURL: capSrv.URL, apiKey: "sk-vl", model: "vl-chat", http: http.DefaultClient,
	}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1", "question": "What color is this?"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", textContent(t, result))
	}
	if textContent(t, result) != "A red square." {
		t.Errorf("expected the model's answer returned verbatim, got %q", textContent(t, result))
	}
	if gotAuth != "Bearer sk-vl" {
		t.Errorf("expected the configured API key sent, got %q", gotAuth)
	}
	if gotBody.Model != "vl-chat" || len(gotBody.Messages) != 1 {
		t.Fatalf("unexpected request: %+v", gotBody)
	}
	if gotBody.Messages[0].Content[0].Text != "What color is this?" {
		t.Errorf("expected the custom question forwarded, got %+v", gotBody.Messages[0].Content[0])
	}
	if gotBody.Messages[0].Content[1].ImageURL == nil || gotBody.Messages[0].Content[1].Type != "image_url" {
		t.Errorf("expected an image_url content block, got %+v", gotBody.Messages[0].Content[1])
	}
}

func TestVisionCaptionTool_DefaultQuestionWhenNotGiven(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	var gotBody captionRequest
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer capSrv.Close()

	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody.Messages[0].Content[0].Text != defaultCaptionQuestion {
		t.Errorf("expected the default question when none given, got %q", gotBody.Messages[0].Content[0].Text)
	}
}

func TestVisionCaptionTool_NonOKStatusIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model overloaded", http.StatusServiceUnavailable)
	}))
	defer capSrv.Close()

	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a non-2xx captioning response")
	}
}

func TestVisionCaptionTool_MalformedJSONIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer capSrv.Close()

	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for a malformed JSON response")
	}
}

func TestVisionCaptionTool_EmptyChoicesIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer capSrv.Close()

	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an empty choices list")
	}
}

func TestVisionCaptionTool_NoAPIKeyOmitsAuthorizationHeader(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	var gotAuth string
	var sawAuthHeader bool
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, sawAuthHeader = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer capSrv.Close()

	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawAuthHeader {
		t.Errorf("expected no Authorization header with no configured API key, got %q", gotAuth)
	}
}

func TestVisionCaptionTool_InvalidBaseURLIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: "://bad-url", http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result for an invalid base URL")
	}
}

func TestVisionCaptionTool_NetworkErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: "http://127.0.0.1:1", http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result connecting to an unreachable address")
	}
}

func TestVisionCaptionTool_BodyReadErrorIsToolError(t *testing.T) {
	filesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1})
	}))
	defer filesSrv.Close()
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer capSrv.Close()
	caption := &captionTool{files: testFilesClient(filesSrv.URL, "tok-123"), enabled: true, baseURL: capSrv.URL, http: http.DefaultClient}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"file_id": "f1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the response body can't be fully read")
	}
}

// --- filesClient ---

func TestFilesClient_EmptyFileIDIsError(t *testing.T) {
	c := testFilesClient("http://unused.invalid", "tok-123")
	_, err := c.fetch(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an empty file_id")
	}
}

func TestFilesClient_TooLargeIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	}))
	defer srv.Close()
	c := testFilesClient(srv.URL, "tok-123")
	_, err := c.fetch(context.Background(), "f1")
	if err == nil {
		t.Fatal("expected an error for an oversized image")
	}
}

func TestFilesClient_UsesFilenameFromContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="photo.jpg"`)
		_, _ = w.Write([]byte{1, 2, 3})
	}))
	defer srv.Close()
	c := testFilesClient(srv.URL, "tok-123")
	img, err := c.fetch(context.Background(), "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.filename != "photo.jpg" {
		t.Errorf("expected filename parsed from Content-Disposition, got %q", img.filename)
	}
}

func TestFilesClient_NetworkErrorIsError(t *testing.T) {
	c := testFilesClient("http://127.0.0.1:1", "tok-123")
	_, err := c.fetch(context.Background(), "f1")
	if err == nil {
		t.Fatal("expected an error connecting to an unreachable address")
	}
}

func TestFilesClient_InvalidBaseURLIsError(t *testing.T) {
	c := testFilesClient("://bad-url", "tok-123")
	_, err := c.fetch(context.Background(), "f1")
	if err == nil {
		t.Fatal("expected an error building a request against an invalid base URL")
	}
}

func TestFilesClient_BodyReadErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("expected a hijackable response writer")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer srv.Close()
	c := testFilesClient(srv.URL, "tok-123")
	_, err := c.fetch(context.Background(), "f1")
	if err == nil {
		t.Fatal("expected an error when the response body can't be fully read")
	}
}

func TestFilesClient_FilenameFallsBackToFileIDWithoutContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{1, 2, 3})
	}))
	defer srv.Close()
	c := testFilesClient(srv.URL, "tok-123")
	img, err := c.fetch(context.Background(), "f1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.filename != "f1" {
		t.Errorf("expected filename to fall back to the file id, got %q", img.filename)
	}
}

// main is not directly tested (it wires real env vars/flags/os.Stdin), but
// this proves the two tool constructions in it don't panic when given a
// completed, in-process round trip through their public shape -- the
// meaningful behavior is covered above via newServer directly.
func TestNewServer_ExposesBothTools(t *testing.T) {
	cs := connectedTestServer(t, disabledSimilarityTool(), disabledCaptionTool())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["vision_similarity"] || !names["vision_caption"] {
		t.Errorf("expected both tools listed, got %+v", names)
	}
}
