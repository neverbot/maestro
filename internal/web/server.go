// Package web serves every HTTP surface of Maestro.
package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
)

// Options carries everything the server needs from the outside.
type Options struct {
	Version  string
	Config   config.Config
	Identity *identity.Service
	Projects *projects.Service
}

// Server routes all four surfaces: web UI, REST API, MCP and SSE.
type Server struct {
	mux     *http.ServeMux
	opts    Options
	handler http.Handler

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

	s := &Server{
		mux:               http.NewServeMux(),
		opts:              opts,
		loginLimiter:      identity.NewLimiter(10, time.Minute),
		loginIPLimiter:    identity.NewLimiter(40, time.Minute),
		registerIPLimiter: identity.NewLimiter(10, time.Minute),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /version", requireCaller(s.handleVersion))
	s.mux.Handle("GET /api/me", requireCaller(s.handleMe))
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	s.mux.HandleFunc("GET /{$}", s.handleRoot)
	s.mux.Handle("GET /api/games", requireCaller(s.handleListGames))
	s.mux.Handle("POST /api/games", requireCaller(s.handleCreateGame))
	s.mux.Handle("GET /api/games/{game}/members", requireCaller(s.requireProject(s.handleListMembers)))
	s.mux.Handle("PATCH /api/games/{game}/members/{user}", requireCaller(s.requireProject(s.handleChangeRole)))
	s.mux.Handle("DELETE /api/games/{game}/members/{user}", requireCaller(s.requireProject(s.handleRemoveMember)))
	s.mux.Handle("POST /api/games/{game}/tokens", requireCaller(s.requireProject(s.handleCreateToken)))
	s.mux.Handle("GET /api/games/{game}/tokens", requireCaller(s.requireProject(s.handleListTokens)))
	s.mux.Handle("DELETE /api/games/{game}/tokens/{token}", requireCaller(s.requireProject(s.handleRevokeToken)))
	// Built once here, not per request in ServeHTTP: authenticate wraps
	// s.mux in a closure, and there is no reason to allocate a fresh one
	// for every single incoming request when the mux it wraps never
	// changes after construction.
	s.handler = s.authenticate(s.mux)
	return s
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
