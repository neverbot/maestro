package web

import (
	"errors"
	"log/slog"
	"net/http"

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
			slog.ErrorContext(r.Context(), "set admin failed",
				"actor_user_id", caller.UserID, "target_email", req.Email, "is_admin", *req.IsAdmin, "error", err)
			writeError(w, http.StatusInternalServerError, errCodeInternal, "could not update admin status")
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
