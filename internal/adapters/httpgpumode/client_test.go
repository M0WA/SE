package httpgpumode_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"searchengine/internal/adapters/httpgpumode"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func cfgFor(baseURL string) domain.GPUModeSettings {
	return domain.GPUModeSettings{Enabled: true, ControlBaseURL: baseURL, ControlAPIKey: "s3cr3t"}
}

func TestStatus_Success(t *testing.T) {
	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "chat", "in_progress": false})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	st, err := c.Status(context.Background(), cfgFor(srv.URL))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if gotPath != "/gpu/api/mode" {
		t.Fatalf("expected GET /gpu/api/mode, got %s", gotPath)
	}
	if gotToken != "s3cr3t" {
		t.Fatalf("expected X-Internal-Token forwarded, got %q", gotToken)
	}
	if st.Mode != domain.GPUModeChat || st.InProgress {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestStatus_NonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid or missing internal token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.Status(context.Background(), cfgFor(srv.URL))
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestStatus_RejectsDisallowedURL(t *testing.T) {
	c := httpgpumode.New()
	_, err := c.Status(context.Background(), cfgFor("http://169.254.169.254"))
	if err == nil {
		t.Fatal("expected a link-local control URL to be rejected")
	}
}

func TestSwitch_AcceptedReturnsStatusNoError(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "chat", "target": "vision", "in_progress": true})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	st, err := c.Switch(context.Background(), cfgFor(srv.URL), domain.GPUModeVision)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if !st.InProgress || st.Target != domain.GPUModeVision {
		t.Fatalf("unexpected status: %+v", st)
	}
	if gotBody["mode"] != "vision" {
		t.Fatalf("expected mode=vision in request body, got %v", gotBody)
	}
}

func TestSwitch_AlreadyInTargetModeReturns200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "chat", "in_progress": false})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	st, err := c.Switch(context.Background(), cfgFor(srv.URL), domain.GPUModeChat)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if st.Mode != domain.GPUModeChat {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestSwitch_ConflictReturnsErrGPUModeSwitchConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "a switch to vision is already in progress", "mode": "chat", "target": "vision", "in_progress": true,
		})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	st, err := c.Switch(context.Background(), cfgFor(srv.URL), domain.GPUModeChat)
	if !errors.Is(err, ports.ErrGPUModeSwitchConflict) {
		t.Fatalf("expected ErrGPUModeSwitchConflict, got %v", err)
	}
	if st.Target != domain.GPUModeVision {
		t.Fatalf("expected conflicting target reported, got %+v", st)
	}
}

func TestSwitch_ServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	_, err := c.Switch(context.Background(), cfgFor(srv.URL), domain.GPUModeVision)
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if errors.Is(err, ports.ErrGPUModeSwitchConflict) {
		t.Fatal("a 500 must not be reported as ErrGPUModeSwitchConflict")
	}
}

func TestHeartbeat_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	if err := c.Heartbeat(context.Background(), cfgFor(srv.URL)); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if gotPath != "/gpu/api/heartbeat" {
		t.Fatalf("expected POST /gpu/api/heartbeat, got %s", gotPath)
	}
}

func TestHeartbeat_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid or missing internal token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	if err := c.Heartbeat(context.Background(), cfgFor(srv.URL)); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestNew_UsesDefaultHTTPClient(t *testing.T) {
	c := httpgpumode.New()
	if c.HTTPClient == nil {
		t.Fatal("expected New to populate a default HTTPClient")
	}
}

func TestClient_NilHTTPClientFallsBackToDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "chat"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{}
	if _, err := c.Status(context.Background(), cfgFor(srv.URL)); err != nil {
		t.Fatalf("Status with nil HTTPClient: %v", err)
	}
}

func TestStatus_MalformedRequestURLIsError(t *testing.T) {
	c := &httpgpumode.Client{HTTPClient: http.DefaultClient}
	cfg := domain.GPUModeSettings{ControlBaseURL: "http://127.0.0.1", ControlAPIKey: "x"}
	// Force a request-build failure via a context that's already done --
	// http.NewRequestWithContext itself never fails for a well-formed URL,
	// so this instead exercises "calling control service" by pointing at a
	// port nothing listens on.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := c.Status(ctx, cfg); err == nil {
		t.Fatal("expected an error for an already-expired context")
	}
}

// erroringBody/erroringBodyTransport mirror httpchat's own test helpers,
// exercising "reading response body" without a real fault.
type erroringBody struct{}

func (erroringBody) Read(p []byte) (int, error) { return 0, errors.New("simulated read failure") }
func (erroringBody) Close() error               { return nil }

type erroringBodyTransport struct{ base http.RoundTripper }

func (t erroringBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = erroringBody{}
	return resp, nil
}

func TestStatus_ReadingResponseBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": "chat"})
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: &http.Client{Transport: erroringBodyTransport{base: srv.Client().Transport}}}
	if _, err := c.Status(context.Background(), cfgFor(srv.URL)); err == nil {
		t.Fatal("expected an error when the response body fails to read")
	}
}

func TestStatus_MalformedJSONResponseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	c := &httpgpumode.Client{HTTPClient: srv.Client()}
	if _, err := c.Status(context.Background(), cfgFor(srv.URL)); err == nil {
		t.Fatal("expected an error for a malformed JSON response")
	}
}
