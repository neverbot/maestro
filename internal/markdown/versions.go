package markdown

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one page of history, in the shape internal/metamodel's
// listings already use: asking for nothing is no opinion and gets the
// default, asking for too much is an opinion and gets the cap.
//
// Exported for the rule the metamodel established for MaxSearchQuery: a
// bound a caller cannot read is a bound a caller trips over, and Task
// 10's docs.history description is built with these values interpolated
// rather than typed out. TestAHistoryPageAsksForTooMuchAndGetsTheCap
// pins the clamp through the exported name.
const (
	// DefaultHistoryPage and MaxHistoryPage bound one page of History.
	DefaultHistoryPage int32 = 50
	MaxHistoryPage     int32 = 200
)

// HistoryFilter selects one document's history.
//
// Cursor is the NextCursor of a previous call. It belongs to the game
// and the document it was issued for and to no other; HistoryPage
// carries the contract.
type HistoryFilter struct {
	Path   string
	Cursor string
	Limit  int32
}

// HistoryPage is one page of version metadata plus the cursor for the
// next.
//
// **NextCursor is set when the page came back full**, and empty
// otherwise, so a caller looping until it is empty is correct and must
// expect a final empty page rather than treating one as an error. The
// rest of the cursor contract — that it is a position and not a
// snapshot, that it cannot be carried to another listing, and that it is
// not signed — is paging.Cursor's, and it is worth reading before paging
// a document that is being edited.
//
// Cursor.Sort, for this listing, is the row's version number spelled in
// decimal; metamodel.EntityPage's is the row's name and
// metamodel.RelationPage's its created_at, and paging.Cursor generalises
// Sort away from all three.
//
// **This is the one listing in Maestro whose sort key is immutable.**
// A version's number never changes and a version row is never updated,
// so the "a row edited to sort before the position is never seen again"
// hazard paging.Cursor documents cannot arise here. Versions are only
// ever appended, at the *newest* end, which the page walks away from.
type HistoryPage struct {
	Versions   []dbq.ListDocumentVersionsRow
	NextCursor string
}

// History returns one page of a document's version metadata, newest
// first.
//
// **No bodies.** A version's body is returned by ReadVersion and by Diff
// and by nothing else (spec §7): prose is the largest payload in the
// system, and a history that carried every body would blow an agent's
// context window on the first call against a document anyone has
// actually worked on. TestAHistoryRowCarriesNoBodyAtAll pins the
// absence over every field of the row rather than over a field it can
// name, since what has to hold is that no such field exists.
//
// The history of a deleted document is readable
// (TestHistoryOfADeletedDocumentIsStillReadable). Deletion is soft
// precisely so that the record survives, and "what did this say before
// someone cut it" is the first question anyone asks.
func (s *Service) History(ctx context.Context, projectID uuid.UUID, f HistoryFilter) (HistoryPage, error) {
	doc, err := s.documentForVersions(ctx, projectID, f.Path)
	if err != nil {
		return HistoryPage{}, err
	}
	limit := paging.Size(f.Limit, DefaultHistoryPage, MaxHistoryPage)

	fingerprint := historyFingerprint(projectID, doc.ID)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseCursor)
	if err != nil {
		return HistoryPage{}, err
	}

	params := dbq.ListDocumentVersionsParams{
		ProjectID: projectID, DocumentID: doc.ID, Limit: limit,
	}
	if after.ID != uuid.Nil {
		version, err := strconv.ParseInt(after.Sort, 10, 32)
		if err != nil {
			// Reachable only by a hand-edited cursor whose fingerprint
			// still agrees, the same shape metamodel.ListRelations
			// reports for a position that is not a timestamp. No test
			// reaches it: forging one takes decoding a real cursor,
			// replacing the sort half and re-encoding.
			return HistoryPage{}, paging.Malformed(
				"its position is not a version number", refuseCursor)
		}
		v := int32(version)
		params.AfterVersion = &v
	}

	rows, err := s.q.ListDocumentVersions(ctx, params)
	if err != nil {
		return HistoryPage{}, fmt.Errorf("list document versions: %w", err)
	}
	page := HistoryPage{Versions: rows}
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort:        strconv.FormatInt(int64(last.Version), 10),
			ID:          last.ID,
			Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// historyFingerprint is the resolved listing one history cursor belongs
