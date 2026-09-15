package restapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
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
// otherwise. Two layers, both required: a literal check that the first
// character is "/" and the second is neither "/" nor "\" (browsers treat
// a leading "\" the same as "/" when resolving a redirect target), plus a
// url.Parse-based check that the value has no host component at all --
// belt and suspenders, since a plain character check alone can't rule out
// every way a value might carry an authority component.
func safeNext(next string) string {
	if next == "/" {
		return next
	}
	if len(next) > 1 && next[0] == '/' && next[1] != '/' && next[1] != '\\' {
		if target, err := url.Parse(strings.ReplaceAll(next, "\\", "/")); err == nil && target.Hostname() == "" {
			return target.String()
		}
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

// loginAttemptWindow/loginMaxAttempts/loginBaseLockout/loginMaxLockout tune
// loginLimiter -- see its doc comment. loginMaxAttempts failures within
// loginAttemptWindow trigger a lockout starting at loginBaseLockout and
// doubling on every further failure while still locked out, capped at
// loginMaxLockout.
const (
	loginAttemptWindow = 15 * time.Minute
	loginMaxAttempts   = 5
	loginBaseLockout   = 30 * time.Second
	loginMaxLockout    = 15 * time.Minute
)

// loginLimiter is a small in-process, per-key (see clientIP) sliding-window
// rate limiter for POST /login -- nothing else in this codebase throttles
// authentication attempts, and /login sits on a public, unauthenticated
// path (see packaging/nginx/searchengine.conf), so without this an
// internet attacker can script an unthrottled password-guessing loop
// against it. Login credentials are compared in constant time
// (checkCredentials), which prevents a timing side-channel but does
// nothing to slow down raw guess volume -- that's this limiter's job.
//
// This limits by client IP only, not by attempted username -- it doesn't
// defend against a distributed attack spreading guesses for one account
// across many source IPs, only the far more common single-source
// brute-force case the actual exploit scenario describes. State is
// in-memory and per-process: it resets on restart and isn't shared across
// admin-server replicas, which is an accepted gap for this single-admin,
// dev/test-deployed app rather than a distributed rate limiter.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginAttempts
}

type loginAttempts struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]*loginAttempts)}
}

// locked reports whether key is currently locked out and, if so, how much
// longer.
func (l *loginLimiter) locked(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || !now.Before(e.lockedUntil) {
		return 0, false
	}
	return e.lockedUntil.Sub(now), true
}

// recordFailure records a failed attempt for key: starts a fresh sliding
// window if the previous one has expired, then locks key out (with
// exponential backoff for repeated lockouts) once it crosses
// loginMaxAttempts failures within the current window.
func (l *loginLimiter) recordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.windowStart) > loginAttemptWindow {
		e = &loginAttempts{windowStart: now}
		l.entries[key] = e
	}
	e.failures++
	if e.failures > loginMaxAttempts {
		backoff := loginBaseLockout << uint(e.failures-loginMaxAttempts-1)
		if backoff <= 0 || backoff > loginMaxLockout {
			backoff = loginMaxLockout
		}
		e.lockedUntil = now.Add(backoff)
	}
}

// recordSuccess clears key's failure history -- a correct login shouldn't
// leave a stale attempt count around to make the next legitimate login
// look like part of an ongoing attack.
func (l *loginLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// clientIP returns the address /login's rate limiter should key on.
// admin-server is only ever reached through the tracked nginx proxy in
// production (see packaging/nginx/searchengine.conf), which always sets
// X-Real-IP to the real client address -- r.RemoteAddr alone would be
// nginx's own loopback address for every request, making every client
// share one rate-limit bucket. Falls back to r.RemoteAddr when the header
// is absent (direct connections, e.g. in tests).
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

// handleLogin is only ever reached via handleLoginRoute, which already
// guarantees the method is POST -- no method check needed here.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	now := time.Now()
	if wait, locked := h.loginLimiter.locked(ip, now); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many failed login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !h.checkCredentials(req.Username, req.Password) {
		h.loginLimiter.recordFailure(ip, now)
		log.Printf("failed login attempt for user %q from %q", req.Username, ip)
		http.Error(w, "incorrect username or password", http.StatusUnauthorized)
		return
	}
	h.loginLimiter.recordSuccess(ip)

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
