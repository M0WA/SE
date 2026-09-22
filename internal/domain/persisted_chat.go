package domain

import "time"

// PersistedChat is one chat conversation a signed-in regular-user account
// has explicitly pinned to persist -- unlike an ordinary chat tab (session-
// only, lost on reload unless exported/imported), a PersistedChat's full
// history is saved server-side and reloaded automatically the next time
// this user visits the chat page. Always scoped to OwnerUserID, same
// convention as UploadedFile. Only a pinned chat may have files attached
// (see UploadedFile.ChatID) -- deleting a PersistedChat cascades to delete
// every file attached to it.
type PersistedChat struct {
	ID          string
	OwnerUserID string
	Title       string
	AgentID     string
	History     []ChatMessage
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
