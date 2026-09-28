package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
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
		writeUnmappedError(w, r, err, "list games failed", "could not list games", "user_id", caller.UserID)
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
		writeUnmappedError(w, r, err, "create game failed", "could not create the game", "user_id", caller.UserID)
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
type ProjectScope struct {
	ProjectID uuid.UUID

	// Slug is the game's stored slug — the address the caller used to
	// reach this handler, in the spelling the game actually carries.
	Slug string

	Role    string
	IsToken bool
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
func (s *Server) requireProject(h func(http.ResponseWriter, *http.Request, Caller, ProjectScope)) func(http.ResponseWriter, *http.Request, Caller) {
	return func(w http.ResponseWriter, r *http.Request, caller Caller) {
		ref := r.PathValue("game")
		scope, err := s.resolveGameRef(r.Context(), caller, ref)
		var wrongGame *boundElsewhereError
		switch {
		case errors.As(err, &wrongGame):
			writeError(w, http.StatusForbidden, errCodeScopeViolation, wrongGame.Error())
			return
		case errors.Is(err, errNoSuchGame):
			writeError(w, http.StatusNotFound, errCodeNotFound, noSuchGameMessage(ref))
			return
		case err != nil:
			// Not a verdict about this caller: RoleOf's own lookup
			// failed (a database error), not "no membership row found".
			// Task 10's authenticate already drew this exact line once,
			// for the same reason — a database failure must surface as
			// a 500 an operator can act on, not as a 403 that reads as
			// "this caller was rejected" when nothing about them was
			// actually evaluated. See resolveProjectScope's own doc
			// comment for the split.
			// Contention is still not a verdict about this caller, and
			// is not a fault either: this lookup runs on every
			// project-scoped request, including the ones racing a
			// deletion of the very game they name, so it is exactly
			// where a lock timeout lands. writeUnmappedError keeps the
			// split this comment draws and adds the one below it.
			writeUnmappedError(w, r, err, "resolve project scope failed", "could not verify game membership",
				"game", ref, "user_id", caller.UserID)
			return
		}
		h(w, r, caller, scope)
	}
}

// resolveGameRef turns the `{game}` path segment into a scope.
func (s *Server) resolveGameRef(ctx context.Context, caller Caller, ref string) (ProjectScope, error) {
	if bound, ok := caller.ScopedProject(); ok {
		project, err := s.opts.Projects.ByID(ctx, bound)
		if err != nil {
			return ProjectScope{}, err
		}
		if !strings.EqualFold(project.Slug, ref) {
			return ProjectScope{}, &boundElsewhereError{bound: project.Slug, requested: ref}
		}
		return ProjectScope{
			ProjectID: project.ID, Slug: project.Slug,
			Role: string(roles.Editor), IsToken: true,
		}, nil
	}

	membership, err := s.opts.Projects.BySlugForUser(ctx, ref, caller.UserID)
	switch {
	case errors.Is(err, projects.ErrProjectNotFound):
		return ProjectScope{}, errNoSuchGame
	case err != nil:
		return ProjectScope{}, err
	}
	return ProjectScope{
		ProjectID: membership.Project.ID, Slug: membership.Project.Slug,
		Role: membership.Role, IsToken: false,
	}, nil
}

// errNoSuchGame is resolveGameRef's "this caller cannot reach a game by
// that name", covering both halves of the non-oracle above. The message
// a caller sees is noSuchGameMessage's, because it names the value that
// was tried and the sentinel cannot.
var errNoSuchGame = errors.New("no game by that name is available to this caller")

// boundElsewhereError is a token caller naming a game other than the one
// its token is bound to. It carries both slugs so the refusal can say
// which is which; neither is a fact the caller did not already hold.
type boundElsewhereError struct {
	bound     string
	requested string
}

func (e *boundElsewhereError) Error() string {
	return fmt.Sprintf("this token is bound to the game %q, and this request names %q",
		e.bound, e.requested)
}

// noSuchGameMessage says what was tried rather than only that it failed.
func noSuchGameMessage(ref string) string {
	if _, err := uuid.Parse(ref); err == nil {
		return fmt.Sprintf("no game named %q is available to you: a game is addressed by "+
			"its slug — the name in its /g/ URL — and not by its id", ref)
	}
	return fmt.Sprintf("no game named %q is available to you", ref)
}

// errScopeViolation and errNotMember are resolveProjectScope's two
// *rejection* outcomes — a caller definitively refused, not a lookup
// that failed — distinguished so a caller of resolveProjectScope can
// tell "wrong game for this token" from "not a member of this game"
// without resolveProjectScope itself knowing whether it is being called
// from requireProject's HTTP-error path or handleEvents's log-and-close
// path (events.go) — the two callers map the same two outcomes to very
// different actions. Neither is returned for a database error: see
// resolveProjectScope's own doc comment for why that is a third,
// separate outcome, wrapped and returned as-is rather than folded into
// errNotMember.
var (
	errScopeViolation = errors.New("token is bound to another game")
	errNotMember      = errors.New("not a member of this game")
)

// resolveProjectScope re-resolves caller's standing in a game it is
// already admitted to: the answer handleEvents (events.go) needs on
// every heartbeat tick, to notice a membership change on an
// otherwise-idle long-lived connection without re-deriving the
// token/membership distinction itself.
func (s *Server) resolveProjectScope(ctx context.Context, caller Caller, game ProjectScope) (ProjectScope, error) {
	if scoped, ok := caller.ScopedProject(); ok {
		if scoped != game.ProjectID {
			return ProjectScope{}, errScopeViolation
		}
		return ProjectScope{
			ProjectID: game.ProjectID, Slug: game.Slug,
			Role: string(roles.Editor), IsToken: true,
		}, nil
	}

	role, err := s.opts.Projects.RoleOf(ctx, caller.UserID, game.ProjectID)
	switch {
	case errors.Is(err, projects.ErrNotAMember):
		return ProjectScope{}, errNotMember
	case err != nil:
		// RoleOf's own error already carries "lookup membership: ..."
		// context (projects.go) — returned as-is, not re-wrapped, so a
		// caller logging it does not see that phrase twice.
		return ProjectScope{}, err
	}
	return ProjectScope{ProjectID: game.ProjectID, Slug: game.Slug, Role: role, IsToken: false}, nil
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	rows, err := s.opts.Projects.ListMembers(r.Context(), scope.ProjectID)
	if err != nil {
		writeUnmappedError(w, r, err, "list members failed", "could not list members", "project_id", scope.ProjectID)
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
			// **Which row is the caller's own**, decided here rather than
			// by a browser comparing two ids it was handed. It is the one
			// row where a change locks the reader out of the game they
			// are standing in, and the screen refuses it on that basis.
			"you": m.UserID == caller.UserID,
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
		writeError(w, http.StatusForbidden, errCodeForbidden, "only a game manager may change a member's role")
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
	case errors.Is(err, projects.ErrMemberNotFound):
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such member")
	case errors.Is(err, projects.ErrProjectNotFound):
		// requireProject's own RoleOf lookup and this SetRole call are
		// two separate round trips, so a game deleted in between (Task
		// 17's handleDeleteGame) is a real race: mapped to 404, not the
		// 500 an unmapped constraint-violation error would fall into.
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such game")
	case errors.Is(err, projects.ErrLastOwner):
		// See handleRemoveMember's doc comment: this is the same guard,
		// reached here when the change would demote the game's only owner.
		writeError(w, http.StatusConflict, errCodeLastOwner, "a game must keep at least one manager — make somebody else one first")
	case err != nil:
		writeUnmappedError(w, r, err, "change role failed", "could not change the member's role",
			"project_id", scope.ProjectID, "target_user_id", targetID)
	default:
		// Published after SetRole has already returned successfully —
		// see publish.go's own doc comment on eventMemberUpdated for why
		// that is the same moment as "committed", why this carries only
		// the target's identity and not their new role (a concurrent
		// second role change could publish out of commit order, so a
		// role field on the wire could go stale with no gap to catch
		// it), and why HumanOnly true here matches handleListMembers'
		// own requireHumanCaller gate.
		s.publish(scope.ProjectID, eventMemberUpdated, "", true, map[string]any{"user_id": targetID})
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
		writeError(w, http.StatusForbidden, errCodeForbidden, "only a game manager may remove another member")
		return
	}

	revoked, err := s.opts.Projects.RemoveMember(r.Context(), targetID, scope.ProjectID)
	switch {
	case errors.Is(err, projects.ErrLastOwner):
		writeError(w, http.StatusConflict, errCodeLastOwner, "a game must keep at least one manager — make somebody else one first")
	case err != nil:
		writeUnmappedError(w, r, err, "remove member failed", "could not remove the member",
			"project_id", scope.ProjectID, "target_user_id", targetID)
	default:
		s.publish(scope.ProjectID, eventMemberRemoved, "", true, map[string]any{"user_id": targetID})
		writeJSON(w, http.StatusOK, map[string]any{"revoked_tokens": revoked})
	}
}

// updateGameRequest is a game's two settings, both of them always sent:
// the screen shows them together and saves them together.
type updateGameRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// handleUpdateGame changes a game's name and address. Owner-only, like
// deletion and for a weaker version of the same reason: it is not
// destructive, but **every URL into this game stops resolving the moment
// the address changes** — a colleague's bookmark, a link in a chat, a
// view somebody pinned. Nothing here forwards the old address: see
// projects.Update for why a redirect nobody can put an end date on was
// the worse answer, and settings.html for the sentence that says so to
// the person about to do it.
func (s *Server) handleUpdateGame(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	if !roles.AtLeast(roles.Role(scope.Role), roles.Owner) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only a game manager may change a game's settings")
		return
	}
	var req updateGameRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	project, err := s.opts.Projects.Update(r.Context(), scope.ProjectID, req.Slug, req.Name)
	switch {
	case errors.Is(err, projects.ErrSlugTaken):
		writeError(w, http.StatusConflict, errCodeSlugTaken, "another game already uses that address")
	case errors.Is(err, projects.ErrSlugInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeSlugInvalid, "that address is not usable")
	case errors.Is(err, projects.ErrNameInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeNameInvalid, "that name is not usable")
	case errors.Is(err, projects.ErrProjectNotFound):
		// The same race handleDeleteGame maps: requireProject resolved
		// this game and a concurrent deletion landed before this write.
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such game")
	case err != nil:
		writeUnmappedError(w, r, err, "update game failed", "could not change the game's settings",
			"project_id", scope.ProjectID)
	default:
		// The new address goes back so the browser can navigate to it:
		// the page the caller is standing on is at the old one, which
		// stopped resolving a moment ago.
		writeJSON(w, http.StatusOK, map[string]any{"id": project.ID, "slug": project.Slug, "name": project.Name})
	}
}

