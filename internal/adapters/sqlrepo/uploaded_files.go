package sqlrepo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

const uploadedFileColumns = "id, user_id, chat_id, filename, content_type, size, created_at"

// ListFiles lists ownerUserID's own files, most recently uploaded first --
// metadata only (no data column), so listing stays cheap regardless of how
// large any individual file's content is.
func (r *Repository) ListFiles(ctx context.Context, ownerUserID string) ([]domain.UploadedFile, error) {
	query := r.ph(`SELECT `+uploadedFileColumns+` FROM uploaded_files WHERE user_id = %s ORDER BY created_at DESC`, 1)
	rows, err := r.db.QueryContext(ctx, query, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("querying uploaded files: %w", err)
	}
	defer rows.Close()

	var out []domain.UploadedFile
	for rows.Next() {
		f, err := scanUploadedFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning uploaded file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFilesForChat is ListFiles narrowed to one chat -- see
// ports.FileStore.ListFilesForChat's own doc comment.
func (r *Repository) ListFilesForChat(ctx context.Context, ownerUserID, chatID string) ([]domain.UploadedFile, error) {
	query := r.ph(`SELECT `+uploadedFileColumns+` FROM uploaded_files WHERE user_id = %s AND chat_id = %s ORDER BY created_at DESC`, 1, 2)
	rows, err := r.db.QueryContext(ctx, query, ownerUserID, chatID)
	if err != nil {
		return nil, fmt.Errorf("querying uploaded files for chat: %w", err)
	}
	defer rows.Close()

	var out []domain.UploadedFile
	for rows.Next() {
		f, err := scanUploadedFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning uploaded file: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SaveFile inserts a new file owned by ownerUserID and attached to chatID,
// minting its ID (see randomFileID) and CreatedAt itself -- the caller
// never picks either.
func (r *Repository) SaveFile(ctx context.Context, ownerUserID, chatID, filename, contentType string, data []byte) (domain.UploadedFile, error) {
	f := domain.UploadedFile{
		ID: randomFileID(), OwnerUserID: ownerUserID, ChatID: chatID, Filename: filename,
		ContentType: contentType, Size: int64(len(data)), CreatedAt: time.Now(),
	}
	insertSQL := r.ph(`INSERT INTO uploaded_files (id, user_id, chat_id, filename, content_type, size, data, created_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s)`, 1, 2, 3, 4, 5, 6, 7, 8)
	if _, err := r.db.ExecContext(ctx, insertSQL, f.ID, f.OwnerUserID, nullableString(f.ChatID), f.Filename, f.ContentType, f.Size, data, f.CreatedAt.UTC().Format(crawledAtLayout)); err != nil {
		return domain.UploadedFile{}, fmt.Errorf("saving uploaded file: %w", err)
	}
	return f, nil
}

// GetFile returns ports.ErrFileNotFound if no file with (ownerUserID, id)
// exists -- scoping the lookup by owner, not just id, is what keeps one
// user from ever downloading another's file even if they somehow guessed
// its ID.
func (r *Repository) GetFile(ctx context.Context, ownerUserID, id string) (domain.UploadedFile, []byte, error) {
	query := r.ph(`SELECT `+uploadedFileColumns+`, data FROM uploaded_files WHERE user_id = %s AND id = %s`, 1, 2)
	row := r.db.QueryRowContext(ctx, query, ownerUserID, id)
	var f domain.UploadedFile
	var chatID sql.NullString
	var createdAt string
	var data []byte
	err := row.Scan(&f.ID, &f.OwnerUserID, &chatID, &f.Filename, &f.ContentType, &f.Size, &createdAt, &data)
	if err == sql.ErrNoRows {
		return domain.UploadedFile{}, nil, ports.ErrFileNotFound
	}
	if err != nil {
		return domain.UploadedFile{}, nil, fmt.Errorf("loading uploaded file (%s): %w", id, err)
	}
	f.ChatID = chatID.String
	f.CreatedAt = parseCrawledAt(createdAt)
	return f, data, nil
}

// DeleteFile returns ports.ErrFileNotFound if no file with (ownerUserID,
// id) exists.
func (r *Repository) DeleteFile(ctx context.Context, ownerUserID, id string) error {
	deleteSQL := r.ph(`DELETE FROM uploaded_files WHERE user_id = %s AND id = %s`, 1, 2)
	res, err := r.db.ExecContext(ctx, deleteSQL, ownerUserID, id)
	if err != nil {
		return fmt.Errorf("deleting uploaded file (%s): %w", id, err)
	}
	return requireRowsAffected(res, id, ports.ErrFileNotFound)
}

func scanUploadedFile(row scanner) (domain.UploadedFile, error) {
	var f domain.UploadedFile
	var chatID sql.NullString
	var createdAt string
	if err := row.Scan(&f.ID, &f.OwnerUserID, &chatID, &f.Filename, &f.ContentType, &f.Size, &createdAt); err != nil {
		return domain.UploadedFile{}, err
	}
	f.ChatID = chatID.String
	f.CreatedAt = parseCrawledAt(createdAt)
	return f, nil
}

// nullableString returns a sql.NullString that's actually NULL for an
// empty s -- used for uploaded_files.chat_id, whose foreign key must see
// SQL NULL (never the empty string, which no chats.id will ever match) for
// a file with no chat association. Mirrors nullableTimeString's own
// empty/zero-means-NULL convention for a different type.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// randomFileID returns a 16-byte random token, hex-encoded (32 characters,
// fitting the mysql dialect's uploaded_files.id VARCHAR(32)) -- unguessable
// the same way a session token is, since a file ID appears directly in a
// download URL (see dialect.go's uploaded_files comment for why this isn't
// a name-derived slug like domain.NewMCPServerID).
func randomFileID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read practically never fails on any platform this
		// runs on; falling back to a timestamp keeps IDs unique enough
		// even in that vanishingly unlikely case, rather than panicking a
		// whole file upload over an ID-generation hiccup (same tolerance
		// dockersandbox.randomHex applies to its own container names).
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
