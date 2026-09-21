package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
)

// --- GET /session ---

func TestHandleSession_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})
	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleSession_AdminRole(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["role"] != domain.RoleAdmin {
		t.Errorf("expected role=admin, got %+v", resp)
	}
}

func TestHandleSession_UserRole(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodGet, "/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["role"] != domain.RoleUser {
		t.Errorf("expected role=user, got %+v", resp)
	}
}

func TestHandleSession_HeadRequestAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodHead, "/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty body for HEAD, got %d bytes", rec.Body.Len())
	}
}

func TestHandleSession_MethodNotAllowed(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})
	req := httptest.NewRequest(http.MethodPost, "/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// --- requireRegularUserAuthPage/API gating (via /account, /account/api) ---

func TestAccount_Unauthenticated_PageRedirectsAPIRefuses(t *testing.T) {
	h := restapi.New(restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass})

	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect for /account, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api", nil)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for /account/api, got %d", rec.Code)
	}
}

func TestAccount_AdminRoleRefused(t *testing.T) {
	h, cookie := authedHandler(t, &fakeSearch{}, &fakeJobService{})

	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for /account as admin, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not available for the admin account") {
		t.Errorf("expected explanatory message, got %q", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for /account/api as admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAccount_UserRoleReachesPageAndAPI(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /account as a regular user, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /account/api as a regular user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountJS_Success(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account.js", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// --- GET /account/api ---

func accountUserAuthedHandler(t *testing.T, store *fakeUserStore, u domain.User) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return userAuthedHandler(t, store, u)
}

func getAccount(t *testing.T, h *restapi.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/account/api", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

func patchAccount(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	switch v := body.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			t.Fatalf("marshaling request body: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPatch, "/account/api", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

// fakeUserRoleSessionStore is a minimal ports.SessionStore fake that
// treats EVERY token as a valid domain.RoleUser session for a fixed
// userID -- used only to reach handleAccount's own requireConfigured(h.users
// != nil) branch with h.users deliberately left nil, which a real login
// (always going through h.users when issuing a role=user session) could
// never produce on its own.
type fakeUserRoleSessionStore struct{}

func (fakeUserRoleSessionStore) CreateSession(context.Context, string, time.Time, string, string) error {
	return nil
}
func (fakeUserRoleSessionStore) ValidSession(context.Context, string) (bool, string, string, error) {
	return true, domain.RoleUser, "user1", nil
}
func (fakeUserRoleSessionStore) RevokeSession(context.Context, string) error { return nil }

func TestHandleAccount_NotConfigured(t *testing.T) {
	h := restapi.New(restapi.Config{Sessions: fakeUserRoleSessionStore{}})
	req := httptest.NewRequest(http.MethodGet, "/account/api", nil)
	req.AddCookie(&http.Cookie{Name: "se_session", Value: "anything"})
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccount_GetSuccess(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	store.users[0].CustomPrompt = "Be terse."
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := getAccount(t, h, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp accountResponseForTest
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Username != "alice" || resp.CustomPrompt != "Be terse." {
		t.Errorf("unexpected response: %+v", resp)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("expected password/password_hash never present, got %s", rec.Body.String())
	}
}

func TestHandleAccount_GetStoreError(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	store.getErr = errors.New("db unavailable")
	rec := getAccount(t, h, cookie)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- PATCH /account/api ---

func TestHandleAccount_PatchCustomPromptOnly(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	newPrompt := "Always answer in haiku."
	rec := patchAccount(t, h, cookie, map[string]interface{}{"custom_prompt": newPrompt})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != newPrompt {
		t.Errorf("expected custom prompt persisted, got %q", store.users[0].CustomPrompt)
	}
	if store.users[0].PasswordHash != testUserPasswordHash {
		t.Errorf("expected password hash unchanged when password omitted, got %q", store.users[0].PasswordHash)
	}
}

func TestHandleAccount_PatchCustomPromptClearedToEmpty(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	store.users[0].CustomPrompt = "Be terse."
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	empty := ""
	rec := patchAccount(t, h, cookie, map[string]interface{}{"custom_prompt": empty})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != "" {
		t.Errorf("expected custom prompt cleared, got %q", store.users[0].CustomPrompt)
	}
}

func TestHandleAccount_PatchOmittedCustomPromptLeavesItUnchanged(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	store.users[0].CustomPrompt = "Be terse."
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, map[string]interface{}{})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != "Be terse." {
		t.Errorf("expected custom prompt unchanged when omitted, got %q", store.users[0].CustomPrompt)
	}
}

func TestHandleAccount_PatchCustomPromptTooLongRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, map[string]interface{}{"custom_prompt": strings.Repeat("a", 4001)})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != "" {
		t.Errorf("expected custom prompt left untouched on rejection, got %q", store.users[0].CustomPrompt)
	}
}

func TestHandleAccount_PatchPasswordChanged(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, map[string]interface{}{"password": "a-brand-new-password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if bcrypt.CompareHashAndPassword([]byte(store.users[0].PasswordHash), []byte("a-brand-new-password")) != nil {
		t.Errorf("expected the stored hash to verify against the new password")
	}
}

func TestHandleAccount_PatchShortPasswordRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, map[string]interface{}{"password": "short"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].PasswordHash != testUserPasswordHash {
		t.Errorf("expected password unchanged on rejection, got %q", store.users[0].PasswordHash)
	}
}

func TestHandleAccount_PatchTooLongPasswordRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, map[string]interface{}{"password": strings.Repeat("a", 73)})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccount_PatchGetErrorPropagates(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	store.getErr = errors.New("db unavailable")
	rec := patchAccount(t, h, cookie, map[string]interface{}{"custom_prompt": "hi"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccount_PatchUpdateStoreError(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	store.updateErr = errors.New("write failed")
	rec := patchAccount(t, h, cookie, map[string]interface{}{"custom_prompt": "hi"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccount_PatchInvalidJSON(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	rec := patchAccount(t, h, cookie, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccount_MethodNotAllowed(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountUserAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// accountResponseForTest mirrors restapi's own unexported accountResponse
// wire shape, for decoding in this external test package.
type accountResponseForTest struct {
	Username     string `json:"username"`
	CustomPrompt string `json:"custom_prompt"`
}
