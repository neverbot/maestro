package web

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/roles"
)

type createTokenRequest struct {
	Label string `json:"label"`
}

// requireTokenMinter checks caller — already confirmed human and a member
// of projectID by requireProjectAccess — holds at least editor standing.
// A token grants an agent whatever access its project binding carries,
// for as long as the token lives, independent of the person who minted
// it; letting a read-only viewer mint one would hand out a credential
// wider than the viewer's own role, a privilege escalation dressed as a
// convenience. Listing (metadata and hint only, never a live value) and
// revoking (which only ever removes access) stay open to any member; see
// handleListTokens and handleRevokeToken.
func (s *Server) requireTokenMinter(w http.ResponseWriter, r *http.Request, caller Caller, projectID uuid.UUID) bool {
	role, err := s.opts.Projects.RoleOf(r.Context(), caller.UserID, projectID)
	if err != nil || !roles.AtLeast(roles.Role(role), roles.Editor) {
		writeError(w, http.StatusForbidden, "forbidden", "only an editor or owner may create tokens")
		return false
	}
	return true
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	if !s.requireTokenMinter(w, r, caller, projectID) {
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return
	}
	if req.Label == "" {
		req.Label = "unnamed token"
	}

	clear, row, err := s.opts.Identity.CreateAPIToken(r.Context(), identity.CreateAPITokenRequest{ProjectID: projectID, UserID: caller.UserID, Label: req.Label})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create the token")
		return
	}
	// The clear value appears here and nowhere else, ever.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":    row.ID,
		"label": row.Label,
		"token": clear,
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	rows, err := s.opts.Identity.ListAPITokens(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": rows})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	tokenID, err := uuid.Parse(r.PathValue("token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such token")
		return
	}
	if err := s.opts.Identity.RevokeAPIToken(r.Context(), identity.RevokeAPITokenRequest{ProjectID: projectID, TokenID: tokenID}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not revoke the token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
