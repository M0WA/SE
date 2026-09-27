package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
)

// withTemplate temporarily swaps the embedded workflow template for raw,
// so a test can force setNodeInput/json.Unmarshal failures inside
// Generate without a real ComfyUI ever seeing the malformed graph --
// restores the real template via t.Cleanup.
func withTemplate(t *testing.T, raw string) {
	t.Helper()
	original := ltxT2VWorkflowTemplate
	ltxT2VWorkflowTemplate = []byte(raw)
	t.Cleanup(func() { ltxT2VWorkflowTemplate = original })
}

func newTestControllerForGenerate(t *testing.T, comfyURL string) *Controller {
	t.Helper()
	c := newTestController(newFakeUnits(), newFakeReady())
	c.comfyReadyURL = comfyURL + "/system_stats"
	return c
}

func TestGenerate_EmptyPromptRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	c.mode = ModeVision
	if _, err := c.Generate(context.Background(), "   "); err != errEmptyPrompt {
		t.Fatalf("expected errEmptyPrompt, got %v", err)
	}
}

func TestGenerate_NotInVisionModeRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	c.mode = ModeChat
	if _, err := c.Generate(context.Background(), "a cat"); err != errNotInVisionMode {
		t.Fatalf("expected errNotInVisionMode, got %v", err)
	}
}

func TestGenerate_SubmitsPromptAndSeedToComfyUI(t *testing.T) {
	var gotBody comfyPromptRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prompt" {
			t.Errorf("expected POST /prompt, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comfyPromptResponse{PromptID: "abc-123"})
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision

	id, err := c.Generate(context.Background(), "a cat riding a bicycle")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "abc-123" {
		t.Fatalf("expected prompt id abc-123, got %q", id)
	}

	node, ok := gotBody.Prompt[positivePromptNodeID].(map[string]any)
	if !ok {
		t.Fatalf("prompt node %q missing or malformed in submitted graph", positivePromptNodeID)
	}
	inputs, ok := node["inputs"].(map[string]any)
	if !ok || inputs[positivePromptInputKey] != "a cat riding a bicycle" {
		t.Fatalf("expected the prompt text to be substituted into node %q, got %+v", positivePromptNodeID, node)
	}

	seedNode, ok := gotBody.Prompt[seedNodeID].(map[string]any)
	if !ok {
		t.Fatalf("seed node %q missing or malformed in submitted graph", seedNodeID)
	}
	seedInputs, ok := seedNode["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("seed node %q inputs malformed", seedNodeID)
	}
	if _, ok := seedInputs[seedInputKey].(float64); !ok {
		t.Fatalf("expected a numeric seed at node %q, got %+v", seedNodeID, seedInputs)
	}
}

func TestGenerate_ComfyUINodeErrorsRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comfyPromptResponse{
			PromptID:   "abc-123",
			NodeErrors: map[string]json.RawMessage{"75": json.RawMessage(`{"errors":["bad node"]}`)},
		})
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error when ComfyUI reports node_errors")
	}
}

func TestGenerate_ComfyUINonOKStatusRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error on a non-200 ComfyUI response")
	}
}

func TestGenerateResult_PendingWhenAbsentFromHistory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	status, _, err := c.GenerateResult(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected pending, got %q", status)
	}
}

func TestGenerateResult_DoneWithFileOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/abc-123" {
			t.Errorf("expected GET /history/abc-123, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abc-123":{"outputs":{"75":{"videos":[{"filename":"out.mp4","subfolder":"video","type":"output"}]}},"status":{"completed":true,"status_str":"success"}}}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	status, file, err := c.GenerateResult(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "done" {
		t.Fatalf("expected done, got %q", status)
	}
	if file.Filename != "out.mp4" || file.Subfolder != "video" || file.Type != "output" {
		t.Fatalf("unexpected file: %+v", file)
	}
}

func TestGenerateResult_FailedWhenCompletedWithNoOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abc-123":{"outputs":{},"status":{"completed":true,"status_str":"error"}}}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	status, _, err := c.GenerateResult(context.Background(), "abc-123")
	if status != "failed" || err == nil {
		t.Fatalf("expected failed status with an error, got status=%q err=%v", status, err)
	}
}

