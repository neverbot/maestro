package web

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
)

// maxAuthRequestBodyBytes bounds the request body accepted by every
// handler in this file. The hashing layer (identity.HashPassword)
// deliberately does not cap password length on its own — that bound
// belongs at the HTTP boundary, per Task 10's note — and the identity
// service's own validation (prepareUser) only rejects an oversized
// password *after* decoding it into memory. 16KiB is generous for the
// largest legitimate body here (an email, a display name and a password,
// each individually bounded well under 2KiB by identity's own checks)
// while still being a small, fixed cost regardless of what a client
// sends.
const maxAuthRequestBodyBytes = 16 * 1024

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	InviteToken string `json:"invite_token"`
}

func decodeAuthBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed or oversized JSON body")
		return false
	}
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeAuthBody(w, r, &req) {
		return
	}
	// Normalized the same way Authenticate normalizes its own lookup
	// (lower-case, trim), so "Bob@x.com", "bob@x.com" and " bob@x.com "
	// share one rate-limit budget instead of three (Task 6, Correction 9).
	email := strings.ToLower(strings.TrimSpace(req.Email))
	ip := clientIP(r)
	// Two independent budgets, both consulted before any password check
	// runs: loginLimiter (per normalized email) stops an attacker who
	// knows one address from being throttled off by traffic from other
	// accounts, and loginIPLimiter (per source IP, looser) stops that
	// same attacker from grinding through many different accounts' own
	// budgets one at a time from a single origin. See the doc comment on
	// Server.loginLimiter/loginIPLimiter in server.go for why these stay
	// separate rather than a single composite "email+ip" key. Both are
	// checked here, before Authenticate is ever called, so a caller whose
	// budget is exhausted gets 429 unconditionally — never a 200 for a
	// correct password that slipped in under the cap, which would
	// reopen exactly the enumeration oracle the sentinel hash in
	// Authenticate exists to close (a status code, or its timing, would
	// leak whether the password was right even though the request was
	// rate-limited).
	if !s.loginLimiter.Allowed(email) || !s.loginIPLimiter.Allowed(ip) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, wait a minute")
		return
	}

	user, err := s.opts.Identity.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		s.loginLimiter.Record(email)
		s.loginIPLimiter.Record(ip)
		writeError(w, http.StatusUnauthorized, "unauthorized", "email or password is wrong")
		return
	}
	// No Reset call here: Allowed never charges the budget, so a
	// successful login never spent anything that needs refunding (Task 6,
	// Correction 8). The old Allow/Reset shape had an early return between
	// the charge and the refund — exactly the one below, if IssueSession
	// fails — that would have silently spent a legitimate user's budget.

	token, expiresAt, err := s.opts.Identity.IssueSession(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":      user.ID,
		"display_name": user.DisplayName,
		"is_admin":     user.IsAdmin,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		_ = s.opts.Identity.RevokeSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeAuthBody(w, r, &req) {
		return
	}

	// An invite token wins over the instance mode: that is the point of a link.
	if req.InviteToken != "" {
		// Keyed on the client's source IP, not req.InviteToken: the token
		// is the very secret being guessed, so keying the limiter on it
		// would hand every guess a fresh, unlimited budget — the limiter
		// would cap nothing (Task 6, Correction 10).
		//
		// This stays IP-only, unlike handleLogin's paired email+IP
		// limiters above: handleLogin needs a second key precisely
		// because req.Email there names an *existing* account that an
		// attacker who merely knows the address can otherwise lock out
		// for free. Here, req.Email (when an invite carries one) is not
		// itself the credential being protected — RedeemInvite already
		// requires the actual invite token to reach this far, and an
		// invite bound to an email an attacker doesn't hold is simply
		// rejected by RedeemInvite's own comparison, at no cost to the
		// invitee, since nothing about that comparison consumes any part
		// of their own budget. There is no account, and no per-address
		// budget, for an attacker to exhaust here — only the shared
		// token-guessing budget IP keying already caps.
		ip := clientIP(r)
		if !s.inviteLimiter.Allowed(ip) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, wait a minute")
			return
		}
		user, err := s.opts.Identity.RedeemInvite(r.Context(), req.InviteToken, identity.CreateUserRequest{
			Email:       req.Email,
			DisplayName: req.DisplayName,
			Password:    req.Password,
		})
		if err != nil {
			s.inviteLimiter.Record(ip)
			s.writeRegistrationError(w, err)
			return
		}
		s.startSessionFor(w, r, user.ID)
		return
	}

	if s.opts.Config.RegistrationMode != config.RegistrationDomainOpen {
		writeError(w, http.StatusForbidden, "invite_required", "this instance only admits invited users")
		return
	}
	user, err := s.opts.Identity.CreateUser(r.Context(), identity.CreateUserRequest{
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
	})
	if err != nil {
		s.writeRegistrationError(w, err)
		return
	}
	s.startSessionFor(w, r, user.ID)
}

func (s *Server) writeRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInviteExpired):
		// Split out from ErrInviteInvalid on purpose (identity, Task 7):
		// reaching this branch already requires holding the actual clear
		// token, so telling the holder their link expired — rather than
		// the generic "not valid" — leaks nothing an attacker without the
		// token could use, and saves a designer clicking a stale Slack
		// link from filing a support request over what is actually a
		// routine expiry.
		writeError(w, http.StatusForbidden, "invite_expired", "this invite has expired")
	case errors.Is(err, identity.ErrInviteInvalid):
		writeError(w, http.StatusForbidden, "invite_invalid", "this invite is not usable")
	case errors.Is(err, identity.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", "that email already has an account")
	case errors.Is(err, identity.ErrEmailNotAllowed):
		writeError(w, http.StatusForbidden, "email_not_allowed", "that email domain cannot register here")
	case errors.Is(err, identity.ErrEmailInvalid):
		writeError(w, http.StatusUnprocessableEntity, "email_invalid", "that email is not a valid address")
	case errors.Is(err, identity.ErrDisplayNameInvalid):
		writeError(w, http.StatusUnprocessableEntity, "display_name_invalid", "that display name is not valid")
	case errors.Is(err, identity.ErrPasswordInvalid):
		writeError(w, http.StatusUnprocessableEntity, "password_invalid", "that password does not meet requirements")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete registration")
	}
}

func (s *Server) startSessionFor(w http.ResponseWriter, r *http.Request, userID uuidValue) {
	token, expiresAt, err := s.opts.Identity.IssueSession(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": userID})
}

// setSessionCookie takes expiresAt from IssueSession rather than
// recomputing time.Now().Add(...) itself: identity.SessionTTL no longer
// exists as an exported constant (it moved into config.Config, Task 6
// Correction 11), and a second computation of the same policy here could
// silently drift from whatever was actually written to the database.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// clientIP extracts the request's source IP for rate-limiting purposes. It
// deliberately does not consult X-Forwarded-For or similar headers: those
// are attacker-controlled unless a trusted reverse proxy is known to set
// them exactly once, which this instance's deployment shape does not yet
// guarantee, and trusting a spoofable header here would reopen exactly the
// "attacker picks their own key" problem Correction 10 fixed.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
