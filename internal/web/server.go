// Package web serves every HTTP surface of Maestro.
package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/realtime"
)

// Options carries everything the server needs from the outside.
type Options struct {
	Version  string
	Config   config.Config
	Identity *identity.Service
	Projects *projects.Service

	// Hub is the realtime fan-out this instance publishes into and the
	// SSE endpoint (events.go) reads from. Nothing publishes yet — see
	// events.go's own doc comment — but the metamodel plan's entity.*
	// and relation.* events will, from services this package does not
	// own, which is why this is a field on Options instead of a value
	// NewServer keeps entirely to itself: whoever builds those services
	// needs the same *realtime.Hub instance the SSE handler is reading
	// from, not a second, disconnected one. Optional: a nil Hub gets a
	// fresh realtime.NewHub(), same as every other build did before this
	// field existed.
	Hub *realtime.Hub

	// SSEMaxLifetime bounds how long GET /api/games/{game}/events keeps
	// one connection open before closing it and forcing the client to
	// reconnect. See events.go's own doc comment for why a bound exists
	// at all. Optional: zero gets defaultSSEMaxLifetime.
	SSEMaxLifetime time.Duration
}

// Server routes all four surfaces: web UI, REST API, MCP and SSE.
type Server struct {
	mux     *http.ServeMux
	opts    Options
	handler http.Handler

	// mcp serves POST /mcp once mcpHandler has confirmed the caller holds
	// a live api token (see that method's own doc comment). It is built
	// from newMCPServer (mcp.go) with Stateless mode, so every tool call
	// is its own independently authenticated HTTP request rather than a
	// long-lived connection.
	mcp http.Handler

	// loginLimiter, loginIPLimiter and registerIPLimiter guard the two
	// unauthenticated endpoints that accept a secret to verify: a
	// password (POST /api/auth/login) and an invite token or a
	// self-service email (POST /api/auth/register), respectively. They
	// are separate Limiter instances, not one shared by key prefix, so a
	// burst of failed logins can never exhaust the budget registration
	// depends on, or vice versa.
	//
	// loginLimiter and loginIPLimiter are two independent budgets on the
	// same endpoint, deliberately never combined into one composite
	// email+ip key: loginLimiter alone (keyed on the normalized email)
	// caps how hard any one account can be attacked regardless of source,
	// but by itself it also lets someone who merely knows a colleague's
	// address lock that account out indefinitely at no cost to
	// themselves, from anywhere. loginIPLimiter adds a second, looser cap
	// keyed on source IP so a single origin cannot grind through many
	// accounts' budgets one at a time either. Composing them into one
	// "email+ip" key would defeat the per-account protection entirely —
	// an attacker rotating source IPs would get a fresh per-account
	// budget on every hop — so handleLogin checks and records both
	// independently instead.
	//
	// registerIPLimiter guards POST /api/auth/register as a whole — both
	// the invite-redemption branch and the domain_open self-service
	// branch, which share one budget rather than one each (see
	// handleRegister's own comment). It is keyed on source IP only, not
	// paired with a second key the way login is: unlike login, nothing
	// on this endpoint names an existing account an attacker could target
	// for a free lockout by spending someone else's budget — the
	// self-service branch creates a brand new account, and the invite
	// branch's actual credential is the token, never the email (Task 6,
	// Correction 10).
	loginLimiter      *identity.Limiter
	loginIPLimiter    *identity.Limiter
	registerIPLimiter *identity.Limiter

	// registeredPatterns and projectScopedPatterns exist for exactly one
	// reason: TestEveryGameScopedRouteGoesThroughRequireProject
	// (server_test.go). registeredPatterns records every pattern ever
	// passed to route/routeFunc, in registration order;
	// projectScopedPatterns records the subset registered through
	// registerProjectRoute. The test walks the first list and fails if
	// any pattern containing "{game}" is missing from the second — see
	// ProjectScope's own doc comment (api_projects.go) for why this test,
	// not the Go type system, is what actually enforces that every
	// project-scoped route resolves its scope through requireProject.
	registeredPatterns    []string
	projectScopedPatterns map[string]bool

	// hub and sseMaxLifetime back the SSE endpoint (events.go). See
	// Options.Hub and Options.SSEMaxLifetime for what they do and why
	// both are injectable rather than values this file keeps entirely
	// to itself.
	hub            *realtime.Hub
	sseMaxLifetime time.Duration
}

