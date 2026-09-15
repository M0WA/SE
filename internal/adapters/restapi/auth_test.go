package restapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
)

func TestHandleLogin_Success(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass, "next": "/admin"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Error("expected a session cookie to be set")
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["redirect"] != "/admin" {
		t.Errorf("expected redirect to /admin, got %q", resp["redirect"])
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("expected no session cookie on failed login")
	}
}

// TestHandleLogin_LockoutAfterTooManyFailures proves the end-to-end
// behavior of loginLimiter (see its doc comment): enough failed attempts
// from the same source lock out even a correct subsequent attempt, with a
// 429 and a Retry-After header telling the caller how long to wait.
func TestHandleLogin_LockoutAfterTooManyFailures(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	wrongBody, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": "wrong"})

	for i := 0; i < 6; i++ { // past loginMaxAttempts (5)
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(wrongBody))
		req.Header.Set("X-Real-IP", "203.0.113.9")
		rec := httptest.NewRecorder()
		h.RoutesAdmin().ServeHTTP(rec, req)
	}

	// Even the *correct* credentials are now refused, since the lockout
	// kicks in before checkCredentials is ever consulted.
	correctBody, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(correctBody))
	req.Header.Set("X-Real-IP", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once locked out, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on a 429")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("expected no session cookie while locked out")
	}
}

// TestHandleLogin_LockoutIsPerSource proves one source's lockout doesn't
// block a different one -- a different client IP still gets a normal 401
// for a wrong password rather than inheriting someone else's lockout.
func TestHandleLogin_LockoutIsPerSource(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	wrongBody, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": "wrong"})

	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(wrongBody))
		req.Header.Set("X-Real-IP", "203.0.113.9")
		rec := httptest.NewRecorder()
		h.RoutesAdmin().ServeHTTP(rec, req)
	}

	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(wrongBody))
	req.Header.Set("X-Real-IP", "203.0.113.10")
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected a different source IP to be unaffected (401, not 429), got %d", rec.Code)
	}
}

func TestHandleLogin_NotConfigured(t *testing.T) {
	h := restapi.New(restapi.Config{})
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "anything"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no admin account is configured, got %d", rec.Code)
	}
}

func TestHandleLogin_RejectsOpenRedirect(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass, "next": "https://evil.example/"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["redirect"] != "/admin" {
		t.Errorf("expected an absolute next to be rejected in favor of /admin, got %q", resp["redirect"])
	}
}

func TestHandleLogin_InvalidJSON(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleLoginPage_HeadRequestAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodHead, "/login", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD request, got %d bytes", rec.Body.Len())
	}
}

func TestHandleLoginRoute_GetServesPage(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/login?next=%2Fadmin", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected GET /login to serve the login page (200), got %d", rec.Code)
	}
}

func TestHandleLoginRoute_UnsupportedMethod(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodPut, "/login", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleLoginPage_AlreadyAuthenticatedRedirects(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected an already-signed-in visitor to be redirected, got %d", rec.Code)
	}
}

func TestHandleLogout_ClearsSession(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(cookie)
	logoutRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from logout, got %d", logoutRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected the revoked session to be treated as unauthenticated, got %d", rec.Code)
	}
}

func TestHandleLogout_WithoutSessionCookieStillSucceeds(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 even with no session cookie, got %d", rec.Code)
	}
}

func TestHandleLogout_MethodNotAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
