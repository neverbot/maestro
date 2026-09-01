package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
)

// SessionCookie is the name of the browser session cookie.
const SessionCookie = "maestro_session"

// uuidValue aliases uuid.UUID so handler signatures stay readable.
type uuidValue = uuid.UUID

// Error codes returned in the "error" field of every JSON error body this
// package writes. Named here, not spelled inline at each call site, so a
// future handler cannot introduce a second string for the same condition
// (e.g. "not_authenticated" alongside "unauthorized") purely by typo. A
// quality review of Task 12 found api_projects.go and api_tokens.go had
// grown eleven inline string literals across their handlers instead of
// using this block, several of them ("forbidden", "not_found") repeated
// verbatim at multiple call sites with no shared constant catching a
// future typo between them; every one of those call sites now uses a
// name from here.
const (
	errCodeUnauthorized   = "unauthorized"
	errCodeInternal       = "internal_error"
	errCodeForbidden      = "forbidden"
	errCodeNotFound       = "not_found"
	errCodeBadRequest     = "bad_request"
	errCodeScopeViolation = "scope_violation"
	errCodeSlugTaken      = "slug_taken"
	errCodeSlugInvalid    = "slug_invalid"
	errCodeNameInvalid    = "name_invalid"
	errCodeInvalidRole    = "invalid_role"
	errCodeLastOwner      = "last_owner"
	errCodeLabelInvalid   = "label_invalid"
)

// Caller is the authenticated principal of a request. It has exactly two
// valid shapes, and this package constructs only those two, through
// newTokenCaller and newSessionCaller: a token caller (TokenID and
// ProjectID both set, carrying the single project that token is bound to)
// or a session caller (both nil, carrying no project — it resolves one
// from the URL and its own membership instead, never from a
// caller-supplied parameter). A zero Caller, or one with only one of
// TokenID/ProjectID set, is not a shape anything in this package ever
// produces; IsToken and ScopedProject below are the vocabulary downstream
// packages should use instead of reading the fields directly, so that
// invariant has exactly one place to hold instead of one per caller.
//
// The fields stay exported because Caller crosses package boundaries —
// every handler downstream of requireCaller receives one — but nothing
// outside auth.go should construct a Caller by struct literal; do so
// through the two constructors below.
//
// Every field is set exclusively from a server-side lookup keyed by a
// value the client cannot forge (the bearer token's hash, or the session
// cookie's hash): nothing in this package ever copies a client-supplied
// header or query parameter onto a Caller.
type Caller struct {
	UserID    uuid.UUID
	IsAdmin   bool
	TokenID   *uuid.UUID
	ProjectID *uuid.UUID
}

// newTokenCaller builds the token-authenticated shape of Caller: TokenID
// and ProjectID are both always set, from the same resolved token row, so
// the two can never disagree about whether this caller carries a project
// binding.
func newTokenCaller(userID uuid.UUID, isAdmin bool, tokenID, projectID uuid.UUID) Caller {
	return Caller{UserID: userID, IsAdmin: isAdmin, TokenID: &tokenID, ProjectID: &projectID}
}

// newSessionCaller builds the session-authenticated shape of Caller:
// TokenID and ProjectID are both always nil. A session caller resolves a
// project from the URL and its own membership on a project-scoped route,
// never from anything carried on the Caller itself.
func newSessionCaller(userID uuid.UUID, isAdmin bool) Caller {
	return Caller{UserID: userID, IsAdmin: isAdmin}
}

// IsToken reports whether this caller was authenticated by an API token
// (true) rather than a session cookie (false).
func (c Caller) IsToken() bool {
	return c.TokenID != nil
}