// NewServer builds the routing tree.
//
// Identity and Projects must both be non-nil: a Server with either unset
// would serve /healthz and every other public route just fine right up
// until the first request carrying a bearer token or a session cookie,
// which panics on a nil *identity.Service inside authenticate — a
// misconfigured process should refuse to start, not crash its first
// authenticated request (or, worse, its first unauthenticated one that
// merely happens to carry a stale cookie — see this task's plan
// corrections for how that was found).
func NewServer(opts Options) *Server {
	if opts.Identity == nil {
		panic("web: NewServer requires a non-nil Identity service")
	}
	if opts.Projects == nil {
		panic("web: NewServer requires a non-nil Projects service")
	}

	hub := opts.Hub
	if hub == nil {
		hub = realtime.NewHub()
	}
	sseMaxLifetime := opts.SSEMaxLifetime
	if sseMaxLifetime <= 0 {
		sseMaxLifetime = defaultSSEMaxLifetime
	}

	s := &Server{
		mux:               http.NewServeMux(),
		opts:              opts,
		loginLimiter:      identity.NewLimiter(10, time.Minute),
		loginIPLimiter:    identity.NewLimiter(40, time.Minute),
		registerIPLimiter: identity.NewLimiter(10, time.Minute),
		hub:               hub,
		sseMaxLifetime:    sseMaxLifetime,
	}
	s.routeFunc("GET /healthz", s.handleHealthz)
	s.route("GET /version", requireCaller(s.handleVersion))
	s.route("GET /api/me", requireCaller(s.handleMe))
	s.routeFunc("POST /api/auth/login", s.handleLogin)
	s.routeFunc("POST /api/auth/logout", s.handleLogout)
	s.routeFunc("POST /api/auth/register", s.handleRegister)
	s.routeFunc("GET /{$}", s.handleRoot)
	s.route("GET /api/games", requireCaller(s.handleListGames))
	s.route("POST /api/games", requireCaller(s.handleCreateGame))
	s.registerProjectRoute("GET /api/games/{game}/members", s.handleListMembers)
	s.registerProjectRoute("PATCH /api/games/{game}/members/{user}", s.handleChangeRole)
	s.registerProjectRoute("DELETE /api/games/{game}/members/{user}", s.handleRemoveMember)
	s.registerProjectRoute("POST /api/games/{game}/tokens", s.handleCreateToken)
	s.registerProjectRoute("GET /api/games/{game}/tokens", s.handleListTokens)
	s.registerProjectRoute("DELETE /api/games/{game}/tokens/{token}", s.handleRevokeToken)
	s.registerProjectRoute("GET /api/games/{game}/events", s.handleEvents)

	// The MCP tools (mcp.go) are built once, here, and mounted in
	// Stateless mode: no Mcp-Session-Id bookkeeping, and every tool call
	// is its own independently-authenticated HTTP request rather than a
	// session kept alive across many — see mcpHandler's own doc comment
	// for why that matters for a revoked token. getServer may return the
	// same *mcp.Server for every request (its own doc comment says so),
	// so the tool set is built exactly once rather than reconstructed on
	// every call.
	mcpServer := s.newMCPServer()
	s.mcp = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	s.route("/mcp", s.mcpHandler())

	// Built once here, not per request in ServeHTTP: authenticate wraps
	// s.mux in a closure, and there is no reason to allocate a fresh one
	// for every single incoming request when the mux it wraps never
	// changes after construction.
	s.handler = s.authenticate(s.mux)
	return s
}

// route registers pattern on the mux and records it in
// s.registeredPatterns — see that field's own doc comment for why.
func (s *Server) route(pattern string, h http.Handler) {
	s.registeredPatterns = append(s.registeredPatterns, pattern)
	s.mux.Handle(pattern, h)
}

// routeFunc is route for a plain handler function, matching
// http.ServeMux.HandleFunc's own shape.
func (s *Server) routeFunc(pattern string, h http.HandlerFunc) {
	s.route(pattern, h)
}

// registerProjectRoute registers pattern through
// requireCaller(s.requireProject(h)) and records pattern as
// project-scoped, so TestEveryGameScopedRouteGoesThroughRequireProject
// can confirm it. This is the only call in this file that is allowed to
// wire up a route whose pattern contains "{game}" — see ProjectScope's
// own doc comment (api_projects.go) for what that guarantees and, just
// as importantly, what it does not.
func (s *Server) registerProjectRoute(pattern string, h func(http.ResponseWriter, *http.Request, Caller, ProjectScope)) {
	if s.projectScopedPatterns == nil {
		s.projectScopedPatterns = map[string]bool{}
	}
	s.projectScopedPatterns[pattern] = true
	s.route(pattern, requireCaller(s.requireProject(h)))
}

// ServeHTTP runs every request through authentication first. authenticate
// never rejects a request on its own — it only attaches a Caller to the
// context when one can be resolved — so a route not wrapped in
// requireCaller is unaffected by wrapping the whole mux here; only
// handlers wrapped in requireCaller actually enforce anything.
//
// /healthz stays public: liveness is genuinely information-free (it says
// nothing beyond "the process accepted this TCP connection and can
// answer"), and a load balancer's or orchestrator's health probe needs to
// reach it without carrying credentials.
//
// /version does not: unlike /healthz, it hands back the exact commit an
// instance is running, which is fingerprinting material, not a health
// signal. An unauthenticated caller could scan for it and match the
// returned SHA against whatever was patched afterward, at zero cost — the
// repository being public makes that matching easier, not harmless, since
// it hands the attacker a precise diff of what the instance is missing.
// /version is behind requireCaller for that reason, even though nothing
// else about it is sensitive.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request, _ Caller) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.opts.Version})
}

// handleMe reports the authenticated caller's own identity: which user,
// whether they are an instance admin, and — for a token caller only — the
// single project that token is bound to and the token's own id. A session
// caller's project_id and token_id are both absent (ScopedProject and
// TokenID both report "none"); it resolves a project from the URL and its
// own membership on project-scoped routes instead, never from a
// caller-supplied parameter. token_id has no consumer yet — it is exposed
// here for a client that wants to show "you are using token X" or
// self-revoke it later (Task 12's token endpoints); nothing reads it back
// out of a response today.
func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, caller Caller) {
	payload := map[string]any{
		"user_id":  caller.UserID,
		"is_admin": caller.IsAdmin,
	}
	if projectID, ok := caller.ScopedProject(); ok {
		payload["project_id"] = projectID
	}
	if caller.TokenID != nil {
		payload["token_id"] = *caller.TokenID
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		slog.Error("encode json response", "error", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal_error","message":"failed to encode response"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
