package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CreateSession persists a login session so any process sharing this
// database -- search-server, admin-server -- recognizes the same token,
// rather than only the process that issued it (an in-memory session store
// can't do that, since search-server and admin-server are separate OS
// processes with no shared memory). Opportunistically deletes every
// already-expired session first, so the table doesn't grow without bound
// purely from old sessions nobody ever explicitly logged out of.
func (r *Repository) CreateSession(ctx context.Context, token string, expiresAt time.Time) error {
	now := time.Now().UTC().Format(crawledAtLayout)
	if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE expires_at < %s`, 1), now); err != nil {
		return fmt.Errorf("pruning expired sessions: %w", err)
	}
	insert := r.ph(`INSERT INTO sessions (token, expires_at) VALUES (%s, %s)`, 1, 2)
	if _, err := r.db.ExecContext(ctx, insert, token, expiresAt.UTC().Format(crawledAtLayout)); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// ValidSession reports whether token names a session that hasn't expired
// yet. An expired session is deleted as a side effect of being found, the
// same lazy-cleanup behavior the old in-memory session store had.
func (r *Repository) ValidSession(ctx context.Context, token string) (bool, error) {
	var expiresAtStr string
	err := r.db.QueryRowContext(ctx, r.ph(`SELECT expires_at FROM sessions WHERE token = %s`, 1), token).Scan(&expiresAtStr)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("loading session: %w", err)
	}
	expiresAt, err := time.Parse(crawledAtLayout, expiresAtStr)
	if err != nil {
		return false, nil
	}
	if time.Now().After(expiresAt) {
		if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE token = %s`, 1), token); err != nil {
			return false, fmt.Errorf("deleting expired session: %w", err)
		}
		return false, nil
	}
	return true, nil
}

// RevokeSession deletes a session outright (a sign-out), regardless of
// whether it had already expired. Deleting a token that doesn't exist is
// not an error -- signing out twice, or of an already-expired session, is a
// normal, harmless occurrence, not a fault.
func (r *Repository) RevokeSession(ctx context.Context, token string) error {
	if _, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM sessions WHERE token = %s`, 1), token); err != nil {
		return fmt.Errorf("revoking session: %w", err)
	}
	return nil
}
