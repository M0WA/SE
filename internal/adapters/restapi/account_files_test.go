package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fileResponse mirrors account_files.go's unexported wire type -- a local
// copy, same as mcpServerResp, since an external _test package can't
// reference an unexported type directly.
type fileResponse struct {
	ID          string `json:"id"`
	ChatID      string `json:"chat_id,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
}

// fakeFileStore is a minimal ports.FileStore fake, keyed by ownerUserID so
// two owners' files never leak into each other's calls -- same isolation
// the real sqlrepo's user_id-scoped SQL gives.
type fakeFileStore struct {
	byOwner   map[string][]fakeFile
	nextID    int
	listErr   error
	saveErr   error
	getErr    error
	deleteErr error
}

type fakeFile struct {
	meta domain.UploadedFile
	data []byte
}

func (f *fakeFileStore) ListFiles(ctx context.Context, ownerUserID string) ([]domain.UploadedFile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.UploadedFile, 0, len(f.byOwner[ownerUserID]))
	for _, ff := range f.byOwner[ownerUserID] {
		out = append(out, ff.meta)
	}
	return out, nil
}

func (f *fakeFileStore) ListFilesForChat(ctx context.Context, ownerUserID, chatID string) ([]domain.UploadedFile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []domain.UploadedFile
	for _, ff := range f.byOwner[ownerUserID] {
		if ff.meta.ChatID == chatID {
			out = append(out, ff.meta)
		}
	}
	return out, nil
}

func (f *fakeFileStore) SaveFile(ctx context.Context, ownerUserID, chatID, filename, contentType string, data []byte) (domain.UploadedFile, error) {
	if f.saveErr != nil {
		return domain.UploadedFile{}, f.saveErr
	}
	if f.byOwner == nil {
		f.byOwner = map[string][]fakeFile{}
	}
	f.nextID++
	meta := domain.UploadedFile{
		ID: "file" + string(rune('0'+f.nextID)), OwnerUserID: ownerUserID, ChatID: chatID,
		Filename: filename, ContentType: contentType, Size: int64(len(data)),
	}
	f.byOwner[ownerUserID] = append(f.byOwner[ownerUserID], fakeFile{meta: meta, data: data})
	return meta, nil
}

func (f *fakeFileStore) GetFile(ctx context.Context, ownerUserID, id string) (domain.UploadedFile, []byte, error) {
	if f.getErr != nil {
		return domain.UploadedFile{}, nil, f.getErr
	}
	for _, ff := range f.byOwner[ownerUserID] {
		if ff.meta.ID == id {
			return ff.meta, ff.data, nil
		}
	}
	return domain.UploadedFile{}, nil, ports.ErrFileNotFound
}

func (f *fakeFileStore) DeleteFile(ctx context.Context, ownerUserID, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	files := f.byOwner[ownerUserID]
	for i, ff := range files {
		if ff.meta.ID == id {
			f.byOwner[ownerUserID] = append(files[:i], files[i+1:]...)
			return nil
		}
	}
	return ports.ErrFileNotFound
}

// testChatID is the pinned-chat id filesAuthedHandler seeds and
// uploadTestFile attaches uploads to, since only a pinned chat may have
// files. A test about chat_id validation uses uploadTestFileWithChatID instead.
const testChatID = "chat-1"

// fakeChatStore is a minimal ports.ChatStore fake, mirroring
// fakeFileStore's own ownerUserID-keyed shape.
type fakeChatStore struct {
	byOwner   map[string][]domain.PersistedChat
	listErr   error
	createErr error
	updateErr error
	deleteErr error
}

func (c *fakeChatStore) ListChats(ctx context.Context, ownerUserID string) ([]domain.PersistedChat, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.byOwner[ownerUserID], nil
}

func (c *fakeChatStore) CreateChat(ctx context.Context, chat domain.PersistedChat) (domain.PersistedChat, error) {
	if c.createErr != nil {
		return domain.PersistedChat{}, c.createErr
	}
	if c.byOwner == nil {
		c.byOwner = map[string][]domain.PersistedChat{}
	}
	if chat.ID == "" {
		chat.ID = testChatID
	}
	c.byOwner[chat.OwnerUserID] = append(c.byOwner[chat.OwnerUserID], chat)
	return chat, nil
}

func (c *fakeChatStore) UpdateChat(ctx context.Context, chat domain.PersistedChat) error {
	if c.updateErr != nil {
		return c.updateErr
	}
	chats := c.byOwner[chat.OwnerUserID]
	for i, existing := range chats {
		if existing.ID == chat.ID {
			chats[i] = chat
			return nil
		}
	}
	return ports.ErrChatNotFound
}

func (c *fakeChatStore) DeleteChat(ctx context.Context, ownerUserID, id string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	chats := c.byOwner[ownerUserID]
	for i, existing := range chats {
		if existing.ID == id {
			c.byOwner[ownerUserID] = append(chats[:i], chats[i+1:]...)
			return nil
		}
	}
	return ports.ErrChatNotFound
}

// filesAuthedHandler logs in as u (role=user, via a real POST /login) with
// Users and Files wired, mirroring accountMCPServersAuthedHandler -- and
// seeds one pinned chat (testChatID) owned by u, since uploads require one.
func filesAuthedHandler(t *testing.T, userStore *fakeUserStore, fileStore *fakeFileStore, u domain.User) (*restapi.Handler, *http.Cookie) {
	t.Helper()
	cfg := restapi.Config{Users: userStore}
	// fileStore is assigned only when non-nil: a nil *fakeFileStore in the
	// ports.FileStore-typed field would be a non-nil interface wrapping a
	// nil pointer (the typed-nil gotcha), passing Handler's nil check and
	// then panicking -- omitting it keeps h.files genuinely nil.
	if fileStore != nil {
		cfg.Files = fileStore
		cfg.Chats = &fakeChatStore{byOwner: map[string][]domain.PersistedChat{
			u.ID: {{ID: testChatID, OwnerUserID: u.ID, Title: "test chat"}},
		}}
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

func uploadTestFile(t *testing.T, h *restapi.Handler, auth func(*http.Request), filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return uploadTestFileWithChatID(t, h, auth, filename, content, testChatID)
}

func uploadTestFileWithChatID(t *testing.T, h *restapi.Handler, auth func(*http.Request), filename string, content []byte, chatID string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if chatID != "" {
		if err := mw.WriteField("chat_id", chatID); err != nil {
			t.Fatalf("building multipart request: %v", err)
		}
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("building multipart request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/account/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	auth(req)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

func TestHandleAccountFiles_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Users: &fakeUserStore{}, Files: &fakeFileStore{}})
	req := httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestHandleAccountFiles_AdminRoleAllowed proves an admin session (a real
// User row with IsAdmin=true) can use self-service files exactly like any
// other account, since being admin only adds /admin/* access on top --
// same as /account/api/mcp-servers.
func TestHandleAccountFiles_AdminRoleAllowed(t *testing.T) {
	h := restapi.New(restapi.Config{
		Users: testAdminUsersStore(), Files: &fakeFileStore{},
	})
	body, _ := json.Marshal(map[string]string{"username": testAdminUser, "password": testAdminPass})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login failed: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]

	req = httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountFiles_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, nil, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountFiles_UploadThenList(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hello"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if created.Filename != "notes.txt" || created.Size != 5 || created.ChatID != testChatID {
		t.Errorf("unexpected created file: %+v", created)
	}

	req := httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var list []fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("expected the uploaded file in the list, got %+v", list)
	}
}

// TestHandleAccountFiles_UploadWithoutChatIDRejected proves an upload with
// no chat_id at all is rejected -- only a pinned chat may have files.
func TestHandleAccountFiles_UploadWithoutChatIDRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFileWithChatID(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hello"), "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with no chat_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAccountFiles_UploadWithForeignChatIDRejected proves a chat_id
// not owned by the caller is rejected, not silently accepted.
func TestHandleAccountFiles_UploadWithForeignChatIDRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFileWithChatID(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hello"), "someone-elses-chat")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 with a foreign chat_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHandleAccountFiles_ListScopedToChat proves a "chat_id" query param
// narrows the list to just that chat's files.
func TestHandleAccountFiles_ListScopedToChat(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])
	uploaded := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "in-chat.txt", []byte("hi"))
	var created fileResponse
	_ = json.Unmarshal(uploaded.Body.Bytes(), &created)

	// A second chat, with no files of its own.
	otherChatID := "chat-2"

	req := httptest.NewRequest(http.MethodGet, "/account/api/files?chat_id="+created.ChatID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var list []fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("expected the chat-scoped file, got %+v", list)
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api/files?chat_id="+otherChatID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	list = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no files for an unrelated chat_id, got %+v", list)
	}
}

func TestHandleAccountFiles_UploadMissingFieldRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("not-file", "x")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/account/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleAccountFiles_UploadTooLargeRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	oversized := bytes.Repeat([]byte("x"), 6*1024*1024)
	rec := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "big.bin", oversized)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an oversized upload, got %d", rec.Code)
	}
}

func TestHandleAccountFiles_UploadAtPerUserCapRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	// Pre-fill to the per-user cap so the next upload is refused --
	// mirrors maxFilesPerUser (account_files.go).
	for i := 0; i < 100; i++ {
		fileStore.byOwner = map[string][]fakeFile{"u1": append(fileStore.byOwner["u1"], fakeFile{meta: domain.UploadedFile{ID: "x", OwnerUserID: "u1"}})}
	}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "one-too-many.txt", []byte("x"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 at the per-user file cap, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountFile_Download(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])
	uploaded := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hello world"))
	var created fileResponse
	_ = json.Unmarshal(uploaded.Body.Bytes(), &created)

	req := httptest.NewRequest(http.MethodGet, "/account/api/files/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("expected downloaded content %q, got %q", "hello world", rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Error("expected a Content-Disposition header on download")
	}
}

// TestHandleAccountFile_DownloadEmptyContentTypeFallsBack covers
// handleAccountFile's own "contentType == ”" branch -- upload never produces
// one in practice, so this seeds the fake store directly, as a
// pre-ContentType-tracking row would look.
func TestHandleAccountFile_DownloadEmptyContentTypeFallsBack(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{byOwner: map[string][]fakeFile{
		"u1": {{meta: domain.UploadedFile{ID: "f1", OwnerUserID: "u1", Filename: "x.bin"}, data: []byte("x")}},
	}}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account/api/files/f1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("expected the empty content type to fall back to application/octet-stream, got %q", got)
	}
}

func TestHandleAccountFile_Unauthenticated(t *testing.T) {
	h := restapi.New(restapi.Config{Users: &fakeUserStore{}, Files: &fakeFileStore{}})
	req := httptest.NewRequest(http.MethodGet, "/account/api/files/x", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleAccountFile_DownloadNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account/api/files/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestHandleAccountFile_WrongOwnerNotFound proves one user can't download
// another's file even by guessing its ID -- looks like a nonexistent ID.
func TestHandleAccountFile_WrongOwnerNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{
		{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash},
		{ID: "u2", Username: "bob", PasswordHash: testUserPasswordHash},
	}}
	fileStore := &fakeFileStore{}
	hAlice, aliceCookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])
	uploaded := uploadTestFile(t, hAlice, func(r *http.Request) { r.AddCookie(aliceCookie) }, "secret.txt", []byte("shh"))
	var created fileResponse
	_ = json.Unmarshal(uploaded.Body.Bytes(), &created)

	hBob, bobCookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[1])
	req := httptest.NewRequest(http.MethodGet, "/account/api/files/"+created.ID, nil)
	req.AddCookie(bobCookie)
	rec := httptest.NewRecorder()
	hBob.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected bob downloading alice's file to 404, got %d", rec.Code)
	}
}

func TestHandleAccountFile_Delete(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])
	uploaded := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hello"))
	var created fileResponse
	_ = json.Unmarshal(uploaded.Body.Bytes(), &created)

	req := httptest.NewRequest(http.MethodDelete, "/account/api/files/"+created.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	var list []fileResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("expected no files after delete, got %+v", list)
	}
}

func TestHandleAccountFile_DeleteNotFound(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodDelete, "/account/api/files/missing", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAccountFiles_MethodNotAllowed(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodPut, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleAccountFile_MethodNotAllowed(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodPut, "/account/api/files/x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// TestHandleAccountFiles_BearerTokenAuth proves the per-turn-token chain
// end to end: a real POST /chat mints a token, passed via SE_FILES_API_TOKEN
// (captured by a fake provider) -- and that same token, as "Authorization:
// Bearer <token>" with no session cookie, authenticates as alice.
func TestHandleAccountFiles_BearerTokenAuth(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{}
	mcpProvider := &fakeMCPToolProvider{}
	h := restapi.New(restapi.Config{
		Users: userStore, Files: fileStore,
		Chats: &fakeChatStore{byOwner: map[string][]domain.PersistedChat{
			"u1": {{ID: testChatID, OwnerUserID: "u1", Title: "test chat"}},
		}},
		Chat: application.NewChatService(
			&fakeChatEndpointStore{endpoint: domain.ChatEndpoint{Enabled: true}},
			&fakeChatCompleter{answer: "hi"},
			&fakeMCPServerStore{servers: []domain.MCPServer{{ID: "files", Name: "files", Transport: "stdio", Command: "mcp-files", Enabled: true}}},
			mcpProvider, nil, nil, nil, "",
		),
	})
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": testUserPassword})
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.RoutesAdmin().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]

	// Upload as the real signed-in session first, so there's something
	// for the token-authenticated request to see.
	uploaded := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hi"))
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("setup upload failed: %d %s", uploaded.Code, uploaded.Body.String())
	}

	chatBody, _ := json.Marshal(map[string]interface{}{"messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req = httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(chatBody))
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat call failed: %d %s", rec.Code, rec.Body.String())
	}
	token := mcpProvider.openedEnv["SE_FILES_API_TOKEN"]
	if token == "" {
		t.Fatal("expected handleChat to mint a non-empty SE_FILES_API_TOKEN")
	}

	req = httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 authenticating with the minted token, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []fileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(list) != 1 || list[0].Filename != "notes.txt" {
		t.Errorf("expected the token-authenticated request to see alice's own file, got %+v", list)
	}

	// A request with neither a session cookie nor a bearer token is
	// refused -- proves the token really is required, not just accepted.
	req = httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no credential at all, got %d", rec.Code)
	}

	// An invalid bearer token is refused the same way.
	req = httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec = httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for an invalid bearer token, got %d", rec.Code)
	}
}

func TestFakeFileStore_ErrorsPropagate(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{listErr: errors.New("boom")}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account/api/files", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when ListFiles errors, got %d", rec.Code)
	}
}

func TestHandleAccountFilesPage_ServesHTML(t *testing.T) {
	h := restapi.New(restapi.Config{})
	req := httptest.NewRequest(http.MethodGet, "/account/files", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	// Unauthenticated -> redirected to /login (requireRegularUserAuthPage),
	// proving the page route is actually gated.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected a redirect to /login, got %d", rec.Code)
	}
}

func TestHandleAccountFilesPage_ServesHTMLWhenAuthenticated(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account/files", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestHandleAccountFiles_UploadListErrorPropagates covers
// handleUploadFile's own ListFiles error branch (the per-user-cap check),
// distinct from the GET-list branch TestFakeFileStore_ErrorsPropagate covers.
func TestHandleAccountFiles_UploadListErrorPropagates(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{listErr: errors.New("boom")}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hi"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when the pre-upload ListFiles errors, got %d", rec.Code)
	}
}

// TestHandleAccountFiles_UploadMalformedMultipartRejected covers the
// decode-error branch, distinct from the oversized-body case.
func TestHandleAccountFiles_UploadMalformedMultipartRejected(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, &fakeFileStore{}, userStore.users[0])

	req := httptest.NewRequest(http.MethodPost, "/account/api/files", bytes.NewReader([]byte("not multipart")))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a malformed multipart body, got %d", rec.Code)
	}
}

// TestHandleAccountFiles_SaveFileErrorPropagates covers handleUploadFile's
// own SaveFile error branch.
func TestHandleAccountFiles_SaveFileErrorPropagates(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{saveErr: errors.New("boom")}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	rec := uploadTestFile(t, h, func(r *http.Request) { r.AddCookie(cookie) }, "notes.txt", []byte("hi"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when SaveFile errors, got %d", rec.Code)
	}
}

// TestHandleAccountFile_DownloadErrorPropagates covers handleAccountFile's
// own GetFile non-ErrFileNotFound error branch.
func TestHandleAccountFile_DownloadErrorPropagates(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{getErr: errors.New("boom")}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	req := httptest.NewRequest(http.MethodGet, "/account/api/files/x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when GetFile errors, got %d", rec.Code)
	}
}

// TestHandleAccountFile_DeleteErrorPropagates covers handleAccountFile's
// own DeleteFile non-ErrFileNotFound error branch.
func TestHandleAccountFile_DeleteErrorPropagates(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	fileStore := &fakeFileStore{deleteErr: errors.New("boom")}
	h, cookie := filesAuthedHandler(t, userStore, fileStore, userStore.users[0])

	req := httptest.NewRequest(http.MethodDelete, "/account/api/files/x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when DeleteFile errors, got %d", rec.Code)
	}
}

func TestHandleAccountFile_NotConfigured(t *testing.T) {
	userStore := &fakeUserStore{users: []domain.User{{ID: "u1", Username: "alice", PasswordHash: testUserPasswordHash}}}
	h, cookie := filesAuthedHandler(t, userStore, nil, userStore.users[0])
	req := httptest.NewRequest(http.MethodGet, "/account/api/files/x", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAccountFilesJS_Served(t *testing.T) {
	h := restapi.New(restapi.Config{})
	req := httptest.NewRequest(http.MethodGet, "/account_files.js", nil)
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}
