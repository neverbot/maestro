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
//
// Deliberately one field, not the whole config.Config: this route is
// public by design (see isPublicPath, auth.go) precisely because an
// unauthenticated caller needs it before login is even possible, which
// means every other field on Config — trusted proxy count, session TTL,
// allowed email domains, the first-admin bootstrap credentials — must
// stay off of it. A future field added to config.Config must not be
// added here by reflex; each one is a separate decision about what an
// anonymous caller gets to learn about this instance.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"registration_mode": string(s.opts.Config.RegistrationMode),
	})
}
