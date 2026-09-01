package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/projects"
)

type createGameRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// requireHumanCaller rejects a bearer-token caller. A token authenticates
// an agent scoped to one game's content (Task 9's "one token = one
// project"); it never authenticates a person entitled to create or list
// games, manage membership, or manage other tokens — every one of those
// is a decision about who gets to hold or grant standing in the product,
// not "this game's content", so it is refused here before any of this
// file's other checks (project scope, role) even run.
func (s *Server) requireHumanCaller(w http.ResponseWriter, caller Caller) bool {
	if caller.IsToken() {
		writeError(w, http.StatusForbidden, "forbidden", "API tokens cannot perform this action")
		return false
	}
	return true
}

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	rows, err := s.opts.Projects.ListForUser(r.Context(), caller.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list games")
		return
	}
	games := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		games = append(games, map[string]any{"id": p.ID, "slug": p.Slug, "name": p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	var req createGameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return
	}
	project, err := s.opts.Projects.Create(r.Context(), req.Slug, req.Name, caller.UserID)
	switch {
	case errors.Is(err, projects.ErrSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", "another game already uses that slug")
	case errors.Is(err, projects.ErrSlugInvalid):
		writeError(w, http.StatusUnprocessableEntity, "slug_invalid", "that slug is not usable")
	case errors.Is(err, projects.ErrNameInvalid):
		writeError(w, http.StatusUnprocessableEntity, "name_invalid", "that name is not usable")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create the game")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"id": project.ID, "slug": project.Slug, "name": project.Name})
	}
}

// requireProjectAccess resolves the {game} path value and checks the caller
// may touch it, built on Caller.ScopedProject and Caller.IsToken rather
// than the raw TokenID/ProjectID fields — see those methods' own doc
// comments in auth.go for why: ScopedProject is where "an admin is not
// exempt from a token's binding" is encoded exactly once, and reading the
// raw fields here instead would re-derive that per call site, with a real
// chance of getting it wrong for one route out of the several this file
// and api_tokens.go add. A token caller may only touch the project its
// token is bound to, admin or not; a session caller must hold a
// membership, resolved fresh on every call rather than trusted from
// anything the caller supplied.
func (s *Server) requireProjectAccess(w http.ResponseWriter, r *http.Request, caller Caller) (uuid.UUID, bool) {
	projectID, err := uuid.Parse(r.PathValue("game"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such game")
		return uuid.Nil, false
	}

	if scoped, ok := caller.ScopedProject(); ok {
		if scoped != projectID {
			writeError(w, http.StatusForbidden, "scope_violation", "this token is bound to another game")
			return uuid.Nil, false
		}
		return projectID, true
	}

	if _, err := s.opts.Projects.RoleOf(r.Context(), caller.UserID, projectID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "you are not a member of this game")
		return uuid.Nil, false
	}
	return projectID, true
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	rows, err := s.opts.Projects.ListMembers(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list members")
		return
	}
	// No email: projects.Member carries none (Task 8's Round 2
	// corrections), because this endpoint has no owner-only gate of its
	// own — any member, viewer included, can list members — and a
	// teammate's email is not something every role should be able to
	// read.
	members := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		members = append(members, map[string]any{
			"id": m.UserID, "display_name": m.DisplayName, "role": m.Role,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

type changeRoleRequest struct {
	Role string `json:"role"`
}

// handleChangeRole promotes or demotes an existing member, or grants
// membership to a user who has none yet — projects.SetRole upserts, and an
// owner directly granting a role without a round trip through an invite
// link is a legitimate shortcut, not a bug, precisely because only an
// owner may reach this handler at all. Only an owner may change any
// role, including their own: unlike removal (handleRemoveMember below),
// there is no self-service path here, so a non-owner can never promote
// themselves.
func (s *Server) handleChangeRole(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	if role, err := s.opts.Projects.RoleOf(r.Context(), caller.UserID, projectID); err != nil || role != "owner" {
		writeError(w, http.StatusForbidden, "forbidden", "only an owner may change a member's role")
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such member")
		return
	}
	var req changeRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return
	}

	err = s.opts.Projects.SetRole(r.Context(), targetID, projectID, req.Role)
	switch {
	case errors.Is(err, projects.ErrRoleInvalid):
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be one of owner, editor, viewer")
	case errors.Is(err, projects.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such member")
	case errors.Is(err, projects.ErrLastOwner):
		// See handleRemoveMember's doc comment: this is the same guard,
		// reached here when the change would demote the game's only owner.
		writeError(w, http.StatusConflict, "last_owner", "a game must keep at least one owner — promote someone else first")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not change the member's role")
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// handleRemoveMember removes a member from a game. An owner may remove any
// member; anyone may remove themselves (leaving a game they are a member
// of is always a member's own choice, not a privilege). Either path can
// still hit projects.ErrLastOwner — a sole owner leaving, or an owner
// trying to remove another owner down to zero — which is reported as 409
// so a human sees "promote someone else first" instead of a generic
// failure, and every other row scoped to this game is never left with
// nobody able to manage it. projects.RemoveMember itself (Task 9, Round
// 2 Correction 9) already revokes, in the same transaction as the
// membership deletion, every API token in this game the removed member
// created — this handler does not need to call identity.RevokeAPIToken
// separately, and must not: doing it here as a second, unrelated call
// would reintroduce the crash window Task 9 closed by putting the
// revocation inside RemoveMember's own transaction.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !s.requireHumanCaller(w, caller) {
		return
	}
	projectID, ok := s.requireProjectAccess(w, r, caller)
	if !ok {
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "no such member")
		return
	}
	if targetID != caller.UserID {
		if role, rerr := s.opts.Projects.RoleOf(r.Context(), caller.UserID, projectID); rerr != nil || role != "owner" {
			writeError(w, http.StatusForbidden, "forbidden", "only an owner may remove another member")
			return
		}
	}

	err = s.opts.Projects.RemoveMember(r.Context(), targetID, projectID)
	switch {
	case errors.Is(err, projects.ErrLastOwner):
		writeError(w, http.StatusConflict, "last_owner", "a game must keep at least one owner — promote someone else first")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not remove the member")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRoot implements the single-game shortcut: one visible game goes
// straight to it, any other count (zero included) falls through to the
// picker shell — a user in zero games still needs a page to land on (an
// empty-state "create your first game" prompt is the SPA's job, not
// this handler's), not a crash indexing games[0] against an empty slice.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	caller, ok := CallerFrom(r.Context())
	if !ok || caller.IsToken() {
		// A bearer token is not a browser session; /  has nothing to redirect
		// it to and it is not human, so it is treated the same as anonymous.
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	games, err := s.opts.Projects.ListForUser(r.Context(), caller.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list games")
		return
	}
	if len(games) == 1 {
		http.Redirect(w, r, "/g/"+games[0].Slug, http.StatusFound)
		return
	}
	s.serveAsset(w, r, "index.html")
}

// serveAsset writes an embedded static file. Task 14 replaces this with the
// real embedded filesystem.
func (s *Server) serveAsset(w http.ResponseWriter, _ *http.Request, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>Maestro</title><div id=app data-asset=\"" + name + "\"></div>"))
}
