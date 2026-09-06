package markdown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// WriteInput is one document write.
//
// **ExpectedVersion is required and 0 spells a create** (spec §7). It is
// a pointer so that "absent" is distinguishable from "zero", and absent
// is invalid_input at its own path rather than a guess: the metamodel
// reads an absent version as "I am creating", and inheriting that here
// would throw away the whole value of 0 — a typo'd path would silently
// become a second document and the caller would be told it succeeded.
//
// IncludeCurrent asks for the current body back on a conflict. It
// defaults to false in Go, and the *tool* defaults it to true (Task 10):
// the default belongs on the wire, where an agent that does not know
// about the argument gets the useful behaviour, and the Go zero value
// stays honest.
//
// **Kind is a pointer for the same reason ExpectedVersion is: "absent"
// and "empty" are different instructions, and a plain string cannot
// distinguish them.** `kind` is a property of the document, not of any
// one edit — it says what shelf a document sits on, and nothing in a
// body-only fix to a typo means "move the shelf." A nil Kind leaves
// whatever is stored alone; a Kind pointing at "" clears it, same as
// pointing at any other value sets it. TestAnEditThatOmitsKindLeavesIt
// Unchanged pins both directions. Revert and Task 10's `docs.write`
// both read from this: Revert never sets it at all
// (TestRevertingLeavesTheDocumentsKindAlone), and the tool leaves
// the argument optional and omits it from the request when the caller
// does not pass one, rather than defaulting it to the empty string on
// the wire the way IncludeCurrent defaults the other way.
type WriteInput struct {
	Path            string
	Content         string
	Kind            *string
	Message         string
	ExpectedVersion *int32
	IncludeCurrent  bool
	Actor           Actor

	// Links replaces the document's whole attachment set when it is
	// non-nil, and leaves it untouched when it is nil.
	//
	// **The pointer is the distinction and it is load-bearing.** A plain
	// slice cannot tell "attach nothing" from "do not touch the links",
	// and reading an omitted array as empty would silently detach every
	// attachment on every ordinary edit — the single most destructive
	// thing this tool could do quietly. `Links: &[]LinkTarget{}` is the
	// one way to say "detach everything", and it has to be said.
	// TestALinksArrayOnAWriteReplacesTheSetAndOmittingItPreservesIt pins
	// all four cases (create with, omit, replace, empty).
	//
	// The same distinction has to survive JSON, which is where Kind's
	// version of this rule was found broken after the fact: `omitempty`
	// on a plain slice omits an empty one, so the wire would collapse
	// "detach everything" back into "say nothing". A pointer plus
	// omitempty does not, in either direction, and
	// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnTheWire pins
	// that here rather than leaving it to the surface that will carry it
	// (Task 10's DocsWriteInput.Links, which must stay `*[]DocsLinkInput`
	// for exactly this reason).
	//
	// **`"links": null` is decided, not just observed.** encoding/json
	// leaves a `*[]T` field nil for both an omitted key and an explicit
	// `null`, so the two already collapse into one Go value with no
	// choice made here. What this comment states is that the collapse is
	// the intended answer: `null` preserves, the same as omitting the
	// field, and not the same as `[]`, which detaches everything. The
	// safe side, since a caller silent about links keeps them — but a
	// caller who sent `null` meaning "detach" gets the opposite with
	// nothing to notice by, so Task 10's tool description must say this
	// in words an agent reads before it guesses.
	// TestOmittingLinksAndSendingAnEmptyArrayAreDifferentOnTheWire pins
	// `null` alongside omitted and empty.
	Links *[]LinkTarget
}

