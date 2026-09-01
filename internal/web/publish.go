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
//
// That is not, however, a guarantee about the *order* two different
// writes are announced in. Seq is assigned at publish time
// (realtime.Hub.Publish), not at commit time, and there is a real window
// between a handler's transaction committing and its call to s.publish
// reaching the hub's lock — nothing serializes two concurrent handler
// goroutines' publish calls to happen in the same order their
// transactions committed in. Two concurrent role changes for the same
// member can therefore commit in one order and publish in the other, and
// this produces no gap at all (both events exist, both got a Seq, both
// were "supposed" to happen) — sseSawGap (events.go) has nothing to
// detect here, because nothing was dropped. A payload that told a client
// "this member's role is now X" could therefore tell it the wrong X,
// stably, with no signal anything is wrong. eventMemberUpdated's own doc
// comment below is this file's answer: carry no field whose value could
// itself go stale from reordering, only the identity of what changed,
// and let the client re-ask the database (which has no such race — a
// second write always physically follows a first) for the current truth.
//
// A future service that owns its own *realtime.Hub reference directly
// (the metamodel plan's entity.* and relation.* events, per Options.Hub's
// own doc comment in server.go) is free to publish from its own layer
// instead; this package's choice does not bind that one.
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
	//
	// The payload carries only user_id — never role. A quality review
	// caught this file's first version doing the opposite (carrying the
	// new role directly, so a client could patch its own list without a
	// round trip) and named the real hazard: this file's own package doc
	// comment above explains why two concurrent role changes for the
	// same member can publish in the opposite order their transactions
	// committed in, with no gap for sseSawGap to catch. A payload
	// carrying a role could therefore leave a client displaying a role
	// the database no longer agrees with, silently and stably. Carrying
	// only the identity of what changed turns this event into an
	// invalidation, not a patch: the client's only correct reaction is
	// "re-fetch this member (or the whole list) over REST", the same
	// re-fetch every other kind in this file already expects a client to
	// fall back on for anything this stream cannot safely tell it
	// directly.
	//
	// HumanOnly is true: handleListMembers, the REST endpoint this event
	// otherwise mirrors, is gated by requireHumanCaller — a token caller
	// cannot list members over REST at all, so a token subscriber must
	// not learn about membership churn over this stream either. MinRole
	// is still empty: among *human* subscribers, this is exactly as open
	// as handleListMembers itself (every member, viewer included).
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
	//
	// HumanOnly is also true, and here it is not redundant with MinRole:
	// resolveProjectScope (api_projects.go) always grants a token caller
	// roles.Editor, which satisfies MinRole editor on its own — a
	// quality review found exactly this gap live, an agent's token
	// receiving another agent's token.minted event, hint and label
	// included, over a stream the minting token's own bearer could not
	// have read the equivalent listing through (handleListTokens is
	// itself gated by requireHumanCaller). HumanOnly closes that
	// specifically; MinRole alone cannot express "no token caller,
	// regardless of role" (see realtime.Event.HumanOnly's own doc
	// comment).
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
	//
	// MinRole roles.Owner and HumanOnly true, matching
	// eventInviteCreated/eventInviteRevoked exactly: a pending invite
	// disappearing because it was redeemed is exactly as owner-only a
	// fact as a pending invite disappearing because it was revoked, and
	// handleListProjectInvites' own owner-only gate does not
	// distinguish the two either — a redeemed invite simply stops
	// appearing (RevokeInvite/RedeemInvite share no separate "redeemed"
	// listing state; inviteResponse's own doc comment explains the
	// related `revoked` computed field). The payload carries only the
	// invite's id, like eventInviteRevoked: enough for an owner's
	// pending-invite list to drop the row, nothing about the new
	// member's role (that arrives, separately, as eventMemberUpdated's
	// own invalidation on the same publish call).
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