// handleDeleteGame deletes a game outright. Owner-only, like
// handleChangeRole and unlike self-removal from handleRemoveMember:
// deleting the game is not a member's own choice the way leaving it is,
// and its irreversibility (every membership, token and bound invite
// scoped to it disappears with it — see projects.Delete's own doc
// comment) is a different order of consequence than one person's own
// membership row.
func (s *Server) handleDeleteGame(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireHumanCaller(w, caller) {
		return
	}
	if !roles.AtLeast(roles.Role(scope.Role), roles.Owner) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only a game manager may delete a game")
		return
	}

	project, err := s.opts.Projects.ByID(r.Context(), scope.ProjectID)
	if err != nil {
		// projects.ErrProjectNotFound is reachable here: requireProject's
		// own membership lookup and this ByID call are two separate round
		// trips, so a game already deleted by a concurrent request (or by
		// this same caller retrying a request whose first response never
		// arrived) lands here as a real race, not a hypothetical one — the
		// exact shape handleChangeRole's own ErrProjectNotFound comment,
		// above in this file, predicted the moment this handler existed.
		// A Task 22 review found that prediction had come true unmapped:
		// this branch fell into the generic 500 below for a caller who
		// had done nothing wrong beyond losing a race.
		if errors.Is(err, projects.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, errCodeNotFound, "no such game")
			return
		}
		writeUnmappedError(w, r, err, "delete game failed", "could not delete the game", "project_id", scope.ProjectID)
		return
	}
	if confirm := r.URL.Query().Get("confirm"); confirm == "" || confirm != project.Slug {
		writeError(w, http.StatusBadRequest, errCodeBadRequest, "confirm must equal the game's slug")
		return
	}

	// Counted before Delete, not after: once the project row is gone,
	// ON DELETE CASCADE has already taken every one of these rows with it
	// and there is nothing left to count — projects.Delete's own single
	// DELETE FROM projects statement carries no rows-affected count for
	// anything it cascades into (see that method's own doc comment). A
	// failure here is logged but never blocks the deletion itself: this
	// is purely the record an operator would otherwise have no way to
	// recover afterward (see the log line below), not a precondition of
	// deleting the game.
	tokensDestroyed, err := s.opts.Identity.CountAPITokensForProject(r.Context(), scope.ProjectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "count api tokens before game deletion failed", "project_id", scope.ProjectID, "error", err)
	}
	invitesDestroyed, err := s.opts.Identity.CountInvitesForProject(r.Context(), scope.ProjectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "count invites before game deletion failed", "project_id", scope.ProjectID, "error", err)
	}

	// The one call in this file the defect was filed against: this
	// DELETE cascades over every row the game owns, so an agent writing
	// the game's content at the same moment deadlocks it (SQLSTATE
	// 40P01) — measured, and provoked deterministically by
	// TestAGameDeletionDeadlockedByAContentWriteIsRetryable. Answering
	// that 500 told the owner their deletion had hit a bug in this
	// server; it had hit a race, and clicking again would have worked.
	if err := s.opts.Projects.Delete(r.Context(), scope.ProjectID); err != nil {
		writeUnmappedError(w, r, err, "delete game failed", "could not delete the game", "project_id", scope.ProjectID)
		return
	}
	slog.InfoContext(r.Context(), "game deleted",
		"project_id", scope.ProjectID, "slug", project.Slug, "user_id", caller.UserID,
		"tokens_destroyed", tokensDestroyed, "invites_destroyed", invitesDestroyed)
	// Published after projects.Delete has already returned successfully
	// — see eventGameDeleted's own doc comment (publish.go) for why every
	// subscriber gets this regardless of role, and why the payload
	// carries nothing beyond the signal itself. This also races the
	// heartbeat re-check every open stream on this project already runs
	// (events.go): that re-check would eventually notice every
	// subscriber's membership disappeared (the cascade deletes it along
	// with the project) and close their stream anyway, up to
	// sseHeartbeatInterval later — this publish just gets there first.
	s.publish(scope.ProjectID, eventGameDeleted, "", false, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleRoot implements the single-game shortcut: one visible game goes
// straight to it, any other count (zero included) falls through to the
// picker shell — a user in zero games still needs a page to land on (an
// empty-state "create your first game" prompt is the SPA's job, not
// this handler's), not a crash indexing games[0] against an empty slice.
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
		// The same classification every other handler in this file now
		// makes, said in this route's own vocabulary rather than through
		// writeUnmappedError: this route serves an HTML navigation and
		// has no JSON error body to put a code in (see this handler's
		// own doc comment). What a browser can still act on is the
		// status — 503 on a reload that would have worked, not a 500
		// that reads as "this instance is broken".
		if metamodel.IsRetryable(err) {
			slog.WarnContext(r.Context(), "root redirect hit database contention", "user_id", caller.UserID, "error", err)
			http.Error(w, retryableAdvice, http.StatusServiceUnavailable)
			return
		}
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