// Write creates or updates one document and records the snapshot.
//
// The whole compare-and-set happens in SQL: UpsertDocument's DO UPDATE
// is guarded by the caller's expected version, so two writers cannot
// both read version 1 and both succeed. The test that actually pins
// that guard is TestTheGuardedUpsertIsWhatRefusesACreationThatRacedAnother,
// which stages the overlap rather than hoping two goroutines produce
// one: unstaged, the locked read below finds the committed row first and
// refuses in Go, leaving the SQL guard untouched — `=` widened to `>=`
// and the guard deleted outright both left the suite green until that
// test existed. The FOR UPDATE read does not buy the refusal; what it
// buys, and what no test here distinguishes, is stated at
// GetDocumentByPathForUpdate.
func (s *Service) Write(ctx context.Context, projectID uuid.UUID, in WriteInput) (dbq.Document, error) {
	content, err := checkWrite(in)
	if err != nil {
		return dbq.Document{}, err
	}

	var written writtenDocument
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		written, err = s.writeOneWith(ctx, q, projectID, in, content)
		return err
	})
	if err != nil {
		return dbq.Document{}, err
	}
	s.publishWrite(projectID, written)
	return written.row, nil
}

// writtenDocument is one landed write travelling from the transaction
// that wrote it to the announcement that follows the commit.
//
// **The links flag travels with the row rather than being re-read from
// the caller's input**, for the reason metamodel.upsertedEntity carries
// its type key: a batch publishes after its transaction, over a slice,
// and pairing a row with the wrong item's answer to "did this write say
// anything about links" is one index slip away. eventDocumentLinked's
// own comment argues why a write silent about links must not announce a
// link change; this is what keeps that true for an item of a batch.
type writtenDocument struct {
	row   dbq.Document
	links bool
}

// publishWrite announces one landed write: the document, and — only when
// the caller said something about the links — the attachment set too.
//
// **Called after the transaction has committed and never from inside
// it**, which is Service.publish's standing rule. Both callers obey it:
// Write publishes after withTx returns, and WriteMany hands this to
// BulkSpec.Publish, which metamodel.BulkUpsert calls only once the
// transaction that wrote the row has committed.
func (s *Service) publishWrite(projectID uuid.UUID, written writtenDocument) {
	row := written.row
	s.publish(projectID, eventDocumentWritten, documentEventMinRole, documentEventHumanOnly,
		DocumentEvent{ID: row.ID, Path: row.Path, Version: row.CurrentVersion})
	// A second event, and only when the caller said something about the
	// links: eventDocumentLinked's comment argues why both are published
	// rather than one, and why a write that says nothing about links
	// must not announce a link change.
	if written.links {
		s.publish(projectID, eventDocumentLinked, documentEventMinRole, documentEventHumanOnly,
			DocumentEvent{ID: row.ID, Path: row.Path, Version: row.CurrentVersion})
	}
}

// writeOneWith is one whole write against a queries handle that is
// already inside a transaction: the document, its version row, and the
// attachments when the caller named any. It publishes nothing.
//
// **This is the unit a batch item is**, which is why it exists at all:
// bulk.go hands one of these to metamodel.BulkSpec.Write, and Write
// calls it through its own withTx. One implementation, so a batched
// write and a single one cannot drift into meaning different things —
// the links replacement inside the same transaction as the body most of
// all, since a link naming an entity that does not exist has to take the
// body down with it either way.
func (s *Service) writeOneWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	in WriteInput, content Content,
) (writtenDocument, error) {
	row, err := s.writeWith(ctx, q, projectID, in, content)
	if err != nil {
		return writtenDocument{}, err
	}
	// Inside the same transaction, so a link naming an entity that
	// does not exist takes the body down with it: a document and
	// what it is about are one change.
	if in.Links != nil {
		if err := s.replaceLinks(ctx, q, projectID, row.ID, *in.Links); err != nil {
			return writtenDocument{}, err
		}
	}
	return writtenDocument{row: row, links: in.Links != nil}, nil
}

