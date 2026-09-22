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

// pinnedChatResp mirrors account_chats.go's own unexported wire type --
// same convention fileResponse/mcpServerResp use for an external _test
// package.
type pinnedChatResp struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	AgentID   string               `json:"agent_id"`
	History   []domain.ChatMessage `json:"history"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt string               `json:"updated_at"`
}

func chatsAuthedHandler(t *testing.T, userStore *fakeUserStore, chatStore *fakeChatStore, u domain.User) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	cfg := restapi.Config{AdminUser: testAdminUser, AdminPass: testAdminPass, Users: userStore}
	if chatStore != nil {
		cfg.Chats = chatStore
	}
	h := restapi.New(cfg)
	body, _ := json.Marshal(map[string]string{"username": u.Username, "password": testUserPassword})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user login failed: %d %s", rec.Code, rec.Body.String())
	}
	return h, rec.Result().Cookies()[0]
}

func createTestChat(t *testing.T, h *restapi.Handler, cookie *http.Cookie, body map[string]interface{}) (int, pinnedChatResp) {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/account/api/chats", bytes.NewReader(data))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var resp pinnedChatResp
	if rec.Code == http.StatusCreated {
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	}
	return rec.Code, resp
}

func TestHandleAccountChats_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, nil, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/chats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountChats_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Users: &fakeUserStore{}, Chats: &fakeChatStore{}})
	req := httptest.NewRequest(http.MethodGet, "/account/api/chats", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAccountChats_CreateThenList(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])

	code, created := createTestChat(t, h, cookie, map[string]interface{}{
		"title": "My chat", "agent_id": "researcher",
		"history": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if created.ID == "" || created.Title != "My chat" || created.AgentID != "researcher" {
		t.Errorf("unexpected created chat: %+v", created)
	}
	if len(created.History) != 1 || created.History[0].Content != "hi" {
		t.Errorf("expected history round tripped, got %+v", created.History)
	}

	req := httptest.NewRequest(http.MethodGet, "/account/api/chats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var list []pinnedChatResp
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("expected the created chat listed, got %+v", list)
	}
}

func TestHandleAccountChats_ListStoreErrorIsInternalServerError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{listErr: errors.New("boom")}, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/chats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountChats_CreateStoreErrorIsInternalServerError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{createErr: errors.New("boom")}, userStore.users[0])
	code, _ := createTestChat(t, h, cookie, map[string]interface{}{"title": "x"})
	if code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", code)
	}
}

func TestHandleAccountChats_CreateMalformedJSONIsBadRequest(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodPost, "/account/api/chats", bytes.NewReader([]byte("not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccountChats_CreateEmptyTitleRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	code, _ := createTestChat(t, h, cookie, map[string]interface{}{"title": ""})
	if code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", code)
	}
}

func TestHandleAccountChats_MethodNotAllowed(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api/chats", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateChat_RenamesAndResyncsHistory(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	_, created := createTestChat(t, h, cookie, map[string]interface{}{"title": "original"})

	body, _ := json.Marshal(map[string]interface{}{
		"title": "renamed", "agent_id": "deep_research",
		"history": []map[string]string{{"role": "user", "content": "q"}, {"role": "assistant", "content": "a"}},
	})
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/"+created.ID, bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated pinnedChatResp
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if updated.Title != "renamed" || updated.AgentID != "deep_research" || len(updated.History) != 2 {
		t.Errorf("unexpected updated chat: %+v", updated)
	}
}

func TestHandleAccountUpdateChat_NotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	body, _ := json.Marshal(map[string]interface{}{"title": "x"})
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/missing", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateChat_EmptyTitleRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	_, created := createTestChat(t, h, cookie, map[string]interface{}{"title": "original"})
	body, _ := json.Marshal(map[string]interface{}{"title": ""})
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/"+created.ID, bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleAccountUpdateChat_WrongOwnerNotFound proves a session can't
// rename/resync another user's chat -- indistinguishable from the ID not
// existing at all.
func TestHandleAccountUpdateChat_WrongOwnerNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash},
		{ID: "u2", Username: "bob", PasswordHash: testUserPasswordHash},
	}}
	chatStore := &fakeChatStore{}
	hAlice, aliceCookie := chatsAuthedHandler(t, userStore, chatStore, userStore.users[0])
	_, created := createTestChat(t, hAlice, aliceCookie, map[string]interface{}{"title": "alice's chat"})

	hBob, bobCookie := chatsAuthedHandler(t, userStore, chatStore, userStore.users[1])
	body, _ := json.Marshal(map[string]interface{}{"title": "hijacked"})
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/"+created.ID, bytes.NewReader(body))
	req.AddCookie(bobCookie)
	rec := httptest.NewRecorder()
	hBob.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected bob renaming alice's chat to report 404, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateChat_MalformedJSONIsBadRequest(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/x", bytes.NewReader([]byte("not json")))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccountUpdateChat_StoreErrorIsInternalServerError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{updateErr: errors.New("boom")}, userStore.users[0])
	body, _ := json.Marshal(map[string]interface{}{"title": "x"})
	req := httptest.NewRequest(http.MethodPatch, "/account/api/chats/c1", bytes.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleAccountDeleteChat_Success(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	_, created := createTestChat(t, h, cookie, map[string]interface{}{"title": "to delete"})

	req := httptest.NewRequest(http.MethodDelete, "/account/api/chats/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/account/api/chats", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(listRec, listReq)
	var list []pinnedChatResp
	_ = json.Unmarshal(listRec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("expected no chats after delete, got %+v", list)
	}
}

func TestHandleAccountDeleteChat_NotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{}, userStore.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api/chats/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAccountDeleteChat_StoreErrorIsInternalServerError(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := chatsAuthedHandler(t, userStore, &fakeChatStore{deleteErr: errors.New("boom")}, userStore.users[0])
	req := httptest.NewRequest(http.MethodDelete, "/account/api/chats/c1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}
