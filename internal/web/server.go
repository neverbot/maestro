// Package web serves every HTTP surface of Maestro.
package web

import (
	"encoding/json"
	"net/http"
)

// Options carries everything the server needs from the outside.
type Options struct {
	Version string
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
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.opts.Version})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
