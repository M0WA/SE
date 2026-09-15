package restapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestSessionStore_CreateThenValidSucceeds(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	if err := s.CreateSession(ctx, "tok-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	valid, err := s.ValidSession(ctx, "tok-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Error("expected a freshly created session to be valid")
	}
}

func TestSessionStore_ValidUnknownTokenReportsFalse(t *testing.T) {
	s := newSessionStore()
	valid, err := s.ValidSession(context.Background(), "never-issued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected an unknown token to be invalid")
	}
}

// TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten proves
// ValidSession's expiry branch: a session past its TTL reports invalid and
// is removed from the store (a subsequent check doesn't need to
// re-discover the same expired entry every time).
func TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	token := "tok-expired"
	if err := s.CreateSession(ctx, token, time.Now().Add(-time.Second)); err != nil { // already expired
		t.Fatalf("unexpected error: %v", err)
	}

	valid, err := s.ValidSession(ctx, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected an expired session to be invalid")
	}
	s.mu.Lock()
	_, stillPresent := s.sessions[token]
	s.mu.Unlock()
	if stillPresent {
		t.Error("expected the expired session to be removed from the store")
	}
}

func TestSessionStore_RevokeInvalidatesSession(t *testing.T) {
	s := newSessionStore()
	ctx := context.Background()
	token := "tok-revoke"
	if err := s.CreateSession(ctx, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.RevokeSession(ctx, token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	valid, err := s.ValidSession(ctx, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Error("expected a revoked session to be invalid")
	}
}

// TestSafeNext covers every branch of safeNext's open-redirect guard: an
// empty value, a bare "/", a same-site path, and the various absolute /
// protocol-relative forms browsers will follow off-site -- "//host",
// "/\host" (backslash is treated the same as a forward slash by browsers
// when resolving a URL, so it's an equally valid open-redirect vector),
// a double-backslash, and a plain absolute URL. The literal character
// check rejects any second character that's "/" or "\" outright (matching
// CodeQL's own go/bad-redirect-check recommendation), even though a
// two-backslash value alone would actually resolve as a safe same-origin
// path in both net/url and real browsers -- deliberately more conservative
// than strictly necessary rather than relying solely on the url.Parse
// layer underneath it.
func TestSafeNext(t *testing.T) {
	cases := []struct {
		name string
		next string
		want string
	}{
		{"empty falls back to /admin", "", "/admin"},
		{"bare slash is safe", "/", "/"},
		{"same-site path is safe", "/admin/jobs", "/admin/jobs"},
		{"protocol-relative // is rejected", "//evil.example", "/admin"},
		{"backslash after slash is rejected", "/\\evil.example", "/admin"},
		{"backslash-prefixed path is rejected", "/\\evil.com", "/admin"},
		{"double-backslash is rejected", "/\\\\evil.com", "/admin"},
		{"absolute URL is rejected", "https://evil.example/", "/admin"},
		{"relative path without leading slash is rejected", "evil.example", "/admin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeNext(tc.next); got != tc.want {
				t.Errorf("safeNext(%q) = %q, want %q", tc.next, got, tc.want)
			}
		})
	}
}

func TestRandomToken_ProducesDistinctNonEmptyTokens(t *testing.T) {
	a := randomToken()
	b := randomToken()
	if a == "" || b == "" {
		t.Error("expected non-empty tokens")
	}
	if a == b {
		t.Error("expected two calls to produce distinct tokens")
	}
}

func TestLoginLimiter_UnknownKeyIsNotLocked(t *testing.T) {
	l := newLoginLimiter()
	if _, locked := l.locked("1.2.3.4", time.Now()); locked {
		t.Error("expected a key with no history to not be locked")
	}
}

func TestLoginLimiter_LocksOutAfterMaxAttempts(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := 0; i < loginMaxAttempts; i++ {
		l.recordFailure("1.2.3.4", now)
		if _, locked := l.locked("1.2.3.4", now); locked {
			t.Fatalf("expected no lockout before crossing loginMaxAttempts, at failure %d", i+1)
		}
	}
	l.recordFailure("1.2.3.4", now) // the failure that crosses the threshold
	wait, locked := l.locked("1.2.3.4", now)
	if !locked {
		t.Fatal("expected a lockout after crossing loginMaxAttempts")
	}
	if wait != loginBaseLockout {
		t.Errorf("expected the first lockout to be loginBaseLockout, got %v", wait)
	}
}

func TestLoginLimiter_LockoutExpires(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := 0; i <= loginMaxAttempts; i++ {
		l.recordFailure("1.2.3.4", now)
	}
	if _, locked := l.locked("1.2.3.4", now.Add(loginBaseLockout+time.Second)); locked {
		t.Error("expected the lockout to have expired")
	}
}

func TestLoginLimiter_BackoffGrowsAndCapsAtMaxLockout(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	// Cross the threshold, then keep failing (each still within the
	// lockout, as a real attacker retrying would) far past what it'd take
	// to exceed loginMaxLockout without capping.
	for i := 0; i < loginMaxAttempts+20; i++ {
		l.recordFailure("1.2.3.4", now)
	}
	wait, locked := l.locked("1.2.3.4", now)
	if !locked {
		t.Fatal("expected still locked out")
	}
	if wait > loginMaxLockout {
		t.Errorf("expected backoff capped at loginMaxLockout (%v), got %v", loginMaxLockout, wait)
	}
	if wait <= loginBaseLockout {
		t.Errorf("expected backoff to have grown past the base lockout, got %v", wait)
	}
}

func TestLoginLimiter_WindowResetsAfterExpiry(t *testing.T) {
	l := newLoginLimiter()
	start := time.Now()
	for i := 0; i < loginMaxAttempts; i++ {
		l.recordFailure("1.2.3.4", start)
	}
	// One more failure, but long after the sliding window expired -- this
	// must start a fresh window (failures reset to 1) rather than treating
	// it as the failure that crosses the threshold.
	later := start.Add(loginAttemptWindow + time.Minute)
	l.recordFailure("1.2.3.4", later)
	if _, locked := l.locked("1.2.3.4", later); locked {
		t.Error("expected a failure in a fresh window to not immediately lock out")
	}
}

func TestLoginLimiter_SuccessClearsFailureHistory(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := 0; i < loginMaxAttempts; i++ {
		l.recordFailure("1.2.3.4", now)
	}
	l.recordSuccess("1.2.3.4")
	l.recordFailure("1.2.3.4", now) // would be failure #1 of a fresh window
	if _, locked := l.locked("1.2.3.4", now); locked {
		t.Error("expected recordSuccess to have cleared the prior failure count")
	}
}

func TestLoginLimiter_KeysAreIndependent(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := 0; i <= loginMaxAttempts; i++ {
		l.recordFailure("1.2.3.4", now)
	}
	if _, locked := l.locked("5.6.7.8", now); locked {
		t.Error("expected a different key to be unaffected by another key's lockout")
	}
}

func TestClientIP_PrefersXRealIP(t *testing.T) {
	r := &http.Request{Header: http.Header{"X-Real-Ip": []string{"203.0.113.5"}}, RemoteAddr: "127.0.0.1:9999"}
	if got := clientIP(r); got != "203.0.113.5" {
		t.Errorf("expected X-Real-IP to win, got %q", got)
	}
}

func TestClientIP_FallsBackToRemoteAddr(t *testing.T) {
	r := &http.Request{Header: http.Header{}, RemoteAddr: "192.0.2.1:1234"}
	if got := clientIP(r); got != "192.0.2.1:1234" {
		t.Errorf("expected RemoteAddr fallback, got %q", got)
	}
}
