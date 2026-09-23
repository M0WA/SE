package domain

import "time"

// User is a DB-backed regular-user account, distinct from the single
// hardcoded admin account (an env-var pair checked via
// subtle.ConstantTimeCompare -- never hashed, never a DB row). A User can
// use cmd/search but is always refused on every /admin/* route. User rows
// are managed entirely through the admin backend; there is no self-service signup.
type User struct {
	ID           string
	Username     string
	PasswordHash string // bcrypt hash -- the plaintext password is never stored or logged
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
	// RoleAdmin is assigned to a session created via the hardcoded admin
	// login. Grants access to both RoutesSearch and RoutesAdmin.
	RoleAdmin = "admin"
	// RoleUser is assigned to a session created via a User row's login.
	// Grants access to RoutesSearch only -- refused with 403 on every
	// RoutesAdmin route.
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