func TestHandleGenerate_HTTPStatusCodes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comfyPromptResponse{PromptID: "abc-123"})
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/generate", testToken, []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}

	body, _ := json.Marshal(generateRequest{Prompt: ""})
	rec = doRequest(t, mux, http.MethodPost, "/gpu/api/generate", testToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty prompt, got %d: %s", rec.Code, rec.Body.String())
	}

	c.mode = ModeChat
	body, _ = json.Marshal(generateRequest{Prompt: "a cat"})
	rec = doRequest(t, mux, http.MethodPost, "/gpu/api/generate", testToken, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when not in vision mode, got %d: %s", rec.Code, rec.Body.String())
	}

	c.mode = ModeVision
	rec = doRequest(t, mux, http.MethodPost, "/gpu/api/generate", testToken, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp generateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.PromptID != "abc-123" {
		t.Fatalf("expected prompt id abc-123, got %q", resp.PromptID)
	}
}

func TestHandleGenerate_UpstreamFailureReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision
	mux := newMux(c, testToken)

	body, _ := json.Marshal(generateRequest{Prompt: "a cat"})
	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/generate", testToken, body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateResult_MissingIDReturnsBadRequest(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/generate/", testToken, nil)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("expected 400 or 404 for a missing id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateResult_DoneIncludesViewURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abc-123":{"outputs":{"75":{"videos":[{"filename":"out.mp4","subfolder":"video","type":"output"}]}},"status":{"completed":true}}}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/generate/abc-123", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp generateResultResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "done" || resp.ViewURL == "" {
		t.Fatalf("expected done with a view_url, got %+v", resp)
	}
}

func TestHandleGenerateResult_UpstreamFailureReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/generate/abc-123", testToken, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestComfyViewProxy_ForwardsWhitelistedParamsOnly(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			t.Errorf("expected GET /view, got %s", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/view?filename=out.mp4&subfolder=video&type=output&evil=drop-me", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "video-bytes" {
		t.Fatalf("expected the upstream body proxied through, got %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("expected the upstream Content-Type forwarded, got %q", rec.Header().Get("Content-Type"))
	}
	if strings.Contains(gotQuery, "evil") {
		t.Fatalf("expected non-whitelisted query params to be dropped, got %q", gotQuery)
	}
}

func TestGenerate_UsesInjectedHTTPClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comfyPromptResponse{PromptID: "abc-123"})
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision
	c.genHTTP = upstream.Client()

	if _, err := c.Generate(context.Background(), "a cat"); err != nil {
		t.Fatalf("unexpected error using the injected client: %v", err)
	}
}

func TestGenerate_MisconfiguredComfyReadyURLRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	c.comfyReadyURL = "://not-a-url"
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error for a malformed comfyReadyURL")
	}
}

func TestGenerate_UnreachableComfyUIRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://127.0.0.1:1/nothing-listens-here")
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error when ComfyUI is unreachable")
	}
}

func TestGenerate_MalformedTemplateJSONRejected(t *testing.T) {
	withTemplate(t, "{not valid json")
	c := newTestControllerForGenerate(t, "http://comfy")
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error decoding a malformed workflow template")
	}
}

func TestGenerate_TemplateMissingPromptNodeRejected(t *testing.T) {
	withTemplate(t, `{"`+seedNodeID+`":{"inputs":{"`+seedInputKey+`":1}}}`)
	c := newTestControllerForGenerate(t, "http://comfy")
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error when the template is missing the prompt node")
	}
}

func TestGenerate_TemplateMissingSeedNodeRejected(t *testing.T) {
	withTemplate(t, `{"`+positivePromptNodeID+`":{"inputs":{"`+positivePromptInputKey+`":""}}}`)
	c := newTestControllerForGenerate(t, "http://comfy")
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error when the template is missing the seed node")
	}
}

