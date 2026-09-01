package web

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
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

// decodeJSONBody enforces the two boundary checks every handler that
// accepts a JSON body needs before it looks at the body at all: a
// declared application/json content type (a browser form post, or a
// client that forgot the header, gets a clear 415 instead of a JSON
// decode error that reads like a malformed body), and the
// maxAuthRequestBodyBytes bound (Task 10's note — see the constant's own
// doc comment, and identity.HashPassword's, for why service validation
// alone is not enough: it only rejects an oversized value after decoding
// it into memory). A body that merely exceeds the bound is reported as
// 413, not the generic 400 an actually-malformed body gets, since
// http.MaxBytesReader's own error (*http.MaxBytesError) lets the two be
// told apart. Despite the name this file's own quality review gave it,
// this is not auth-specific: every JSON-accepting handler in this
// package uses it, api_projects.go and api_tokens.go included, so a
// handler that reads r.Body directly is the exception that needs
// justifying, not the rule.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	return true
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Normalized the same way Authenticate normalizes its own lookup
	// (lower-case, trim), so "Bob@x.com", "bob@x.com" and " bob@x.com "
	// share one rate-limit budget instead of three (Task 6, Correction 9).
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		// Reject before either limiter is ever touched: an empty key
		// would otherwise give every anonymous, credential-less probe a
		// single shared "" bucket to spend against, for no benefit — the
		// request is malformed regardless of what the limiter says.
		writeError(w, http.StatusBadRequest, "bad_request", "email is required")
		return
	}
	ip := s.clientIP(r)
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
		slog.ErrorContext(r.Context(), "issue session failed", "user_id", user.ID, "error", err)
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

// handleLogout revokes the caller's session server-side and clears the
// cookie. Revocation is checked, not fire-and-forgotten: a session that
// fails to revoke because of a transient database failure must not be
// reported as a successful logout — the cookie stays valid server-side
// either way, so telling the client "you are logged out" while leaving
// the credential live (and clearing the client's own copy, which only
// makes retrying harder) would be actively misleading. The client sees
// 500 and can retry; the cookie is only cleared once revocation actually
// happened, or there was nothing to revoke in the first place.
//
// Revocation is only ever checked at request admission, here and in
// authenticate (internal/web/auth.go) — it does not, and structurally
// cannot, tear down a connection that is already open. A long-lived
// handler that keeps a session's connection alive past this check (the
// SSE stream Task 14 adds) will not notice a logout that happens after it
// accepted the connection; that handler has to design around it on its
// own terms (see authenticate's own doc comment on this same limitation).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		if err := s.opts.Identity.RevokeSession(r.Context(), cookie.Value); err != nil {
			slog.ErrorContext(r.Context(), "revoke session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "could not log out")
			return
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	// Every path through this handler ends by calling either
	// identity.RedeemInvite or identity.CreateUser, and both insert a new
	// account and pay a full argon2 derivation to do it. That makes
	// *every* branch below — invite redemption and open self-service
	// registration alike — an account-flood and CPU-exhaustion vector at
	// unlimited rate without a limiter, and (since this product sends no
	// verification email) an unauthenticated, unlimited-rate enumeration
	// oracle on top: the response distinguishes an out-of-domain address
	// from a malformed one from a taken one from success, on two branches
	// neither of which requires a credential to reach.
	//
	// One IP-keyed budget, checked once here rather than duplicated per
	// branch, covers both. It stays IP-only rather than pairing with a
	// second key the way handleLogin's email+IP pair does: unlike login,
	// req.Email here never names an *existing*, attacker-targetable
	// account whose budget could be spent out from under its owner — the
	// self-service branch is creating a brand new account, and the invite
	// branch's real credential is the token, not the email (see the
	// invite-branch comment below, and Task 6 Correction 10, for why the
	// token itself is never the limiter key).
	ip := s.clientIP(r)
	if !s.registerIPLimiter.Allowed(ip) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, wait a minute")
		return
	}

	// An invite token wins over the instance mode: that is the point of a link.
	if req.InviteToken != "" {
		user, err := s.opts.Identity.RedeemInvite(r.Context(), req.InviteToken, identity.CreateUserRequest{
			Email:       req.Email,
			DisplayName: req.DisplayName,
			Password:    req.Password,
		})
		if err != nil {
			s.registerIPLimiter.Record(ip)
			s.writeRegistrationError(w, r, err)
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
		s.registerIPLimiter.Record(ip)
		s.writeRegistrationError(w, r, err)
		return
	}
	s.startSessionFor(w, r, user.ID)
}

