package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
)

type setAdminRequest struct {
	IsAdmin bool `json:"is_admin"`
}

// handleSetAdmin promotes or demotes an instance admin. Admin-only
// (requireAdminCaller, api_invites.go's own doc comment on why —
// instance-admin standing is the same "who gets to hold or grant
// standing in the product" authority that gates the account-only invite
// surface, and it is gated on that exact authority: only an existing
// admin may mint another one.
//
// Why this exists at all: BootstrapFirstAdmin (users.go) is the only
// other mechanism in this codebase that ever sets IsAdmin, and it only
// ever runs once, against an empty users table, at first boot. Before
// this endpoint, exactly one account could ever hold that authority for
// the life of an instance — losing it (the operator forgetting a
// password with no reset flow anywhere in this product, leaving the
// company, anything) meant the instance could never admit anyone new
// through POST /api/invites again. This closes that by letting an
// existing admin promote a colleague, deliberately not by any other
// mechanism (an invite cannot carry admin status — createInstanceInviteRequest
// has no such field, and createProjectInviteRequest's Role is a
// project role, not this instance-wide one).
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
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
		return
	}
	var req setAdminRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if err := s.opts.Identity.SetAdmin(r.Context(), targetID, req.IsAdmin); err != nil {
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			writeError(w, http.StatusNotFound, errCodeNotFound, "no such user")
		case errors.Is(err, identity.ErrLastAdmin):
			writeError(w, http.StatusConflict, errCodeLastAdmin, "the instance must keep at least one admin — promote someone else first")
		default:
			slog.ErrorContext(r.Context(), "set admin failed", "target_user_id", targetID, "error", err)
			writeError(w, http.StatusInternalServerError, errCodeInternal, "could not update admin status")
		}
		return
	}

	// No SSE event: every kind publish.go defines is keyed on a project
	// id (realtime.Hub is keyed by project, hub.go's own subs map), and
	// instance-admin status has no project to key against — the same
	// reason the account-only invite endpoints (api_invites.go,
	// handleCreateInstanceInvite/handleRevokeInstanceInvite) publish
	// nothing either. See publish.go's own package doc comment for the
	// full reasoning this decision reuses.
	writeJSON(w, http.StatusOK, map[string]any{"id": targetID, "is_admin": req.IsAdmin})
}