// to: the game, then this domain's listing, then the document.
//
// **The project id is first, always**, which is paging.Fingerprint's
// standing rule and the one that was learned the hard way — a
// fingerprint that omitted it let one game's cursor page another game's
// rows. It is worth saying exactly what that part buys *here*, because
// the honest answer is "nothing yet": the document id already
// discriminates between games, since two games' documents at one path
// are two different rows, so dropping the project id leaves
// TestACursorFromAnotherGamesHistoryIsRefused green. That test is this
// domain's required equivalent of the metamodel's
// TestACursorFromAnotherGameIsRefused and it pins the *behaviour*;
// TestTheHistoryFingerprintLeadsWithTheProjectId pins the composition
// itself, which is the only way this rule can be pinned in a listing
// whose other parts happen to be sufficient. Both exist because the
// part stops being redundant the moment this listing grows a filter
// that is not derived from a per-game row.
//
// "document_versions" is a domain discriminator, which
// paging.Fingerprint mandates for nobody and this package supplies
// anyway: internal/metamodel spells its own listings "entities",
// "related" and "relations", and two domains reaching for one word is a
// collision nothing would catch.
//
// The document id is resolved, not the path as spelled, so two
// spellings of one path give one fingerprint;
// TestHistoryPagesWithACursorBelongingToItsOwnDocument covers the other
// half, that two documents of one game do not share one.
func historyFingerprint(projectID, documentID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "document_versions", documentID.String())
}

// ReadVersion returns one past version in full, body included.
//
// It is the only public read of a version's body, frontmatter, message
// and author — Task 3's correction 9 recorded that those had no public
// reader at all and asserted them through raw SQL until this landed —
// and TestTheFirstWriteAlsoWritesVersionOneWithItsAuthorAndMessage is
// where the body and the frontmatter are now read back through it, in
// place of the SQL that used to stand there.
func (s *Service) ReadVersion(ctx context.Context, projectID uuid.UUID, path string, version int32) (dbq.DocumentVersion, error) {
	doc, err := s.documentForVersions(ctx, projectID, path)
	if err != nil {
		return dbq.DocumentVersion{}, err
	}
	row, err := s.q.GetDocumentVersion(ctx, dbq.GetDocumentVersionParams{
		ProjectID: projectID, DocumentID: doc.ID, Version: version,
	})
	if err != nil {
		return dbq.DocumentVersion{}, notFound(err, missingVersion("version", path, version),
			"read document version")
	}
	return row, nil
}

// documentForVersions resolves a path to the document its versions hang
// from, deleted documents included.
//
// It is the one place the version calls touch `documents`, and what it
// establishes is *identity* — which document — while the project filter
// here is what keeps a path from naming another game's document in the
// first place (TestReadingAnotherGamesVersionIsNotFound covers both
// callers, and TestTwoGamesSharingOnePathKeepSeparateHistories covers
// the case a miss cannot show, where the path is taken in both games).
//
// ListDocumentVersions and GetDocumentVersion filter on their own
// denormalised project_id as well, which is the spec's §5 rule for these
// two tables and not defence in depth in the metamodel's sense. It is
// worth saying plainly what no test here can show: **that second filter
// cannot be made to matter by any call this package offers**, because a
// document id only ever reaches those queries from the resolution above,
// and 0007_documents.sql's composite key makes a version row whose
// project_id disagrees with its document's unrepresentable
// (TestAVersionRowCannotClaimAGameItsDocumentDoesNotBelongTo forces that
// refusal). The plan's Task 6 predicted dropping either query's project
// filter would redden TestReadingAnotherGamesVersionIsNotFound; it does
// not, and it is recorded here rather than left as a mutation someone
// re-runs and mistrusts. The filters stay because Task 11's REST mirror
// may address a document by id, where the resolution above is not in the
// path at all.
func (s *Service) documentForVersions(ctx context.Context, projectID uuid.UUID, path string) (dbq.Document, error) {
	if err := CheckPath(path); err != nil {
		return dbq.Document{}, err
	}
	row, err := s.q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: path, IncludeDeleted: true,
	})
	if err != nil {
		return dbq.Document{}, notFound(err, missingDocument(path), "resolve document")
	}
	return row, nil
}

