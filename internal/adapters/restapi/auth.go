package restapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const sessionCookieName = "se_session"

// sessionStore is a small in-memory session table, used only as the
// fallback when no ports.SessionStore is configured. Lost on restart and
// visible only to the process that created it -- fine for tests, not for
// production where search-server and admin-server must share a login.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]sessionRecord
}

// sessionRecord mirrors what the SQL-backed sessions table stores per
// token -- see ports.SessionStore's doc comment for why role/userID live
// here rather than in the cookie itself.
type sessionRecord struct {
	expiresAt time.Time
	role      string
	userID    string
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]sessionRecord)}
}

func (s *sessionStore) CreateSession(_ context.Context, token string, expiresAt time.Time, role string, userID string) error {
	s.mu.Lock()
	s.sessions[token] = sessionRecord{expiresAt: expiresAt, role: role, userID: userID}
	s.mu.Unlock()
	return nil
}

func (s *sessionStore) ValidSession(_ context.Context, token string) (bool, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[token]
	if !ok {
		return false, "", "", nil
	}
	if time.Now().After(rec.expiresAt) {
		delete(s.sessions, token)
		return false, "", "", nil
	}
	return true, rec.role, rec.userID, nil
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

// sessionRoleFor resolves the caller's role from the se_session cookie by
// looking up the SERVER-SIDE session record -- the cookie itself is always
// just an opaque random token, so this is the only place a role is ever
// determined; nothing the client sends can influence it. ok is false for
// no cookie, an unknown token, or an expired one; role is domain.RoleAdmin
// or domain.RoleUser when ok is true.
func (h *Handler) sessionRoleFor(r *http.Request) (role string, ok bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	valid, role, _, err := h.sessions.ValidSession(r.Context(), c.Value)
	if err != nil || !valid {
		return "", false
	}
	return role, true
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	_, ok := h.sessionRoleFor(r)
	return ok
}

// requireAuthPage gates an HTML page: unauthenticated visitors are sent to
// the login page, carrying the original path so they land back on it.
// Either role passes -- used by RoutesSearch (and RoutesAdmin's own
// unauthenticated-vs-authenticated pages that aren't admin-only, if any).
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
// 401, since there's no page to redirect an API client to. Either role
// passes.
func (h *Handler) requireAuthAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// requireAdminAuthPage is requireAuthPage's admin-only counterpart, gating
// every RoutesAdmin page. Unauthenticated -> redirect to /login exactly
// like requireAuthPage (nothing to distinguish yet). Authenticated but
// role != domain.RoleAdmin (a regular user) -> a plain 403, NOT a redirect
// to /login -- a logged-in regular user can't "log in harder", so bouncing
// them back to the login page would just be a dead end dressed up as a
// login prompt.
func (h *Handler) requireAdminAuthPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role, ok := h.sessionRoleFor(r)
		if !ok {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
			return
		}
		if role != domain.RoleAdmin {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// requireAdminAuthAPI is requireAuthAPI's admin-only counterpart, gating
// every /admin/api/* endpoint. Unauthenticated -> 401. Authenticated but
// not domain.RoleAdmin -> 403.
func (h *Handler) requireAdminAuthAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role, ok := h.sessionRoleFor(r)
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if role != domain.RoleAdmin {
			http.Error(w, "admin access required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// internalAPIKeyHeader is the header a trusted local caller (e.g. a
// SearXNG engine plugin) presents to requireAuthAPIOrInternalKey in place
// of a session cookie.
const internalAPIKeyHeader = "X-Internal-API-Key"

// requireAuthAPIOrInternalKey gates a JSON endpoint the same way
// requireAuthAPI does, plus one additional bypass: when
// h.internalSearchAPIKey is configured (non-empty) and the request's
// X-Internal-API-Key header matches it exactly, the request is let through
// with no session check at all. This exists so a trusted same-host caller
// -- a SearXNG engine plugin folding this instance's own index into
// SearXNG's aggregated search, rather than /search staying a
// browser-session-only endpoint -- can call /search without ever having a
// browser session. It's opt-in and secure-by-default: h.internalSearchAPIKey
// is empty unless an admin explicitly sets SEARCH_INTERNAL_API_KEY, in
// which case this behaves byte-for-byte like requireAuthAPI (session
// cookie required, 401 otherwise). The comparison uses
// subtle.ConstantTimeCompare rather than ==, so a caller without the key
// can't learn it one byte at a time via response-timing differences.
func (h *Handler) requireAuthAPIOrInternalKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.internalSearchAPIKey != "" && requestHasSecretHeader(r, internalAPIKeyHeader, h.internalSearchAPIKey) {
			next(w, r)
			return
		}
		if !h.isAuthenticated(r) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// requestHasSecretHeader reports whether r carries header set to exactly
// secret, compared via subtle.ConstantTimeCompare (not ==) so a caller
// without the right value can't learn it one byte at a time via
// response-timing differences -- shared by every shared-secret-header check
// in this package (requireAuthAPIOrInternalKey above, requireCrawlInternalToken
// in crawl_internal.go), so that comparison detail only needs to be gotten
// right once.
func requestHasSecretHeader(r *http.Request, header, secret string) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get(header)), []byte(secret)) == 1
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// safeNext keeps post-login redirects on this site, rejecting an
// absolute/protocol-relative "next" (an open-redirect vector otherwise).
// Two checks, both required: the first two characters rule out "//" and
// "\\" (browsers treat a leading "\" like "/"), and url.Parse confirms no
// host component at all.
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
	if !requireGetOrHead(w, r) {
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
// rate limiter for public, unauthenticated POST /login -- without it an
// attacker can script unthrottled password guessing. In-memory/per-process
// and IP-only, an accepted gap for this single-admin, dev/test app.
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

// clientIP returns the address /login's rate limiter keys on. Production
// nginx always sets X-Real-IP; r.RemoteAddr alone would be nginx's own
// loopback address, sharing one bucket across every client. Falls back
// to r.RemoteAddr when absent (direct connections, e.g. tests).
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

// dummyPasswordHash is compared against (via bcrypt.CompareHashAndPassword)
// whenever a login's username doesn't match any domain.User row, so that
// path takes roughly the same time as a real user with a wrong password --
// without it, a login attempt for a nonexistent username would return
// faster than one for a real username, letting an attacker enumerate valid
// usernames by response timing alone. The actual password compared against
// it is never checked for a match (there's no way it legitimately could
// be); only the constant-time work matters here.
var dummyPasswordHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing-safety"), bcrypt.DefaultCost)

// authenticatedRole checks user/pass against the hardcoded admin account
// first, then (if that fails and h.users is configured) against
// domain.User rows -- returns the resulting session role and, for a
// domain.RoleUser match, that user's ID (empty otherwise), or ok=false if
// neither matched. Every failure path -- wrong admin password, unknown
// username, wrong user password -- does the same amount of constant-time/
// bcrypt work and returns the identical ok=false, so none of the three is
// distinguishable from the others by response timing or shape.
func (h *Handler) authenticatedRole(ctx context.Context, user, pass string) (role string, userID string, ok bool) {
	if h.checkCredentials(user, pass) {
		return domain.RoleAdmin, "", true
	}
	if h.users == nil {
		return "", "", false
	}
	u, err := h.users.GetUserByUsername(ctx, user)
	if err != nil {
		if !errors.Is(err, ports.ErrUserNotFound) {
			log.Printf("auth: looking up user %q: %v", user, err)
		}
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(pass))
		return "", "", false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass)) != nil {
		return "", "", false
	}
	return domain.RoleUser, u.ID, true
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

	req, ok := decodeJSON[loginRequest](w, r)
	if !ok {
		return
	}
	role, userID, matched := h.authenticatedRole(r.Context(), req.Username, req.Password)
	if !matched {
		h.loginLimiter.recordFailure(ip, now)
		log.Printf("failed login attempt for user %q from %q", req.Username, ip)
		http.Error(w, "incorrect username or password", http.StatusUnauthorized)
		return
	}
	h.loginLimiter.recordSuccess(ip)

	sessionTTL := h.opSettings.Get().SessionTTL
	token := randomToken()
	if err := h.sessions.CreateSession(r.Context(), token, time.Now().Add(sessionTTL), role, userID); err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	h.setSessionCookie(w, r, token, int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(req.Next)})
}

// setSessionCookie sets (or, with value="" and maxAge=-1, clears) the
// session cookie -- shared by handleLogin and handleLogout, which
// otherwise each built the identical http.Cookie literal differing only
// in Value and MaxAge.
func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = h.sessions.RevokeSession(r.Context(), c.Value)
	}
	h.setSessionCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
