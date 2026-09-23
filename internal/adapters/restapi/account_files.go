package restapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// maxUploadedFileBytes bounds a single POST /account/api/files body -- files
// are for the model to read as text (see cmd/mcp-files' read_file), not a
// blob store, so this stays well under chat-turn content sizes.
// maxFilesPerUser is a simple per-owner cap against unbounded growth, same
// tier as maxChatMessages -- neither is admin-configurable.
const (
	maxUploadedFileBytes = 5 * 1024 * 1024
	maxFilesPerUser      = 100
)

// fileTokenStore is a small in-memory table of short-lived bearer tokens,
// each scoped to one userID -- minted per chat turn and handed to
// cmd/mcp-files (SE_FILES_API_TOKEN env var) so it can call back into
// /account/api/files as that user, without ever holding their real session
// cookie or this deployment's shared DB credentials. Deliberately not
// backed by ports.SessionStore/a DB table: it's minted and consumed
// entirely within one process's lifetime, unlike a real session.
type fileTokenStore struct {
	mu     sync.Mutex
	tokens map[string]fileTokenRecord
}

type fileTokenRecord struct {
	userID    string
	chatID    string
	expiresAt time.Time
}

// fileTokenTTL comfortably outlasts mcpclient's 60s callTimeout across a
// turn's follow-up rounds, without outliving one real chat turn.
const fileTokenTTL = 10 * time.Minute

func newFileTokenStore() *fileTokenStore {
	return &fileTokenStore{tokens: make(map[string]fileTokenRecord)}
}

// issue mints a fresh token for userID scoped to chatID ("" if this turn
// has no pinned chat), valid for fileTokenTTL.
func (s *fileTokenStore) issue(userID, chatID string) string {
	token := randomToken()
	s.mu.Lock()
	s.tokens[token] = fileTokenRecord{userID: userID, chatID: chatID, expiresAt: time.Now().Add(fileTokenTTL)}
	s.mu.Unlock()
	return token
}

// resolve returns the (userID, chatID) token was minted for, ok=false if
// unknown/expired -- evicts an expired entry on the way out, like sessionStore.ValidSession.
func (s *fileTokenStore) resolve(token string) (userID, chatID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tokens[token]
	if !ok {
		return "", "", false
	}
	if time.Now().After(rec.expiresAt) {
		delete(s.tokens, token)
		return "", "", false
	}
	return rec.userID, rec.chatID, true
}

// fileAccessUserID resolves the caller's user ID for a /account/api/files
// request, from either a session cookie (any role -- every session belongs
// to a real domain.User row) or an "Authorization: Bearer <token>" header
// (cmd/mcp-files calling back for that turn). tokenChatID is set only for
// the bearer path -- the pinned chat that token was minted for; a cookie
// caller resolves chat scope from the request itself instead. errStatus is
// 0 on success, 401 if neither resolved.
func (h *Handler) fileAccessUserID(r *http.Request) (userID, tokenChatID string, errStatus int) {
	if _, uid, sessionOK := h.sessionRoleFor(r); sessionOK && uid != "" {
		return uid, "", 0
	}
	if h.fileTokens != nil {
		if token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); hasBearer && token != "" {
			if uid, chatID, ok := h.fileTokens.resolve(token); ok {
				return uid, chatID, 0
			}
		}
	}
	return "", "", http.StatusUnauthorized
}

// requireFileAccess is fileAccessUserID plus writing the matching error
// response -- the "resolve or refuse" boilerplate every handler repeats.
func (h *Handler) requireFileAccess(w http.ResponseWriter, r *http.Request) (userID, tokenChatID string, ok bool) {
	userID, tokenChatID, status := h.fileAccessUserID(r)
	if status == 0 {
		return userID, tokenChatID, true
	}
	http.Error(w, authRequiredMsg, status)
	return "", "", false
}

