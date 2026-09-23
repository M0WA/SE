package sqlrepo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const chatColumns = "id, user_id, title, agent_id, history, created_at, updated_at"

// ListChats lists ownerUserID's own pinned chats, most recently updated
// first -- the set reloaded automatically on the chat page.
func (r *Repository) ListChats(ctx context.Context, ownerUserID string) ([]domain.PersistedChat, error) {
	query := r.ph(`SELECT `+chatColumns+` FROM chats WHERE user_id = %s ORDER BY updated_at DESC`, 1)
	rows, err := r.db.QueryContext(ctx, query, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("querying chats: %w", err)
	}
	defer rows.Close()

	var out []domain.PersistedChat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning chat: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateChat inserts a new pinned chat, minting its ID (see randomChatID)
// and CreatedAt/UpdatedAt itself -- the caller never picks any of these.
func (r *Repository) CreateChat(ctx context.Context, c domain.PersistedChat) (domain.PersistedChat, error) {
	history, err := json.Marshal(c.History)
	if err != nil {
		return domain.PersistedChat{}, fmt.Errorf("encoding history: %w", err)
	}
	now := time.Now()
	c.ID = randomChatID()
	c.CreatedAt, c.UpdatedAt = now, now
	insertSQL := r.ph(`INSERT INTO chats (id, user_id, title, agent_id, history, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5, 6, 7)
	if _, err := r.db.ExecContext(ctx, insertSQL, c.ID, c.OwnerUserID, c.Title, c.AgentID, string(history),
		now.UTC().Format(crawledAtLayout), now.UTC().Format(crawledAtLayout)); err != nil {
		return domain.PersistedChat{}, fmt.Errorf("creating chat: %w", err)
	}
	return c, nil
}

// UpdateChat replaces c's editable fields (Title/AgentID/History) and
// bumps UpdatedAt to now -- ports.ErrChatNotFound if no chat with
// (c.OwnerUserID, c.ID) exists.
func (r *Repository) UpdateChat(ctx context.Context, c domain.PersistedChat) error {
	history, err := json.Marshal(c.History)
	if err != nil {
		return fmt.Errorf("encoding history: %w", err)
	}
	updateSQL := r.ph(`UPDATE chats SET title = %s, agent_id = %s, history = %s, updated_at = %s WHERE id = %s AND user_id = %s`, 1, 2, 3, 4, 5, 6)
	res, err := r.db.ExecContext(ctx, updateSQL, c.Title, c.AgentID, string(history), time.Now().UTC().Format(crawledAtLayout), c.ID, c.OwnerUserID)
	if err != nil {
		return fmt.Errorf("updating chat (%s): %w", c.ID, err)
	}
	return requireRowsAffected(res, c.ID, ports.ErrChatNotFound)
}

// DeleteChat returns ports.ErrChatNotFound if no chat with (ownerUserID,
// id) exists. Deletes every attached file FIRST, in the same transaction,
// rather than relying on uploaded_files.chat_id's ON DELETE CASCADE:
// SQLite only enforces foreign keys with "PRAGMA foreign_keys = ON", which
// this package never sets (confirmed -- files silently survived chat
// deletion under sqlite without this, while Postgres cascaded correctly).
// Explicit delete keeps behavior identical across dialects.
func (r *Repository) DeleteChat(ctx context.Context, ownerUserID, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning chat delete transaction: %w", err)
	}
	defer tx.Rollback()

	deleteFilesSQL := r.ph(`DELETE FROM uploaded_files WHERE user_id = %s AND chat_id = %s`, 1, 2)
	if _, err := tx.ExecContext(ctx, deleteFilesSQL, ownerUserID, id); err != nil {
		return fmt.Errorf("deleting files for chat (%s): %w", id, err)
	}
	deleteChatSQL := r.ph(`DELETE FROM chats WHERE user_id = %s AND id = %s`, 1, 2)
	res, err := tx.ExecContext(ctx, deleteChatSQL, ownerUserID, id)
	if err != nil {
		return fmt.Errorf("deleting chat (%s): %w", id, err)
	}
	if err := requireRowsAffected(res, id, ports.ErrChatNotFound); err != nil {
		return err
	}
	return tx.Commit()
}

func scanChat(row scanner) (domain.PersistedChat, error) {
	var c domain.PersistedChat
	var history sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&c.ID, &c.OwnerUserID, &c.Title, &c.AgentID, &history, &createdAt, &updatedAt); err != nil {
		return domain.PersistedChat{}, err
	}
	if history.Valid && history.String != "" {
		if err := json.Unmarshal([]byte(history.String), &c.History); err != nil {
			return domain.PersistedChat{}, fmt.Errorf("decoding history: %w", err)
		}
	}
	c.CreatedAt = parseCrawledAt(createdAt)
	c.UpdatedAt = parseCrawledAt(updatedAt)
	return c, nil
}

// randomChatID returns a 16-byte hex-encoded random token -- unguessable,
// since chat IDs appear directly in /account/api/chats/{id} URLs (see
// randomFileID's doc comment).
func randomChatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read practically never fails; a timestamp fallback
		// keeps IDs unique enough in that case rather than panicking a
		// chat creation (same tolerance randomFileID/dockersandbox.randomHex apply).
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