// checkWrite judges everything about one write that can be judged before
// the database is touched, and returns the split content the write will
// store.
//
// It is separate from Write for one reason: a batch has to run it per
// item, *inside* the item's own attempt, so that an item with a bad path
// is reported at its own index and the rest of the batch still lands.
// Left in Write's body it would have been copied into the batch — the
// defect this repository has shipped most often — and the copy would
// have been the one to drift, since only one of the two has a test for
// every refusal.
// TestEveryProblemWithOneWriteIsReportedInOnePass pins the single-write
// pass and TestABatchLandsTheGoodDocumentsAndReportsTheRest the batch's.
func checkWrite(in WriteInput) (Content, error) {
	// Every problem in one pass: a caller whose path and whose kind are
	// both wrong hears about both, rather than fixing one, calling again
	// and learning about the other.
	problems := pathProblems(in.Path)
	if in.Kind != nil {
		problems = append(problems, checkShortText("kind", *in.Kind, MaxKindLen)...)
	}
	problems = append(problems, checkShortText("message", in.Message, MaxMessageLen)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path: "expected_version",
			Message: "is required: pass 0 to create a document, or the version you read " +
				"to update the one that is there",
		})
	}
	// Only the array's length is judged here; each element is judged
	// inside the transaction by replaceLinks, at its own index, because
	// resolving an endpoint is a read and a read belongs where the write
	// it guards is.
	if in.Links != nil && len(*in.Links) > MaxLinksPerWrite {
		problems = append(problems, metamodel.FieldError{
			Path: "links",
			Message: fmt.Sprintf("carries %d attachments and the most one write takes is %d: "+
				"a document about that many things is a taxonomy, and a taxonomy is "+
				"entities and relations", len(*in.Links), MaxLinksPerWrite),
		})
	}
	if len(problems) > 0 {
		return Content{}, invalidInputProblems(problems)
	}

	// The content is checked after the arguments above and reported on
	// its own, not folded into that pass: SplitContent's refusals are
	// ordered among themselves (encoding, then controls, then the
	// frontmatter block) and reporting one of those beside a bad path
	// would claim a completeness this function does not have — a
	// document with two frontmatter problems still hears about one.
	return SplitContent(in.Path, in.Content)
}

