package markdown

import (
	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/roles"
)

// Event kinds this package publishes, with the role and human-only
// gating each one carries.
//
// They live here, together, for the reason internal/metamodel/events.go
// and internal/web/publish.go both give: the gating of an event is a
// decision about who may learn a fact, and a decision made four call
// sites apart is a decision re-made four times.
//
// Two rules every payload here obeys. A payload carries the *identity*
// of what changed — a path, a version number, an id — and never a value
// a client could then treat as current: publication order is not commit
// order, so a payload holding a body could stably tell a client the
// wrong body with nothing to signal it, and prose is the largest payload
// in the system besides. And publication happens after the transaction
// commits, never inside it (Service.publish's own doc comment).
const (
	// eventDocumentWritten fires from Write once its transaction has
	// committed. Task 4's eventDocumentDeleted and Task 6's
	// eventDocumentReverted, below, state their own gating here rather
	// than copying this one.
	//
	// **The gating below is decided for documents, not inherited.**
	// internal/metamodel/events.go argues four pairs of its own and
	// lands on the same two values; internal/web's member, token and
	// invite events land on the opposite value for HumanOnly. Both
	// arguments were read before this one was written, and neither
	// transfers by itself: prose is a third kind of fact.
	//
	// **MinRole is empty — every member of the game, viewer included.**
	// The question a MinRole answers is "may this subscriber learn that
	// this changed", and the honest test for it is whether the
	// subscriber could have read the thing on demand. Nothing in this
	// domain gates *reading* a document above viewer: Task 11 mounts the
	// reading view as a content GET, and internal/web's
	// registerContentRoute applies requireEditor to non-GET routes only,
	// so a viewer's browser renders a quest's script. Gating the
	// invalidation above viewer would therefore withhold the signal from
	// exactly the reader who has no other way to know — a browser
	// showing prose it will not re-fetch — while withholding nothing it
	// could not already read. There is no "the push carries more than
	// the data" case to make here, which is the case that makes
	// internal/web's member events restrictive: DocumentEvent carries an
	// id, a path and a number, all three of which any viewer can read
	// back through docs.read a moment later.
	//
	// **HumanOnly is false**, and it is the considered answer rather
	// than the default. internal/web's member, token and invite events
	// all set it true, because each mirrors a REST listing that
	// requireHumanCaller refuses a token caller outright — a token
	// subscriber learning about membership churn would be reading, over
	// the stream, a fact it could not have asked for over REST. A
	// document inverts every part of that. Task 10 mounts docs.read and
	// docs.write on MCP *for agents specifically*, so the agent is not
	// an incidental listener on a human surface, it is the surface's
	// intended caller, and one that may read every document on demand
	// loses no confidentiality by being told one moved. It is also the
	// subscriber with the most to lose from silence, and this domain is
	// where that costs most: the spec's own definition of done is two
	// agents rewriting one script, expected_version is required on every
	// write, and an agent whose base moved without being told meets it
	// as a version_conflict it cannot explain — the one refusal in this
	// package that costs a whole turn to recover from.
	//
	// **The known consequence, recorded rather than gated around.** One
	// event per write, and a seeding agent writing four hundred
	// documents puts four hundred events on every subscription.
	// realtime.Hub buffers 64 per subscription and drops the rest, and
	// internal/web/events.go turns the resulting gap in Seq into a
	// synthetic resync — so the failure mode is "go and re-read", not
	// silent loss. That is the right answer for a burst that size, and
	// role gating would not have improved it: it would only have starved
	// the viewer this decision exists to serve. The metamodel settled
	// the same question the same way (its entity events' comment carries
	// the full argument, including why coalescing is not the fix), and
	// the reason it applies unchanged here is that the volumes are
	// alike: a document write is one row per event, like an entity, not
	// a handful per game, like a type.
	eventDocumentWritten = "document.written"

	// eventDocumentDeleted fires from Delete once its transaction has
	// committed. The gating is documentEventMinRole /
	// documentEventHumanOnly, the pair eventDocumentWritten's comment
	// argues: a deletion is the same kind of fact as a write — the
	// game's own content moved — and a viewer whose browser is
	// rendering the document is precisely the subscriber who cannot
	// find out any other way.
	//
	// **A subscriber must read this as an invalidation of the
	// document's links as well.** Nothing cascades on a soft delete —
	// document_links rows survive, because the document does — but a UI
	// listing an entity's documents will stop being shown this one, and
	// a client that only re-reads the document itself keeps a stale
	// entry in that list.
	//
	// The payload is a DocumentEvent like a write's, carrying the
	// *tombstone's* version number rather than the last live one. That
	// is the number a subscriber must compare against what it holds to
	// know its copy is stale, and it is the number a caller passes as
	// expected_version to bring the document back.
	// TestADeletionIsAnnounced pins the kind and the version;
	// TestNoDeletionIsAnnouncedWhenTheTombstoneCannotBeWritten pins
	// that a delete which rolls back announces nothing, which is the
	// case TestADeletionIsAnnounced cannot reach on its own.
	eventDocumentDeleted = "document.deleted"

	// eventDocumentReverted fires from Revert once its transaction has
	// committed, with documentEventMinRole / documentEventHumanOnly —
	// the pair eventDocumentWritten's comment argues, for the same
	// reason eventDocumentDeleted takes it: a revert is the game's own
	// content moving, and the subscriber who cannot find out any other
	// way is the viewer whose browser is rendering the document.
	//
	// It is a separate kind from document.written even though a revert
	// *is* a write, and the reason is what a subscriber does with it: a
	// browser showing "an agent rewrote this" and a browser showing "Ana
	// reverted this to version 7" are two different sentences, and a
	// client that had to infer the second from the first would have to
	// fetch the history to find out. The payload carries the version
	// that was restored as well as the new one, which is the whole
	// difference.
	//
	// TestRevertIsAnnouncedWithTheVersionItRestored pins the kind and
	// both numbers; TestNoRevertIsAnnouncedWhenTheRevertIsRefused pins
	// that a revert which never happened announces nothing.
	eventDocumentReverted = "document.reverted"

	// eventDocumentMoved fires from Move once its transaction has
	// committed, with documentEventMinRole / documentEventHumanOnly —
	// the pair eventDocumentWritten's comment argues, taken here for a
	// reason that is sharper than for a write.
	//
	// **A move is the one change in this domain a subscriber cannot
	// discover by re-reading what it holds.** Every other event names a
	// path that still answers: a client told `lore/duskwood` changed
	// re-reads `lore/duskwood` and gets the new content. A client told
	// nothing, whose document has moved, re-reads `lore/duskwood` and is
	// answered not_found — indistinguishable, from the outside, from a
	// deletion. That is why the payload is a MoveEvent and not a
	// DocumentEvent: From is what the subscriber is holding and To is
	// where it has to look, and a payload carrying one path could only
	// ever be half an instruction.
	//
	// The same argument is the whole reason this event has to exist at
	// all rather than being folded into document.written. A move does
	// not change a document's content, so a client that treated it as a
	// write would re-fetch a path that is no longer there.
	// TestAMoveIsAnnouncedWithBothEnds pins the kind and both paths;
	// TestNoMoveIsAnnouncedWhenTheMoveIsRefused pins that a refused move
	// announces nothing.
	eventDocumentMoved = "document.moved"

	// eventDocumentLinked fires from LinkAdd, LinkRemove and any Write
	// that carried a links array, once the transaction has committed,
	// with documentEventMinRole / documentEventHumanOnly — the pair
	// eventDocumentWritten's comment argues, for the reason the other
	// two kinds take it: what a document is about is the game's own
	// content, and the viewer whose browser is rendering an entity page
	// has no other way to learn that the page's list of documents moved.
	//
	// One kind covers attach and detach, deliberately: the payload
	// carries the document's identity and nothing about the link set, so
	// there is no sentence a client could write from "linked" that it
	// could not write from "unlinked", and both have the same recovery —
	// re-read the document's links, or the entity's. Two kinds would be
	// two things to subscribe to for one refresh.
	// TestLinkingIsAnnounced pins that both operations announce this one
	// kind, and TestNoLinkIsAnnouncedWhenTheAttachmentIsRefused pins
	// that a refused one announces nothing.
	//
	// **A write that carries a links array publishes both
	// document.written and document.linked**, in that order. A client
	// watching only one of them is watching for one of the two things
	// that changed, and the alternative — folding the link change into
	// the write event — would mean a client had to re-read links on
	// every write in case one had.
	// TestAWriteCarryingLinksAnnouncesBothTheWriteAndTheLink pins the
	// order, and pins that an edit with no links array announces the
	// write alone.
	//
	// The payload is a DocumentEvent, carrying the document's *unmoved*
	// version: a link is not the document's content and LinkAdd does not
	// advance current_version (see its doc comment), so a subscriber
	// comparing the number against what it holds learns nothing from it
	// and must re-read the links rather than the body.
	eventDocumentLinked = "document.linked"
)