func TestSetNodeInput_NodeHasUnexpectedShape(t *testing.T) {
	graph := map[string]any{"n1": "not-an-object"}
	if err := setNodeInput(graph, "n1", "value", "x"); err == nil {
		t.Fatal("expected an error for a non-object node")
	}
}

func TestSetNodeInput_NodeHasNoInputsKey(t *testing.T) {
	graph := map[string]any{"n1": map[string]any{"class_type": "Foo"}}
	if err := setNodeInput(graph, "n1", "value", "x"); err == nil {
		t.Fatal("expected an error for a node with no inputs key")
	}
}

func TestSetNodeInput_InputsHaveUnexpectedShape(t *testing.T) {
	graph := map[string]any{"n1": map[string]any{"inputs": "not-an-object"}}
	if err := setNodeInput(graph, "n1", "value", "x"); err == nil {
		t.Fatal("expected an error for non-object inputs")
	}
}

func TestGenerate_MalformedComfyUIResponseRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not valid json"))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error decoding a malformed ComfyUI response")
	}
}

func TestGenerate_EmptyPromptIDFromComfyUIRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comfyPromptResponse{PromptID: ""})
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	c.mode = ModeVision

	if _, err := c.Generate(context.Background(), "a cat"); err == nil {
		t.Fatal("expected an error when ComfyUI returns no prompt id")
	}
}

func TestMustMarshal_FallsBackOnMarshalError(t *testing.T) {
	got := mustMarshal(map[string]json.RawMessage{"x": json.RawMessage("not valid json")})
	if got == "" {
		t.Fatal("expected a non-empty fallback string")
	}
}

func TestGenerateResult_MisconfiguredComfyReadyURLRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	c.comfyReadyURL = "://not-a-url"

	if _, _, err := c.GenerateResult(context.Background(), "abc-123"); err == nil {
		t.Fatal("expected an error for a malformed comfyReadyURL")
	}
}

func TestGenerateResult_UnreachableComfyUIRejected(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://127.0.0.1:1/nothing-listens-here")

	if _, _, err := c.GenerateResult(context.Background(), "abc-123"); err == nil {
		t.Fatal("expected an error when ComfyUI is unreachable")
	}
}

func TestGenerateResult_MalformedHistoryResponseRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not valid json"))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)

	if _, _, err := c.GenerateResult(context.Background(), "abc-123"); err == nil {
		t.Fatal("expected an error decoding a malformed history response")
	}
}

func TestGenerateResult_StillPendingWhenPresentButNotCompleted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abc-123":{"outputs":{},"status":{"completed":false}}}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	status, _, err := c.GenerateResult(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "pending" {
		t.Fatalf("expected pending, got %q", status)
	}
}

func TestComfyProxy_ExactPrefixPathDirectorForwardsAsSlash(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, ok := newComfyProxy(upstream.URL + "/system_stats").(*httputil.ReverseProxy)
	if !ok {
		t.Fatal("expected a *httputil.ReverseProxy")
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/gpu/comfy", nil)
	proxy.Director(req)
	if req.URL.Path != "/" {
		t.Fatalf("expected the bare prefix to forward as /, got %q", req.URL.Path)
	}
}

func TestComfyViewProxy_MisconfiguredReadyURLReturnsBadGateway(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	c.comfyReadyURL = "://not-a-url"
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/view?filename=out.mp4", testToken, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestComfyViewProxy_UnreachableComfyUIReturnsBadGateway(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://127.0.0.1:1/nothing-listens-here")
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/view?filename=out.mp4", testToken, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateResult_EmptyPathValueReturnsBadRequest(t *testing.T) {
	c := newTestControllerForGenerate(t, "http://comfy")
	req := httptest.NewRequest(http.MethodGet, "/gpu/api/generate/x", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	c.handleGenerateResult(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateResult_FailedStatusIncludesError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abc-123":{"outputs":{},"status":{"completed":true,"status_str":"error"}}}`))
	}))
	defer upstream.Close()

	c := newTestControllerForGenerate(t, upstream.URL)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/generate/abc-123", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp generateResultResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != "failed" || resp.Error == "" {
		t.Fatalf("expected a failed status with an error message, got %+v", resp)
	}
}