// writeWith does the work against any queries handle, so Revert shares
// one implementation with this one. Every caller runs it inside a
// transaction, and none of them publishes from in here.
func (s *Service) writeWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	in WriteInput, content Content,
) (dbq.Document, error) {
	expected := *in.ExpectedVersion

	// Read under the row lock, so the spelling and the version this
	// caller is told about are the ones its own write will meet. No
	// deleted_at filter: a write to a deleted path resurrects it.
	existing, err := q.GetDocumentByPathForUpdate(ctx, dbq.GetDocumentByPathForUpdateParams{
		ProjectID: projectID, Path: in.Path,
	})
	switch {
	case err == nil:
		// Spelling before version: a caller failing for both reasons
		// hears the one it can act on.
		if existing.Path != in.Path {
			return dbq.Document{}, pathRespellingError(in.Path, existing.Path)
		}
		// This check is behaviourally redundant with the SQL guard on
		// UpsertDocument: deleting it leaves the suite green at
		// `-count=3`, because the guarded upsert refuses on its own and
		// conflictAfterFailedUpsert's re-read reports the same current
		// version and body conflictOn would have. It stays anyway, for
		// the same reason GetDocumentByPathForUpdate's own comment keeps
		// FOR UPDATE though nothing here distinguishes its presence: a
		// caller already known to be wrong is turned away before its
		// body, its frontmatter and a version row are built, sent and
		// rolled back. Two identical arguments, stated once rather than
		// applied to one case and left silent on the other.
		if expected != existing.CurrentVersion {
			return dbq.Document{}, s.conflictOn(ctx, q, projectID, existing, in.IncludeCurrent)
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Creation. A caller that expected a version of a document that
		// does not exist gets not_found and not version_conflict: there
		// is nothing to merge onto, so "merge and retry" would send it
		// round a loop that cannot terminate.
		if expected != 0 {
			return dbq.Document{}, missingDocument(in.Path)
		}
	default:
		return dbq.Document{}, fmt.Errorf("lock document: %w", err)
	}

	row, err := q.UpsertDocument(ctx, dbq.UpsertDocumentParams{
		ProjectID:       projectID,
		Path:            in.Path,
		Kind:            in.Kind,
		Title:           content.Title,
		Summary:         content.Summary,
		BodyMd:          content.Body,
		Frontmatter:     content.FrontmatterJSON,
		ExpectedVersion: expected,
		ActorUserID:     in.Actor.UserID,
		ActorTokenID:    in.Actor.TokenID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The guarded DO UPDATE matched nothing: between the locked read
		// above and this statement another writer created or advanced
		// the row. Reachable on the creation path, where there was
		// nothing to lock
		// (TestTheGuardedUpsertIsWhatRefusesACreationThatRacedAnother).
		return dbq.Document{}, s.conflictAfterFailedUpsert(ctx, q, projectID, in)
	}
	if err != nil {
		if mapped := oversizeForIndex(err); errors.Is(mapped, ErrInvalidInput) {
			return dbq.Document{}, mapped
		}
		// 0007_documents.sql gives documents the same two composite
		// foreign keys into api_tokens that every table in
		// 0004_metamodel.sql carries, so a token scoped to another game
		// cannot be recorded as this document's writer. The judgement is
		// metamodel's, shared rather than copied — see actorColumns,
		// which is where the column names live.
		if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return dbq.Document{}, mapped
		}
		return dbq.Document{}, fmt.Errorf("upsert document: %w", err)
	}
	// **There is deliberately no `row.Path != in.Path` check here**, and
	// the plan's Task 3 said there should be, so the reason is worth
	// writing down. The claim was that a creation racing another
	// creation under a different spelling passes both the locked read
	// and the guard, and that comparing the returned spelling closes it.
	// It cannot happen: a row this statement *updated* was found by the
	// guard, which only matches when expected equals the stored
	// current_version, and the racing creator's row is at version 1
	// while a creating caller passes 0 — so the losing creation always
	// falls to the ErrNoRows arm above rather than reaching here, and a
	// row this statement *inserted* carries the caller's own spelling
	// by construction. Proved by mutation: deleting the check left the
	// whole suite green at -count=3, including
	// TestACreationLosingToADifferentlySpelledPathIsToldTheSpelling,
	// which is the staged version of exactly that race and which
	// conflictAfterFailedUpsert answers. A check that cannot fire is a
	// second claim about a race that only one place actually handles.
	if _, err := q.InsertDocumentVersion(ctx, dbq.InsertDocumentVersionParams{
		ProjectID:  projectID,
		DocumentID: row.ID,
		Version:    row.CurrentVersion,
		// The *stored* spelling, off the row this statement just wrote,
		// not in.Path: a write addressed under another casing reaches
		// the existing document and UpsertDocument leaves the path
		// column alone, so the caller's spelling is not this version's
		// address. Recording it would put a spelling in the history that
		// no reader of the document ever sees.
		// TestAVersionRecordsTheStoredSpellingAndNotTheCallers pins it.
		Path:        row.Path,
		Title:       row.Title,
		Summary:     row.Summary,
		BodyMd:      row.BodyMd,
		Frontmatter: row.Frontmatter,
		Message:     in.Message,
		// Stated rather than defaulted: InsertDocumentVersion requires
		// the argument so that Delete's tombstone is the only row in
		// the table that could ever carry true, and so that a future
		// snapshot-writing path cannot inherit the wrong answer by
		// saying nothing. TestOnlyTheTombstoneVersionIsMarkedDeleted
		// reads all four versions of one document back and pins which
		// of them is the tombstone.
		Deleted:       false,
		AuthorUserID:  in.Actor.UserID,
		AuthorTokenID: in.Actor.TokenID,
	}); err != nil {
		// document_versions names its actor `author_`, not `updated_by_`
		// — a version is authored once and never edited — under the same
		// composite key shape, which is the prefix actorColumns was
		// missing.
		//
		// **Unreachable today, and kept for the reason the row-count
		// arms in internal/views are.** A version is never inserted
		// outside the transaction that wrote its document, and
		// documents' own composite key is checked first, so a foreign
		// token is always refused one statement earlier: deleting these
		// three lines leaves TestADocumentWrittenWithAnotherGamesTokenIsRefusedAsSuch
		// green. What it costs to keep is nothing, and what it buys is
		// that a future snapshot-writing path — a restore, a squash —
		// does not inherit a raw SQLSTATE by being written somewhere
		// this arm was never added.
		if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
			return dbq.Document{}, mapped
		}
		return dbq.Document{}, fmt.Errorf("insert document version: %w", err)
	}
	return row, nil
}

