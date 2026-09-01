// Package web serves every HTTP surface of Maestro.
package web

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

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
	mux  *http.ServeMux
	opts Options
}

// NewServer builds the routing tree.
func NewServer(opts Options) *Server {
	s := &Server{mux: http.NewServeMux(), opts: opts}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /version", requireCaller(s.handleVersion))
	s.mux.Handle("GET /api/me", requireCaller(s.handleMe))
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
	s.authenticate(s.mux).ServeHTTP(w, r)
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
// single project that token is bound to. A session caller's ProjectID is
// always nil here; it resolves a project from the URL and its own
// membership on project-scoped routes instead, never from a
// caller-supplied parameter.
func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, caller Caller) {
	payload := map[string]any{
		"user_id":  caller.UserID,
		"is_admin": caller.IsAdmin,
	}
	if caller.ProjectID != nil {
		payload["project_id"] = *caller.ProjectID
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		slog.Error("encode json response", "error", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
