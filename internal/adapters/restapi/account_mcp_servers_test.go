package restapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
)

// accountMCPServersAuthedHandler logs in as u (role=user, via a real POST
// /login) with Users and UserMCPServers wired, mirroring userAuthedHandler.
func accountMCPServersAuthedHandler(t *testing.T, userStore *fakeUserStore, mcpStore *fakeUserMCPServerStore, u domain.User) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	h := restapi.New(restapi.Config{
		Users: userStore, UserMCPServers: mcpStore,
	})
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

func createTestUserMCPServer(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, mcpServerResp) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/account/api/mcp-servers", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var resp mcpServerResp
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decoding create response: %v", err)
		}
	}
	return rec.Code, resp
}

func patchTestUserMCPServer(t *testing.T, h *restapi.Handler, cookie *http.Cookie, id string, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/account/api/mcp-servers/"+id, bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

func TestHandleAccountMCPServers_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Users: &fakeUserStore{}, UserMCPServers: &fakeUserMCPServerStore{}})
	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestHandleAccountMCPServers_AdminRoleAllowed proves an admin session (a
// real User row with IsAdmin=true) can use self-service personal MCP
// servers exactly like any other account -- same as the rest of /account/api.
func TestHandleAccountMCPServers_AdminRoleAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{
		Users: testAdminUsersStore(), UserMCPServers: &fakeUserMCPServerStore{},
	})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login failed: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]

	listReq := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Errorf("expected 200 for an admin session, got %d", listRec.Code)
	}
}

// TestHandleAccountMCPServers_NotConfigured builds its own Config instead
// of passing a nil *fakeUserMCPServerStore: a nil concrete pointer in the
// interface field is a non-nil interface (the typed-nil gotcha), defeating
// the nil check. Omitting the field, like the admin equivalent, is the
// only way to get a genuinely nil interface.
func TestHandleAccountMCPServers_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: userStore})
	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("user login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when mcp servers aren't configured, got %d", rec.Code)
	}
}

func TestHandleAccountMCPServers_MethodNotAllowed(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api/mcp-servers", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAccountMCPServers_CreateInvalidJSON(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodPost, "/account/api/mcp-servers", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAccountMCPServers_CreateValidation covers every
// validateUserMCPServerRequest rejection branch: empty name, "stdio"
// transport (the http-only enforcement itself), unrecognized transport,
// and missing base_url.
func TestHandleAccountMCPServers_CreateValidation(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])

	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing name", map[string]interface{}{"transport": "http", "base_url": "https://example.com/mcp"}},
		{"stdio transport rejected", map[string]interface{}{"name": "web", "transport": "stdio", "command": "/bin/sh"}},
		{"unrecognized transport", map[string]interface{}{"name": "web", "transport": "carrier-pigeon"}},
		{"http missing base_url", map[string]interface{}{"name": "web", "transport": "http"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := createTestUserMCPServer(t, h, cookie, tc.body)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", code)
			}
		})
	}
}

// TestHandleAccountMCPServers_CreateForcesHTTPTransport proves create
// always stores Transport "http" -- the handler forces it explicitly too,
// belt to validateUserMCPServerRequest's suspenders.
func TestHandleAccountMCPServers_CreateForcesHTTPTransport(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	code, created := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "My Notes", "transport": "http", "base_url": "https://example.com/mcp",
		"enabled": true, "prompt": "Be terse.", "gated_by_web_search": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if created.Transport != "http" {
		t.Errorf("expected transport forced to http, got %q", created.Transport)
	}
	if created.ID != "my_notes" {
		t.Errorf("expected the ID minted from the name, got %q", created.ID)
	}
	if created.BaseURL != "https://example.com/mcp" || !created.Enabled {
		t.Errorf("unexpected round trip: %+v", created)
	}
	if created.Prompt != "Be terse." || !created.GatedByWebSearch {
		t.Errorf("expected prompt/gated_by_web_search round tripped, got %+v", created)
	}
}

