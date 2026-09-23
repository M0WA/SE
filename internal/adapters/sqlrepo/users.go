package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const userColumns = "id, username, password_hash, is_admin, custom_prompt, created_at, updated_at"

// ListUsers lists every regular-user account, ordered by username for a
// stable admin table order (mirrors ListMCPServers ordering by name).
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
// username -- used by the login path to check a submitted username.
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
// ports.ErrUsernameTaken if u.Username is taken (UNIQUE at the DB layer --
// see dialect.go's users table -- a real constraint under concurrent
// creates, not a check-then-insert race).
func (r *Repository) CreateUser(ctx context.Context, u domain.User) error {
	insertSQL := r.ph(`INSERT INTO users (`+userColumns+`) VALUES (%s, %s, %s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5, 6, 7)
	_, err := r.db.ExecContext(ctx, insertSQL, u.ID, u.Username, u.PasswordHash, u.IsAdmin, u.CustomPrompt,
		u.CreatedAt.UTC().Format(crawledAtLayout), u.UpdatedAt.UTC().Format(crawledAtLayout))
	if err != nil {
		if isUniqueViolationError(err) {
			return ports.ErrUsernameTaken
		}
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// UpdateUser replaces u's stored fields wholesale (ID never changes),
// returning ports.ErrUserNotFound if no row with u.ID exists. Used both
// for an admin password reset and a user's self-service password/prompt
// update -- both load-then-selectively-change (restapi.handleAdminUpdateUser
// / handleAccount).
func (r *Repository) UpdateUser(ctx context.Context, u domain.User) error {
	updateSQL := r.ph(`UPDATE users SET username = %s, password_hash = %s, is_admin = %s, custom_prompt = %s, updated_at = %s WHERE id = %s`, 1, 2, 3, 4, 5, 6)
	res, err := r.db.ExecContext(ctx, updateSQL, u.Username, u.PasswordHash, u.IsAdmin, u.CustomPrompt, u.UpdatedAt.UTC().Format(crawledAtLayout), u.ID)
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
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.CustomPrompt, &createdAt, &updatedAt); err != nil {
		return domain.User{}, err
	}
	u.CreatedAt = parseCrawledAt(createdAt)
	u.UpdatedAt = parseCrawledAt(updatedAt)
	return u, nil
}

// isUniqueViolationError reports whether err is a unique-constraint
// violation on an INSERT, across all three dialects -- distinct from
// isAlreadyExistsError's benign migration race on CREATE TABLE/INDEX; this
// is a genuine conflict (CreateUser's username UNIQUE) that should surface
// as ports.ErrUsernameTaken, not a 500. Only ever called with non-nil err,
// so no nil check here.
func isUniqueViolationError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // SQLite
		strings.Contains(msg, "duplicate key value violates unique constraint") || // Postgres
		strings.Contains(msg, "Duplicate entry") // MySQL
}
