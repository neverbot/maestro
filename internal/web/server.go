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
	s.mux.HandleFunc("GET /version", s.handleVersion)
	s.mux.Handle("GET /api/me", requireCaller(s.handleMe))
	return s
}

// ServeHTTP runs every request through authentication first. authenticate
// never rejects a request on its own — it only attaches a Caller to the
// context when one can be resolved — so routes that permit anonymous
// access (/healthz, /version) are unaffected by wrapping the whole mux
// here; only handlers wrapped in requireCaller actually enforce anything.
//
// /healthz and /version stay public deliberately: both are diagnostic
// endpoints with no user data, the kind of thing a load balancer's health
// probe or a deploy script checks without carrying credentials, and this
// is a public repository, so /version's commit SHA is already visible in
// the source history it is built from — gating it behind auth would cost
// real operational convenience for no corresponding secrecy gain.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.authenticate(s.mux).ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
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
