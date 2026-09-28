package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
)

// setAdminRequest is the body PATCH /api/admins accepts.
type setAdminRequest struct {
	Email   string `json:"email"`
	IsAdmin *bool  `json:"is_admin"`
}

// handleSetAdmin promotes or demotes an instance admin, identified by
// email. Admin-only (requireAdminCaller, api_invites.go's own doc
// comment on why — instance-admin standing is the same "who gets to
// hold or grant standing in the product" authority that gates the
// account-only invite surface, and it is gated on that exact authority:
// only an existing admin may mint another one.
func (s *Server) handleSetAdmin(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	var req setAdminRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, errCodeBadRequest, "email is required")
		return
	}
	if req.IsAdmin == nil {
		writeError(w, http.StatusBadRequest, errCodeBadRequest, "is_admin is required")
		return
	}

	if err := s.opts.Identity.SetAdminByEmail(r.Context(), req.Email, *req.IsAdmin); err != nil {
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
		case errors.Is(err, identity.ErrLastAdmin):
			writeError(w, http.StatusConflict, errCodeLastAdmin, "the instance must keep at least one admin — promote someone else first")
		default:
			// Named both parties explicitly (Task 22): the previous
			// version of this log line carried only the raw error, with
			// neither the acting admin nor the target email — the most
			// privilege-sensitive mutation in the product was also the
			// least attributable one, unlike handleDeleteGame and every
			// other consequential mutation in this file, which already
			// log the acting caller.
			writeUnmappedError(w, r, err, "set admin failed", "could not update admin status",
				"actor_user_id", caller.UserID, "target_email", req.Email, "is_admin", *req.IsAdmin)
		}
		return
	}

	// Success is logged too, for the same reason: this endpoint's failure
	// path already named both parties, but a promotion or demotion that
	// *succeeds* is the more common case an operator or a future
	// incident review would actually need to reconstruct — "who granted
	// admin to whom, and when" — and nothing recorded that before.
	slog.InfoContext(r.Context(), "admin status changed",
		"actor_user_id", caller.UserID, "target_email", req.Email, "is_admin", *req.IsAdmin)

	// No SSE event: every kind publish.go defines is keyed on a project
	// id (realtime.Hub is keyed by project, hub.go's own subs map), and
	// instance-admin status has no project to key against — the same
	// reason the account-only invite endpoints (api_invites.go,
	// handleCreateInstanceInvite/handleRevokeInstanceInvite) publish
	// nothing either. See publish.go's own package doc comment for the
	// full reasoning this decision reuses.
	writeJSON(w, http.StatusOK, map[string]any{"email": req.Email, "is_admin": *req.IsAdmin})
}

// --- The accounts on this instance ------------------------------------

// userResponse is one account as this surface publishes it.
type userResponse struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	IsAdmin     bool      `json:"is_admin"`
	CreatedAt   time.Time `json:"created_at"`
	// You is the caller's own row. The screen needs it for the one thing
	// an administrator must not be allowed to do by accident — take
	// their own standing away and lock themselves out of the page they
	// are standing on — and the server decides it rather than the
	// browser comparing two ids it was handed.
	You bool `json:"you"`
}

// handleListUsers answers who is on this instance. Admin-only.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	size := 0
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errCodeBadRequest, "page_size must be a number")
			return
		}
		size = parsed
	}
	page, err := s.opts.Identity.ListUsers(r.Context(), r.URL.Query().Get("cursor"), int32(size))
	if err != nil {
		if errors.Is(err, identity.ErrCursorInvalid) {
			writeError(w, http.StatusBadRequest, errCodeBadRequest,
				"that cursor is not one this listing issued")
			return
		}
		writeUnmappedError(w, r, err, "list users failed", "could not list the accounts")
		return
	}
	users := make([]userResponse, len(page.Users))
	for i, user := range page.Users {
		users[i] = userResponse{
			ID:          user.ID,
			Email:       user.Email,
			DisplayName: user.DisplayName,
			IsAdmin:     user.IsAdmin,
			CreatedAt:   user.CreatedAt,
			You:         user.ID == caller.UserID,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "next_cursor": page.NextCursor})
}

// updateUserRequest is the whole of what an administrator may change
// about somebody else's account. Every field is a pointer and an absent
// one is left alone, so a screen editing a name does not have to send an
// email back and cannot blank one by forgetting it.
type updateUserRequest struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
	IsAdmin     *bool   `json:"is_admin"`
}

// handleUpdateUser changes an account's address, name and standing.
// Admin-only.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
		return
	}
	var req updateUserRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	current, err := s.opts.Identity.UserByID(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
		return
	}

	if req.Email != nil || req.DisplayName != nil {
		email := current.Email
		if req.Email != nil {
			email = *req.Email
		}
		displayName := current.DisplayName
		if req.DisplayName != nil {
			displayName = *req.DisplayName
		}
		updated, err := s.opts.Identity.UpdateIdentity(r.Context(), identity.UpdateIdentityRequest{
			UserID:      targetID,
			Email:       email,
			DisplayName: displayName,
		})
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
			return
		case errors.Is(err, identity.ErrEmailTaken):
			writeError(w, http.StatusConflict, errCodeEmailTaken, "another account already signs in with that address")
			return
		case errors.Is(err, identity.ErrEmailInvalid), errors.Is(err, identity.ErrDisplayNameInvalid):
			writeError(w, http.StatusUnprocessableEntity, errCodeBadRequest, err.Error())
			return
		case err != nil:
			writeUnmappedError(w, r, err, "update user failed", "could not save that account",
				"target_user_id", targetID)
			return
		}
		current = updated
	}

	if req.IsAdmin != nil && *req.IsAdmin != current.IsAdmin {
		if err := s.opts.Identity.SetAdmin(r.Context(), targetID, *req.IsAdmin); err != nil {
			switch {
			case errors.Is(err, identity.ErrUserNotFound):
				writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
			case errors.Is(err, identity.ErrLastAdmin):
				writeError(w, http.StatusConflict, errCodeLastAdmin,
					"the instance must keep at least one admin — promote someone else first")
			default:
				writeUnmappedError(w, r, err, "set admin failed", "could not change that account's standing",
					"target_user_id", targetID)
			}
			return
		}
		current.IsAdmin = *req.IsAdmin
		// The same line handleSetAdmin logs, for the same reason: this is
		// the most privilege-sensitive mutation in the product and both
		// parties belong in the record.
		slog.InfoContext(r.Context(), "admin status changed",
			"actor_user_id", caller.UserID, "target_email", current.Email, "is_admin", current.IsAdmin)
	}

	writeJSON(w, http.StatusOK, userResponse{
		ID:          current.ID,
		Email:       current.Email,
		DisplayName: current.DisplayName,
		IsAdmin:     current.IsAdmin,
		CreatedAt:   current.CreatedAt,
		You:         current.ID == caller.UserID,
	})
}
