package restapi

import (
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"searchengine/internal/domain"
)

// maxCustomPromptLength bounds a user's self-service custom prompt (mirrors
// chat.go's maxChatMessageContentLength), sized generously for real instructions.
const maxCustomPromptLength = 4000

// handleSession reports the current session's role, for the search page's nav
// JS. Reachable by either role (requireAuthAPI), unlike every other /account/* route.
func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	if !requireGetOrHead(w, r) {
		return
	}
	role, _, ok := h.sessionRoleFor(r)
	if !ok {
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodHead {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"role": role})
}

// accountResponse is a domain.User's self-service view -- PasswordHash is
// never included, same as admin_users.go's userResponse.
type accountResponse struct {
	Username     string `json:"username"`
	CustomPrompt string `json:"custom_prompt"`
}

func toAccountResponse(u domain.User) accountResponse {
	return accountResponse{Username: u.Username, CustomPrompt: u.CustomPrompt}
}

// updateAccountRequest uses pointer fields so CustomPrompt can distinguish
// "omitted" from "cleared to empty". A present-but-invalid password is
// still rejected, same as account creation, never silently ignored.
type updateAccountRequest struct {
	Password     *string `json:"password"`
	CustomPrompt *string `json:"custom_prompt"`
}

// handleAccount is the self-service counterpart to admin_users.go's handlers,
// scoped to the calling session's own row (no ID in the URL). GET returns the
// account, PATCH updates password/custom prompt. Gated by
// requireRegularUserAuthAPI; admin sessions never reach here.
func (h *Handler) handleAccount(w http.ResponseWriter, r *http.Request) {
	if !requireConfigured(w, h.users != nil, "account") {
		return
	}
	_, userID, ok := h.sessionRoleFor(r)
	if !ok || userID == "" {
		http.Error(w, authRequiredMsg, http.StatusUnauthorized)
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