// TestHandleAccountMCPServers_CreateDedupesIDOnNameCollision mirrors the
// admin test: two servers from the same owner with the same name get
// distinct IDs, per domain.NewMCPServerID's dedupe rule.
func TestHandleAccountMCPServers_CreateDedupesIDOnNameCollision(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])

	_, first := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://a.example.com",
	})
	_, second := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://b.example.com",
	})
	if first.ID == second.ID {
		t.Errorf("expected distinct IDs for two servers named the same, got both %q", first.ID)
	}
}

func TestHandleAccountMCPServers_ListError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{listErr: errors.New("db unavailable")}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountMCPServers_CreateListErrorPropagates(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{listErr: errors.New("db unavailable")}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	code, _ := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com",
	})
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

func TestHandleAccountMCPServers_CreateStoreError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{createErr: errors.New("write failed")}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	code, _ := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com",
	})
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

func TestHandleAccountGetMCPServer_ListError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{listErr: errors.New("db unavailable")}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_ListErrorLookingUpExisting(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{listErr: errors.New("db unavailable")}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	rec := patchTestUserMCPServer(t, h, cookie, "anything", map[string]interface{}{
		"name": "x", "transport": "http", "base_url": "https://example.com",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_StoreError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	mcpStore := &fakeUserMCPServerStore{}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	_, created := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com",
	})
	mcpStore.updateErr = errors.New("write failed")
	rec := patchTestUserMCPServer(t, h, cookie, created.ID, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com",
	})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountDeleteMCPServer_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: userStore})
	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodDelete, "/account/api/mcp-servers/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountMCPServers_ListScopedToOwner(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		newTestUser("user1", "alice"), newTestUser("user2", "bob"),
	}}
	mcpStore := &fakeUserMCPServerStore{}
	hAlice, cookieAlice := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	if _, created := createTestUserMCPServer(t, hAlice, cookieAlice, map[string]interface{}{
		"name": "alice's server", "transport": "http", "base_url": "https://a.example.com",
	}); created.ID == "" {
		t.Fatalf("expected alice's server to be created")
	}
	hBob, cookieBob := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[1])
	if _, created := createTestUserMCPServer(t, hBob, cookieBob, map[string]interface{}{
		"name": "bob's server", "transport": "http", "base_url": "https://b.example.com",
	}); created.ID == "" {
		t.Fatalf("expected bob's server to be created")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	listReq.AddCookie(cookieAlice)
	listRec := httptest.NewRecorder()
	hAlice.RoutesSearch().ServeHTTP(listRec, listReq)
	var list []mcpServerResp
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].Name != "alice's server" {
		t.Errorf("expected alice to see only her own server, got %+v", list)
	}
}

func TestHandleAccountGetMCPServer_Success(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	_, created := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com/mcp",
	})

	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountGetMCPServer_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: userStore})
	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers/anything", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountGetMCPServer_NotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleAccountGetMCPServer_WrongOwnerNotFound proves bob can't read
