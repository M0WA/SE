package restapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// minUserPasswordLength is enforced on both account creation and a
// password reset -- a floor against a trivially guessable password, not a
// full strength policy.
const minUserPasswordLength = 8

// maxUserPasswordLength mirrors bcrypt.GenerateFromPassword's own hard
// limit (bcrypt.ErrPasswordTooLong, >72 bytes) -- checked explicitly here
// so a too-long password gets a clear 400 like every other validation
// failure, rather than bcrypt's own error surfacing as an opaque 500.
const maxUserPasswordLength = 72

// validateUserPassword enforces min/max length, shared by account
// creation and a password reset.
func validateUserPassword(w http.ResponseWriter, password string) bool {
	if len(password) < minUserPasswordLength {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return false
	}
	if len(password) > maxUserPasswordLength {
		http.Error(w, "password must be at most 72 bytes", http.StatusBadRequest)
		return false
	}
	return true
}

// userResponse is the wire shape for a domain.User -- PasswordHash is
// NEVER included, on create, list, get, or update; there is no wire
// representation of it at all. CustomPrompt is included here (unlike the
// self-service accountResponse in account.go, which this mirrors) so the
// admin-only per-user edit subpage (/admin/users/{id}) can view and change
// it on a user's behalf, same as the user can themselves via /account.
type userResponse struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	CustomPrompt string    `json:"custom_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toUserResponse(u domain.User) userResponse {
	return userResponse{ID: u.ID, Username: u.Username, CustomPrompt: u.CustomPrompt, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// createUserRequest's CustomPrompt is optional and plain (not a pointer,
// unlike updateUserRequest's) -- there's no "omitted vs explicitly empty"
// ambiguity to preserve on a brand new row, an absent value is simply
// empty either way.
type createUserRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	CustomPrompt string `json:"custom_prompt"`
}

// updateUserRequest uses pointer fields for the same reason
// updateAccountRequest (account.go) does: "omitted" (nil) and "explicitly
// cleared to empty string" must be distinguishable for CustomPrompt, and a
// present-but-invalid Password must be rejected (400), never silently
// ignored. Username is never editable once created -- a User's ID is
// minted from it at creation time (domain.NewUserID, same convention as
// domain.NewChatHookID); changing it afterward would orphan the original
// ID a session's user_id/log lines still reference.
type updateUserRequest struct {
	Password     *string `json:"password"`
	CustomPrompt *string `json:"custom_prompt"`
}

// handleAdminUsers lists (GET) or creates (POST) regular-user accounts,
// mirroring handleAdminChatHooks' style closely.
func (h *Handler) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "users") {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := h.users.ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, mapSlice(users, toUserResponse))
	case http.MethodPost:
		h.handleCreateUser(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[createUserRequest](w, r)
	if !ok {
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		http.Error(w, "username must not be empty", http.StatusBadRequest)
		return
	}
	// A regular-user account sharing the hardcoded admin's own username
	// would be ambiguous at login time (which of the two accounts did a
	// submitted username/password pair mean?) -- reject it outright rather
	// than define a precedence rule for something that should never
	// legitimately happen.
	if h.adminUser != "" && username == h.adminUser {
		http.Error(w, "username is reserved for the admin account", http.StatusBadRequest)
		return
	}
	if !validateUserPassword(w, req.Password) {
		return
	}
	if len(req.CustomPrompt) > maxCustomPromptLength {
		http.Error(w, "custom prompt too long", http.StatusBadRequest)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	existing, err := h.users.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	existingIDs := existingIDSet(existing, func(u domain.User) string { return u.ID })
	now := time.Now().UTC()
	u := domain.User{
		ID: domain.NewUserID(username, existingIDs), Username: username,
		PasswordHash: string(hash), CustomPrompt: req.CustomPrompt, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.users.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, ports.ErrUsernameTaken) {
			http.Error(w, "username already taken", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toUserResponse(u))
}

// handleAdminGetUser returns one user by ID (username, custom_prompt,
// timestamps -- never the password), backing the admin-only per-user edit
// subpage (/admin/users/{id}).
func (h *Handler) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "users") {
		return
	}
	u, err := h.users.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ports.ErrUserNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u))
}

// handleAdminUpdateUser lets the admin reset a user's password and/or
// change their custom_prompt on their behalf (see updateUserRequest --
// username is never editable). Loads the existing row first and only
// changes whichever fields were actually present in the request; every
// other field (Username, CreatedAt, ID) is passed through untouched.
func (h *Handler) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "users") {
		return
	}
	req, ok := decodeJSON[updateUserRequest](w, r)
	if !ok {
		return
	}
	if req.CustomPrompt != nil && len(*req.CustomPrompt) > maxCustomPromptLength {
		http.Error(w, "custom prompt too long", http.StatusBadRequest)
		return
	}
	var passwordHash string
	if req.Password != nil {
		if !validateUserPassword(w, *req.Password) {
			return
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		passwordHash = string(hashed)
	}

	id := r.PathValue("id")
	existing, err := h.users.GetUser(r.Context(), id)
	if err != nil {
		if errors.Is(err, ports.ErrUserNotFound) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Password != nil {
		existing.PasswordHash = passwordHash
	}
	if req.CustomPrompt != nil {
		existing.CustomPrompt = *req.CustomPrompt
	}
	existing.UpdatedAt = time.Now().UTC()
	err = h.users.UpdateUser(r.Context(), existing)
	respondOrNotFound(w, err, ports.ErrUserNotFound, "user not found", toUserResponse(existing))
}

func (h *Handler) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "users") {
		return
	}
	err := h.users.DeleteUser(r.Context(), r.PathValue("id"))
	respondOrNotFound(w, err, ports.ErrUserNotFound, "user not found", map[string]bool{"ok": true})
}
