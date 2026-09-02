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
)

// typeEventMinRole and typeEventHumanOnly are the gating decided above,
// named so the two call sites read as the decision rather than as two
// bare literals a later edit could drift apart.
const (
	typeEventMinRole   = roles.Role("")
	typeEventHumanOnly = false
)
