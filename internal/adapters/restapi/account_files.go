package restapi

import (
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
// here are meant for the model to inspect as text (see cmd/mcp-files'
// read_file tool), not a general-purpose blob store, so this stays small
// enough to keep well clear of maxChatMessageContentLength-scale content
// once read back into a chat turn. maxFilesPerUser is a simple per-owner
// cap against unbounded storage growth, the same tier of safety limit as
// maxChatMessages -- neither is admin-configurable, both are meant to just
// be generous enough that a real user never hits them by accident.
const (
	maxUploadedFileBytes = 5 * 1024 * 1024
	maxFilesPerUser      = 100
)

// fileTokenStore is a small in-memory table of short-lived bearer tokens,
// each scoped to exactly one userID -- minted once per chat turn (see
// handleChat) and handed to cmd/mcp-files (via the SE_FILES_API_TOKEN env
// var, see application.ChatOptions.FileAccessToken) so it can call back
// into /account/api/files as that turn's own signed-in user, without ever
// holding that user's real session cookie (a browser-only HttpOnly cookie
// the backend has no legitimate way to read) or this deployment's shared
// database credentials (a much broader grant than "this one user's own
// files" -- see ports.FileStore's own doc comment). Deliberately NOT
// backed by ports.SessionStore/a DB table: unlike a login, this token is
// minted and consumed entirely within one process's lifetime (search-server
// mints it, then spawns and talks to its own mcp-files subprocess), so it
// never needs to be recognized by a different process the way a real
// session does.
type fileTokenStore struct {
	mu     sync.Mutex
	tokens map[string]fileTokenRecord
}

type fileTokenRecord struct {
	userID    string
	expiresAt time.Time
}

// fileTokenTTL is generous over mcpclient's own 60s callTimeout (a turn's
// worth of read_file/write_file calls could span several follow-up rounds,
// each subject to that same ceiling) without staying valid meaningfully
// longer than one real chat turn ever takes.
const fileTokenTTL = 10 * time.Minute

func newFileTokenStore() *fileTokenStore {
	return &fileTokenStore{tokens: make(map[string]fileTokenRecord)}
}

// issue mints a fresh token for userID, valid for fileTokenTTL.
func (s *fileTokenStore) issue(userID string) string {
	token := randomToken()
	s.mu.Lock()
	s.tokens[token] = fileTokenRecord{userID: userID, expiresAt: time.Now().Add(fileTokenTTL)}
	s.mu.Unlock()
	return token
}

// userIDFor resolves token to the userID it was minted for, ok=false if
// the token is unknown or has expired -- lazily evicting an expired entry
// on the way out, same convention sessionStore.ValidSession uses.
func (s *fileTokenStore) userIDFor(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tokens[token]
	if !ok {
		return "", false
	}
	if time.Now().After(rec.expiresAt) {
		delete(s.tokens, token)
		return "", false
	}
	return rec.userID, true
}

// fileAccessUserID resolves the calling user's ID for a /account/api/files
// request, from EITHER a normal role=user session cookie (a person
// browsing their own /account/files page) OR an "Authorization: Bearer
// <token>" header validated against h.fileTokens (cmd/mcp-files calling
// back on that turn's own user's behalf). errStatus is 0 on success, or
// the HTTP status to respond with on failure: 403 if a session exists but
// is role=admin (mirrors requireRegularUserAuthAPI's own admin-forbidden
// behavior -- an admin session has no domain.User row of its own to own a
// file under), 401 if nothing identifies the caller at all.
func (h *Handler) fileAccessUserID(r *http.Request) (userID string, errStatus int) {
	if role, uid, sessionOK := h.sessionRoleFor(r); sessionOK {
		if role == domain.RoleUser && uid != "" {
			return uid, 0
		}
		if role == domain.RoleAdmin {
			return "", http.StatusForbidden
		}
	}
	if h.fileTokens != nil {
		if token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); hasBearer && token != "" {
			if uid, ok := h.fileTokens.userIDFor(token); ok {
				return uid, 0
			}
		}
	}
	return "", http.StatusUnauthorized
}

// requireFileAccess is fileAccessUserID plus writing the matching error
// response on failure -- the "resolve or refuse" 3 lines every
// /account/api/files handler repeats.
func (h *Handler) requireFileAccess(w http.ResponseWriter, r *http.Request) (userID string, ok bool) {
	userID, status := h.fileAccessUserID(r)
	if status == 0 {
		return userID, true
	}
	msg := "authentication required"
	if status == http.StatusForbidden {
		msg = "this feature is not available for the admin account"
	}
	http.Error(w, msg, status)
	return "", false
}

// fileResponse is the wire shape of one domain.UploadedFile's metadata --
// never the file content itself (see handleAccountFile for download).
type fileResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
}

func toFileResponse(f domain.UploadedFile) fileResponse {
	return fileResponse{
		ID: f.ID, Filename: f.Filename, ContentType: f.ContentType,
		Size: f.Size, CreatedAt: f.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// handleAccountFiles lists (GET) or uploads (POST) the calling user's own
// files -- see fileAccessUserID for who may call this.
func (h *Handler) handleAccountFiles(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.files != nil, "files") {
		return
	}
	userID, ok := h.requireFileAccess(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		files, err := h.files.ListFiles(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(files, toFileResponse))
	case http.MethodPost:
		h.handleUploadFile(w, r, userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUploadFile reads a multipart/form-data body's "file" field (the
// plain HTML file-input encoding, and what cmd/mcp-files' write_file tool
// also constructs server-side) and stores it. r.Body is capped at
// maxUploadedFileBytes+1 BEFORE any multipart parsing touches it, so an
// oversized upload is rejected as a plain read error rather than being
// buffered into memory first.
func (h *Handler) handleUploadFile(w http.ResponseWriter, r *http.Request, userID string) {
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
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `missing "file" form field`, http.StatusBadRequest)
		return
	}
	defer file.Close()
	// No separate "is data too big" check needed after this: r.Body is
	// already wrapped in http.MaxBytesReader(maxUploadedFileBytes+1)
	// above, and multipart encoding always adds some non-zero overhead
	// (boundary markers, part headers) on top of the file's own content,
	// so a successful ParseMultipartForm already guarantees len(data) here
	// is strictly less than maxUploadedFileBytes+1.
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "reading upload", http.StatusInternalServerError)
		return
	}
	// header.Filename is never empty here: net/http's own multipart form
	// parsing (mime/multipart/formdata.go) routes a part with no/empty
	// filename into r.MultipartForm.Value instead of .File, so a part
	// that reaches here via a successful r.FormFile("file") above always
	// carried a non-empty one (confirmed against net/http's own parser,
	// not just reasoned about).
	filename := header.Filename
	contentType := header.Header.Get("Content-Type")
	f, err := h.files.SaveFile(r.Context(), userID, filename, contentType, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toFileResponse(f))
}

// handleAccountFile downloads (GET) or deletes (DELETE) one of the calling
// user's own files by ID.
func (h *Handler) handleAccountFile(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.files != nil, "files") {
		return
	}
	userID, ok := h.requireFileAccess(w, r)
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
