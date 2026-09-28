package web

import "net/http"

// handleConfig exposes the one thing an unauthenticated visitor needs to
// make sense of /login before signing in: which registration mode this
// instance runs in. A visitor arriving at an invite_only instance with no
// invite link otherwise sees a bare password box and nothing to explain
// it; a domain_open instance had no reachable UI for self-service
// registration at all before this endpoint existed — login.html's script
// is this handler's only consumer, and decides its copy and which form to
// show from the single field returned here.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"registration_mode": string(s.opts.Config.RegistrationMode),
	})
}
