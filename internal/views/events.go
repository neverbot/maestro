package views

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
//
// They live here, together, for the reason internal/metamodel/events.go,
// internal/markdown/events.go and internal/web/publish.go all give: the
// gating of an event is a decision about who may learn a fact, and a
// decision made at two call sites is a decision made twice.
//
// Two rules every payload here obeys, and they are the reason the
// payload below carries what it does. A payload carries the *identity*
// of what changed and never a value a client could then treat as
// current: publication order is not commit order, so a payload holding a
// view's query document could stably tell a client the wrong query with
// nothing to signal it. And publication happens after the transaction
// commits, never inside it — Service.publish's own doc comment, and
// TestNoViewEventIsPublishedWhenTheCommitFails, which is the one
// placement a refusal test cannot catch.
const (
	// eventViewUpserted and eventViewRemoved fire from UpsertView and
	// RemoveView once their transaction has committed.
	//
	// **The gating is argued for views rather than copied from the
	// domain next door**, which is the whole reason this file exists;
	// that both other domains landed on the same two values is a
	// conclusion here and not a premise.
	//
	// **MinRole is empty — every member of the game, viewer included.**
	// The question a MinRole answers is whether the subscriber could
	// have read the thing on demand, and a view is *derived* content:
	// it names types and fields a viewer may already list, it draws
	// entities and relations a viewer's browser already renders, and
	// nothing in this domain gates opening one above viewer. Gating the
	// invalidation above viewer would therefore withhold the signal from
	// exactly the reader who has no other way to know — a browser
	// holding a picture drawn from a query that has just been rewritten
	// under it — while withholding nothing that reader could not fetch
	// a moment later.
	//
	// **HumanOnly is false**, and this is the half that would be wrong
	// by default. internal/web's member, token and invite events set it
	// true because each mirrors a REST listing requireHumanCaller
	// refuses a token caller outright; a view inverts that in both
	// directions. Task 15 mounts views.list, views.get and views.run on
	// MCP for agents specifically, so an agent may already read every
	// one of these rows whenever it likes and withholding the push buys
	// no confidentiality at all. It also costs the most here: an agent
	// composing against a view — validating, upserting, running it in a
	// loop — is the subscriber whose next call is judged against a
	// document another writer may just have moved, and a version_conflict
	// it cannot explain is what it gets instead of the invalidation.
	eventViewUpserted = "view.upserted"
	eventViewRemoved  = "view.removed"

	// eventViewPositions fires from SetPositions and from ClearPositions
	// once their write has landed.
	//
	// **It is a third kind, and Task 15 decided it rather than inheriting
	// the silence.** Task 13 published nothing for a drag, on the ground
	// that only two kinds were registered and a third would have reached
	// nobody. That ground is gone — this task registers the kinds — and the
	// argument on the other side is the one this file's header already
	// makes for an invalidation: a browser holding a picture has no other
	// way to learn the picture changed, and a drag is exactly that
	// situation, with the same reader and no error anywhere in between. Two
	// designers in one shared session would otherwise see different
	// arrangements indefinitely, each convinced theirs is the arrangement.
	//
	// **ClearPositions publishes it too**, both in its whole-view form and
	// its per-entity one: a wiped arrangement is the invalidation that
	// matters most, and a client that learned about drags and not about
	// clears would hold the one picture nobody can see any more.
	//
	// The gating is view.upserted's — the same MinRole and the same
	// HumanOnly, through the same two constants — because a caller who may
	// not learn that a view changed may not learn that its nodes moved
	// either, and the question a MinRole answers ("could this subscriber
	// have read the thing on demand") has the same answer for a position as
	// for the query that draws it: views.get and views.run are open to
	// every member of the game.
	eventViewPositions = "view.positions"
)

// viewEventMinRole and viewEventHumanOnly are the gating decided above,
// named so each of the four call sites reads as the decision rather than
// as two bare literals a later edit could drift apart. They are their own
// constants and not an alias of another domain's pair: the three
// decisions happen to agree today, and naming one in terms of another
// would make a later change to either silently change both.
const (
	viewEventMinRole   = roles.Role("")
	viewEventHumanOnly = false
)

// viewEvent is the payload of view.upserted and view.removed.
//
// **Identity only**, for the reason this file's header gives: id and
// key address the row, and a client that wants the query re-reads it.
//
// Version is in the payload and is the one field worth defending, since
// it is a value rather than an address. It is here because it is the
// token a caller's *next* write must carry, so a subscriber that has the
// view open learns from one event both that it moved and what to merge
// onto — and because it is monotonic, which no other value on the row
// is. What it is not is a snapshot a client may trust as current:
// publication order is not commit order, so two edits in flight can
// arrive as 3 then 2, and a client that renders a version rather than
// re-reading one will eventually render the older of the two. The rule
// stands unchanged: re-read.
type viewEvent struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Version int32     `json:"version"`
}

// viewPositionsEvent is the payload of view.positions: the view's id and
// its key, and nothing else.
//
// **It carries no coordinates, and that is the rule rather than a
// judgement made here.** This file's header says a payload never carries
// a value a client could then treat as current, and an arrangement is
// exactly such a value — publication order is not commit order, so two
// drags in flight can arrive in the other order and leave a client
// rendering the earlier one for good. The reader re-reads through
// views.get or views.run, which is also what keeps this payload from
// handing an arrangement to a subscriber whose own read would have been
// refused: the re-read is scoped, the event is not a back door around it.
//
// **It carries no version either**, and that is not an oversight of the
// pair above. A position write deliberately does not advance the view's
// version (positions.go says why), so a version on this payload would be
// a number that did not move for a change that did — which is worse than
// absent, because a subscriber merging on it would believe it had
// learned something.
type viewPositionsEvent struct {
	ID  uuid.UUID `json:"id"`
	Key string    `json:"key"`
}
