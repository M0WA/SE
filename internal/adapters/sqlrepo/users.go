package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const userColumns = "id, username, password_hash, created_at, updated_at"

// ListUsers lists every DB-backed regular-user account, ordered by
// username for a stable, human-friendly admin table order (mirrors
// ListChatHooks ordering by name for the same reason).
func (r *Repository) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying users: %w", err)
	}
	defer rows.Close()

	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns ports.ErrUserNotFound if no row with id exists.
func (r *Repository) GetUser(ctx context.Context, id string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT `+userColumns+` FROM users WHERE id = %s`, 1), id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return domain.User{}, ports.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("loading user (%s): %w", id, err)
	}
	return u, nil
}

// GetUserByUsername returns ports.ErrUserNotFound if no row has that
// username -- used by the login path to check a submitted username against
// a DB-backed regular-user account.
func (r *Repository) GetUserByUsername(ctx context.Context, username string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, r.ph(`SELECT `+userColumns+` FROM users WHERE username = %s`, 1), username)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return domain.User{}, ports.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("loading user by username (%s): %w", username, err)
	}
	return u, nil
}

// CreateUser inserts a new regular-user account, returning
// ports.ErrUsernameTaken if u.Username is already used by a different row
// (username is UNIQUE at the DB layer -- see dialect.go's users table --
// so this is a real constraint enforced under concurrent creates, not just
// a check-then-insert race).
func (r *Repository) CreateUser(ctx context.Context, u domain.User) error {
	insertSQL := r.ph(`INSERT INTO users (`+userColumns+`) VALUES (%s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5)
	_, err := r.db.ExecContext(ctx, insertSQL, u.ID, u.Username, u.PasswordHash,
		u.CreatedAt.UTC().Format(crawledAtLayout), u.UpdatedAt.UTC().Format(crawledAtLayout))
	if err != nil {
		if isUniqueViolationError(err) {
			return ports.ErrUsernameTaken
		}
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// UpdateUser replaces u's stored fields wholesale (ID never changes after
// creation), returning ports.ErrUserNotFound if no row with u.ID exists.
// Used for a password reset -- see restapi.handleAdminUpdateUser, which
// loads the existing row first and only changes PasswordHash/UpdatedAt.
func (r *Repository) UpdateUser(ctx context.Context, u domain.User) error {
	updateSQL := r.ph(`UPDATE users SET username = %s, password_hash = %s, updated_at = %s WHERE id = %s`, 1, 2, 3, 4)
	res, err := r.db.ExecContext(ctx, updateSQL, u.Username, u.PasswordHash, u.UpdatedAt.UTC().Format(crawledAtLayout), u.ID)
	if err != nil {
		return fmt.Errorf("updating user (%s): %w", u.ID, err)
	}
	return requireRowsAffected(res, u.ID, ports.ErrUserNotFound)
}

// DeleteUser returns ports.ErrUserNotFound if no row with id exists.
func (r *Repository) DeleteUser(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, r.ph(`DELETE FROM users WHERE id = %s`, 1), id)
	if err != nil {
		return fmt.Errorf("deleting user (%s): %w", id, err)
	}
	return requireRowsAffected(res, id, ports.ErrUserNotFound)
}

func scanUser(row scanner) (domain.User, error) {
	var u domain.User
	var createdAt, updatedAt string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &createdAt, &updatedAt); err != nil {
		return domain.User{}, err
	}
	u.CreatedAt = parseCrawledAt(createdAt)
	u.UpdatedAt = parseCrawledAt(updatedAt)
	return u, nil
}

// isUniqueViolationError reports whether err is a unique-constraint
// violation on an INSERT, across all three dialects -- distinct from
// isAlreadyExistsError above, which is scoped to the benign
// concurrent-migration race on CREATE TABLE/ALTER TABLE/CREATE INDEX; this
// is for a genuine application-level conflict (CreateUser's username
// UNIQUE constraint) that should surface as ports.ErrUsernameTaken, not a
// generic 500. Like isAlreadyExistsError, only ever called with a non-nil
// err (CreateUser's own error check guards the call), so no nil check here.
func isUniqueViolationError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // SQLite
		strings.Contains(msg, "duplicate key value violates unique constraint") || // Postgres
		strings.Contains(msg, "Duplicate entry") // MySQL
}