// fileResponse is the wire shape of one domain.UploadedFile's metadata --
// never the file content itself (see handleAccountFile for download).
type fileResponse struct {
	ID          string `json:"id"`
	ChatID      string `json:"chat_id,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
}

func toFileResponse(f domain.UploadedFile) fileResponse {
	return fileResponse{
		ID: f.ID, ChatID: f.ChatID, Filename: f.Filename, ContentType: f.ContentType,
		Size: f.Size, CreatedAt: f.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// handleAccountFiles lists (GET) or uploads (POST) the calling user's own
// files -- see fileAccessUserID for who may call. GET scopes to one chat
// when chat_id is given (token's own, or the query param), else all files.
func (h *Handler) handleAccountFiles(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.files != nil, "files") {
		return
	}
	userID, tokenChatID, ok := h.requireFileAccess(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		chatID := tokenChatID
		if chatID == "" {
			chatID = r.URL.Query().Get("chat_id")
		}
		var files []domain.UploadedFile
		var err error
		if chatID != "" {
			files, err = h.files.ListFilesForChat(r.Context(), userID, chatID)
		} else {
			files, err = h.files.ListFiles(r.Context(), userID)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(files, toFileResponse))
	case http.MethodPost:
		h.handleUploadFile(w, r, userID, tokenChatID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUploadFile reads a multipart "file" field and stores it attached to
// a chat: a bearer-token caller (cmd/mcp-files) gets tokenChatID; a
// session-cookie caller supplies "chat_id", verified as one of this user's
// own pinned chats -- a missing/foreign chat_id is rejected, never
// silently uploaded unattached. r.Body is capped at maxUploadedFileBytes+1
// before multipart parsing, so an oversized upload fails as a read error.
func (h *Handler) handleUploadFile(w http.ResponseWriter, r *http.Request, userID, tokenChatID string) {
	existing, err := h.files.ListFiles(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(existing) >= maxFilesPerUser {
		http.Error(w, fmt.Sprintf("you already have %d files, the maximum allowed", maxFilesPerUser), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadedFileBytes+1)
	if err := r.ParseMultipartForm(maxUploadedFileBytes + 1); err != nil {
		http.Error(w, "invalid or oversized upload (max "+fmt.Sprintf("%d", maxUploadedFileBytes)+" bytes)", http.StatusBadRequest)
		return
	}
	chatID := tokenChatID
	if chatID == "" {
		chatID = r.FormValue("chat_id")
	}
	if chatID == "" {
		http.Error(w, "chat_id is required -- only a pinned chat may have files attached", http.StatusBadRequest)
		return
	}
	if tokenChatID == "" {
		if !h.userOwnsChat(r.Context(), userID, chatID) {
			http.Error(w, "chat not found", http.StatusBadRequest)
			return
		}
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `missing "file" form field`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	// No separate size check needed: r.Body is already wrapped in
	// http.MaxBytesReader(maxUploadedFileBytes+1) above, and multipart
	// overhead guarantees len(data) here is under that bound.
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "reading upload", http.StatusInternalServerError)
		return
	}
	// header.Filename is never empty here: net/http's multipart parser
	// routes a part with no filename into r.MultipartForm.Value instead of
	// .File, so a successful r.FormFile("file") above guarantees one.
	filename := header.Filename
	contentType := header.Header.Get("Content-Type")
	f, err := h.files.SaveFile(r.Context(), userID, chatID, filename, contentType, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toFileResponse(f))
}

// userOwnsChat reports whether chatID is one of userID's own pinned chats
// -- the check a session-cookie upload needs before trusting a
// client-supplied chat_id (a bearer-token upload's chat_id is already
// scoped by the token). false, including h.chats unconfigured, never a crash.
func (h *Handler) userOwnsChat(ctx context.Context, userID, chatID string) bool {
	if h.chats == nil {
		return false
	}
	chats, err := h.chats.ListChats(ctx, userID)
	if err != nil {
		return false
	}
	for _, c := range chats {
		if c.ID == chatID {
			return true
		}
	}
	return false
}

// handleAccountFile downloads (GET) or deletes (DELETE) one of the calling
// user's own files by ID.
func (h *Handler) handleAccountFile(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.files != nil, "files") {
		return
	}
	userID, _, ok := h.requireFileAccess(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodGet:
		f, data, err := h.files.GetFile(r.Context(), userID, id)
		if errors.Is(err, ports.ErrFileNotFound) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		contentType := f.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(f.Filename, `"`, "'")+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodDelete:
		err := h.files.DeleteFile(r.Context(), userID, id)
		respondOrNotFound(w, err, ports.ErrFileNotFound, "file not found", map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleAccountFilesPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", accountFilesHTML)
}

func (h *Handler) handleAccountFilesJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "application/javascript; charset=utf-8", accountFilesJS)
}
