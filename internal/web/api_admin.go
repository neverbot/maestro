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
//
// IsAdmin is a pointer, not a bare bool, and its absence is a 400 — a
// Round 2 review found the original bare-bool shape let an empty or
// malformed body silently decode to the zero value, false, and demote
// whoever Email named without the caller ever having said so. A pointer
// makes "the field was not sent" observable (nil) instead of
// indistinguishable from "the field was sent as false", the same
// distinction InviteRequest.ProjectID/*uuid.UUID and every other
// optional field in this codebase already uses for exactly this reason.
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
//
// Why this exists at all: BootstrapFirstAdmin (users.go) is the only
// other mechanism in this codebase that ever sets IsAdmin at first
// boot. Before this endpoint, exactly one account could ever hold that
// authority for the life of a running instance — losing it (the
// operator forgetting a password with no reset flow anywhere in this
// product, leaving the company, anything) meant the instance could
// never admit anyone new through POST /api/invites again. This closes
// that by letting an existing admin promote a colleague, deliberately
// not by any other mechanism (an invite cannot carry admin status —
// createInstanceInviteRequest has no such field, and
// createProjectInviteRequest's Role is a project role, not this
// instance-wide one). BootstrapFirstAdmin's own re-promotion path
// (users.go) is the other half: a restart-based recovery for when the
// count reaches one and that one account is itself unreachable, which
// this endpoint alone cannot fix (there is no admin left to call it).
//
// Identified by email, not a path-parameter user id: a Round 2 review
// named the gap this closes directly — with only a UUID selector, an
// admin had no way to find a colleague's id at all (no listing endpoint
// exists, and none is added here — see this decision's own reasoning
// below), so the only real path to a UUID was the colleague pasting
// their own /api/me response into a chat message. Email reuses the
// selector this codebase's admin surface already established:
// POST /api/invites (api_invites.go, createInstanceInviteRequest.Email)
// already identifies who an account-only invite is for the same way,
// so an admin who invited a colleague by email already has the one
// piece of information this endpoint needs, with nothing new to look up.
// The alternative — an admin-only GET /api/users listing every account's
// id, email, display name and admin flag — was considered and rejected:
// it would hand an admin a standing, browsable directory of every
// address on the instance for a capability this one selector already
// covers, and it would be a second surface to keep in sync with this
// one rather than the single selector both this endpoint and account-only
// invites now share.
//
// Self-demotion is allowed: an admin who is not the instance's last one
// may step down freely, the same "leaving is always your own choice"
// principle handleRemoveMember applies to project membership. What is
// refused, unconditionally, including against a caller demoting
// themselves, is identity.SetAdmin's own ErrLastAdmin guard — see that
// error's doc comment for why the instance can never be allowed to reach
// zero admins on purpose.
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
//
// **No password hash, and no session.** `identity.User` carries neither,
// which is the reason this handler may build a response from it at all;
// the shape is written by hand anyway, in snake_case, like every other
// response in this package, so a field added to the domain type does not
// appear on the wire by accident.
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
//
// **The screen said this was impossible.** Its own prose read "Maestro
// cannot say who they are: the server has no endpoint that answers it",
// and the only write was an email typed from memory into a field. An
// instance whose administrators cannot read the list of accounts is an
// instance where the flag gets granted to an address that belongs to
// nobody and nobody ever learns.
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
//
// **The standing goes through identity.SetAdmin and not through this
// statement**, because that is where the rule lives that an instance
// must keep one administrator — counted under a `FOR UPDATE` so two
// concurrent demotions cannot race each other to zero. A second path
// that wrote the column directly would be that rule with a hole in it.
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