// documentEventMinRole and documentEventHumanOnly are the gating decided
// above, named so the call sites read as the decision rather than as two
// bare literals a later edit could drift apart. Tasks 4 and 6 both reuse
// them — the kinds this package publishes are one
// decision about one kind of fact, unlike the metamodel's four pairs,
// which cover two genuinely different facts (a game's vocabulary and its
// content). TestADocumentEventReachesAViewerAndATokenAlike is what pins
// the two values; without it they are a comment.
const (
	documentEventMinRole   = roles.Role("")
	documentEventHumanOnly = false
)

// DocumentEvent is the payload of every document event. It is a typed
// struct, never hand-built JSON with caller values interpolated into it,
// and it carries identity only.
//
// Path is the *stored* spelling, not the caller's: paths match without
// regard to case, so repeating the caller's spelling would name an
// identity no other reader sees.
type DocumentEvent struct {
	ID      uuid.UUID `json:"id"`
	Path    string    `json:"path"`
	Version int32     `json:"version"`
}

// RevertEvent is document.reverted's payload: identity, the new version,
// and the version whose content was restored.
//
// It carries no body, like every payload here: publication order is not
// commit order, so a payload holding prose could stably tell a client
// the wrong prose. FromVersion is the one thing a DocumentEvent could
// not have said, and it is the whole reason for a second payload type.
// MoveEvent is document.moved's payload: identity, both ends of the
// change of address, and the version the move landed on.
//
// **Two paths, not one, and that is the reason for a third payload
// type.** From is the address a subscriber is holding and To is the
// address it must use from now on; a DocumentEvent carrying only the
// new path would tell a client that something it cannot identify has
// moved somewhere, and one carrying only the old path would tell it
// where to stop looking and not where to look.
//
// To is the *stored* spelling, read back off the moved row, for the
// reason DocumentEvent.Path is: it is the identity every other reader
// sees. From is the caller's spelling of the old address, which is the
// one thing here that cannot be read back — the row no longer carries
// it — and it names the same address under the fold whatever the
// capitalisation, which is all a subscriber matching against what it
// holds needs.
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