// conflictAfterFailedUpsert re-reads a path whose guarded upsert matched
// no row and names what actually stands in the way — a respelling, or a
// version this caller was holding that has since moved.
func (s *Service) conflictAfterFailedUpsert(ctx context.Context, q *dbq.Queries,
	projectID uuid.UUID, in WriteInput,
) error {
	// IncludeDeleted is true, and Task 4 is what makes that load-bearing
	// rather than cosmetic: a write whose guard failed because a *delete*
	// advanced the version must re-read the tombstone in order to report
	// the version it has to merge onto. With the filter off (IncludeDeleted:
	// false), this read would find nothing and the arm below would turn
	// the caller's own version_conflict into an internal_error.
	// TestACreationRacingACreateAndDeleteIsToldTheTombstone stages exactly
	// that race and covers it; TestAStaleVersionCannotSilentlyResurrectA
	// Document does not reach this function at all — its stale write is
	// refused earlier, by writeWith's own conflictOn under the locked
	// read.
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: in.Path, IncludeDeleted: true,
	})
	if err != nil {
		// **pgx.ErrNoRows lands here as an internal_error, and that is
		// checked rather than assumed.** It would mean the row vanished
		// between the failed upsert and this statement, one statement
		// later in the same transaction — which takes a *hard* delete,
		// and this package has none: Delete is soft (see its comment),
		// and no query in internal/db/queries deletes a documents row at
		// all. The one thing that does remove documents is a project's
		// own ON DELETE CASCADE, and that takes the same row lock this
		// transaction is already holding, so it waits rather than racing.
		// Task 3's review recorded this case for Task 4 to settle; it is
		// settled as unreachable, and it is left as an internal_error
		// deliberately, because if it ever fires the cause is a hard
		// delete nobody wrote, not a caller's mistake.
		return fmt.Errorf("re-read document after a failed upsert: %w", err)
	}
	if row.Path != in.Path {
		return pathRespellingError(in.Path, row.Path)
	}
	return s.conflictOn(ctx, q, projectID, row, in.IncludeCurrent)
}

// conflictOn builds the conflict a caller must merge onto, carrying the
// current document when the caller asked for it and *who wrote it*
// whether or not it did.
//
// Deleted is read off the row rather than passed in, so no call site can
// forget it: every path that reaches here has already re-read the
// document, and whether that document is a tombstone is a property of
// what was read and not of who is asking. It is the difference between
// telling a caller to merge and re-read — which for a deleted document
// answers not_found — and telling it that writing brings the document
// back. Both directions are pinned:
// TestAStaleVersionCannotSilentlyResurrectADocument and
// TestAnOrdinaryConflictDoesNotClaimTheDocumentWasDeleted.
func (s *Service) conflictOn(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	row dbq.Document, include bool,
) error {
	conflict := &ConflictError{
		Current:     row.CurrentVersion,
		Include:     include,
		Deleted:     row.DeletedAt.Valid,
		Title:       row.Title,
		BodyMD:      row.BodyMd,
		Frontmatter: json.RawMessage(row.Frontmatter),
		UpdatedAt:   row.UpdatedAt.Time,
	}
	// Resolved through the same queries handle the caller is already
	// holding, so a conflict raised inside a write's transaction does not
	// leave it to answer "who wrote the version you have to merge onto".
	authors, err := s.authorsWith(ctx, q, projectID, []Actor{
		{UserID: row.UpdatedByUserID, TokenID: row.UpdatedByTokenID},
	})
	if err != nil {
		// The conflict is the answer and the label is a detail of it, so
		// a failure to resolve one name must not turn a refusal the
		// caller can act on into an internal_error it cannot. The caller
		// is told the version to merge onto either way; what it loses is
		// the name beside it.
		slog.WarnContext(ctx, "could not resolve the author of a conflicting document",
			"project_id", projectID, "path", row.Path, "error", err)
		return conflict
	}
	conflict.Author = authors[0]
	return conflict
}

