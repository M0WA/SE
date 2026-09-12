package restapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "se_session"

// sessionStore is a small in-memory session table, used only as the
// fallback when no ports.SessionStore is configured (e.g. in tests that
// don't care about cross-process session sharing). Sessions are lost on
// restart, and are only ever visible to the one process that created
// them -- fine for that fallback case, but not for production, where
// search-server and admin-server are separate processes that must
// recognize the same login (see ports.SessionStore's doc comment).
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) CreateSession(_ context.Context, token string, expiresAt time.Time) error {
	s.mu.Lock()
	s.sessions[token] = expiresAt
	s.mu.Unlock()
	return nil
}

func (s *sessionStore) ValidSession(_ context.Context, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false, nil
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false, nil
	}
	return true, nil
}

func (s *sessionStore) RevokeSession(_ context.Context, token string) error {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	return nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("restapi: failed to read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// authConfigured reports whether an admin account has been set up at all.
// Until it is, /login always refuses and /crawl and /admin stay locked --
// authentication fails closed rather than defaulting to open access.
func (h *Handler) authConfigured() bool {
	return h.adminUser != "" && h.adminPass != ""
}

func (h *Handler) checkCredentials(user, pass string) bool {
	if !h.authConfigured() {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(h.adminUser)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(h.adminPass)) == 1
	return userOK && passOK
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	valid, err := h.sessions.ValidSession(r.Context(), c.Value)
	return err == nil && valid
}

// requireAuthPage gates an HTML page: unauthenticated visitors are sent to
// the login page, carrying the original path so they land back on it.
func (h *Handler) requireAuthPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthenticated(r) {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireAuthAPI gates a JSON endpoint: unauthenticated callers get a plain
// 401, since there's no page to redirect an API client to.
func (h *Handler) requireAuthAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// safeNext keeps post-login redirects on this site: an absolute or
// protocol-relative "next" value is rejected in favor of the default, since
// a query-string-controlled redirect target is an open-redirect vector
// otherwise.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/admin"
}

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.isAuthenticated(r) {
		next := safeNext(r.URL.Query().Get("next"))
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(loginHTML)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Next     string `json:"next"`
}

// handleLogin is only ever reached via handleLoginRoute, which already
// guarantees the method is POST -- no method check needed here.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !h.checkCredentials(req.Username, req.Password) {
		http.Error(w, "incorrect username or password", http.StatusUnauthorized)
		return
	}

	sessionTTL := h.opSettings.Get().SessionTTL
	token := randomToken()
	if err := h.sessions.CreateSession(r.Context(), token, time.Now().Add(sessionTTL)); err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(req.Next)})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = h.sessions.RevokeSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
