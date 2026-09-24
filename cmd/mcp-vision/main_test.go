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

// testURLFetcher always allows -- netguard.URLAllowed would otherwise
// reject every httptest server (they all listen on 127.0.0.1, which
// AllowedIP's real policy blocks as loopback).
func testURLFetcher() *urlImageFetcher {
	return &urlImageFetcher{http: http.DefaultClient, urlAllowed: func(string) bool { return true }}
}

func disabledSimilarityTool() *similarityTool {
	return &similarityTool{files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(), enabled: false, http: http.DefaultClient}
}

func disabledCaptionTool() *captionTool {
	return &captionTool{files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(), enabled: false, http: http.DefaultClient}
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

// --- vision_similarity / vision_caption via image_url ---

func TestVisionSimilarityTool_ImageURLSuccess(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer imgSrv.Close()
	var gotBody visionSimilarityRequest
	simSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"matches":[]}`))
	}))
	defer simSrv.Close()

	similarity := &similarityTool{
		files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(),
		enabled: true, baseURL: simSrv.URL, http: http.DefaultClient,
	}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"image_url": imgSrv.URL + "/photo.png"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", textContent(t, result))
	}
	if gotBody.MimeType != "image/png" || gotBody.Base64 == "" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
}

func TestVisionSimilarityTool_FileIDAndImageURLBothGivenIsToolError(t *testing.T) {
	similarity := &similarityTool{
		files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(),
		enabled: true, baseURL: "http://unused.invalid", http: http.DefaultClient,
	}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{"file_id": "f1", "image_url": "https://example.com/a.png"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when both file_id and image_url are given")
	}
}

func TestVisionSimilarityTool_NeitherFileIDNorImageURLIsToolError(t *testing.T) {
	similarity := &similarityTool{
		files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(),
		enabled: true, baseURL: "http://unused.invalid", http: http.DefaultClient,
	}
	cs := connectedTestServer(t, similarity, disabledCaptionTool())
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_similarity", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when neither file_id nor image_url is given")
	}
}

func TestVisionCaptionTool_ImageURLSuccess(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff})
	}))
	defer imgSrv.Close()
	var gotBody captionRequest
	capSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a jpeg"}}]}`))
	}))
	defer capSrv.Close()

	caption := &captionTool{
		files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(),
		enabled: true, baseURL: capSrv.URL, http: http.DefaultClient,
	}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"image_url": imgSrv.URL + "/photo.jpg"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", textContent(t, result))
	}
	if textContent(t, result) != "a jpeg" {
		t.Errorf("unexpected caption: %q", textContent(t, result))
	}
	if gotBody.Messages[0].Content[1].ImageURL == nil {
		t.Fatalf("expected an image_url content block, got %+v", gotBody.Messages[0].Content[1])
	}
}

func TestVisionCaptionTool_ImageURLFetchErrorIsToolError(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer imgSrv.Close()
	caption := &captionTool{
		files: testFilesClient("http://unused.invalid", "tok-123"), urlFetcher: testURLFetcher(),
		enabled: true, baseURL: "http://unused.invalid", http: http.DefaultClient,
	}
	cs := connectedTestServer(t, disabledSimilarityTool(), caption)
	result, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "vision_caption", Arguments: map[string]any{"image_url": imgSrv.URL},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the image_url fetch fails")
	}
}

// --- urlImageFetcher ---

func TestURLImageFetcher_EmptyURLIsError(t *testing.T) {
	f := testURLFetcher()
	_, err := f.fetch(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an empty image_url")
	}
}

func TestURLImageFetcher_NotAllowedIsError(t *testing.T) {
	f := &urlImageFetcher{http: http.DefaultClient, urlAllowed: func(string) bool { return false }}
	_, err := f.fetch(context.Background(), "http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("expected an error for a URL netguard disallows")
	}
}

func TestURLImageFetcher_NewURLImageFetcherUsesNetguardURLAllowed(t *testing.T) {
	f := newURLImageFetcher(http.DefaultClient)
	// A loopback address is never allowed under netguard's real AllowedIP
	// policy -- proves the constructor really wires netguard.URLAllowed in,
	// not just testURLFetcher's always-true stub.
	_, err := f.fetch(context.Background(), "http://127.0.0.1:1/x")
	if err == nil {
		t.Fatal("expected netguard to reject a loopback image_url")
	}
}

func TestURLImageFetcher_NonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer srv.Close()
	f := testURLFetcher()
	_, err := f.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestURLImageFetcher_TooLargeIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, maxImageBytes+1))
	}))
	defer srv.Close()
	f := testURLFetcher()
	_, err := f.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for an oversized image")
	}
}

func TestURLImageFetcher_NonImageContentTypeIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()
	f := testURLFetcher()
	_, err := f.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a non-image content-type")
	}
}

// looksLikeImage's empty-content-type branch can't be exercised through a
// real httptest round trip -- net/http always sniffs and sets some
// Content-Type on an unset one before writing the response -- so it's
// tested directly instead.
func TestLooksLikeImage_EmptyContentTypeIsAccepted(t *testing.T) {
	if !looksLikeImage("") {
		t.Error("expected an empty content-type to be accepted")
	}
}

func TestURLImageFetcher_OctetStreamContentTypeIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()
	f := testURLFetcher()
	if _, err := f.fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("unexpected error for an octet-stream content-type: %v", err)
	}
}

func TestURLImageFetcher_NetworkErrorIsError(t *testing.T) {
	f := &urlImageFetcher{http: http.DefaultClient, urlAllowed: func(string) bool { return true }}
	_, err := f.fetch(context.Background(), "http://127.0.0.1:1/x")
	if err == nil {
		t.Fatal("expected an error connecting to an unreachable address")
	}
}

func TestURLImageFetcher_BodyReadErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("failed to hijack connection: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort")
		buf.Flush()
	}))
	defer srv.Close()
	f := testURLFetcher()
	_, err := f.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected an error when the response body can't be fully read")
	}
}

func TestURLImageFetcher_FilenameDerivedFromURLPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{1})
	}))
	defer srv.Close()
	f := testURLFetcher()
	img, err := f.fetch(context.Background(), srv.URL+"/some/path/photo.png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.filename != "photo.png" {
		t.Errorf("expected filename derived from the URL path, got %q", img.filename)
	}
}

func TestURLImageFetcher_FilenameFallsBackToFullURLWithoutSlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{1})
	}))
	defer srv.Close()
	f := testURLFetcher()
	img, err := f.fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if img.filename != srv.URL+"/" {
		t.Errorf("expected the full URL as filename when nothing follows the trailing slash, got %q", img.filename)
	}
}

func TestURLImageFetcher_InvalidRequestURLIsError(t *testing.T) {
	f := &urlImageFetcher{http: http.DefaultClient, urlAllowed: func(string) bool { return true }}
	_, err := f.fetch(context.Background(), "http://[::1]:namedport/x")
	if err == nil {
		t.Fatal("expected an error building a request against a malformed URL")
	}
}

// --- imageSource ---

func TestImageSource_NeitherGivenIsError(t *testing.T) {
	_, err := imageSource(context.Background(), testFilesClient("http://unused.invalid", "tok-123"), testURLFetcher(), "", "")
	if err == nil {
		t.Fatal("expected an error when neither file_id nor image_url is given")
	}
}

func TestImageSource_BothGivenIsError(t *testing.T) {
	_, err := imageSource(context.Background(), testFilesClient("http://unused.invalid", "tok-123"), testURLFetcher(), "f1", "https://example.com/a.png")
	if err == nil {
		t.Fatal("expected an error when both file_id and image_url are given")
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
