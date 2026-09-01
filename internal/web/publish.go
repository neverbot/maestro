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
// hub in sight, and every mutation they expose is already reached
// exclusively through a handler in this package, which by the time it
// calls s.publish has already observed the service call return without
// error. Since every one of these services commits its own transaction
// internally (Service.withTx, projects.go and identity.go) before
// returning, "the service call returned nil" and "the write is durable"
// are the same moment here — there is no separate "after commit" step to
// get right or wrong, and no risk of announcing a change that a
// still-open transaction might yet roll back.
//
// A future service that owns its own *realtime.Hub reference directly
// (the metamodel plan's entity.* and relation.* events, per Options.Hub's
// own doc comment in server.go) is free to publish from its own layer
// instead; this package's choice does not bind that one.
const (
	// eventGameDeleted fires once, from handleDeleteGame, after
	// projects.Delete returns successfully. Every subscriber of the
	// deleted game receives it regardless of role (MinRole is empty):
	// the game is gone for everyone who was watching it, not gone
	// selectively by rank, and every one of them needs to stop trusting
	// the stream (and the REST API) for this project id the same
	// instant, not find out only when their own heartbeat re-check next
	// happens to fail (up to sseHeartbeatInterval later). The payload
	// carries nothing beyond the empty object handleEvents already
	// treats a nil Payload as (realtime.Event.Payload's own doc
	// comment): a client's only correct reaction to "this game is gone"
	// is to stop referencing it, never to patch its local state from a
	// deletion event's fields, so there is nothing a fuller payload
	// would let a client do that a bare signal does not already cover.
	eventGameDeleted = "game.deleted"

	// eventMemberUpdated fires from handleChangeRole after
	// projects.SetRole succeeds. SetRole itself upserts — "grant a role
	// to someone with none yet" and "change an existing member's role"
	// are the same database operation (its own doc comment explains
	// why) — so this one event kind covers both a new member appearing
	// and an existing one's role changing; a client applies it the same
	// way either time, as an upsert into its own member list. MinRole is
	// empty: handleListMembers, the REST endpoint this event mirrors, is
	// open to every member regardless of role (Task 8's own design), so
	// gating the live feed tighter than the page a viewer can already
	// load would only make the viewer's browser tab lie by omission
	// until its next manual refresh, without withholding anything a
	// viewer cannot already read on demand.
	eventMemberUpdated = "member.updated"

	// eventMemberRemoved fires from handleRemoveMember after
	// projects.RemoveMember succeeds. MinRole empty, for the same reason
	// as eventMemberUpdated: who left a game is exactly the kind of fact
	// handleListMembers already answers for a viewer today.
	eventMemberRemoved = "member.removed"

	// eventTokenMinted and eventTokenRevoked fire from handleCreateToken
	// and handleRevokeToken. Both are gated at MinRole roles.Editor,
	// deliberately *tighter* than handleListTokens itself: that REST
	// endpoint is open to any member (metadata only, never a secret —
	// apiTokenResponse's own doc comment), so a viewer who explicitly
	// asks can already see the same fields this event carries. What the
	// role gate withholds is not the data but the *push*: an agent
	// credential being minted or revoked is exactly the kind of ambient
	// churn a viewer's open browser tab has no standing reason to be
	// notified of in real time just for having a tab open, as opposed to
	// the deliberate act of opening the token list to go check. This is
	// a considered inconsistency with the REST endpoint's own openness,
	// not an oversight: it does not reduce what a viewer can learn
	// (still reachable by GET at any time), only how eagerly it is
	// pushed at them.
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
	// leak more than the REST surface it mirrors ever does.
	eventInviteCreated = "invite.created"
	eventInviteRevoked = "invite.revoked"
)

// publish assigns a project's event into s.hub, the single point every
// handler in this package that mutates project-scoped state routes
// through, so the event-kind and role-gating decisions documented on the
// constants above live in one place rather than being re-decided, and
// re-typed, at each call site.
func (s *Server) publish(projectID uuid.UUID, kind string, minRole roles.Role, payload any) {
	s.hub.Publish(realtime.Event{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   string(minRole),
		Payload:   payload,
	})
}
