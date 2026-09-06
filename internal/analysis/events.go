package analysis

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
//
// They live here, together, for the reason internal/metamodel/events.go,
// internal/views/events.go and internal/web/publish.go all give: the
// gating of an event is a decision about who may learn a fact, and a
// decision made at two call sites is a decision made twice.
//
// **The gating is argued for routes rather than copied from the domain
// next door**, which is the whole reason this file exists.
// internal/views/events.go records exactly this reasoning for its own
// pair: that three domains happen to agree on two values is a conclusion
// and not a premise, and naming one in terms of another would make a
// later change to either silently change both.
//
// Two rules every payload here obeys. A payload carries the **identity**
// of what changed and never a value a client could then treat as
// current, because publication order is not commit order: two edits in
// flight can arrive as 3 then 2, and a client that renders a payload
// rather than re-reading will eventually render the older of the two.
// And publication happens **after the transaction commits**, never
// inside it — Service.publish's own doc comment, and
// TestNoRouteEventIsPublishedWhenTheCommitFails, which is the one
// placement a refusal test cannot catch, because a rolled-back write and
// a refused write look identical from outside.
const (
	// eventRouteUpserted and eventRouteRemoved fire from UpsertRoute and
	// RemoveRoute once their transaction has committed.
	eventRouteUpserted = "route.upserted"
	eventRouteRemoved  = "route.removed"
)

// **`route.checked` is not declared here, and the reason is a rule this
// file would otherwise break.** The plan puts all three kinds in this
// file so that one gating decision is made once. Its publisher is
// CheckRoute, which does not exist yet — and a constant with no reader is
// a mechanism nothing reads, which this repository's linter refuses
// outright and which is the same rule stated in prose three files away.
// So the *decision* is recorded here and the constant is declared beside
// its publisher, which is the only place it can be used from:
//
//   - the kind is `route.checked`;
//   - its gating is routeEventMinRole and routeEventHumanOnly, the two
//     constants below, taken rather than re-decided — a caller who may
//     learn that a route changed may learn that it was checked;
//   - its payload is the **verdict summary** (holds or does not, and the
//     counts) and never the per-step list, for the rule this file's
//     header states: publication order is not commit order, so a client
//     that rendered the steps out of an event would eventually render
//     the older of two checks. routes.get is where the per-step answer
//     comes from.

// routeEventMinRole and routeEventHumanOnly are the gating decided here,
// named so each call site reads as the decision rather than as two bare
// literals a later edit could drift apart.
//
// **MinRole is empty — every member of the game, viewer included.** The
// question a MinRole answers is whether the subscriber could have read
// the thing on demand. A route names entities a viewer can already list,
// nothing in this domain gates reading one above viewer, and the
// subscriber who most needs the invalidation is a browser holding a
// check result that has just gone stale — the reader with no other way
// to learn it. Gating the signal above viewer would withhold it from
// exactly that reader while withholding nothing they could not fetch a
// moment later.
//
// **HumanOnly is false**, and this is the half that would be wrong by
// default. internal/web's member, token and invite events set it true
// because each mirrors a REST listing that refuses a token caller
// outright; a route inverts that. routes.check is mounted on MCP for
// agents specifically, so an agent may already read every one of these
// rows whenever it likes and withholding the push buys no
// confidentiality at all. It also costs the most here: an agent looping
// over check and upsert is the subscriber whose next write is judged
// against a version another writer has just moved, and a
// version_conflict it cannot explain is what it gets instead of the
// invalidation.
const (
	routeEventMinRole   = roles.Role("")
	routeEventHumanOnly = false
)

// routeEvent is the payload of route.upserted and route.removed.
//
// **Identity only**: id and key address the row, and a client that wants
// the steps re-reads them. Version is the one value here, and it is the
// one worth defending — it is the token a caller's *next* write must
// carry, so a subscriber holding the route open learns from one event
// both that it moved and what to merge onto, and it is monotonic, which
// no other value on the row is. What it is not is a snapshot a client
// may trust as current; the rule stands unchanged: re-read.
type routeEvent struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Version int32     `json:"version"`
}
