package domain

import "time"

// PersistedChat is one chat conversation a user has pinned to persist --
// unlike an ordinary session-only chat tab, its full history is saved
// server-side and reloaded automatically. Scoped to OwnerUserID. Only a
// pinned chat may have files attached (UploadedFile.ChatID); deleting it
// cascades to delete those files.
type PersistedChat struct {
	ID          string
	OwnerUserID string
	Title       string
	AgentID     string
	History     []ChatMessage
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
