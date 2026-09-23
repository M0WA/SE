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
	"searchengine/internal/ports"
)

// fakeUserStore is a minimal ports.UserStore fake, mirroring
// fakeMCPServerStore's shape (chat_test.go) closely.
type fakeUserStore struct {
	users     []domain.User
	listErr   error
	getErr    error
	createErr error
	updateErr error
	deleteErr error
	// getCount counts GetUser calls -- used by chat_test.go to prove a
	// role=admin session never even attempts a per-user prompt lookup.
	getCount int
}

func (f *fakeUserStore) ListUsers(ctx context.Context) ([]domain.User, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.users, nil
}

func (f *fakeUserStore) GetUser(ctx context.Context, id string) (domain.User, error) {
	f.getCount++
	if f.getErr != nil {
		return domain.User{}, f.getErr
	}
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return domain.User{}, ports.ErrUserNotFound
}

func (f *fakeUserStore) GetUserByUsername(ctx context.Context, username string) (domain.User, error) {
	if f.getErr != nil {
		return domain.User{}, f.getErr
	}
	for _, u := range f.users {
		if u.Username == username {
			return u, nil
		}
	}
	return domain.User{}, ports.ErrUserNotFound
}

func (f *fakeUserStore) CreateUser(ctx context.Context, u domain.User) error {
	if f.createErr != nil {
		return f.createErr
	}
	for _, existing := range f.users {
		if existing.Username == u.Username {
			return ports.ErrUsernameTaken
		}
	}
	f.users = append(f.users, u)
	return nil
}

func (f *fakeUserStore) UpdateUser(ctx context.Context, u domain.User) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	for i, existing := range f.users {
		if existing.ID == u.ID {
			f.users[i] = u
			return nil
		}
	}
	return ports.ErrUserNotFound
}

func (f *fakeUserStore) DeleteUser(ctx context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i, existing := range f.users {
		if existing.ID == id {
			f.users = append(f.users[:i], f.users[i+1:]...)
			return nil
		}
	}
	return ports.ErrUserNotFound
}

// testUserPassword/testUserPasswordHash are shared across this file's
// tests -- bcrypt hashing is deliberately slow, so it's computed once.
const testUserPassword = "correct-horse-battery"

var testUserPasswordHash = mustBcryptHash(testUserPassword)

func mustBcryptHash(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return string(hash)
}

func newTestUser(id, username string) domain.User {
	now := time.Now().UTC()
	return domain.User{ID: id, Username: username, PasswordHash: testUserPasswordHash, CreatedAt: now, UpdatedAt: now}
}

var testAdminPasswordHash = mustBcryptHash(testAdminPass)

// newTestAdminUser mirrors newTestUser but for an IsAdmin=true row -- the
// drop-in replacement wherever a test used to rely on the now-removed
// hardcoded ADMIN_USER/ADMIN_PASSWORD account.
func newTestAdminUser(id, username string) domain.User {
	u := newTestUser(id, username)
	u.PasswordHash = testAdminPasswordHash
	u.IsAdmin = true
	return u
}

// testAdminUsersStore returns a fresh *fakeUserStore seeded with exactly
// one IsAdmin=true row for testAdminUser/testAdminPass -- the drop-in
// replacement for restapi.Config{AdminUser: testAdminUser, AdminPass:
// testAdminPass} now that admin is just a flag on an ordinary users row.
func testAdminUsersStore() *fakeUserStore {
	return &fakeUserStore{users: []domain.User{newTestAdminUser("admin1", testAdminUser)}}
}

// fakeAdminRoleSessionStore treats every token as a valid role=admin
// session for a fixed userID -- lets a test mint an authenticated admin
// session without a real POST /login, which now always goes through
// h.users and would corrupt a CRUD test's own store contents (e.g. an
// exact-count ListUsers assertion) if seeded with an extra admin row just
// to make login succeed. Mirrors fakeUserRoleSessionStore (account_test.go).
type fakeAdminRoleSessionStore struct{}

