package web

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/roles"
)

// requireAdminCaller rejects a token caller (the same reasoning
// requireHumanCaller gives — inviting people to the instance or a game is
// a decision about who gets to hold or grant standing in the product, not
// a token's own "this game's content") and, on top of that, any human
// caller whose Caller.IsAdmin is false.
//
// This is the first place anywhere in this codebase that IsAdmin actually
// gates an action — every other reader of it (handleMe, mcp.go's whoami,
// api_auth.go's login/register responses) only ever reports it, never
// enforces on it; RoleOf and requireProject never consult it at all (see
// ProjectScope's own doc comment in api_projects.go). That is deliberate,
// not an oversight this task is quietly fixing: BootstrapFirstAdmin is the
// only mechanism in this plan that ever sets IsAdmin, so gating an
// endpoint on it is gating on "the one operator this instance was stood
// up by (or a colleague they've since promoted, once such a path exists)"
// — the same authority that would otherwise be reaching for psql. See
// api_invites.go's own package doc comment above handleCreateInstanceInvite
// for why account-only invite administration is scoped to that authority
// specifically, rather than to any project's own owner.
func requireAdminCaller(w http.ResponseWriter, caller Caller) bool {
	if !requireHumanCaller(w, caller) {
		return false
	}
	if !caller.IsAdmin {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only an instance admin may perform this action")
		return false
	}
	return true
}

