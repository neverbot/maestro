package markdown

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes, with the role and human-only
// gating each one carries.
const (
	// eventDocumentWritten fires from Write once its transaction has
	// committed. Task 4's eventDocumentDeleted and Task 6's
	// eventDocumentReverted, below, state their own gating here rather
	// than copying this one.
	eventDocumentWritten = "document.written"

	// eventDocumentDeleted fires from Delete once its transaction has
	// committed. The gating is documentEventMinRole /
	// documentEventHumanOnly, the pair eventDocumentWritten's comment
	// argues: a deletion is the same kind of fact as a write — the
	// game's own content moved — and a viewer whose browser is
	// rendering the document is precisely the subscriber who cannot
	// find out any other way.
	eventDocumentDeleted = "document.deleted"

	// eventDocumentReverted fires from Revert once its transaction has
	// committed, with documentEventMinRole / documentEventHumanOnly —
	// the pair eventDocumentWritten's comment argues, for the same
	// reason eventDocumentDeleted takes it: a revert is the game's own
	// content moving, and the subscriber who cannot find out any other
	// way is the viewer whose browser is rendering the document.
	eventDocumentReverted = "document.reverted"

	// eventDocumentMoved fires from Move once its transaction has
	// committed, with documentEventMinRole / documentEventHumanOnly —
	// the pair eventDocumentWritten's comment argues, taken here for a
	// reason that is sharper than for a write.
	eventDocumentMoved = "document.moved"

	// eventDocumentLinked fires from LinkAdd, LinkRemove and any Write
	// that carried a links array, once the transaction has committed,
	// with documentEventMinRole / documentEventHumanOnly — the pair
	// eventDocumentWritten's comment argues, for the reason the other
	// two kinds take it: what a document is about is the game's own
	// content, and the viewer whose browser is rendering an entity page
	// has no other way to learn that the page's list of documents moved.
	eventDocumentLinked = "document.linked"
)

// documentEventMinRole and documentEventHumanOnly are the gating decided
// above, named so the call sites read as the decision rather than as two
// bare literals a later edit could drift apart. Tasks 4 and 6 both reuse
// them — the kinds this package publishes are one decision about one kind
// of fact, unlike the metamodel's four pairs, which cover two genuinely
// different facts (a game's vocabulary and its content).
// TestDocumentsArea's "a document event reaches a viewer and a token alike"
// case is what pins the two values; without it they are a comment.
const (
	documentEventMinRole   = roles.Role("")
	documentEventHumanOnly = false
)

// DocumentEvent is the payload of every document event. It is a typed
// struct, never hand-built JSON with caller values interpolated into it,
// and it carries identity only.
type DocumentEvent struct {
	ID      uuid.UUID `json:"id"`
	Path    string    `json:"path"`
	Version int32     `json:"version"`
}

// RevertEvent is document.reverted's payload: identity, the new version,
// and the version whose content was restored.
type MoveEvent struct {
	ID      uuid.UUID `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Version int32     `json:"version"`
}

type RevertEvent struct {
	ID          uuid.UUID `json:"id"`
	Path        string    `json:"path"`
	Version     int32     `json:"version"`
	FromVersion int32     `json:"from_version"`
}
