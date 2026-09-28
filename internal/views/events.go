package views

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes into its hub, with the role and
// human-only gating each one carries.
const (
	// eventViewUpserted and eventViewRemoved fire from UpsertView and
	// RemoveView once their transaction has committed.
	eventViewUpserted = "view.upserted"
	eventViewRemoved  = "view.removed"

	// eventViewPositions fires from SetPositions and from ClearPositions
	// once their write has landed.
	eventViewPositions = "view.positions"

	// eventViewBackground fires from SetBackground once its write has
	// landed, and it is the fourth kind for the third task's reason
	// rather than for a fourth one.
	eventViewBackground = "view.background"
)

// viewEventMinRole and viewEventHumanOnly are the gating decided above,
// named so each of the six call sites reads as the decision rather than
// as two bare literals a later edit could drift apart. They are their own
// constants and not an alias of another domain's pair: the three
// decisions happen to agree today, and naming one in terms of another
// would make a later change to either silently change both.
const (
	viewEventMinRole   = roles.Role("")
	viewEventHumanOnly = false
)

// viewEvent is the payload of view.upserted and view.removed.
type viewEvent struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Version int32     `json:"version"`
}

// viewPlacementEvent is the payload of view.positions and
// view.background: the view's id and its key, and nothing else.
type viewPlacementEvent struct {
	ID  uuid.UUID `json:"id"`
	Key string    `json:"key"`
}