// refuseCursor turns internal/paging's message into this package's own
// error, at the argument's own path.
//
// The messages live in internal/paging so that this domain and the
// metamodel cannot tell a caller two different things about one bad
// cursor; the *type* is this domain's, because that is what
// internal/web's existing invalid_input arm matches on. It is declared
// here, beside the first listing that pages, and **Task 8's document
// listing must reuse it** rather than declaring a second one.
//
// It satisfies paging's contract that a refuse function never returns
// nil: invalidInput always builds a *metamodel.ValidationError, and
// paging panics naming the message if it ever did not.
// TestAMalformedHistoryCursorIsInvalidInputAtItsOwnPath asserts the
// path and the whole sentence.
func refuseCursor(message string) error { return invalidInput("cursor", message) }

// missingVersion names both the document and the version asked for, at
// the argument's own path — `version` for a read, `to_version` for a
// revert, `from_version`/`to_version` for a diff (see versionArgument)
// — so a caller that mistyped one of two numbers is told which.
func missingVersion(path, docPath string, version int32) error {
	return &MissingError{
		Path:    path,
		Message: fmt.Sprintf("the document at %q has no version %d", docPath, version),
	}
}

// RevertInput restores a past version's content as a new version.
//
// There is no Kind: `kind` is a property of the document and is not
// versioned, so a revert leaves it alone. Restoring a grouping label
// alongside the prose would be a second, unannounced change. Nothing is
// needed to achieve that beyond saying nothing: Task 3's review made
// WriteInput.Kind a *string whose nil means "say nothing about kind",
// and the writeWith call below simply never sets it.
// TestRevertingLeavesTheDocumentsKindAlone pins it.
//
// Message is optional here and only here: a revert has an obvious one
// ("reverted to version N") and forcing a caller to type it would be
// asking for what the call already knows.
// TestARevertTakesAMessageOfItsOwnWhenOneIsGiven pins that a caller's
// own message wins.
type RevertInput struct {
	Path            string
	ToVersion       int32
	ExpectedVersion *int32
	Message         string
	IncludeCurrent  bool
	Actor           Actor
}