// ScopedProject returns the single project a token caller is bound to,
// and true. It returns the zero UUID and false for a session caller,
// which carries no project of its own.
//
// This is where "an admin is not exempt from a token's binding" is
// encoded once for every downstream consumer instead of once per
// call site: ScopedProject never consults IsAdmin, so a caller whose
// IsAdmin is true still gets exactly the token's own ProjectID back, not
// an unscoped pass. Task 12's authorization layer (requireProjectMember
// or equivalent — not built here; see this task's plan corrections) is
// expected to build on this, not on the raw fields.
func (c Caller) ScopedProject() (uuid.UUID, bool) {
	if c.ProjectID == nil {
		return uuid.UUID{}, false
	}
	return *c.ProjectID, true
}

type callerKey struct{}

// CallerFrom returns the authenticated caller of a request, if any.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// authenticate resolves a bearer token or a session cookie into a Caller and
// puts it in the request context. It never fails the request itself:
// handlers decide whether anonymous access is acceptable, through
// requireCaller. It always calls next, even on a database error, so that a
// transient authentication-layer failure surfaces as a 401/500 from the
// handler it can't reach (or not at all, for a route that permits anonymous
// access such as /healthz) rather than aborting the request in a way a
// caller further down the chain does not expect.
//
// A request carrying an Authorization header with the Bearer scheme takes
// the bearer path and stays there: it never falls back to a session
// cookie the same request might also carry, even if the bearer value
// turns out to be invalid. A header of any other scheme, or none at all,
// falls through to the cookie check below it. Two credentials of
// different kinds on one request is not a case this product needs to
// arbitrate finer than "the header wins when it names Bearer, or nobody
// is authenticated" — falling back to the cookie on a bad bearer value
// would make the effective identity of a request depend on which of two
// credentials happened to still be valid, which is a harder property to
// reason about for no real benefit.
//
// This resolution happens once, at the start of a request. For a
// short-lived request that is the whole story, but a handler that keeps
// the connection open past that point — the SSE stream Task 14 adds — does
// not get re-evaluated for the rest of its lifetime: it does not notice a
// logout, a password change, a token revocation or an expulsion that
// happens after the connection was accepted, and its session, if it has
// one, never slides forward through resolveSessionCaller again. That is
// not a defect in this middleware; it is a constraint every long-lived
// handler downstream of it has to design around (a bounded connection
// lifetime, a periodic re-check, or accepting the exposure), not something
// this method can fix by trying harder per-request.
//
// A request under a public path prefix (none exist yet; Task 15's static
// asset tree and Task 14's SSE handshake are the first candidates) should
// be short-circuited before it reaches the cookie lookup below, not
// merely left unauthenticated by requireCaller: every static asset on a
// page otherwise costs one identical, wasted session lookup per request.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			caller, ok, err := s.resolveBearerCaller(ctx, token)
			if err != nil {
				writeError(w, http.StatusInternalServerError, errCodeInternal, "could not verify the token")
				return
			}
			if ok {
				ctx = context.WithValue(ctx, callerKey{}, caller)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if cookie, err := r.Cookie(SessionCookie); err == nil {
			caller, ok, err := s.resolveSessionCaller(ctx, cookie.Value)
			if err != nil {
				writeError(w, http.StatusInternalServerError, errCodeInternal, "could not verify the session")
				return
			}
			if ok {
				ctx = context.WithValue(ctx, callerKey{}, caller)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveBearerCaller resolves a bearer token to a Caller. The bool result
// is false, with a nil error, for an absent or invalid token — the ordinary
// "not authenticated" outcome; a non-nil error means the lookup itself
// failed (a database error), which the caller must not silently treat as
// "not authenticated" the way an invalid credential is.
//
// The project binding on the returned Caller comes from ResolveAPIToken's
// own row, which is trustworthy as an authorization scope only because
// projects.RemoveMember revokes a departing member's tokens for that
// project in the same transaction as the membership removal (see
// ResolveAPIToken's doc comment in internal/identity/tokens.go). This
// method does not, and must not, re-derive membership itself: doing so on
// every request would be the exact hot-path cost Task 9 already removed
// from the token layer, moved here instead of eliminated.
//
// This is a single lookup, not two: ResolveAPIToken's own query joins
// users for IsAdmin (APITokenSummary.UserIsAdmin — see that field's doc
// comment in tokens.go), so this method no longer calls identity.UserByID
// separately the way an earlier version of this file did.
func (s *Server) resolveBearerCaller(ctx context.Context, token string) (Caller, bool, error) {
	summary, err := s.opts.Identity.ResolveAPIToken(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrTokenInvalid) {
			return Caller{}, false, nil
		}
		return Caller{}, false, err
	}
	return newTokenCaller(summary.UserID, summary.UserIsAdmin, summary.ID, summary.ProjectID), true, nil
}

// resolveSessionCaller resolves a session cookie value to a Caller. Same
// (found, error) contract as resolveBearerCaller: an absent or expired
// session is (false, nil); a database error is (false, err).
//
// This is also where sliding sessions are implemented: UserForSession
// returns the session's own expiry (Task 6) alongside the user precisely
// so this method could act on it. A session more than halfway through its
// SESSION_TTL lifetime is a candidate for renewal (capped at
// identity.maxSessionLifetime from the session's creation), so a caller
// in continued use never gets logged out mid-session; one that goes
// quiet simply expires on schedule, and one in continuous use for months
// eventually stops being renewed and expires at the cap. The halfway
// check here decides only whether to *attempt* a renewal, keeping that
// decision off the per-request path for a session nowhere near expiry —
// it does not, by itself, guarantee only one write when several requests
// cross halfway together; ExtendSession's own WHERE clause is what
// actually prevents that pile-up (see its doc comment in sessions.go for
// why the earlier version of this comment overclaimed that and was
// wrong).
//
// This lives in the authentication middleware, not in a REST handler
// (Task 11's login/logout/registration endpoints), because resolving a
// session cookie into a Caller is the one thing every session-authenticated
// request does, regardless of which route it is headed to — a login
// handler runs once per session and never sees the requests that follow
// it, so it cannot be where "still active, worth renewing" gets decided.
func (s *Server) resolveSessionCaller(ctx context.Context, token string) (Caller, bool, error) {
	user, expiresAt, err := s.opts.Identity.UserForSession(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrNoSession) {
			return Caller{}, false, nil
		}
		return Caller{}, false, err
	}

	if ttl := s.opts.Config.SessionTTL; ttl > 0 && time.Until(expiresAt) < ttl/2 {
		if _, err := s.opts.Identity.ExtendSession(ctx, token, ttl); err != nil {
			// Sliding renewal is a convenience, not part of the
			// authentication decision: a failed extend must not turn an
			// otherwise-valid, already-authenticated session into a hard
			// failure. The session simply keeps its existing expiry and
			// gets another chance to renew on its next request.
			slog.ErrorContext(ctx, "extend session failed; continuing with the resolved session",
				"user_id", user.ID, "error", err)
		}
	}

	return newSessionCaller(user.ID, user.IsAdmin), true, nil
}

// setNoStoreHeaders sets the two response headers every response whose
// content depends on who is asking needs, regardless of which handler
// produces it: Cache-Control forbids a shared cache or a browser
// back-button restore from replaying a response computed for one
// identity to another, and Vary tells any cache in front of this server
// that the response depends on exactly the two headers authenticate
// reads, not just the URL. requireCaller sets these for every handler it
// wraps; handleRoot (server.go) is the one response in this package that
// bypasses requireCaller entirely — it has to treat "no caller" as a
// redirect to /login rather than a 401 — and calls this directly so it
// does not ship as the most identity-dependent response in the product
// with neither header.
func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Cookie, Authorization")
}

// requireCaller wraps a handler so anonymous requests get a 401, and adds
// the response headers every authenticated response needs (see
// setNoStoreHeaders).
func requireCaller(h func(http.ResponseWriter, *http.Request, Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := CallerFrom(r.Context())
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="maestro"`)
			writeError(w, http.StatusUnauthorized, errCodeUnauthorized, "authentication required")
			return
		}
		setNoStoreHeaders(w)
		h(w, r, caller)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
