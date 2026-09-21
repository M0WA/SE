package domain

import "time"

// User is a DB-backed regular-user account, entirely distinct from the
// single hardcoded admin account (an ADMIN_USER/ADMIN_PASSWORD-style
// env-var pair, checked in restapi.Handler.checkCredentials via
// subtle.ConstantTimeCompare -- never hashed, never a row in any table). A
// User can log in and use cmd/search (the index page, /search, /chat) but
// is always refused on every /admin/* route -- see RoleAdmin/RoleUser below
// and restapi's requireAdminAuthPage/requireAdminAuthAPI. User rows are
// created, listed, and deleted entirely through the admin backend
// (Settings -> Users); there is no self-service signup.
type User struct {
	ID           string
	Username     string
	PasswordHash string // bcrypt hash (bcrypt.DefaultCost) -- the plaintext password is never stored or logged
	// CustomPrompt is free text this user has set for themselves (via the
	// self-service /account page) -- injected as its own leading system
	// message in every chat turn THEY send, in addition to (not instead of)
	// the admin-configured endpoint-wide SystemPrompt and any active MCP
	// server's own prompt. Expanded through the same %c-style placeholder
	// mechanism application.expandPromptPlaceholders already applies to
	// those (see application.ChatService.Chat). Empty means no per-user
	// prompt is injected.
	CustomPrompt string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session role constants. A session's role lives purely in its own
// server-side record (see ports.SessionStore) -- the se_session cookie
// itself stays an opaque random token, exactly as before this concept was
// introduced, so a caller can never set or influence their own role: it is
// resolved fresh from the session store on every request, the same trust
// model ValidSession already used for "is this session valid at all."
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

// NewUserID derives an ID from a username the same way NewMCPServerID
// derives one from an MCP server's display name (lowercased, non-alphanumeric
// runs collapsed, trimmed to fit UserIDPattern), appending the shortest
// numeric suffix that avoids colliding with a key in existing (every other
// User's ID). Falls back to a timestamp-derived ID if username has no
// alphanumeric characters.
func NewUserID(username string, existing map[string]bool) string {
	return mintSlugID(username, existing, "user")
}