func (fakeAdminRoleSessionStore) CreateSession(context.Context, string, time.Time, string, string) error {
	return nil
}
func (fakeAdminRoleSessionStore) ValidSession(context.Context, string) (bool, string, string, error) {
	return true, domain.RoleAdmin, "admin1", nil
}
func (fakeAdminRoleSessionStore) RevokeSession(context.Context, string) error { return nil }

// adminAuthedHandlerWithUsers builds a Handler with store wired in as
// Users and an admin session minted via fakeAdminRoleSessionStore (not a
// real POST /login -- store is the CRUD tests' own fixture, e.g. an
// exact-length user list, which a real admin login would corrupt by
// needing its own extra admin row seeded into the same store), for the
// CRUD tests below. See userAuthedHandler for a DB-backed user session
// instead, and auth_test.go for real end-to-end admin login coverage.
func adminAuthedHandlerWithUsers(t *testing.T, store ports.UserStore) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	return adminAuthedHandlerFromConfig(t, restapi.Config{Users: store})
}

// userAuthedHandler logs in as u via a real POST /login, proving the
// DB-backed-account login path works end to end, not just that
// authenticatedRole would theoretically accept it.
func userAuthedHandler(t *testing.T, store *fakeUserStore, u domain.User) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{Users: store})
	body, _ := json.Marshal(map[string]string{"username": u.Username, "password": testUserPassword})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user login failed: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected a session cookie after login")
	}
	return h, cookies[0]
}

// --- Role enforcement: the core security boundary this feature adds ---

// TestRoleEnforcement_AdminSessionReachesBothRouteSets is the key
// regression check: regular-user accounts must not narrow admin access.
func TestRoleEnforcement_AdminSessionReachesBothRouteSets(t *testing.T) {
	store := &fakeUserStore{}
	h, cookie := adminAuthedHandlerWithUsers(t, store)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected admin session to reach an admin API route (200), got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected admin session to reach the search index (200), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoleEnforcement_UserSessionReachesSearchButNotAdminAPI(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected a regular-user session to reach the search index (200), got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected a regular-user session to be refused an admin API route (403), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoleEnforcement_UserSessionReachesSearchButNotAdminPage(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected a regular-user session to be refused the admin overview page (403), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoleEnforcement_UnauthenticatedRefusedOnBothRouteSets(t *testing.T) {
	h := restapi.New(restapi.Config{})

	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unauthenticated admin API request, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec = httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect for an unauthenticated admin page request, got %d", rec.Code)
	}
}

// --- Login: DB-backed-account path ---

func TestHandleLogin_DBUserSuccess(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: store})
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_DBUserWrongPasswordFails(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: store})
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "wrong-password"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestHandleLogin_UserLookupErrorFailsClosed proves a genuine store error
// during the DB-user lookup still fails login (401) rather than panicking
// -- authenticatedRole must never let a lookup failure become a success.
func TestHandleLogin_UserLookupErrorFailsClosed(t *testing.T) {
	store := &fakeUserStore{getErr: errors.New("db unavailable")}
	h := restapi.New(restapi.Config{Users: store})
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "whatever"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleLogin_UnknownUsernameFails(t *testing.T) {
	store := &fakeUserStore{}
	h := restapi.New(restapi.Config{Users: store})
	body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "whatever"})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestHandleLogin_NoUsersConfiguredAlwaysFails proves a Handler with
// h.users == nil (e.g. crawl-server) fails every login attempt closed --
// there is no separate hardcoded admin fallback any more, so this must
// never panic or, worse, silently authenticate when unset.
func TestHandleLogin_NoUsersConfiguredAlwaysFails(t *testing.T) {
	h := restapi.New(restapi.Config{})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAdminUsers: GET (list) / POST (create) ---

func TestHandleAdminUsers_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminUsers_ListSuccess(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice"), newTestUser("user2", "bob")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 users, got %d", len(got))
	}
	for _, u := range got {
		if _, hasHash := u["password_hash"]; hasHash {
			t.Errorf("expected password_hash to never be present in the response, got %+v", u)
		}
		if _, hasPassword := u["password"]; hasPassword {
			t.Errorf("expected password to never be present in the response, got %+v", u)
		}
	}
}

