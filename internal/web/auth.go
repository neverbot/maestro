package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
)

// SessionCookie is the name of the browser session cookie.
const SessionCookie = "maestro_session"

// Caller is the authenticated principal of a request. A caller authenticated
// by an API token carries the single project that token is bound to; a
// caller authenticated by a session cookie carries none, and resolves the
// project from the URL and its own membership instead (see the handlers
// that use it — never from a caller-supplied parameter).
//
// Every field here is set exclusively from a server-side lookup keyed by a
// value the client cannot forge (the bearer token's hash, or the session
// cookie's hash): nothing in this package ever copies a client-supplied
// header or query parameter onto a Caller.
type Caller struct {
	UserID    uuid.UUID
	IsAdmin   bool
	TokenID   *uuid.UUID
	ProjectID *uuid.UUID
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
// access such as /healthz and /version) rather than aborting the request in
// a way a caller further down the chain does not expect.
//
// A request carrying an Authorization header takes the bearer path and
// stays there: it never falls back to a session cookie the same request
// might also carry, even if the bearer value turns out to be invalid. Two
// credentials of different kinds on one request is not a case this product
// needs to arbitrate finer than "the header wins, or nobody is
// authenticated" — falling back to the cookie on a bad header would make
// the effective identity of a request depend on which of two credentials
// happened to still be valid, which is a harder property to reason about
// for no real benefit.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			caller, ok, err := s.resolveBearerCaller(ctx, token)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "could not verify the token")
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
				writeError(w, http.StatusInternalServerError, "internal_error", "could not verify the session")
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
func (s *Server) resolveBearerCaller(ctx context.Context, token string) (Caller, bool, error) {
	summary, err := s.opts.Identity.ResolveAPIToken(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrTokenInvalid) {
			return Caller{}, false, nil
		}
		return Caller{}, false, err
	}

	user, err := s.opts.Identity.UserByID(ctx, summary.UserID)
	if err != nil {
		// A live token whose user cannot be loaded is not the ordinary
		// "invalid credential" case ResolveAPIToken already handles
		// itself: api_tokens.user_id is ON DELETE CASCADE, so a token
		// only outlives its user under a concurrent delete racing this
		// exact request. Either way this is a lookup failure, not an
		// absent or bad credential, and must not be swallowed into a
		// silent 401.
		return Caller{}, false, err
	}

	projectID := summary.ProjectID
	tokenID := summary.ID
	return Caller{
		UserID:    summary.UserID,
		IsAdmin:   user.IsAdmin,
		TokenID:   &tokenID,
		ProjectID: &projectID,
	}, true, nil
}

// resolveSessionCaller resolves a session cookie value to a Caller. Same
// (found, error) contract as resolveBearerCaller: an absent or expired
// session is (false, nil); a database error is (false, err).
//
// UserForSession also returns the session's own expiry (Task 6), which
// would let this method call identity.ExtendSession to slide the session
// forward on use. Deliberately not done here yet: today's sessions are
// absolute-lifetime, and renewing on literally every authenticated request
// would write to the sessions table on the hottest path in the product for
// no behavioural difference most requests would ever need — the same
// mistake Task 9 fixed for token last_used_at via touchThrottle. Wiring
// sliding renewal (with an equivalent threshold, so it writes only once
// per some interval instead of once per request) is left to whichever task
// first needs sessions to outlive SESSION_TTL under continued use.
func (s *Server) resolveSessionCaller(ctx context.Context, token string) (Caller, bool, error) {
	user, _, err := s.opts.Identity.UserForSession(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrNoSession) {
			return Caller{}, false, nil
		}
		return Caller{}, false, err
	}
	return Caller{UserID: user.ID, IsAdmin: user.IsAdmin}, true, nil
}

// requireCaller wraps a handler so anonymous requests get a 401.
func requireCaller(h func(http.ResponseWriter, *http.Request, Caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := CallerFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		h(w, r, caller)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
