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
// identity.ChangeOwnPassword does the work — verify the current
// password, rotate the hash and revoke every session of the account in
// one transaction — so everything here is HTTP plumbing: authorization,
// rate limiting, error mapping, and a fresh session for the request that
// made the change.
//
// The current password is required: without it a stolen session cookie
// alone could lock the owner out permanently, since the rotation revokes
// every session. With it, a thief who only has the cookie gains nothing,
// and the owner can rotate the thief out of every tab including their
// own. API tokens are not revoked by a rotation and have no expiry, so a
// compromised account should also have its token list checked.
//
// Human-only: a bearer token authenticates an agent scoped to one game's
// content, never a person with a password to rotate.
//
// The request that rotates keeps its session: a fresh one is issued and
// set as the response cookie, so the endpoint logs you out everywhere
// except here.
//
// Two limiters, and the order matters. ChangeOwnPassword always runs, so
// a correct current password always succeeds: changePasswordLimiter (10
// wrong guesses a minute) is consulted only once a guess is already
// wrong, to decide whether that failure reports 401 or 429. Gating entry
// on it instead would let a session thief spend the budget on deliberate
// wrong guesses and hold the owner's rotation endpoint shut — and this
// product has no password reset to fall back on.
//
// Verifying unconditionally leaves nothing bounding argon2 derivations
// under a flood, which is what changePasswordFloodLimiter is for: a
// looser per-account budget (60/minute) that does gate entry, set high
// enough that no legitimate caller reaches it, and recorded on every
// attempt because cost is what it bounds.
//
// Both budgets live in process memory: one per replica, not one per
// instance.
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
	// makes a per-account budget sufficient on its own: there is no
	// "attacker picks their own key" concern to guard against with a
	// second, IP-keyed limiter the way handleLogin's loginIPLimiter
	// answers for an anonymous caller who could otherwise spread guesses
	// across many email keys, because every guess against this endpoint
	// already spends the budget tied to the account being attacked,
	// regardless of which key an attacker might wish it used instead.
	key := caller.UserID.String()

	// changePasswordFloodLimiter, not changePasswordLimiter, gates entry
	// — see this function's own doc comment for why the two limiters
	// exist and why only the looser one may ever refuse a request before
	// its password is checked.
	if !s.changePasswordFloodLimiter.Allowed(key) {
		writeError(w, http.StatusTooManyRequests, errCodeRateLimited, "too many attempts, wait a minute")
		return
	}
	s.changePasswordFloodLimiter.Record(key)

	if err := s.opts.Identity.ChangeOwnPassword(r.Context(), caller.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, identity.ErrInvalidCredentials):
			// The correctness-gated limiter: consulted only now, on a
			// guess that has already turned out wrong, to decide whether
			// this particular failure reports 401 (budget remains) or
			// 429 (exhausted) — never to refuse a guess before it has
			// been checked. A weak new password (below) is a validation
			// failure, not a guess, and recording it here would let an
			// attacker who cannot guess the current password burn a
			// legitimate user's budget for free by repeatedly submitting
			// a correct current password with a deliberately short new
			// one.
			if !s.changePasswordLimiter.Allowed(key) {
				writeError(w, http.StatusTooManyRequests, errCodeRateLimited, "too many attempts, wait a minute")
				return
			}
			s.changePasswordLimiter.Record(key)
			writeError(w, http.StatusUnauthorized, errCodeUnauthorized, "current password is wrong")
		case errors.Is(err, identity.ErrPasswordUnchanged):
			writeError(w, http.StatusUnprocessableEntity, errCodePasswordUnchanged, "the new password must differ from the current one")
		case errors.Is(err, identity.ErrPasswordInvalid):
			writeError(w, http.StatusUnprocessableEntity, errCodePasswordInvalid, "that password does not meet requirements")
		default:
			// The change itself has not happened yet on this path, so
			// contention here is answerable with "send the same request
			// again" — the caller still holds the current password the
			// resend needs. The IssueSession failure *below* is the
			// opposite case and keeps its unconditional 500.
			writeUnmappedError(w, r, err, "change password failed", "could not change the password",
				"user_id", caller.UserID)
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
		// **Deliberately not writeUnmappedError**, for the reason the
		// comment above gives and startSessionFor (api_auth.go) gives at
		// length: the password has already changed, so "send the same
		// request again" is advice that cannot work — the resend would
		// be refused 401, the current password it names being the old
		// one. The message already says the only thing that helps.
		slog.ErrorContext(r.Context(), "issue session after password change failed", "user_id", caller.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "password changed, but could not start a new session — please log in again")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	w.WriteHeader(http.StatusNoContent)
}