func TestHandleAdminUsers_ListStoreError(t *testing.T) {
	store := &fakeUserStore{listErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminUsers_CreateSuccess(t *testing.T) {
	store := &fakeUserStore{}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"username": "carol", "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 || store.users[0].Username != "carol" {
		t.Fatalf("expected the user to be persisted, got %+v", store.users)
	}
	if store.users[0].PasswordHash == "" || store.users[0].PasswordHash == "a-long-enough-password" {
		t.Errorf("expected the stored password to be hashed, not blank or plaintext, got %q", store.users[0].PasswordHash)
	}
	if bcrypt.CompareHashAndPassword([]byte(store.users[0].PasswordHash), []byte("a-long-enough-password")) != nil {
		t.Errorf("expected the stored hash to verify against the submitted password")
	}
	if strings.Contains(rec.Body.String(), "a-long-enough-password") {
		t.Errorf("expected the response to never echo the password, got: %s", rec.Body.String())
	}
	if store.users[0].IsAdmin {
		t.Errorf("expected is_admin to default to false when omitted, got true")
	}
}

// TestHandleAdminUsers_CreateWithIsAdminTrue proves is_admin round-trips
// through create -- any account can be minted as an admin directly.
func TestHandleAdminUsers_CreateWithIsAdminTrue(t *testing.T) {
	store := &fakeUserStore{}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]interface{}{
		"username": "carol", "password": "a-long-enough-password", "is_admin": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 || !store.users[0].IsAdmin {
		t.Fatalf("expected the new user to be persisted with is_admin=true, got %+v", store.users)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if isAdmin, _ := resp["is_admin"].(bool); !isAdmin {
		t.Errorf("expected is_admin=true in the response, got %+v", resp)
	}
}

func TestHandleAdminUsers_CreateEmptyUsernameRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{"username": "  ", "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUsers_CreateShortPasswordRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{"username": "dave", "password": "short"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminUsers_CreateTooLongPasswordRejected proves a password over
// bcrypt's 72-byte limit gets a clean 400, not an opaque 500.
func TestHandleAdminUsers_CreateTooLongPasswordRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{"username": "dave", "password": strings.Repeat("a", 73)})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminUsers_CreateNoUsernameIsReserved proves no username is
// special-cased any more -- there is no separate hardcoded admin account
// whose name would collide, so even "admin" is just an ordinary username.
func TestHandleAdminUsers_CreateNoUsernameIsReserved(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUsers_CreateDuplicateUsernameConflict(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUsers_CreateListErrorPropagates(t *testing.T) {
	store := &fakeUserStore{listErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"username": "eve", "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUsers_CreateStoreError(t *testing.T) {
	store := &fakeUserStore{createErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"username": "eve", "password": "a-long-enough-password"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUsers_MethodNotAllowed(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAdminUsers_InvalidJSON(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// --- handleAdminUpdateUser: PATCH (password reset) ---

func TestHandleAdminUpdateUser_Success(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"password": "a-brand-new-password"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if bcrypt.CompareHashAndPassword([]byte(store.users[0].PasswordHash), []byte("a-brand-new-password")) != nil {
		t.Errorf("expected the stored hash to verify against the new password")
	}
	if store.users[0].Username != "alice" {
		t.Errorf("expected username unchanged by a password reset, got %q", store.users[0].Username)
	}
}

func TestHandleAdminUpdateUser_ShortPasswordRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"password": "short"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateUser_TooLongPasswordRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"password": strings.Repeat("a", 73)})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUpdateUser_NotFound(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{"password": "a-brand-new-password"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/missing", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateUser_GetErrorPropagates(t *testing.T) {
	store := &fakeUserStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"password": "a-brand-new-password"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateUser_UpdateStoreError(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}, updateErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"password": "a-brand-new-password"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminUpdateUser_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, nil)
	body, _ := json.Marshal(map[string]string{"password": "a-brand-new-password"})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestHandleAdminUpdateUser_IsAdminGrantedSucceeds proves is_admin
// round-trips through update -- promoting a regular account to admin.
func TestHandleAdminUpdateUser_IsAdminGrantedSucceeds(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]interface{}{"is_admin": true})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !store.users[0].IsAdmin {
		t.Errorf("expected user1 to be promoted to admin")
	}
}

// TestHandleAdminUpdateUser_DemoteLastAdminRejected proves demoting the
// sole remaining admin is refused (400), and the row is left unchanged --
// otherwise no admin session could ever reach this page again.
func TestHandleAdminUpdateUser_DemoteLastAdminRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestAdminUser("admin1", "solo-admin")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]interface{}{"is_admin": false})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/admin1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !store.users[0].IsAdmin {
		t.Errorf("expected the last admin's IsAdmin to remain true after a refused demotion, got %+v", store.users[0])
	}
}

// TestHandleAdminUpdateUser_DemoteNonLastAdminSucceeds proves demoting one
// of two-or-more admins is allowed.
func TestHandleAdminUpdateUser_DemoteNonLastAdminSucceeds(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{
		newTestAdminUser("admin1", "admin-one"),
		newTestAdminUser("admin2", "admin-two"),
	}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]interface{}{"is_admin": false})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/admin1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].IsAdmin {
		t.Errorf("expected admin1 to be demoted")
	}
	if !store.users[1].IsAdmin {
		t.Errorf("expected admin2 to remain an admin")
	}
}

