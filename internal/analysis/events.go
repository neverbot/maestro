package analysis

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
const (
	// eventRouteUpserted and eventRouteRemoved fire from UpsertRoute and
	// RemoveRoute once their transaction has committed.
	eventRouteUpserted = "route.upserted"
	eventRouteRemoved  = "route.removed"
)

// **`route.checked` is not declared here, and the reason is a rule this
// file would otherwise break.** The plan puts all three kinds in this
// file so that one gating decision is made once. Its publisher is
// CheckRoute — and when this file was written CheckRoute did not exist,
// so the constant would have had no reader, which is a mechanism nothing
// reads and which this repository's linter refuses outright. So the
// *decision* is recorded here and the constant is declared beside its
// publisher, which is the only place it can be used from. The decision
// stands unchanged now that the publisher exists; what follows is what
// check.go implements, and TestCheckingARoutePublishesItsVerdictSummary
// asserts the gating and the payload rather than trusting this list:
const (
	routeEventMinRole   = roles.Role("")
	routeEventHumanOnly = false
)

// routeEvent is the payload of route.upserted and route.removed.
type routeEvent struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Version int32     `json:"version"`
}
