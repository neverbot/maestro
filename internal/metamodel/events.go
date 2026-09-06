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

	// eventTypeRenamed fires from RenameEntityType once its transaction
	// has committed, under the same gating as its two neighbours: a
	// rename is a change to the game's vocabulary, which is exactly what
	// eventTypeUpserted's argument above is about, and a subscriber that
	// may read every type on demand loses nothing by being told one
	// changed handle.
	//
	// **It is a kind of its own rather than another type.upserted**, and
	// that is the whole reason it exists. Its payload is the only one in
	// this package that carries two spellings (typeRenameEvent), because
	// a subscriber holding the type under its old key cannot act on an
	// event that names only the new one — it has no way to know which of
	// its entries to drop. A `type.upserted` carrying only the new key
	// would leave a stale entry under the old key in every client that
	// caches by key, which is every client that renders a type list.
	eventTypeRenamed = "type.renamed"

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
	// **Task 7 settled this, and not by coalescing.** Two things
	// changed the question. The first is that coalescing cannot deliver
	// what this comment wanted from it: collapsing many events into one
	// necessarily throws away the per-row identity, so the coalesced
	// event says "entities changed, re-read" — which is precisely what
	// the resync already says. With a bounded buffer and no durable log
	// behind the hub, per-row payloads and burst-proof delivery are not
	// both available, and a change that renamed the resync would have
	// been the only thing on offer.
	//
	// The second is that the subscriber this comment worried about no
	// longer needs the stream for this. Task 7's entities.upsert answers
	// the batch synchronously with every row that landed — its key, its
	// id and its new version (BulkResult.Written) — so the agent driving
	// a four-hundred-row seed learns what happened from its own call and
	// not from a stream it is racing. What it would have learned from
	// the per-row events, it now has by the time the call returns.
	//
	// So the events below are for *other* subscribers: the designer's
	// browser, and an agent watching someone else's writes. For those,
	// overflow-to-resync is already the right behaviour — the correct
	// response to "four hundred things changed" is to re-read, not to
	// apply four hundred patches — and it is what they get.
	//
	// What remains open is not coalescing but batching in the SSE
	// writer: internal/web/events.go writes and flushes once per event,
	// and draining the channel into one write would shrink the window in
	// which the hub finds the buffer full. That is a transport
	// performance change, it alters no payload and no contract, and it
	// belongs to whoever owns the realtime surface — it is not owned
	// here and it is not what this comment used to promise.
	eventEntityUpserted = "entity.upserted"
	eventEntityRemoved  = "entity.removed"

	// eventRelationTypeUpserted and eventRelationTypeRemoved fire from
	// UpsertRelationType and RemoveRelationType once their transaction
	// has committed.
	//
	// The gating lands on the same two values as the type events, and it
	// is stated here rather than inherited because a relation type is not
	// an entity type: it is the game's *edge* vocabulary, including the
	// endpoint rules every later write is judged against.
	//
	// MinRole is empty — every member of the game, viewer included.
	// Reading relation types is not role-gated anywhere: a viewer's
	// browser renders the graph these types define, so gating the
	// invalidation above viewer would leave exactly the reader who cannot
	// re-fetch on demand looking at a graph that silently drifts.
	//
	// HumanOnly is false. Task 7 mounts relation_types.list and
	// relation_types.upsert on MCP for agents, so an agent may already
	// read every one of these rows on demand and withholding the push
	// buys nothing. It costs more here than anywhere else in this
	// package: an endpoint rule moving under a seeding agent turns its
	// next relations.upsert into an endpoint_type_mismatch it cannot
	// explain, and this is the one event that warns it.
	eventRelationTypeUpserted = "relation_type.upserted"
	eventRelationTypeRemoved  = "relation_type.removed"

	// eventRelationTypeRenamed is eventTypeRenamed for the edge
	// vocabulary, with the same payload and the same argument for being
	// a kind of its own; see it. The gating is this pair's, not that
	// pair's, for the reason all four gating constants below are four
	// and not two.
	eventRelationTypeRenamed = "relation_type.renamed"

	// eventRelationUpserted and eventRelationRemoved fire from
	// UpsertRelation, UpsertRelations and RemoveRelation once their
	// transaction has committed.
	//
	// MinRole empty and HumanOnly false again, and for the reasons the
	// entity events give: an edge is the game's content, a viewer's
	// browser renders it, and an agent that may read every edge on demand
	// loses no confidentiality by being told one moved. The payload
	// carries the edge's id, the stored spelling of its type key and both
	// endpoint ids — identity a subscriber can re-read from — and never
	// the edge's fields.
	//
	// **The same known consequence as the entity events, and a little
	// worse.** These fire once per edge, so seeding a game's graph puts
	// one event per edge on every subscription, and a game has more edges
	// than entities. What a subscriber actually receives from a burst
	// that size is not one event per edge: realtime.Hub buffers 64 events
	// per subscription and drops the rest, each drop leaves a gap in that
	// subscription's own Seq, and internal/web/events.go turns the gap
	// into a synthetic "resync" rather than silent loss. Task 7 settled
	// what to do about that, and the answer was not coalescing — see the
	// entity events above for the argument and for what is actually
	// still open.
	//
	// **Not every edge that disappears is announced as
	// `relation.removed`, and a subscriber must know which events stand
	// in for the ones it will not get.** Three removals delete edges by
	// cascade and publish only the parent's own event:
	// `entity.removed` takes every edge touching that entity with it
	// (`relations.source_id` / `.target_id` are `ON DELETE CASCADE`),
	// `relation_type.removed` with cascade takes every edge of that type
	// (`RemoveRelationType`'s `DeleteRelationsOfType`), and
	// `type.removed` with cascade takes the type's entities and, through
	// them, their edges. **A subscriber must therefore treat those three
	// events as edge invalidations**: on any of them, every edge it holds
	// that names the removed entity, the removed relation type, or an
	// entity of the removed entity type is gone, and the only correct
	// response is to drop it or re-read.
	//
	// **A fourth effect of `type.removed` is not an edge invalidation and
	// is announced rather than inferred.** Removing an entity type also
	// prunes its id out of every relation type's endpoint lists
	// (`RemoveEntityType`), which edits rows the caller never named and
	// moves no `version`. That one is *not* left to a subscriber to
	// derive: `RemoveEntityType` publishes a `relation_type.upserted` per
	// row it changed, because the row is still there and what changed is
	// the rule it states — the same change a caller-visible edit of the
	// same two columns makes, arriving by another route. It is affordable
	// where the per-edge cascade is not: a game has a handful of relation
	// types and thousands of edges.
	//
	// It is stated rather than left to be inferred because the consumers
	// are two tasks away from this decision and every *other* way an edge
	// disappears is announced one by one, so a client written against
	// `relation.removed` alone is a client that renders dead edges:
	// Task 6's one-hop traversal caches the neighbourhood of a node and
	// Task 8's page draws the graph from it. Publishing one
	// `relation.removed` per cascaded edge is the alternative, and it is
	// deliberately not taken — the parent's own event already carries
	// enough to invalidate, and a cascade over a well-connected entity
	// would put thousands of events on a 64-deep subscription buffer,
	// which is the coalescing problem above at its worst rather than a
	// solution to this one. If Tasks 6-8 find the coarse signal too
	// blunt in practice, the answer is a payload that carries the count
	// or the ids, not a per-edge event.
	eventRelationUpserted = "relation.upserted"
	eventRelationRemoved  = "relation.removed"
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

// relationTypeEventMinRole / relationTypeEventHumanOnly and
// relationEventMinRole / relationEventHumanOnly are the gating decided
// above. They are four constants rather than two, and neither pair is an
// alias of the other or of the entity pairs: all four decisions happen to
// agree today, and naming one in terms of another would make a later
// change to either silently change both.
const (
	relationTypeEventMinRole   = roles.Role("")
	relationTypeEventHumanOnly = false

	relationEventMinRole   = roles.Role("")
	relationEventHumanOnly = false
)
