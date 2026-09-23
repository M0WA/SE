package domain

import "time"

// User is a DB-backed account -- the only kind there is. There is no
// separate hardcoded admin account; admin access is just IsAdmin=true on an
// otherwise ordinary User row, so any number of accounts can hold it, any
// one of them can be revoked without touching the others, and an admin gets
// every self-service feature (personal prompt, personal MCP servers, files,
// persistent chats) a regular user gets, on top of /admin/* access. User
// rows are managed entirely through the admin backend (or, to create the
// first one before any admin exists to do that, packaging/create-admin.sh);
// there is no self-service signup.
type User struct {
	ID           string
	Username     string
	PasswordHash string // bcrypt hash -- the plaintext password is never stored or logged
	// IsAdmin grants /admin/* access (domain.RoleAdmin instead of
	// domain.RoleUser at login) on top of every regular-user self-service
	// feature -- it never takes any away. At least one User row must keep
	// this set at all times; restapi refuses to demote or delete the last one.
	IsAdmin bool
	// CustomPrompt is free text this user set via the self-service /account
	// page -- injected as its own leading system message in every chat turn
	// they send, in addition to the admin-configured SystemPrompt/MCP
	// prompt. Empty means nothing injected.
	CustomPrompt string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session role constants. A session's role lives purely in its server-side
// record (ports.SessionStore) -- the cookie itself stays an opaque random
// token, so a caller can never set or influence their own role.
const (
	// RoleAdmin is assigned to a session created by logging into a User row
	// with IsAdmin=true. Grants access to both RoutesSearch and RoutesAdmin.
	RoleAdmin = "admin"
	// RoleUser is assigned to a session created by logging into a User row
	// with IsAdmin=false. Grants access to RoutesSearch only -- refused
	// with 403 on every RoutesAdmin route.
	RoleUser = "user"
)

// UserIDPattern is SlugIDPattern -- short enough to fit the sqlrepo MySQL
// dialect's users.id VARCHAR(20) column.
var UserIDPattern = SlugIDPattern

// NewUserID derives an ID from a username the same way NewMCPServerID does,
// appending the shortest numeric suffix that avoids colliding with existing.
func NewUserID(username string, existing map[string]bool) string {
	return mintSlugID(username, existing, "user")
}
