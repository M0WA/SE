package domain

import "time"

// UploadedFile is one file a regular-user account uploaded, or a
// file-operations MCP tool created on their behalf, for inspection/download
// during chat -- scoped to OwnerUserID, never visible to another user
// (including admin, which has no User row). Content bytes are stored
// separately (ports.FileStore.SaveFile/GetFile) so listing never loads full content.
type UploadedFile struct {
	ID          string
	OwnerUserID string
	// ChatID ties this file to the PersistedChat it was attached during --
	// non-empty for any file created after pinned chats shipped. Deleting
	// the PersistedChat cascades to delete this file. Empty for an
	// older file, still listed/downloadable, just untied to a conversation.
	ChatID      string
	Filename    string
	ContentType string
	Size        int64
	CreatedAt   time.Time
}