// Read returns one document in full, by path.
//
// A soft-deleted document is not found. Deletion is soft so that nothing
// is lost and a mistaken removal is recoverable (spec §3), not so that
// every reader has to filter — a caller that wants the deleted row asks
// the listing for it (Task 8) or reads a version (ReadVersion, whose
// document resolution deliberately includes deleted rows).
// TestDeletingADocumentHidesItFromReadsAndKeepsItsHistory pins the
// hiding, and
// TestWritingToADeletedPathResurrectsItAndContinuesTheNumbering pins
// that the same path reads again once it is written to.
func (s *Service) Read(ctx context.Context, projectID uuid.UUID, path string) (dbq.Document, error) {
	if err := CheckPath(path); err != nil {
		return dbq.Document{}, err
	}
	row, err := s.q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: path, IncludeDeleted: false,
	})
	if err != nil {
		return dbq.Document{}, notFound(err, missingDocument(path), "read document")
	}
	return row, nil
}

// DeleteInput is one soft deletion.
//
// Message is recorded on the tombstone version, so "why was this cut"
// is answerable from the history rather than from someone's memory.
//
// ExpectedVersion is required for the same reason it is on a write, and
// there is no value with a special meaning here: 0 spells "create" on a
// write, and on a delete it can match no stored current_version at all
// — the column starts at 1 and only climbs — so a caller passing it is
// simply refused, as not_found if the path is free and as a conflict if
// it is not.
type DeleteInput struct {
	Path            string
	Message         string
	ExpectedVersion *int32
	Actor           Actor
}

// Delete soft-deletes one document: the row keeps its path and its whole
// history, and a later write to the same path resurrects it.
//
// **It appends a tombstone version and advances current_version**, and
// the plan's Task 4 argues why at length; in short, so that
// current_version never disagrees with the newest version row, and so
// that a caller holding a version from before the delete conflicts
// instead of resurrecting the document without noticing
// (TestAStaleVersionCannotSilentlyResurrectADocument).
//
// **It returns the tombstoned row**, which the plan's signature did not.
// The version number the deletion landed on is the one a caller must
// pass as expected_version to bring the document back, and it is the one
// number Read cannot supply, because Read is precisely what stops
// answering. Returning nothing would leave "delete, then undo" to a
// caller inferring current + 1 from what it happened to hold.
// TestDeletingADocumentHidesItFromReadsAndKeepsItsHistory reads it back.
//
// Hard deletion is deliberately not offered. Losing a designer's writing
// to an agent's mistaken tool call is not a risk this product takes; an
// instance admin who genuinely must purge a row does it in the database.
// That is also what keeps GetDocumentByPathForUpdate's third argument
// for FOR UPDATE hypothetical rather than load-bearing, and what keeps
// conflictAfterFailedUpsert's re-read from ever missing its row: with no
// hard delete anywhere in this package, a row that failed the guarded
// upsert is still there to be re-read a statement later, so that
// function's error arm cannot turn a caller's own conflict into an
// internal_error.
func (s *Service) Delete(ctx context.Context, projectID uuid.UUID, in DeleteInput) (dbq.Document, error) {
	// One pass over every argument, as Write does: a caller whose path
	// and whose message are both wrong hears about both.
	// TestEveryProblemWithOneDeleteIsReportedInOnePass pins it.
	problems := pathProblems(in.Path)
	problems = append(problems, checkShortText("message", in.Message, MaxMessageLen)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path:    "expected_version",
			Message: "is required: pass the version you read, so a delete cannot race an edit",
		})
	}
	if len(problems) > 0 {
		return dbq.Document{}, invalidInputProblems(problems)
	}

	var removed dbq.Document
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		row, err := q.SoftDeleteDocument(ctx, dbq.SoftDeleteDocumentParams{
			ProjectID:       projectID,
			Path:            in.Path,
			ExpectedVersion: *in.ExpectedVersion,
			ActorUserID:     in.Actor.UserID,
			ActorTokenID:    in.Actor.TokenID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Three things reach here and they need telling apart, which
			// is why this is a re-read and not a bare not_found: the
			// path names no document at all, it names a document already
			// deleted, or it names one whose version has moved. Only the
			// third is a conflict, and only the third has a recovery
			// that is not "stop".
			return s.deleteRefusal(ctx, q, projectID, in)
		}
		if err != nil {
			// The soft delete writes updated_by_* and the tombstone
			// below writes author_*, so a foreign token trips a
			// composite key on either statement; both are mapped rather
			// than the first one only.
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("soft delete document: %w", err)
		}
		removed = row
		// The tombstone carries the document exactly as it stood: the
		// same title, summary, body and frontmatter the last live
		// version had, with deleted true. A tombstone that blanked them
		// would make the history unreadable at the one point a reader
		// most wants to see what was lost.
		// TestATombstonesBodyIsStillReadableAsAVersion pins all four.
		if _, err := q.InsertDocumentVersion(ctx, dbq.InsertDocumentVersionParams{
			ProjectID:  projectID,
			DocumentID: row.ID,
			Version:    row.CurrentVersion,
			// The path the document was deleted at, which after Task
			// 15's move is no longer "the path it has always been at".
			Path:          row.Path,
			Title:         row.Title,
			Summary:       row.Summary,
			BodyMd:        row.BodyMd,
			Frontmatter:   row.Frontmatter,
			Message:       in.Message,
			Deleted:       true,
			AuthorUserID:  in.Actor.UserID,
			AuthorTokenID: in.Actor.TokenID,
		}); err != nil {
			if mapped := metamodel.ActorConstraintViolation(err); errors.Is(mapped, ErrActorNotInGame) {
				return mapped
			}
			return fmt.Errorf("insert tombstone version: %w", err)
		}
		return nil
	})
	if err != nil {
		return dbq.Document{}, err
	}
	s.publish(projectID, eventDocumentDeleted, documentEventMinRole, documentEventHumanOnly,
		DocumentEvent{ID: removed.ID, Path: removed.Path, Version: removed.CurrentVersion})
	return removed, nil
}

