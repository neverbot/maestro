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
// including that attacker's own stolen tab. That designer's other
// standing credentials are a separate concern this endpoint does not
// reach: an API token minted before the rotation keeps working
// afterwards (tokens have no expiry, only explicit revocation — Task 9),
// so rotating a password is not, by itself, a way to be sure a thief who
// minted one first is locked out. A designer who suspects their session
// was compromised should also check that game's token list
// (GET .../tokens) for anything they don't recognise.
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
//
// Rate limiting is two limiters, not one, and the order they run in
// matters — a Round 2 review found the original single-limiter design
// could deny the exact remedy this endpoint exists to provide. That
// design checked changePasswordLimiter.Allowed before ever verifying
// anything: once ten wrong guesses had spent the budget, even a request
// carrying the correct current password was refused with 429. Live, that
// is the threat model's own premise turned into a weapon — a session
// thief spends the budget with ten deliberately wrong guesses from the
// hijacked session (cheap: no argon2 derivation happens if the entry gate
// is what refuses the request) and holds the real owner's rotation
// endpoint shut for as long as they keep spending it, with no password
// reset anywhere in this product to fall back on. Fixed by verifying
// first: ChangeOwnPassword always runs, regardless of any budget, so a
// correct current password always succeeds. changePasswordLimiter (10
// wrong guesses per minute) is now consulted only once a guess has
// already turned out wrong, purely to decide whether *that* failure
// reports 401 or 429 — it can delay how fast an attacker learns their
// next guess failed, but it can never turn a caller who actually knows
// the password away.
//
// Verifying unconditionally reopens a narrower problem the entry gate
// used to close for free: nothing now bounds how many argon2
// derivations a flood of requests against one account can force,
// regardless of correctness. changePasswordFloodLimiter is the answer —
// a second, much looser per-account budget (60/minute) that *does* gate
// entry, before ChangeOwnPassword ever runs, the way the single limiter
// used to. Its ceiling is set high enough that no legitimate use — a
// designer mistyping their current password a few times, even a
// scripted retry — should ever reach it; its job is bounding worst-case
// CPU cost under a genuine flood, not shaping the experience of a real
// caller the way changePasswordLimiter's tighter 401-vs-429 choice does.
// It is recorded on every attempt, correct or not, since cost is what it
// bounds, not correctness.
//
// Both limiters live in process memory (identity.Limiter's own doc
// comment) — a multi-replica deployment has one independent budget per
// process, not one shared across the instance, the same caveat that
// applies to every other limiter this codebase has built (Task 6, Task
// 11).
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
