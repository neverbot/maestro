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
	// Fires once from handleDeleteGame, after projects.Delete succeeds.
	// Every subscriber gets it whatever their role, token callers
	// included: their credential is about to stop resolving and this is
	// the only signal that connection will get about why. The payload is
	// empty, because the only correct reaction to "this game is gone" is
	// to stop referencing it.
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

	// Gated at MinRole roles.Editor, tighter than the REST listing these
	// mirror, which any member may read. What the gate withholds is the
	// push and not the data: a viewer who asks still sees the same
	// metadata. A viewer's token list therefore drifts while every other
	// list on the page stays live, and a viewer-facing client has to
	// refetch that one on its own.
	eventTokenMinted  = "token.minted"
	eventTokenRevoked = "token.revoked"

	// Fired from the project-scoped invite endpoints, never the
	// account-only ones, which have no project to publish against.
	// MinRole roles.Owner matches the REST surface this mirrors: a
	// pending invite is a future grant of membership, which this package
	// already treats as owner-only to read. HumanOnly adds nothing a
	// token caller could pass anyway, and is set to declare the rule
	// rather than lean on another gate having the same effect.
	eventInviteCreated = "invite.created"
	eventInviteRevoked = "invite.revoked"

	// Fires from handleRegister, which is unauthenticated and
	// project-less: it has neither a Caller nor a ProjectScope to publish
	// through the way every other call site here does. RedeemInvite
	// reports the consumed invite's id and its project, so the handler
	// can publish with what it was just handed and the hub stays out of
	// internal/identity.
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