// TestHandleAdminUpdateUser_DemoteLastAdminListErrorPropagates proves a
// ListUsers failure during the last-admin check surfaces as 500, not a
// silently-allowed demotion.
func TestHandleAdminUpdateUser_DemoteLastAdminListErrorPropagates(t *testing.T) {
	store := &fakeUserStore{
		users:   []domain.User{newTestAdminUser("admin1", "solo-admin")},
		listErr: errors.New("db unavailable"),
	}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]interface{}{"is_admin": false})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/admin1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- handleAdminDeleteUser: DELETE ---

func TestHandleAdminDeleteUser_Success(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 0 {
		t.Errorf("expected the user removed, got %+v", store.users)
	}
}

func TestHandleAdminDeleteUser_NotFound(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleAdminDeleteUser_GetErrorPropagates proves a genuine GetUser
// error (not ErrUserNotFound) surfaces as 500, not treated as not-found.
func TestHandleAdminDeleteUser_GetErrorPropagates(t *testing.T) {
	store := &fakeUserStore{getErr: errors.New("db down")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminDeleteUser_StoreError(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}, deleteErr: errors.New("write failed")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminDeleteUser_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, nil)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

// TestHandleAdminDeleteUser_LastAdminRejected proves deleting the sole
// remaining admin is refused (400), and the row still exists afterward --
// otherwise no admin session could ever reach this page again.
func TestHandleAdminDeleteUser_LastAdminRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestAdminUser("admin1", "solo-admin")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/admin1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 {
		t.Errorf("expected the last admin to still exist after a refused delete, got %+v", store.users)
	}
}

// TestHandleAdminDeleteUser_NonLastAdminSucceeds proves deleting one of
// two-or-more admins is allowed.
func TestHandleAdminDeleteUser_NonLastAdminSucceeds(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{
		newTestAdminUser("admin1", "admin-one"),
		newTestAdminUser("admin2", "admin-two"),
	}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/admin1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 || store.users[0].ID != "admin2" {
		t.Errorf("expected only admin2 to remain, got %+v", store.users)
	}
}

// TestHandleAdminDeleteUser_LastAdminListErrorPropagates proves a
// ListUsers failure during the last-admin check surfaces as 500, not a
// silently-allowed delete.
func TestHandleAdminDeleteUser_LastAdminListErrorPropagates(t *testing.T) {
	store := &fakeUserStore{
		users:   []domain.User{newTestAdminUser("admin1", "solo-admin")},
		listErr: errors.New("db unavailable"),
	}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/admin/api/users/admin1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Users page/JS static routes ---

func TestHandleAdminUsersPage_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminUsersJS_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin_users.js", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminUsersPage_UserRoleRefused(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

// --- handleAdminGetUser: GET /admin/api/users/{id} ---

func TestHandleAdminGetUser_Success(t *testing.T) {
	u := newTestUser("user1", "alice")
	u.CustomPrompt = "Be terse."
	store := &fakeUserStore{users: []domain.User{u}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got["username"] != "alice" || got["custom_prompt"] != "Be terse." {
		t.Errorf("unexpected response: %+v", got)
	}
	if _, hasHash := got["password_hash"]; hasHash {
		t.Errorf("expected the response to never contain the password hash, got: %s", rec.Body.String())
	}
}

func TestHandleAdminGetUser_NotFound(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAdminGetUser_StoreError(t *testing.T) {
	store := &fakeUserStore{getErr: errors.New("db unavailable")}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAdminGetUser_NotConfigured(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAdminGetUser_UserRoleRefused(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

// --- Admin-editable custom_prompt (create + update) ---

func TestHandleAdminUsers_CreateWithCustomPrompt(t *testing.T) {
	store := &fakeUserStore{}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{
		"username": "carol", "password": "a-long-enough-password", "custom_prompt": "Answer briefly.",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.users) != 1 || store.users[0].CustomPrompt != "Answer briefly." {
		t.Fatalf("expected custom_prompt stored, got %+v", store.users)
	}
}

func TestHandleAdminUsers_CreateTooLongCustomPromptRejected(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	body, _ := json.Marshal(map[string]string{
		"username": "carol", "password": "a-long-enough-password", "custom_prompt": strings.Repeat("a", 4001),
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/users", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAdminUpdateUser_CustomPromptOnlyLeavesPasswordUnchanged proves
// the admin can edit just the custom prompt -- an omitted password is left
// alone, not rejected as missing/too-short.
func TestHandleAdminUpdateUser_CustomPromptOnlyLeavesPasswordUnchanged(t *testing.T) {
	u := newTestUser("user1", "alice")
	originalHash := u.PasswordHash
	store := &fakeUserStore{users: []domain.User{u}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"custom_prompt": "Be terse."})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != "Be terse." {
		t.Errorf("expected custom_prompt updated, got %q", store.users[0].CustomPrompt)
	}
	if store.users[0].PasswordHash != originalHash {
		t.Errorf("expected password hash unchanged, got %q, want %q", store.users[0].PasswordHash, originalHash)
	}
}

// TestHandleAdminUpdateUser_CustomPromptClearedToEmpty proves an explicit
// empty string clears the prompt, unlike omitting the field entirely.
func TestHandleAdminUpdateUser_CustomPromptClearedToEmpty(t *testing.T) {
	u := newTestUser("user1", "alice")
	u.CustomPrompt = "Old prompt."
	store := &fakeUserStore{users: []domain.User{u}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"custom_prompt": ""})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.users[0].CustomPrompt != "" {
		t.Errorf("expected custom_prompt cleared, got %q", store.users[0].CustomPrompt)
	}
}

func TestHandleAdminUpdateUser_TooLongCustomPromptRejected(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	body, _ := json.Marshal(map[string]string{"custom_prompt": strings.Repeat("a", 4001)})
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAdminUpdateUser_NoFieldsIsANoOp(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := adminAuthedHandlerWithUsers(t, store)
	req := httptest.NewRequest(http.MethodPatch, "/admin/api/users/user1", bytes.NewReader([]byte("{}")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- User subpage/JS static routes ---

func TestHandleAdminUserPage_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}})
	req := httptest.NewRequest(http.MethodGet, "/admin/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminUserJS_Success(t *testing.T) {
	h, cookie := adminAuthedHandlerWithUsers(t, &fakeUserStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin_user.js", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandleAdminUserPage_UserRoleRefused(t *testing.T) {
	store := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := userAuthedHandler(t, store, store.users[0])
	req := httptest.NewRequest(http.MethodGet, "/admin/users/user1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}
