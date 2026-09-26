package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testToken = "s3cr3t-token"

func doRequest(t *testing.T, mux http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRequireToken_RejectsMissingOrWrongToken(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.mode = ModeChat
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/mode", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", rec.Code)
	}

	rec = doRequest(t, mux, http.MethodGet, "/gpu/api/mode", "wrong-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}
}

func TestHandleGetMode_ReturnsCurrentStatus(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.mode = ModeChat
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/gpu/api/mode", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if st.Mode != ModeChat {
		t.Fatalf("expected mode chat, got %+v", st)
	}
}

func TestHandlePostMode_InvalidJSONBodyRejected(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/mode", testToken, []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandlePostMode_UnknownModeRejected(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	mux := newMux(c, testToken)

	body, _ := json.Marshal(modeRequest{Mode: "sleep"})
	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/mode", testToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePostMode_ValidSwitchAccepted(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady()
	ready.callsUntilReady["http://comfy/ready"] = 0
	c := newTestController(units, ready)
	c.mode = ModeChat
	mux := newMux(c, testToken)

	body, _ := json.Marshal(modeRequest{Mode: ModeVision})
	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/mode", testToken, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !st.InProgress || st.Target != ModeVision {
		t.Fatalf("expected in-progress switch to vision, got %+v", st)
	}
}

func TestHandlePostMode_ConflictReturnsErrorBody(t *testing.T) {
	units := newFakeUnits()
	units.active["vllm-chat.service"] = true
	ready := newFakeReady() // never ready -- stays in progress
	c := newTestController(units, ready)
	c.mode = ModeChat
	c.switchTimeout = 5 * time.Second
	mux := newMux(c, testToken)

	firstBody, _ := json.Marshal(modeRequest{Mode: ModeVision})
	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/mode", testToken, firstBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected first switch accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	secondBody, _ := json.Marshal(modeRequest{Mode: ModeChat})
	rec2 := doRequest(t, mux, http.MethodPost, "/gpu/api/mode", testToken, secondBody)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var errResp modeErrorResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if errResp.Error == "" || errResp.Target != ModeVision {
		t.Fatalf("expected a populated conflict error body, got %+v", errResp)
	}
}

func TestHandleHeartbeat_ReturnsNoContentAndResetsTimer(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	c.lastHeartbeat = c.now().Add(-time.Hour)
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodPost, "/gpu/api/heartbeat", testToken, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

func TestHandleHealthz_NoTokenRequired(t *testing.T) {
	c := newTestController(newFakeUnits(), newFakeReady())
	mux := newMux(c, testToken)

	rec := doRequest(t, mux, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no token, got %d", rec.Code)
	}
}
