package restapi

import (
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"searchengine/internal/domain"
)

// maxCustomPromptLength bounds a user's self-service custom prompt --
// mirrors chat.go's own maxChatMessageContentLength precedent for a
// similarly user-supplied text bound, sized generously since a custom
// prompt is meant to hold real instructions, not a single chat message.
const maxCustomPromptLength = 4000

// handleSession answers who the current session is, for the search page's
// own JS to decide which nav link to show (admin backend vs. self-service
// account page) -- reachable by EITHER role, unlike every /account/*
// route, so it's gated by requireAuthAPI (any authenticated role), not
// requireRegularUserAuthAPI.
func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if !requireGetOrHead(w, r) {
		return
	}
	role, _, ok := h.sessionRoleFor(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"role": role})
}

// accountResponse is the wire shape for a domain.User's self-service view
// of their own account -- PasswordHash is NEVER included, same discipline
// as admin_users.go's userResponse.
type accountResponse struct {
	Username     string `json:"username"`
	CustomPrompt string `json:"custom_prompt"`
}

func toAccountResponse(u domain.User) accountResponse {
	return accountResponse{Username: u.Username, CustomPrompt: u.CustomPrompt}
}

// updateAccountRequest uses pointer fields so "omitted" (nil) and
// "explicitly cleared to empty string" are distinguishable for
// CustomPrompt -- a user might legitimately want to clear their custom
// prompt back to empty, which a plain non-pointer string couldn't tell
// apart from "not sent". Password stays required-if-present: a
// present-but-invalid password (e.g. too short) is rejected the same way
// account creation rejects one, never silently ignored.
type updateAccountRequest struct {
	Password     *string `json:"password"`
	CustomPrompt *string `json:"custom_prompt"`
}

// handleAccount is the self-service counterpart to admin_users.go's
// handleAdminUsers/handleAdminUpdateUser, scoped to the CALLING session's
// own account only (there is no ID in the URL -- a regular user can only
// ever see or change their own row). GET returns the current account, PATCH
// updates password and/or custom prompt. Gated by requireRegularUserAuthAPI
// -- a role=admin session never reaches here (no domain.User row to act
// on).
func (h *Handler) handleAccount(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "account") {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		u, err := h.users.GetUser(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toAccountResponse(u))
	case http.MethodPatch:
		h.handleUpdateAccount(w, r, userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleUpdateAccount(w http.ResponseWriter, r *http.Request, userID string) {
	req, ok := decodeJSON[updateAccountRequest](w, r)
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

	existing, err := h.users.GetUser(r.Context(), userID)
	if err != nil {
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
	if err := h.users.UpdateUser(r.Context(), existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toAccountResponse(existing))
}

func (h *Handler) handleAccountPage(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/html; charset=utf-8", accountHTML)
}

func (h *Handler) handleAccountJS(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "text/javascript; charset=utf-8", accountJS)
}
