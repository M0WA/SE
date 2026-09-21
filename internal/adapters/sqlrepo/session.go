package sqlrepo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// hashSessionToken returns the hex-encoded SHA-256 digest stored as the
// sessions table's key, never the raw token -- so DB-only access (a
// backup, a SQL injection) yields digests that can't be replayed as a
// live session cookie, the same way a password hash can't log in directly.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession persists a login session so every process sharing this
// database recognizes the same token, not just the one that issued it.
// Opportunistically deletes every already-expired session first, so the
// table doesn't grow unbounded from sessions nobody explicitly logged out of.
func (r *Repository) CreateSession(ctx context.Context, token string, expiresAt time.Time, role string, userID string) error {
	now := time.Now().UTC().Format(crawledAtLayout)
	if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE expires_at < %s`, 1), now); err != nil {
		return fmt.Errorf("pruning expired sessions: %w", err)
	}
	insert := r.ph(`INSERT INTO sessions (token, expires_at, role, user_id) VALUES (%s, %s, %s, %s)`, 1, 2, 3, 4)
	if _, err := r.db.ExecContext(ctx, insert, hashSessionToken(token), expiresAt.UTC().Format(crawledAtLayout), role, userID); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// ValidSession reports whether token names a session that hasn't expired
// yet and, if so, the role and userID it was created with. An expired
// session is deleted as a side effect of being found, the same
// lazy-cleanup behavior the old in-memory session store had.
func (r *Repository) ValidSession(ctx context.Context, token string) (bool, string, string, error) {
	hashed := hashSessionToken(token)
	var expiresAtStr, role, userID string
	err := r.db.QueryRowContext(ctx, r.ph(`SELECT expires_at, role, user_id FROM sessions WHERE token = %s`, 1), hashed).Scan(&expiresAtStr, &role, &userID)
	if err == sql.ErrNoRows {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", fmt.Errorf("loading session: %w", err)
	}
	expiresAt, err := time.Parse(crawledAtLayout, expiresAtStr)
	if err != nil {
		return false, "", "", nil
	}
	if time.Now().After(expiresAt) {
		if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE token = %s`, 1), hashed); err != nil {
			return false, "", "", fmt.Errorf("deleting expired session: %w", err)
		}
		return false, "", "", nil
	}
	return true, role, userID, nil
}

// RevokeSession deletes a session outright (a sign-out), regardless of
// whether it had already expired. Deleting a token that doesn't exist is
// not an error -- signing out twice, or of an already-expired session, is a
// normal, harmless occurrence, not a fault.
func (r *Repository) RevokeSession(ctx context.Context, token string) error {
	if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE token = %s`, 1), hashSessionToken(token)); err != nil {
		return fmt.Errorf("revoking session: %w", err)
	}
	return nil
}