// alice's server by guessing its ID -- looks like a nonexistent ID.
func TestHandleAccountGetMCPServer_WrongOwnerNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		newTestUser("user1", "alice"), newTestUser("user2", "bob"),
	}}
	mcpStore := &fakeUserMCPServerStore{}
	hAlice, cookieAlice := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	_, created := createTestUserMCPServer(t, hAlice, cookieAlice, map[string]interface{}{
		"name": "alice's server", "transport": "http", "base_url": "https://a.example.com",
	})
	hBob, cookieBob := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[1])

	req := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers/"+created.ID, nil)
	req.AddCookie(cookieBob)
	rec := httptest.NewRecorder()
	hBob.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob reading alice's server, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_ReplacesEditableFields(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	_, created := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com/mcp", "enabled": true,
	})

	rec := patchTestUserMCPServer(t, h, cookie, created.ID, map[string]interface{}{
		"name": "renamed", "transport": "http", "base_url": "https://renamed.example.com/mcp", "enabled": false,
		"prompt": "renamed prompt", "gated_by_web_search": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got mcpServerResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Name != "renamed" || got.BaseURL != "https://renamed.example.com/mcp" || got.Enabled {
		t.Errorf("expected every editable field replaced, got %+v", got)
	}
}

func TestHandleAccountUpdateMCPServer_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h := restapi.New(restapi.Config{Users: userStore})
	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	loginReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(loginRec, loginReq)
	cookie := loginRec.Result().Cookies()[0]

	rec := patchTestUserMCPServer(t, h, cookie, "anything", map[string]interface{}{
		"name": "x", "transport": "http", "base_url": "https://example.com",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_InvalidJSON(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodPatch, "/account/api/mcp-servers/anything", bytes.NewReader([]byte("{not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_InvalidRequest(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	rec := patchTestUserMCPServer(t, h, cookie, "anything", map[string]interface{}{"name": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateMCPServer_NotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	rec := patchTestUserMCPServer(t, h, cookie, "missing", map[string]interface{}{
		"name": "x", "transport": "http", "base_url": "https://example.com",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleAccountUpdateMCPServer_WrongOwnerNotFound is the get test's write-path counterpart.
func TestHandleAccountUpdateMCPServer_WrongOwnerNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		newTestUser("user1", "alice"), newTestUser("user2", "bob"),
	}}
	mcpStore := &fakeUserMCPServerStore{}
	hAlice, cookieAlice := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	_, created := createTestUserMCPServer(t, hAlice, cookieAlice, map[string]interface{}{
		"name": "alice's server", "transport": "http", "base_url": "https://a.example.com",
	})
	hBob, cookieBob := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[1])

	rec := patchTestUserMCPServer(t, hBob, cookieBob, created.ID, map[string]interface{}{
		"name": "hijacked", "transport": "http", "base_url": "https://evil.example.com",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob updating alice's server, got %d", rec.Code)
	}
}

func TestHandleAccountDeleteMCPServer_Success(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	_, created := createTestUserMCPServer(t, h, cookie, map[string]interface{}{
		"name": "server", "transport": "http", "base_url": "https://example.com/mcp",
	})

	req := httptest.NewRequest(http.MethodDelete, "/account/api/mcp-servers/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountDeleteMCPServer_NotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api/mcp-servers/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleAccountDeleteMCPServer_WrongOwnerNotFound is the get test's delete-path counterpart.
func TestHandleAccountDeleteMCPServer_WrongOwnerNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		newTestUser("user1", "alice"), newTestUser("user2", "bob"),
	}}
	mcpStore := &fakeUserMCPServerStore{}
	hAlice, cookieAlice := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[0])
	_, created := createTestUserMCPServer(t, hAlice, cookieAlice, map[string]interface{}{
		"name": "alice's server", "transport": "http", "base_url": "https://a.example.com",
	})
	hBob, cookieBob := accountMCPServersAuthedHandler(t, userStore, mcpStore, userStore.users[1])

	req := httptest.NewRequest(http.MethodDelete, "/account/api/mcp-servers/"+created.ID, nil)
	req.AddCookie(cookieBob)
	rec := httptest.NewRecorder()
	hBob.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for bob deleting alice's server, got %d", rec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/account/api/mcp-servers", nil)
	listReq.AddCookie(cookieAlice)
	listRec := httptest.NewRecorder()
	hAlice.RoutesSearch().ServeHTTP(listRec, listReq)
	var list []mcpServerResp
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected alice's server to survive bob's failed delete, got %+v", list)
	}
}

func TestHandleAccountMCPServersPages_ServedForRegularUser(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{newTestUser("user1", "alice")}}
	h, cookie := accountMCPServersAuthedHandler(t, userStore, &fakeUserMCPServerStore{}, userStore.users[0])

	for _, path := range []string{"/account/mcp-servers", "/account/mcp-servers/new", "/account_mcp_servers.js", "/account_mcp_server.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.RoutesSearch().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: expected 200, got %d", path, rec.Code)
		}
	}
}
