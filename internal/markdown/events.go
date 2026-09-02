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
	// committed. Tasks 5 and 6 add the remaining kinds and state their
	// own gating here rather than copying this one; Task 4's
	// eventDocumentDeleted, below, is the first to do so.
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
)

// documentEventMinRole and documentEventHumanOnly are the gating decided
// above, named so the call sites read as the decision rather than as two
// bare literals a later edit could drift apart. Task 4 already reuses
// them and Tasks 5 and 6 do too — the kinds this package publishes are one
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
