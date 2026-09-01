package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/neverbot/maestro/internal/identity"
)

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword rotates the caller's own password.
// identity.ChangeOwnPassword (Task 21's own doc comment there) does the
// actual work — verify the current password, then rotate the hash and
// revoke every session belonging to the account in one transaction — so
// everything below is HTTP plumbing: authorization, rate limiting, error
// mapping, and re-issuing a fresh session for the request that made the
// change.
//
// Requiring the current password is the whole answer to "what does this
// endpoint cost an attacker who has a live session but not the
// password": without it, a stolen session cookie alone would be able to
// lock the real owner out permanently (ChangePassword revokes every
// session, including the one that requested it), which is strictly
// worse than what a stolen cookie already grants on its own. With it, an
// attacker who only ever had the cookie gains nothing here they did not
// already have — and a designer who suspects their password leaked can
// still use it to rotate their attacker straight out, everywhere,
// including that attacker's own stolen tab.
//
// Human-only (requireHumanCaller): a bearer token authenticates an
// agent scoped to one game's content, never a person with a password of
// their own to rotate — the same reasoning requireHumanCaller's own doc
// comment (api_projects.go) gives for every other account-level action
// in this codebase.
//
// Changing your password here logs you out everywhere *except* here: a
// designer who just rotated their password and is instantly thrown out
// of the very tab they did it in would read that as a bug, not a safety
// feature, so this handler re-issues a fresh session immediately after
// ChangeOwnPassword succeeds and sets it as the response's cookie —
// coherent with the endpoint's own stated purpose (kick out every
// *other* device, including a thief's) without punishing the one request
// that just proved it knows the new password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireHumanCaller(w, caller) {
		return
	}

	var req changePasswordRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	// Keyed on the caller's own user id, not an attacker-supplied value:
	// unlike handleLogin's email (an anonymous caller's own claim, not
	// yet verified), caller.UserID here already comes from a resolved,
	// server-side session lookup (authenticate, auth.go) — trustworthy
	// the same way handleCreateToken's scope.ProjectID is. That is what
	// makes a single per-user budget sufficient: there is no "attacker
	// picks their own key" concern to guard against with a second,
	// IP-keyed limiter the way handleLogin's loginIPLimiter answers for
	// an anonymous caller who could otherwise spread guesses across many
	// email keys, because every guess against this endpoint already
	// spends the one budget tied to the account being attacked,
	// regardless of which key an attacker might wish it used instead.
	key := caller.UserID.String()
	if !s.changePasswordLimiter.Allowed(key) {
		writeError(w, http.StatusTooManyRequests, errCodeRateLimited, "too many attempts, wait a minute")
		return
	}

	if err := s.opts.Identity.ChangeOwnPassword(r.Context(), caller.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, identity.ErrInvalidCredentials):
			// Only this branch records against the limiter: it is the
			// only outcome that represents a guess at the account's real
			// password. A weak new password (below) is a validation
			// failure, not a guess, and recording it would let an
			// attacker who cannot guess the current password burn a
			// legitimate user's budget for free by repeatedly submitting
			// a correct current password with a deliberately short new
			// one — spending a budget the check above is supposed to
			// protect, not be a vector for exhausting.
			s.changePasswordLimiter.Record(key)
			writeError(w, http.StatusUnauthorized, errCodeUnauthorized, "current password is wrong")
		case errors.Is(err, identity.ErrPasswordInvalid):
			writeError(w, http.StatusUnprocessableEntity, "password_invalid", "that password does not meet requirements")
		default:
			slog.ErrorContext(r.Context(), "change password failed", "user_id", caller.UserID, "error", err)
			writeError(w, http.StatusInternalServerError, errCodeInternal, "could not change the password")
		}
		return
	}

	token, expiresAt, err := s.opts.Identity.IssueSession(r.Context(), caller.UserID)
	if err != nil {
		// The password did change and every session, including this
		// one, is already gone — there is no way to undo that from here,
		// and no reason to try: the new password is exactly what the
		// caller asked for. All this failure means is that the request
		// that made the change cannot also carry a fresh cookie back;
		// the client has to log in again with the new password, same as
		// any other device.
		slog.ErrorContext(r.Context(), "issue session after password change failed", "user_id", caller.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "password changed, but could not start a new session — please log in again")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	w.WriteHeader(http.StatusNoContent)
}
