package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/roles"
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
// file's other checks (project scope, role) even run. A plain function,
// not a method: it uses neither a receiver nor the request, only the
// already-resolved Caller — a quality review flagged the unused receiver
// after ProjectScope (below) took over what little project lookup this
// used to help with.
func requireHumanCaller(w http.ResponseWriter, caller Caller) bool {
	if caller.IsToken() {
		writeError(w, http.StatusForbidden, errCodeForbidden, "API tokens cannot perform this action")
		return false
	}
	return true
}

// roleList renders roles.All() as a comma-separated string for an error
// message, so "role must be one of owner, editor, viewer" can never drift
// from the vocabulary roles.Valid actually checks against — a literal
// string here once already had to be found and fixed by hand when this
// package's error messages were reviewed.
func roleList() string {
	all := roles.All()
	names := make([]string, len(all))
	for i, r := range all {
		names[i] = string(r)
	}
	return strings.Join(names, ", ")
}

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireHumanCaller(w, caller) {
		return
	}
	rows, err := s.opts.Projects.ListForUser(r.Context(), caller.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list games failed", "user_id", caller.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not list games")
		return
	}
	games := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		games = append(games, map[string]any{"id": p.ID, "slug": p.Slug, "name": p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireHumanCaller(w, caller) {
		return
	}
	var req createGameRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	project, err := s.opts.Projects.Create(r.Context(), req.Slug, req.Name, caller.UserID)
	switch {
	case errors.Is(err, projects.ErrSlugTaken):
		writeError(w, http.StatusConflict, errCodeSlugTaken, "another game already uses that slug")
	case errors.Is(err, projects.ErrSlugInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeSlugInvalid, "that slug is not usable")
	case errors.Is(err, projects.ErrNameInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeNameInvalid, "that name is not usable")
	case err != nil:
		slog.ErrorContext(r.Context(), "create game failed", "user_id", caller.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not create the game")
	default:
		// Ids, not slugs, are what the rest of the API addresses a game
		// by (see ProjectScope's own doc comment below): the SPA maps
		// slug to id once, from this response and from GET /api/games,
		// and every other route in this file and api_tokens.go takes the
		// id from the URL. A slug can be renamed later; an id cannot, so
		// nothing downstream has to care whether it was.
		writeJSON(w, http.StatusCreated, map[string]any{"id": project.ID, "slug": project.Slug, "name": project.Name})
	}
}

// ProjectScope is what requireProject resolves before a project-scoped
// handler ever runs: the {game} path value turned into a real project
// id, the caller's role within it, and whether the caller reached it by
// token.
//
// This is NOT enforced by the type system, and an earlier version of
// this comment claimed it was — a quality review disproved that
// directly, by writing a handler in this same package that parsed
// r.PathValue("game") itself, called s.opts.Projects.ListMembers
// straight from a non-member caller with no ProjectScope anywhere in
// its signature, and registered it on the mux. It compiled and it
// served the data. Every handler in this file lives in package web, and
// nothing about an unexported struct stops a same-package function from
// ignoring it entirely — Go has no visibility boundary narrower than the
// package.
//
// What ProjectScope actually buys: it resolves {game} and the caller's
// role exactly once per request (see requireProject's own doc comment
// for the time-of-check window a second, per-handler RoleOf lookup used
// to leave open), and it makes the guarded path the only *ergonomic*
// one — a handler that wants scope.ProjectID or scope.Role has to take a
// ProjectScope parameter to get it, and the only routine way to produce
// one is requireProject. That is a convention with a pit of success, not
// a guarantee. The actual enforcement is registerProjectRoute
// (server.go) plus TestEveryGameScopedRouteGoesThroughRequireProject
// (server_test.go), which records every pattern this server registers
// and fails the build if a pattern containing "{game}" was wired up any
// other way — a bypass like the reviewer's now has to dodge a route
// registration convention *and* a test that inspects the whole routing
// table, not just this type's shape.
//
// Role is always populated, token callers included — see requireProject's
// own doc comment for why a token's role is always roles.Editor rather
// than looked up — so a handler that needs a role gate never has to
// branch on IsToken to decide whether Role even means anything.
type ProjectScope struct {
	ProjectID uuid.UUID
	Role      string
	IsToken   bool
}

// requireProject wraps a project-scoped handler, resolving {game} and the
// caller's standing in it exactly once before the handler runs, and
// handing the result to the handler as a ProjectScope it cannot
// fabricate on its own (see that type's own doc comment for why this
// replaced a bare helper function). This also closes a real
// time-of-check-to-time-of-use window a quality review found in the
// previous shape: every role-gated handler used to call RoleOf a second
// time, after this same lookup, to decide "is this caller an owner" —
// which meant a demotion landing between the two lookups was still
// admitted by whichever check ran first. There is now exactly one
// RoleOf call per request, and every handler downstream reads the same
// answer.
//
// Built on Caller.ScopedProject and Caller.IsToken rather than the raw
// TokenID/ProjectID fields — see those methods' own doc comments in
// auth.go for why: ScopedProject is where "an admin is not exempt from a
// token's binding" is encoded exactly once, and reading the raw fields
// here instead would re-derive that per call site.
//
// A token caller's Role is always roles.Editor, never looked up from
// membership. This is Task 12's quality review settling the token role
// model deliberately: a token outlives the person who minted it, so
// deriving its access from that person's *current* membership on every
// request would silently change what an already-issued token can do
// whenever its minter's own role changes — exactly backwards from "a
// token outlives the person who made it". Instead, projects.SetRole
// revokes a member's tokens outright the moment their standing drops
// below editor (mirroring what RemoveMember already does for a lost
// membership entirely), so a token that still resolves is guaranteed to
// belong to someone who was at least an editor as of their last role
// change — which is exactly the invariant "editor-equivalent, no live
// lookup needed" depends on.
//
// Call this only through registerProjectRoute (server.go), never
// s.mux.Handle directly — see ProjectScope's own doc comment for why
// that distinction is checked by a test, not just a naming convention.
func (s *Server) requireProject(h func(http.ResponseWriter, *http.Request, Caller, ProjectScope)) func(http.ResponseWriter, *http.Request, Caller) {
	return func(w http.ResponseWriter, r *http.Request, caller Caller) {
		projectID, err := uuid.Parse(r.PathValue("game"))
		if err != nil {
			writeError(w, http.StatusNotFound, errCodeNotFound, "no such game")
			return
		}

		if scoped, ok := caller.ScopedProject(); ok {
			if scoped != projectID {
				writeError(w, http.StatusForbidden, errCodeScopeViolation, "this token is bound to another game")
				return
			}
			h(w, r, caller, ProjectScope{ProjectID: projectID, Role: string(roles.Editor), IsToken: true})
			return
		}

		role, err := s.opts.Projects.RoleOf(r.Context(), caller.UserID, projectID)
		if err != nil {
			writeError(w, http.StatusForbidden, errCodeForbidden, "you are not a member of this game")
			return
		}
		h(w, r, caller, ProjectScope{ProjectID: projectID, Role: role, IsToken: false})
	}
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	rows, err := s.opts.Projects.ListMembers(r.Context(), scope.ProjectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list members failed", "project_id", scope.ProjectID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not list members")
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
// themselves. The owner check reads scope.Role, resolved once by
// requireProject, rather than calling RoleOf again — see that type's own
// doc comment for the time-of-check window a second lookup here used to
// leave open.
func (s *Server) handleChangeRole(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	if !roles.AtLeast(roles.Role(scope.Role), roles.Owner) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only an owner may change a member's role")
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such member")
		return
	}
	var req changeRoleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	revoked, err := s.opts.Projects.SetRole(r.Context(), targetID, scope.ProjectID, req.Role)
	switch {
	case errors.Is(err, projects.ErrRoleInvalid):
		writeError(w, http.StatusBadRequest, errCodeInvalidRole, "role must be one of "+roleList())
	case errors.Is(err, projects.ErrUserNotFound):
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such member")
	case errors.Is(err, projects.ErrProjectNotFound):
		// Unreachable today (nothing in this plan deletes a project yet),
		// but requireProject's own RoleOf lookup and this SetRole call are
		// two separate round trips, so a project deleted in between is a
		// real race the moment game deletion lands — mapped now so that
		// day this is a 404, not a 500 nobody mapped in time.
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such game")
	case errors.Is(err, projects.ErrLastOwner):
		// See handleRemoveMember's doc comment: this is the same guard,
		// reached here when the change would demote the game's only owner.
		writeError(w, http.StatusConflict, errCodeLastOwner, "a game must keep at least one owner — promote someone else first")
	case err != nil:
		slog.ErrorContext(r.Context(), "change role failed", "project_id", scope.ProjectID, "target_user_id", targetID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not change the member's role")
	default:
		// Mirrors handleRemoveMember's own response shape: a demotion
		// below editor revokes the target's tokens in this project
		// (projects.SetRole, above) exactly the way removal already
		// does, so the response mirrors it too instead of answering an
		// empty 200 after silently killing their agents.
		writeJSON(w, http.StatusOK, map[string]any{"revoked_tokens": revoked})
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
//
// Answers 200 with the labels of every token RemoveMember just revoked,
// not a bare 204: a quality review pointed out that silently killing a
// removed member's agents and saying nothing left the owner with no way
// to know which of their agents had just stopped working. A revoked
// token id means nothing on its own; a label ("nightly export", "seed
// agent") is what a UI can put in a toast and a person can act on.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	targetID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such member")
		return
	}
	if targetID != caller.UserID && !roles.AtLeast(roles.Role(scope.Role), roles.Owner) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only an owner may remove another member")
		return
	}

	revoked, err := s.opts.Projects.RemoveMember(r.Context(), targetID, scope.ProjectID)
	switch {
	case errors.Is(err, projects.ErrLastOwner):
		writeError(w, http.StatusConflict, errCodeLastOwner, "a game must keep at least one owner — promote someone else first")
	case err != nil:
		slog.ErrorContext(r.Context(), "remove member failed", "project_id", scope.ProjectID, "target_user_id", targetID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not remove the member")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"revoked_tokens": revoked})
	}
}

// handleRoot implements the single-game shortcut: one visible game goes
// straight to it, any other count (zero included) falls through to the
// picker shell — a user in zero games still needs a page to land on (an
// empty-state "create your first game" prompt is the SPA's job, not
// this handler's), not a crash indexing games[0] against an empty slice.
//
// This bypasses requireCaller — it needs "no caller" to mean "redirect
// to /login", not a 401 — so it is responsible for its own
// setNoStoreHeaders call: it is the most identity-dependent response in
// the product (a redirect to one game for one caller, to login for
// another, to the picker shell for a third), and a quality review found
// it was shipping with neither Cache-Control nor Vary. Its failure path
// uses http.Error, not writeJSON/writeError: this route only ever serves
// an HTML navigation (a redirect or the SPA shell), so a JSON error body
// on the one path that can fail was the odd one out, not a convenience.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)
	caller, ok := CallerFrom(r.Context())
	if !ok || caller.IsToken() {
		// A bearer token is not a browser session; /  has nothing to redirect
		// it to and it is not human, so it is treated the same as anonymous.
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	games, err := s.opts.Projects.ListForUser(r.Context(), caller.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list games for root redirect failed", "user_id", caller.UserID, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
