package web

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes into s.hub (realtime/hub.go). Every
// one of these is fired from the handler that performs the mutation, not
// from internal/projects or internal/identity — see this file's own
// package-level placement for why: those two services have no dependency
// on realtime today, are exercised directly by their own tests with no
// hub in sight, and (with one exception, documented on eventMemberUpdated
// and eventInviteRedeemed below) every mutation they expose is reached
// exclusively through a handler in this package, which by the time it
// calls s.publish has already observed the service call return without
// error. Since every one of these services commits its own transaction
// internally (Service.withTx, projects.go and identity.go) before
// returning, "the service call returned nil" and "the write is durable"
// are the same moment here for any single write.
const (
	// eventGameDeleted fires once, from handleDeleteGame, after
	// projects.Delete returns successfully. Every subscriber of the
	// deleted game receives it regardless of role (MinRole is empty) and
	// regardless of HumanOnly (false, unlike every other kind below): a
	// token caller's own credential is about to stop resolving too, once
	// the cascade this event announces finishes removing the membership
	// it depends on (ProjectScope's own doc comment, api_projects.go),
	// and it is already holding this exact connection open — there is no
	// REST listing this mirrors and no reason to withhold the one signal
	// that connection will ever get about why it is about to stop
	// working. The payload carries nothing beyond the empty object
	// handleEvents already treats a nil Payload as (realtime.Event.Payload's
	// own doc comment): a client's only correct reaction to "this game
	// is gone" is to stop referencing it, never to patch its local state
	// from a deletion event's fields, so there is nothing a fuller
	// payload would let a client do that a bare signal does not already
	// cover.
	eventGameDeleted = "game.deleted"

	// eventMemberUpdated fires from handleChangeRole after
	// projects.SetRole succeeds, and from handleRegister
	// (internal/web/api_auth.go) after identity.RedeemInvite grants
	// project membership as a side effect of account creation — see this
	// constant's second doc paragraph below for why that second call
	// site exists and what it means for Decision 1's own reasoning
	// above. SetRole itself upserts — "grant a role to someone with none
	// yet" and "change an existing member's role" are the same database
	// operation (its own doc comment explains why) — so this one event
	// kind covers both a new member appearing and an existing one's role
	// changing.
	eventMemberUpdated = "member.updated"

	// eventMemberRemoved fires from handleRemoveMember after
	// projects.RemoveMember succeeds. MinRole empty and HumanOnly true,
	// for the same reasons as eventMemberUpdated: who left a game is
	// exactly the kind of fact handleListMembers already answers for a
	// viewer today, and exactly the kind of fact requireHumanCaller
	// already refuses a token caller. The payload (user_id only) carries
	// no ordering hazard the way eventMemberUpdated's role field did — a
	// removal is not a value that can itself go stale, only a row that
	// either exists or does not — but it stays minimal anyway, matching
	// its sibling.
	eventMemberRemoved = "member.removed"

	// eventTokenMinted and eventTokenRevoked fire from handleCreateToken
	// and handleRevokeToken. Both are gated at MinRole roles.Editor,
	// deliberately *tighter* than handleListTokens' own role gate (none
	// — any member): that REST endpoint is open to any member (metadata
	// only, never a secret — apiTokenResponse's own doc comment), so a
	// viewer who explicitly asks can already see the same fields this
	// event carries. What the role gate withholds is not the data but
	// the *push*: an agent credential being minted or revoked is exactly
	// the kind of ambient churn a viewer's open browser tab has no
	// standing reason to be notified of in real time just for having a
	// tab open, as opposed to the deliberate act of opening the token
	// list to go check. This is a considered inconsistency with the REST
	// endpoint's own role openness, not an oversight — a client author
	// relying on this stream to keep a token list current should know a
	// viewer's copy will drift silently while every other list on the
	// page stays live, and must keep polling GET .../tokens (or refetch
	// on its own schedule) for that one list if a viewer-facing UI wants
	// it current at all.
	eventTokenMinted  = "token.minted"
	eventTokenRevoked = "token.revoked"

	// eventInviteCreated and eventInviteRevoked fire from
	// handleCreateProjectInvite and handleRevokeProjectInvite — the
	// project-scoped invite endpoints, never the account-only ones
	// (handleCreateInstanceInvite, handleRevokeInstanceInvite), which
	// have no project id to publish against at all. Gated at MinRole
	// roles.Owner, matching handleListProjectInvites exactly (both
	// require an owner already): a pending invite is a future grant of
	// membership, up to and including ownership, and this package
	// already treats that as owner-only business to *read*, not only to
	// create — publishing it to every member regardless of role would
	// leak more than the REST surface it mirrors ever does. HumanOnly is
	// true too, though it adds nothing MinRole owner does not already
	// exclude on its own: a token caller's Role is always roles.Editor
	// (never Owner), so no token subscriber could ever have met this
	// MinRole in the first place. Set anyway, for the same reason every
	// kind in this file that mirrors a requireHumanCaller-gated REST
	// endpoint sets it: declaring the real rule (this is human-only
	// business) rather than relying on an unrelated gate to have the
	// same effect by coincidence.
	eventInviteCreated = "invite.created"
	eventInviteRevoked = "invite.revoked"

	// eventInviteRedeemed fires from handleRegister
	// (internal/web/api_auth.go), not from a handler in this file, the
	// moment identity.RedeemInvite reports a project-bound invite was
	// just consumed. This is Decision 1's one real exception: redeeming
	// an invite is the second producer of project membership besides
	// handleChangeRole's own SetRole call, and the handler that runs it
	// (handleRegister) is unauthenticated and project-less — it has
	// neither a Caller nor a ProjectScope to reach s.publish through the
	// way every other call site in this file does. identity.RedeemInvite
	// was extended to report the consumed invite's id and, when the
	// invite was project-bound, that project's id, specifically so
	// handleRegister could still call s.publish directly with the
	// project id RedeemInvite just handed back, keeping the hub itself
	// out of internal/identity — see RedeemInvite's own doc comment for
	// why that shape was chosen over giving identity.Service a *realtime.Hub
	// field.
	eventInviteRedeemed = "invite.redeemed"
)

// publish assigns a project's event into s.hub, the single point every
// caller in this package that announces project-scoped state routes
// through, so the event-kind, role-gating and human-only decisions
// documented on the constants above live in one place rather than being
// re-decided, and re-typed, at each call site. humanOnly is passed
// explicitly, not inferred from kind, so a call site's own gating
// decision is visible at the call site, not only in a comment three
// files away.
func (s *Server) publish(projectID uuid.UUID, kind string, minRole roles.Role, humanOnly bool, payload any) {
	s.hub.Publish(realtime.Event{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   string(minRole),
		HumanOnly: humanOnly,
		Payload:   payload,
	})
}
