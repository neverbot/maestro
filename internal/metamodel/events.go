package metamodel

import "github.com/neverbot/maestro/internal/roles"

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
//
// They live here, together, for the same reason internal/web/publish.go
// collects its own: the gating of an event is a decision about who may
// learn a fact, and a decision made three call sites apart is a decision
// re-made three times. Tasks 4, 5 and 6 add entity.*, relation-type and
// relation kinds to this file and state their own gating here rather than
// copying whatever the neighbouring call site happened to pass.
//
// Two things every payload in this package obeys, argued at length in
// internal/web/publish.go's package comment and repeated here only
// because a reader of this file should not have to find it: a payload
// carries the *identity* of what changed and never a value a client could
// treat as current (publication order is not commit order, so a payload
// holding a label could stably say the wrong label with nothing to signal
// it), and publication happens after the transaction commits, never
// inside it (Service.publish's own doc comment).
const (
	// eventTypeUpserted and eventTypeRemoved fire from UpsertEntityType
	// and RemoveEntityType once their transaction has committed.
	//
	// **HumanOnly is false, deliberately, and it is the considered
	// answer here rather than the default.** There is a precedent, and
	// it is the strongest part of the argument: internal/web's
	// eventGameDeleted (publish.go, published from api_projects.go's
	// handleDeleteGame) sets exactly this gating, and argues it at
	// length — a token subscriber gets that event regardless of role and
	// regardless of HumanOnly because there is no REST listing it
	// mirrors and no reason to withhold from a connection the one signal
	// it will ever get. The same two conditions hold here. What follows
	// is why they hold, since the rest of internal/web goes the other
	// way. Its member, token and invite events all set
	// it true, because each mirrors a REST listing that requireHumanCaller
	// refuses a token caller outright — a token subscriber learning about
	// membership churn would be reading, over the stream, a fact it
	// could not have asked for over REST. An entity type is the opposite
	// case in every respect. It is not instance administration, it is the
	// game's own vocabulary, and Task 7 mounts types.list and
	// types.upsert on MCP *for agents specifically*: an agent that may
	// read every type on demand loses nothing by being told one changed.
	// It is also the subscriber with the most to lose from not being
	// told — a seeding agent's next entities.upsert is judged against the
	// schema that just moved, so withholding the invalidation buys no
	// confidentiality and costs a round of schema_violation errors an
	// agent cannot explain. Copying the member-event shape here would
	// have been inertia, not a rule — and eventGameDeleted shows the
	// project already refuses that inertia where the reasoning does not
	// apply.
	//
	// MinRole is empty — every member of the game, viewer included.
	// Reading types is not role-gated anywhere: a viewer's browser
	// renders the type list, so gating the invalidation above viewer
	// would leave exactly the reader who cannot re-fetch on demand
	// looking at a list that silently drifts. Unlike the token events,
	// there is no "the push is more than the data" argument to make: a
	// type changing is the game's content changing, which is the whole
	// reason a designer has the page open.
	eventTypeUpserted = "type.upserted"
	eventTypeRemoved  = "type.removed"

	// eventEntityUpserted and eventEntityRemoved fire from UpsertEntity,
	// UpsertEntities and RemoveEntity once their transaction has
	// committed.
	//
	// The gating lands on the same two values as the type events, and the
	// reasoning is restated rather than inherited because the two are not
	// the same fact. A type is a game's vocabulary; an entity is the
	// content itself, which is a much larger and much noisier stream.
	//
	// MinRole is empty — every member of the game, viewer included.
	// Nothing about reading entities is role-gated: a viewer's browser
	// renders the entity list, and gating the change above viewer would
	// leave exactly the reader who cannot re-fetch on demand watching a
	// list that silently drifts. A viewer who may read a row on demand
	// loses no confidentiality by being told it moved, and the payload
	// carries no value beyond the identity they could already fetch.
	//
	// HumanOnly is false. Task 7 mounts entities.list and entities.upsert
	// on MCP for agents, so an agent may already read every one of these
	// rows whenever it likes; withholding the push buys nothing. It also
	// costs the most here of anywhere in this package: a seeding agent's
	// next write is judged against a schema and a key space another
	// writer may just have moved, and the failure it gets instead is a
	// schema_violation or a version_conflict it cannot explain. This is
	// the opposite case from internal/web's member, token and invite
	// events, which set HumanOnly true because each mirrors a REST
	// listing requireHumanCaller refuses a token caller outright.
	//
	// **The known consequence, recorded rather than gated around.** These
	// events fire once per row, so a 400-row seed puts 400 events on
	// every subscription — the same volume a partial batch's own
	// per-item commits produce, and the reason UpsertEntities publishes
	// identities rather than a count. Role gating would not fix that; it
	// would only starve the viewer this decision exists to serve.
	//
	// What a subscriber actually receives from a burst that size is not
	// 400 events, and the difference matters to the argument above.
	// realtime.Hub buffers 64 events per subscription and drops the rest
	// (subscriberBuffer, hub.go); each drop leaves a gap in that
	// subscription's own Seq, and internal/web/events.go turns the gap
	// into a synthetic "resync" — "you missed something, go and re-read"
	// — rather than silent loss. So the failure mode is degradation, not
	// disappearance, which is what makes shipping the per-row events
	// safe. But it cuts both ways: the seeding agent named above as the
	// subscriber with the most to lose is precisely the one whose own
	// burst overflows the buffer, so what it gets back is a resync, not
	// the per-row warning that would have explained its next
	// schema_violation. The warning is real for a subscriber watching
	// *someone else's* burst, and for the designer's browser; for the
	// agent driving the batch it only becomes real once the bursts are
	// coalesced.
	//
	// Coalescing therefore belongs to the transport, where the
	// subscriber and its backlog are visible, and **Tasks 6 and 7 own
	// it** — as the thing that makes the benefit claimed here true, not
	// as a polish item.
	eventEntityUpserted = "entity.upserted"
	eventEntityRemoved  = "entity.removed"
)

// typeEventMinRole and typeEventHumanOnly are the gating decided above,
// named so the two call sites read as the decision rather than as two
// bare literals a later edit could drift apart.
const (
	typeEventMinRole   = roles.Role("")
	typeEventHumanOnly = false
)

// entityEventMinRole and entityEventHumanOnly are the gating decided
// above for entity.upserted and entity.removed. They are their own
// constants rather than an alias of the type-event pair: the two
// decisions happen to agree today, and naming one in terms of the other
// would make a later change to either silently change both.
const (
	entityEventMinRole   = roles.Role("")
	entityEventHumanOnly = false
)
