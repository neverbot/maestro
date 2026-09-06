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
	"github.com/neverbot/maestro/internal/metamodel"
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
//
// Task 22's own review found that claim had quietly stopped being true:
// api_auth.go alone had grown fourteen inline literals of its own (this
// file's login, logout and registration handlers), never migrated when
// this block was written for Task 12's files; api_password.go mixed
// constants and literals inside one switch; and four of api_auth.go's
// literals ("unauthorized", "internal_error", "bad_request",
// "rate_limited") duplicated a constant that already existed here,
// unused, the whole time. Every call site in this package now uses a
// name from this block — see api_auth.go and api_password.go's diffs for
// that migration — so this comment's claim is accurate again, not merely
// aspirational.
const (
	// vocab:error_codes begin — the codes a game-content tool can return.
	//
	// This region is a delimited vocabulary, not a comment: the bundle's
	// reference pages enumerate exactly these codes inside a
	// ```vocab:error_codes``` fence, and TestBundleErrorCodesMatchTheSurface
	// set-compares the two in both directions. A code added here without a
	// recovery on that page fails the build, and so does a code on that
	// page that no tool can produce.
	//
	// Membership is not a judgement call and is not maintained by hand:
	// TestTheErrorCodeRegionIsExactlyWhatAnMCPToolCanReturn parses this
	// package for every constant handed to mcpErrorResult and requires
	// that set to equal this region, in both directions. errCodeForbidden
	// sits deliberately *below* the end marker — it is written by the HTTP
	// admin handlers and no MCP tool can produce it, so a bundle page
	// teaching an agent to recover from it would be teaching a recovery
	// from an error that never arrives.
	errCodeUnauthorized   = "unauthorized"
	errCodeInternal       = "internal_error"
	errCodeNotFound       = "not_found"
	errCodeBadRequest     = "bad_request"
	errCodeScopeViolation = "scope_violation"
	// errCodeRetryable is the one code in this vocabulary whose recovery
	// is to change nothing and send the same call again: the database
	// refused a statement over contention (a lock timeout, a deadlock, a
	// serialization failure, a cancelled statement) rather than over
	// anything the caller sent. metamodel.IsRetryable is the classifier
	// and its doc comment argues the decision; mcpErrorFor is what puts
	// it on the wire. Before it existed all four landed on
	// internal_error, which tells an agent to give up on a call that
	// would have worked.
	errCodeRetryable = "retryable"

	// The metamodel's own vocabulary, mirrored here so the MCP surface
	// spells a code in exactly one place. The names match
	// internal/metamodel/errors.go's sentinels one for one, deliberately:
	// a change to either is a change to the public contract and should
	// not be possible to make on one side only without the other reading
	// as obviously stale.
	errCodeVersionConflict      = "version_conflict"
	errCodeSchemaViolation      = "schema_violation"
	errCodeInvalidSchema        = "invalid_schema"
	errCodeInvalidInput         = "invalid_input"
	errCodeEndpointTypeMismatch = "endpoint_type_mismatch"
	errCodeInUse                = "in_use"

	// The views domain's four. internal/views/errors.go argues each of
	// them as a *different recovery* and argues why there is no fifth for
	// a timeout: 57014 already maps to errCodeRetryable, whose meaning —
	// resend — is the right first advice, and a code naming the same
	// recovery as an existing code is how a vocabulary rots.
	errCodeQueryInvalid         = "query_invalid"
	errCodeRendererRequirements = "renderer_requirements"
	errCodeLimitExceeded        = "limit_exceeded"
	errCodeQueryStale           = "query_stale"
	// vocab:error_codes end

	// Below the region: codes only the HTTP surface writes. An agent
	// never sees one of these, so the bundle never names one.
	errCodeForbidden = "forbidden"

	errCodeSlugTaken            = "slug_taken"
	errCodeSlugInvalid          = "slug_invalid"
	errCodeNameInvalid          = "name_invalid"
	errCodeInvalidRole          = "invalid_role"
	errCodeLastOwner            = "last_owner"
	errCodeLabelInvalid         = "label_invalid"
	errCodeLastAdmin            = "last_admin"
	errCodeRateLimited          = "rate_limited"
	errCodeUnsupportedMediaType = "unsupported_media_type"
	errCodeRequestTooLarge      = "request_too_large"

	// errCodeInviteRequestInvalid maps identity.ErrInviteRequestInvalid —
	// a malformed invite *creation* request (api_invites.go,
	// writeCreateInviteError). Its value predates this constant's own
	// current name: it was originally named errCodeInviteInvalid, which
	// a Task 22 review flagged as actively misleading — the constant
	// named for "invite invalid" was not the one that actually fired for
	// identity.ErrInviteInvalid at redemption (writeRegistrationError
	// used the literal "invite_invalid" instead, unconstant, the whole
	// time this one existed). Renamed so its name matches the condition
	// it maps, and errCodeInviteInvalid below now names the condition
	// its own name promises.
	errCodeInviteRequestInvalid = "invite_request_invalid"
	// errCodeInviteInvalid maps identity.ErrInviteInvalid — an invite
	// token that is unknown, already redeemed, or otherwise unusable at
	// redemption time (writeRegistrationError, api_auth.go). Distinct
	// from errCodeInviteRequestInvalid above: that one fires when an
	// admin's own request to create an invite was malformed, before any
	// token exists to redeem.
	errCodeInviteInvalid      = "invite_invalid"
	errCodeInviteRequired     = "invite_required"
	errCodeInviteExpired      = "invite_expired"
	errCodeEmailTaken         = "email_taken"
	errCodeEmailNotAllowed    = "email_not_allowed"
	errCodeEmailInvalid       = "email_invalid"
	errCodeDisplayNameInvalid = "display_name_invalid"
	errCodePasswordInvalid    = "password_invalid"
	errCodePasswordUnchanged  = "password_unchanged"
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
// A request under a public path prefix — isPublicPath below, which now
// covers /login, /api/config, /static/ and /g/ — is short-circuited
// before it reaches the cookie lookup below, not merely left
// unauthenticated by requireCaller: every static asset on a page
// otherwise costs one identical, wasted session lookup per request.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()

		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			caller, ok, err := s.resolveBearerCaller(ctx, token)
			if err != nil {
				// Admission is swept too, not only the handlers behind
				// it: this lookup runs on every authenticated request, so
				// a contended one would otherwise report every agent's
				// call during a lock storm as a server fault before any
				// handler that classifies properly ever ran.
				writeUnmappedError(w, r, err, "resolve bearer caller failed", "could not verify the token")
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
				writeUnmappedError(w, r, err, "resolve session caller failed", "could not verify the session")
				return
			}
			if ok {
				ctx = context.WithValue(ctx, callerKey{}, caller)
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isPublicPath reports whether path never needs a Caller at all, so
// authenticate can skip its bearer/cookie resolution entirely rather than
// merely leaving the result unused by a handler not wrapped in
// requireCaller. Task 15's own note (its plan entry, and this package's
// doc comment on authenticate above) is explicit about why this matters
// for more than tidiness: a browser sends its session cookie on every
// same-origin request regardless of whether the handler ever reads
// CallerFrom, so a page pulling twenty embedded assets from GET
// /static/... would otherwise cost twenty identical, wasted session
// lookups — a real database round trip apiece. GET /login and GET
// /g/{slug} (server.go) are included for the same reason: neither
// resolves a Caller either (login.html and game.html decide everything
// from the API responses their own script fetches after the page
// loads), so there is nothing for a lookup here to buy either of them.
//
// GET /api/config (api_config.go) is included because it is
// unauthenticated by design — it exists precisely so a visitor who has
// not signed in yet can learn this instance's registration mode before
// login is even possible — so a session lookup ahead of it would only
// ever resolve a Caller this handler never reads.
//
// GET /{$} (handleRoot) is deliberately NOT covered: it is the one
// public-ish route whose entire response depends on whether a Caller is
// present and, if so, who — "no caller" redirects to /login, exactly one
// game redirects straight there, anything else serves the picker shell —
// so it is the opposite of a route this function exists to fast-path.
func isPublicPath(p string) bool {
	if p == "/login" || p == "/api/config" {
		return true
	}
	if strings.HasPrefix(p, "/static/") {
		return true
	}
	if strings.HasPrefix(p, "/g/") {
		return true
	}
	return false
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

// resolveBearerCallerReadOnly is resolveBearerCaller's read-only
// counterpart: same (Caller, bool, error) contract, built on
// identity.CheckAPIToken instead of ResolveAPIToken, so it never records
// use. It exists for exactly one caller — the SSE heartbeat re-check
// (internal/web/events.go) — which must re-ask "is this token still
// live" every few seconds without that asking itself counting as
// activity; see CheckAPIToken's own doc comment (tokens.go) for why that
// distinction matters. Every other bearer-authenticated code path in
// this package still goes through resolveBearerCaller: a request that
// only asks whether a credential works is what "activity" means for
// last_used_at everywhere except this one re-check.
func (s *Server) resolveBearerCallerReadOnly(ctx context.Context, token string) (Caller, bool, error) {
	summary, err := s.opts.Identity.CheckAPIToken(ctx, token)
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

// resolveSessionCallerReadOnly is resolveSessionCaller's read-only
// counterpart: it resolves the session exactly the same way
// (UserForSession) but never calls ExtendSession, so calling it cannot
// slide a session's expiry forward. It exists for exactly one caller —
// the SSE heartbeat re-check (internal/web/events.go) — which re-asks
// "is this session still valid" every few seconds purely to notice a
// logout, password change, or removal from the game; without this
// distinction, that re-check would itself be activity, and a forgotten
// open browser tab would keep a session alive for up to
// identity.maxSessionLifetime with nobody actually present. An open
// stream is not activity — only a request that does something with the
// session counts. Every other session-authenticated code path still goes
// through resolveSessionCaller, sliding renewal included.
func (s *Server) resolveSessionCallerReadOnly(ctx context.Context, token string) (Caller, bool, error) {
	user, _, err := s.opts.Identity.UserForSession(ctx, token)
	if err != nil {
		if errors.Is(err, identity.ErrNoSession) {
			return Caller{}, false, nil
		}
		return Caller{}, false, err
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

// retryableAdvice is the one sentence the REST surface says about
// database contention, written here once and used by every handler in
// this package through writeRetryableError below. mcpErrorFor
// (mcp_errors.go) argues both halves of it at length — why the code is
// still `retryable` when 57014 can also mean "too expensive", and why the
// database's own message is logged rather than carried — and says it in
// the vocabulary of a tool call ("send the same call again") because that
// is the surface it answers. This is the same advice in the vocabulary of
// an HTTP request.
const retryableAdvice = "the database refused this over contention; send the same request again. " +
	"If it keeps failing, the request is too expensive as written rather than " +
	"unlucky: ask for less rather than resending it again"

// writeRetryableError is the contention answer, in one place. Both REST
// callers of it — writeDomainError's tail for the content surface
// (api_metamodel.go) and writeUnmappedError below for the
// game-administration one — log first, on their own terms, and then call
// this: the status, the code and the sentence are not theirs to choose
// separately, and this repository's most repeated defect is a rule
// written once and copied.
func writeRetryableError(w http.ResponseWriter) {
	writeCodedError(w, http.StatusServiceUnavailable, errCodeRetryable, retryableAdvice, nil)
}

// writeUnmappedError is the tail every REST handler in this package
// shares: an error no arm above it recognised is contention first and a
// server fault only as the default.
//
// **It exists because the game-administration handlers did not have
// that tail.** api_projects.go, api_tokens.go, api_invites.go,
// api_auth.go, api_admin.go and api_password.go each mapped their own
// domain sentinels carefully and then answered *every* remaining error
// with 500 internal_error — including the four contention SQLSTATEs
// metamodel.IsRetryable admits, which the content surface has reported
// as `retryable` since Task 7. A game deletion cascading over a game an
// agent is concurrently writing deadlocks in Postgres (SQLSTATE 40P01),
// and that deadlock was being reported to the caller as our bug rather
// than as the one thing that would have worked: sending the same request
// again. TestAGameDeletionDeadlockedByAContentWriteIsRetryable provokes
// exactly that pair and is red without this function.
//
// logMsg and clientMsg stay the caller's: the operator-facing line names
// which operation failed ("delete game failed") and the caller-facing one
// names it in the product's own words ("could not delete the game"), and
// neither is something a shared tail can invent. attrs are the caller's
// own log fields, and are logged on both paths — an operator seeing a run
// of contention wants the project id as much as they want it on a fault.
//
// Not every 500 in those files may become a 503: see
// handleChangePassword's and startSessionFor's own comments for the two
// call sites where "send the same request again" would be wrong advice
// because the request already changed something, and which therefore
// keep their unconditional 500 deliberately.
func writeUnmappedError(w http.ResponseWriter, r *http.Request, err error, logMsg, clientMsg string, attrs ...any) {
	if metamodel.IsRetryable(err) {
		// Warn, not error: nothing is broken, and the database's own
		// message ("deadlock detected", "canceling statement due to lock
		// timeout") is logged rather than carried to the caller for the
		// reason mcpErrorFor gives — it describes this server's
		// internals, not the caller's next move, and an operator seeing a
		// run of these wants to know which lock.
		slog.WarnContext(r.Context(), "request hit database contention",
			append([]any{"path", r.URL.Path, "operation", logMsg, "error", err}, attrs...)...)
		writeRetryableError(w)
		return
	}
	slog.ErrorContext(r.Context(), logMsg, append(attrs, "error", err)...)
	writeError(w, http.StatusInternalServerError, errCodeInternal, clientMsg)
}