// Revert writes forward: it creates a *new* version whose content equals
// ToVersion's, with an automatic message when the caller gives none.
//
// **History is append-only.** There is no state in which version 9
// exists and version 8 does not, which is what lets a designer trust
// that the record of the writing is the record of the writing.
// TestRevertWritesForwardRatherThanRewritingHistory pins that a revert
// of a three-version document leaves four versions, not one.
//
// **The restored content is the content that was stored**, not the
// content re-derived from the old body: title, summary and frontmatter
// come off the version row verbatim. Re-running today's derivation rules
// over yesterday's body would mean "revert" restored something that was
// never there the day the derivation changed. That reuse is possible
// because writeWith takes its Content from its caller, which is why
// SplitContent runs in Write and not in writeWith; if a later change
// moves the split inside, revert silently starts re-deriving and
// TestRevertKeepsTheStoredTitleRatherThanDerivingItAgain is what goes
// red.
//
// Reverting to a tombstone version restores that version's body and
// leaves the document alive; it does not re-delete it
// (TestRevertingToATombstoneRestoresItsBodyAndLeavesTheDocumentAlive).
// A revert is a content operation, and a caller that wants the document
// gone says so with delete — where the expected version, and therefore
// the refusal if someone else has since written, is its own.
func (s *Service) Revert(ctx context.Context, projectID uuid.UUID, in RevertInput) (dbq.Document, error) {
	// One pass over every argument, as Write and Delete do:
	// TestEveryProblemWithOneRevertIsReportedInOnePass pins that four
	// problems arrive as four fields of one refusal.
	problems := pathProblems(in.Path)
	problems = append(problems, checkShortText("message", in.Message, MaxMessageLen)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path:    "expected_version",
			Message: "is required: pass the version you read, so a revert cannot race an edit",
		})
	}
	if in.ToVersion < 1 {
		problems = append(problems, metamodel.FieldError{
			Path:    "to_version",
			Message: "must be 1 or greater: versions are numbered from 1",
		})
	}
	if len(problems) > 0 {
		return dbq.Document{}, invalidInputProblems(problems)
	}

	message := in.Message
	if message == "" {
		message = fmt.Sprintf("reverted to version %d", in.ToVersion)
	}

	var (
		written dbq.Document
		from    int32
	)
	err := s.withTx(ctx, func(q *dbq.Queries) error {
		existing, err := q.GetDocumentByPathForUpdate(ctx, dbq.GetDocumentByPathForUpdateParams{
			ProjectID: projectID, Path: in.Path,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return missingDocument(in.Path)
		}
		if err != nil {
			return fmt.Errorf("lock document: %w", err)
		}
		if existing.Path != in.Path {
			return pathRespellingError(in.Path, existing.Path)
		}
		// Behaviourally redundant with writeWith's own check and with
		// UpsertDocument's SQL guard — removing it leaves the suite
		// green, verified by mutation, because the guarded upsert
		// refuses on its own and reports the same version and body. It
		// stays for the reason writeWith's identical check states: a
		// caller already known to be wrong is turned away before the
		// version it asked to restore is even read. One standard,
		// applied to all three of these guards rather than argued for
		// one of them.
		if *in.ExpectedVersion != existing.CurrentVersion {
			return conflictOn(existing, in.IncludeCurrent)
		}

		// Read the version to restore **before** anything is written, so
		// a revert to a version that does not exist leaves the document
		// exactly as it stood. Moving this below writeWith would still
		// roll the transaction back, but the version numbering would
		// have advanced in a way a reader of this function could not
		// see. TestRevertingToAMissingVersionNamesTheArgument asserts
		// both the argument path and that nothing was written.
		target, err := q.GetDocumentVersion(ctx, dbq.GetDocumentVersionParams{
			ProjectID: projectID, DocumentID: existing.ID, Version: in.ToVersion,
		})
		if err != nil {
			return notFound(err, missingVersion("to_version", in.Path, in.ToVersion),
				"read the version to revert to")
		}
		from = target.Version

		written, err = s.writeWith(ctx, q, projectID, WriteInput{
			Path:            in.Path,
			Message:         message,
			ExpectedVersion: in.ExpectedVersion,
			IncludeCurrent:  in.IncludeCurrent,
			Actor:           in.Actor,
		}, Content{
			Title:           target.Title,
			Summary:         target.Summary,
			Body:            target.BodyMd,
			FrontmatterJSON: target.Frontmatter,
		})
		return err
	})
	if err != nil {
		return dbq.Document{}, err
	}
	// After the commit, never inside it: Service.publish's own comment
	// carries the argument. TestNoRevertIsAnnouncedWhenTheRevertIsRefused
	// is the half this package can reach on its own.
	s.publish(projectID, eventDocumentReverted, documentEventMinRole, documentEventHumanOnly,
		RevertEvent{ID: written.ID, Path: written.Path, Version: written.CurrentVersion, FromVersion: from})
	return written, nil
}