// inviteResponse is the wire shape of one outstanding invite — the single
// shape every listing and creation response in this file builds from, by
// embedding, never by hand-writing a second map that happens to carry the
// same fields. A quality review caught exactly that drift on this task's
// first pass: writeCreatedInvite built its own map literal instead of
// using this type, and it observably differed (the map always emitted
// "email", "project_id" and "role" as null where this type's `omitempty`
// tags drop them) — the exact divergence between the two surfaces this
// type exists to prevent, arriving inside a single file. See
// writeCreatedInvite below for how creation now reuses this type instead
// of re-declaring its fields.
//
// It carries what InviteSummary carries, including CreatedBy — never a
// token or a hash: unlike api_tokens.go's apiTokenResponse, an invite has
// no hint field at all (identity.InviteSummary's own doc comment says
// so), so once the clear value in a creation response is gone, it is
// gone; the only remedy for a lost link is to revoke this row (findable
// by CreatedBy, on a game with several owners) and mint a new one.
//
// Revoked mirrors CreatedBy in why it exists: RevokeInvite and
// RevokeProjectInvite both revoke by setting expires_at to now() rather
// than deleting the row (their own doc comments explain why — the audit
// trail), which means a revoked invite keeps appearing in this listing,
// indistinguishable from a still-live one by eye, until
// PruneExpiredInvites next runs. Revoked is computed here, not stored: it
// is true exactly when ExpiresAt is no longer in the future, which is
// also true of an invite that simply ran out its own TTL rather than
// being revoked — RevokeInvite's own mechanism makes those two states
// identical at the data level (see that method's doc comment), so this
// field answers "is this row still redeemable", not "did an admin
// deliberately pull it", the same question RedeemInvite itself answers
// with ErrInviteExpired for both cases (see this task's plan corrections
// for why that response is left undifferentiated on purpose).
type inviteResponse struct {
	ID        uuid.UUID  `json:"id"`
	Email     *string    `json:"email,omitempty"`
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
	Role      *string    `json:"role,omitempty"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	Revoked   bool       `json:"revoked"`
}

func inviteResponseFrom(inv identity.InviteSummary) inviteResponse {
	return inviteResponse{
		ID:        inv.ID,
		Email:     inv.Email,
		ProjectID: inv.ProjectID,
		Role:      inv.Role,
		CreatedBy: inv.CreatedBy,
		CreatedAt: inv.CreatedAt,
		ExpiresAt: inv.ExpiresAt,
		Revoked:   !inv.ExpiresAt.After(time.Now()),
	}
}

// redeemPath renders the fragment-carried redemption link RedeemInvite's
// own caller is expected to hand out — see login.html/app.js's own
// handling of window.location.hash for why the token lives in the
// fragment (never sent to the server, never logged by an intermediate
// proxy) rather than a query parameter. Returned as a path, not an
// absolute URL: this package never learns its own externally-visible
// scheme or host (there is no Config field for it), and a relative path
// is exactly what an admin pastes after their instance's own address, or
// what a client-side fetch already resolves against window.location
// today (safeReturnPath, app.js).
func redeemPath(token string) string {
	return "/login#invite=" + token
}

// createdInviteResponse is a creation response: everything
// inviteResponse already says about the row, plus the clear token and
// its redemption path, shown exactly once. Embedding inviteResponse
// (rather than a second, hand-written field list) is the fix for the
// drift this file shipped with on its first pass — see inviteResponse's
// own doc comment.
type createdInviteResponse struct {
	inviteResponse
	Token      string `json:"token"`
	RedeemPath string `json:"redeem_path"`
}

func writeCreatedInvite(w http.ResponseWriter, token string, summary identity.InviteSummary) {
	writeJSON(w, http.StatusCreated, createdInviteResponse{
		inviteResponse: inviteResponseFrom(summary),
		Token:          token,
		RedeemPath:     redeemPath(token),
	})
}

func writeCreateInviteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrInviteRequestInvalid):
		writeError(w, http.StatusUnprocessableEntity, errCodeInviteInvalid, err.Error())
	default:
		slog.ErrorContext(r.Context(), "create invite failed", "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not create the invite")
	}
}

// createInstanceInviteRequest is the body POST /api/invites accepts. It
// deliberately has no project_id or role field: the instance-wide
// admin surface can never grant project membership, not merely by a
// validation this handler happens to apply, but by construction — there
// is nowhere in this type for a client to put one, so nothing decoded
// from it can ever reach identity.InviteRequest.ProjectID or .Role. A
// caller who wants an invite that also grants a game and a role uses
// POST /api/games/{game}/invites instead (handleCreateProjectInvite,
// below), gated on that game's own owner role rather than on
// Caller.IsAdmin — see requireAdminCaller's own doc comment for why those
// are deliberately two different authorities, not one relaxed into the
// other.
type createInstanceInviteRequest struct {
	Email string `json:"email"`
}

// handleCreateInstanceInvite mints an account-only invite: no game, no
// role, optionally bound to an email. This is the surface that answers
// this task's own acceptance question — a fresh instance with zero games
// and exactly one user (the bootstrap admin) can still admit its second
// human, entirely through this endpoint, with no game needing to exist
// first and no operator ever opening psql. Admin-only (requireAdminCaller):
// see that function's own doc comment for why this is gated on
// Caller.IsAdmin rather than on any project's owner role — an account-only
// invite has no project to be an owner of.
func (s *Server) handleCreateInstanceInvite(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	var req createInstanceInviteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	userID := caller.UserID
	token, summary, err := s.opts.Identity.CreateInvite(r.Context(), identity.InviteRequest{
		Email:     req.Email,
		CreatedBy: &userID,
	})
	if err != nil {
		writeCreateInviteError(w, r, err)
		return
	}
	writeCreatedInvite(w, token, summary)
}

// handleListInstanceInvites lists every outstanding account-only invite.
// Admin-only, and deliberately so even though every other listing
// endpoint in this package (handleListMembers, handleListTokens) is open
// to any member: identity.ListOutstandingInvites is already scoped in SQL
// to project_id IS NULL (see that query's own doc comment) specifically
// so this admin-only gate is enforcing a real boundary rather than a
// redundant one — nothing about it could accidentally leak a project's
// own invite roster to a non-member, but the account-only roster itself
// (which email addresses this instance's admin has invited, and when) is
// still not everyone's business, the same judgement call
// api_tokens.go's ListAPITokens made about a token's minter.
func (s *Server) handleListInstanceInvites(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	rows, err := s.opts.Identity.ListOutstandingInvites(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "list outstanding invites failed", "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not list invites")
		return
	}
	invites := make([]inviteResponse, len(rows))
	for i, row := range rows {
		invites[i] = inviteResponseFrom(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}

// handleRevokeInstanceInvite revokes one account-only invite by id.
// Admin-only, mirroring handleListInstanceInvites. Revoking an unknown id,
// or one that belongs to a project-bound invite, is the same silent
// no-op RevokeInvite's own doc comment establishes (mirroring
// handleRevokeToken's identical convention for tokens): a 404 that
// distinguished "no such invite" from "that invite is project-bound"
// would let this admin-only surface confirm the existence of a specific
// game's invite despite never being able to list one.
func (s *Server) handleRevokeInstanceInvite(w http.ResponseWriter, r *http.Request, caller Caller) {
	if !requireAdminCaller(w, caller) {
		return
	}
	inviteID, err := uuid.Parse(r.PathValue("invite"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such invite")
		return
	}
	if err := s.opts.Identity.RevokeInvite(r.Context(), inviteID); err != nil {
		slog.ErrorContext(r.Context(), "revoke invite failed", "invite_id", inviteID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not revoke the invite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireProjectOwner combines requireHumanCaller with the owner-only
// gate shared by handleCreateProjectInvite, handleListProjectInvites and
// handleRevokeProjectInvite. A quality review found the human-caller
// check and the role comparison repeated verbatim across all three, with
// three slightly different messages the only thing that varied — three
// edit sites for one gate, the exact drift risk inviteResponse's own doc
// comment names for the response shape above. action names the specific
// thing refused ("invite someone into a game", "list this game's
// invites", "revoke this game's invites") so each call site keeps its
// own wording; the check itself is now written once. See
// handleCreateProjectInvite's own doc comment for why owner, not
// editor-or-owner the way handleCreateToken is gated.
func requireProjectOwner(w http.ResponseWriter, caller Caller, scope ProjectScope, action string) bool {
	if !requireHumanCaller(w, caller) {
		return false
	}
	if !roles.AtLeast(roles.Role(scope.Role), roles.Owner) {
		writeError(w, http.StatusForbidden, errCodeForbidden, "only an owner may "+action)
		return false
	}
	return true
}

// createProjectInviteRequest is the body POST /api/games/{game}/invites
// accepts. There is no project_id field: the game comes from the URL,
// resolved by requireProject before this handler ever runs, the same
// convention every other project-scoped write in this package (
// handleCreateToken, handleChangeRole) already follows.
type createProjectInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// handleCreateProjectInvite mints an invite bound to this game and a
// role. Owner-only — not editor-or-owner the way handleCreateToken is —
// because an invite is a deferred grant of membership: redeeming it runs
// the same UpsertMembership handleChangeRole runs directly, and
// handleChangeRole restricts *every* role change, including a lateral
// viewer-to-viewer no-op, to an owner. An invite that could grant a role
// up to and including owner has to be gated at the ceiling of what it can
// grant, not the floor of what a particular request happens to ask for —
// an editor-gated check that only rejected an owner-role *request* would
// still let an editor mint a viewer-role invite, silently doing a slice
// of what handleChangeRole reserves entirely for owners. This directly
// answers this task's own "should granting owner require an owner"
// question: yes, and by construction — there is no per-role branch here
// to get wrong, because every request this handler accepts already
// required an owner regardless of which role it names.
func (s *Server) handleCreateProjectInvite(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireProjectOwner(w, caller, scope, "invite someone into a game") {
		return
	}
	var req createProjectInviteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	projectID := scope.ProjectID
	userID := caller.UserID
	token, summary, err := s.opts.Identity.CreateInvite(r.Context(), identity.InviteRequest{
		Email:     req.Email,
		ProjectID: &projectID,
		Role:      req.Role,
		CreatedBy: &userID,
	})
	if err != nil {
		writeCreateInviteError(w, r, err)
		return
	}
	// Never the clear token: the same reasoning apiTokenResponse's own
	// doc comment gives for a minted token event, applied to an invite —
	// a shown-once secret has no business leaving this one response.
	// MinRole roles.Owner matches handleListProjectInvites exactly; see
	// eventInviteCreated's own doc comment (publish.go).
	s.publish(projectID, eventInviteCreated, roles.Owner, map[string]any{
		"id": summary.ID, "role": req.Role, "email": summary.Email,
	})
	writeCreatedInvite(w, token, summary)
}

// handleListProjectInvites lists every outstanding invite bound to this
// game. Owner-only, matching handleCreateProjectInvite: an outstanding
// invite is a pending grant of membership, not yet a real one, and this
// package already treats every *actual* role change (handleChangeRole) as
// owner-only business — an editor who could list a pending owner-role
// invite would learn about a promotion nobody has actually granted them
// visibility into anywhere else. This is stricter than handleListMembers
// (open to any member) on purpose, not an oversight: membership is a
// settled fact every member already benefits from seeing (who else can
// touch this game), while a pending invite is closer to
// handleListTokens/handleListMembers' owner-only siblings in what it
// exposes about the game's future, not its present.
func (s *Server) handleListProjectInvites(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireProjectOwner(w, caller, scope, "list this game's invites") {
		return
	}
	rows, err := s.opts.Identity.ListOutstandingInvitesForProject(r.Context(), scope.ProjectID)
	if err != nil {
		slog.ErrorContext(r.Context(), "list outstanding project invites failed", "project_id", scope.ProjectID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not list invites")
		return
	}
	invites := make([]inviteResponse, len(rows))
	for i, row := range rows {
		invites[i] = inviteResponseFrom(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}

// handleRevokeProjectInvite revokes one invite bound to this game.
// Owner-only, matching handleCreateProjectInvite and
// handleListProjectInvites. Revoking an unknown id, or one that belongs
// to a different game (or to no game at all), is the same silent no-op
// handleRevokeToken's own doc comment establishes for tokens, and for the
// identical reason: distinguishing the cases would let an owner of one
// game probe whether some other id belongs to a different one.
func (s *Server) handleRevokeProjectInvite(w http.ResponseWriter, r *http.Request, caller Caller, scope ProjectScope) {
	if !requireProjectOwner(w, caller, scope, "revoke this game's invites") {
		return
	}
	inviteID, err := uuid.Parse(r.PathValue("invite"))
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, "no such invite")
		return
	}
	if err := s.opts.Identity.RevokeProjectInvite(r.Context(), scope.ProjectID, inviteID); err != nil {
		slog.ErrorContext(r.Context(), "revoke project invite failed", "project_id", scope.ProjectID, "invite_id", inviteID, "error", err)
		writeError(w, http.StatusInternalServerError, errCodeInternal, "could not revoke the invite")
		return
	}
	// Published even for the no-op case, the same convention
	// eventTokenRevoked follows and for the identical reason (this
	// handler's own doc comment above): the two are indistinguishable
	// from this event alone, which is fine, since a client just drops a
	// row matching this id.
	s.publish(scope.ProjectID, eventInviteRevoked, roles.Owner, map[string]any{"id": inviteID})
	w.WriteHeader(http.StatusNoContent)
}