func (s *Server) writeRegistrationError(w http.ResponseWriter, r *http.Request, err error) {
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
		// A wrapped, non-sentinel error — almost certainly a genuine
		// database failure (CreateUser's or RedeemInvite's own
		// fmt.Errorf("create user: %w", err) and similar). The client
		// gets a fixed, generic message; the operator gets the actual
		// error server-side, since otherwise nothing anywhere records
		// that this happened at all.
		slog.ErrorContext(r.Context(), "registration failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete registration")
	}
}

func (s *Server) startSessionFor(w http.ResponseWriter, r *http.Request, userID uuidValue) {
	token, expiresAt, err := s.opts.Identity.IssueSession(r.Context(), userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "issue session failed", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start a session")
		return
	}
	s.setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": userID})
}

// sessionCookieTemplate builds the *http.Cookie shared by every place this
// package sets or clears the session cookie, so Path, HttpOnly, Secure and
// SameSite cannot drift between the "set" and "clear" call sites the way
// two independently maintained cookie literals eventually would.
func (s *Server) sessionCookieTemplate(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Path:     "/",
		HttpOnly: true,
		// Trusting X-Forwarded-Proto unconditionally would tell this
		// instance it is on HTTPS whenever any caller claims so, which is
		// exactly the spoofing clientIP below refuses for
		// X-Forwarded-For. Both headers are only ever consulted once
		// Config.TrustedProxyCount says a reverse proxy is actually there
		// to have set them honestly — see s.behindTrustedProxy.
		Secure:   r.TLS != nil || (s.behindTrustedProxy() && r.Header.Get("X-Forwarded-Proto") == "https"),
		SameSite: http.SameSiteLaxMode,
	}
}

// setSessionCookie takes expiresAt from IssueSession rather than
// recomputing time.Now().Add(...) itself: identity.SessionTTL no longer
// exists as an exported constant (it moved into config.Config, Task 6
// Correction 11), and a second computation of the same policy here could
// silently drift from whatever was actually written to the database.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	c := s.sessionCookieTemplate(r)
	c.Value = token
	c.Expires = expiresAt
	http.SetCookie(w, c)
}

// clearSessionCookie expires the session cookie in the browser. It shares
// sessionCookieTemplate with setSessionCookie so the two can never drift
// on Path, HttpOnly, Secure or SameSite — only a plain literal MaxAge: -1
// here differs.
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	c := s.sessionCookieTemplate(r)
	c.Value = ""
	c.MaxAge = -1
	http.SetCookie(w, c)
}

// behindTrustedProxy reports whether this instance is configured to trust
// a reverse proxy in front of it (Config.TrustedProxyCount > 0). It is the
// single gate both clientIP and sessionCookieTemplate check before reading
// any header a direct caller could otherwise forge — X-Forwarded-For and
// X-Forwarded-Proto respectively — so the two headers are trusted, or not,
// on the same operator decision rather than two independently-drifting
// ones.
func (s *Server) behindTrustedProxy() bool {
	return s.opts.Config.TrustedProxyCount > 0
}

// clientIP extracts the request's source IP for rate-limiting purposes.
//
// With Config.TrustedProxyCount at its default of zero (a directly exposed
// instance), this reads only r.RemoteAddr and never consults
// X-Forwarded-For: that header is attacker-controlled on a direct
// connection, and trusting it here would let a caller pick their own
// rate-limit key, reopening exactly the "attacker picks their own key"
// problem Correction 10 fixed for invite redemption.
//
// Maestro is, however, commonly deployed behind a reverse proxy — and
// there, RemoteAddr is the proxy's own address on every single request.
// Leaving TrustedProxyCount at zero in that deployment does not merely
// fail to identify individual clients: it collapses every caller on the
// instance into one shared rate-limit bucket keyed on the proxy's
// address, so ten requests from anyone exhausts the invite, register or
// login-IP budget for everyone else until the window rolls — a denial of
// onboarding (and of login) that is worse than having no limiter at all.
// An operator running behind N trusted reverse proxies must set
// TRUSTED_PROXY_COUNT=N for this method to see through them to the real
// client; the zero-value default is safe only for an instance reachable
// directly, and is not a "conservative" choice that happens to also work
// behind a proxy.
//
// When TrustedProxyCount is positive, this trusts exactly that many
// rightmost entries of X-Forwarded-For as having been appended, in order,
// by that many trusted hops (never removed or reordered), and reads the
// entry immediately to their left as the real client — the standard
// "N trusted hops" interpretation, matching how each hop is expected to
// append the address it directly observed. If the header carries fewer
// entries than TrustedProxyCount — a misconfiguration, or a hop that
// failed to set it — there is no entry that can be trusted as the real
// client, so this falls back to RemoteAddr (the nearest trusted hop's own
// address) rather than guessing.
func (s *Server) clientIP(r *http.Request) string {
	if n := s.opts.Config.TrustedProxyCount; n > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			if n <= len(parts) {
				return parts[len(parts)-n]
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