// deleteRefusal names what actually stood in the way of a delete that
// matched no row.
func (s *Service) deleteRefusal(ctx context.Context, q *dbq.Queries,
	projectID uuid.UUID, in DeleteInput,
) error {
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: in.Path, IncludeDeleted: true,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return missingDocument(in.Path)
	}
	if err != nil {
		return fmt.Errorf("re-read document after a refused delete: %w", err)
	}
	if row.DeletedAt.Valid {
		// not_found, like a path that was never here, because the
		// recovery is the same: there is nothing to delete. The message
		// is what separates the two, and both are asserted —
		// TestDeletingTwiceIsNotFoundRatherThanASecondTombstone and
		// TestDeletingADocumentThatWasNeverThereIsNotFoundNamingThePath.
		// A conflict would be the wrong shape: it would tell a caller to
		// merge onto a version and try again, and trying again can only
		// produce this same answer forever.
		return &MissingError{
			Path: "path",
			Message: fmt.Sprintf("the document at %q was already deleted; "+
				"write to the path to bring it back", in.Path),
		}
	}
	// IncludeCurrent is deliberately false: a caller deleting a document
	// is not merging prose, and echoing a body it asked to remove would
	// be the largest payload in the system attached to the one call that
	// wanted none of it. TestDeletingWithAStaleVersionIsAConflict asserts
	// the conflict carries the version and no body.
	//
	// The path is not re-checked for a respelling here, unlike
	// conflictAfterFailedUpsert. It cannot differ in a way that matters:
	// SoftDeleteDocument matches on lower(path), so a delete addressed
	// under another casing reaches the same row and succeeds, and the
	// stored spelling comes back on it
	// (TestAPathIsMatchedWithoutRegardToCaseOnDelete). A write refuses a
	// respelling because it would otherwise overwrite content under a
	// handle the caller did not mean; a delete overwrites nothing and
	// removes exactly the document the caller named.
	return s.conflictOn(ctx, q, projectID, row, false)
}
