package domain

import "time"

// UploadedFile is one file a signed-in regular-user (domain.RoleUser)
// account has uploaded, or a first-party file-operations MCP tool
// (cmd/mcp-files' "write_file") has created on that user's behalf, for
// inspection/download during chat -- always scoped to OwnerUserID, never
// visible to any other user, including the admin account (which has no
// User row of its own to own a file under -- see ports.FileStore's own
// doc comment). Content bytes are stored separately from this struct (see
// ports.FileStore.SaveFile/GetFile) so listing a user's files never needs
// to load every file's full content.
type UploadedFile struct {
	ID          string
	OwnerUserID string
	Filename    string
	ContentType string
	Size        int64
	CreatedAt   time.Time
}
