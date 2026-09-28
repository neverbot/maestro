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
	Links *[]LinkTarget
}

// Write creates or updates one document and records the snapshot.
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
type writtenDocument struct {
	row   dbq.Document
	links bool
}

// publishWrite announces one landed write: the document, and — only when
// the caller said something about the links — the attachment set too.
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
		// round a loop that cannot terminate. This is the rule
		// metamodel.RemovedError states for the four metamodel tables
		// and for a saved view, reached here first and by the same
		// reasoning — a version claim is a claim about a row that
		// exists.
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
		// The guarded DO UPDATE matched nothing: between the locked read above
		// and this statement another writer created or advanced the row.
		// Reachable on the creation path, where there was nothing to lock
		// (TestViewsArea's "the guarded upsert is what refuses a creation that
		// raced another" case).
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
	// the plan's Task 3 said there should be, so the reason is worth writing
	// down. The claim was that a creation racing another creation under a
	// different spelling passes both the locked read and the guard, and that
	// comparing the returned spelling closes it. It cannot happen: a row this
	// statement *updated* was found by the guard, which only matches when
	// expected equals the stored current_version, and the racing creator's row
	// is at version 1 while a creating caller passes 0 — so the losing
	// creation always falls to the ErrNoRows arm above rather than reaching
	// here, and a row this statement *inserted* carries the caller's own
	// spelling by construction. Proved by mutation: deleting the check left
	// the whole suite green at -count=3, including TestDocumentsArea's "a
	// creation losing to a differently spelled path is told the spelling"
	// case, which is the staged version of exactly that race and which
	// conflictAfterFailedUpsert answers. A check that cannot fire is a second
	// claim about a race that only one place actually handles.
	if _, err := q.InsertDocumentVersion(ctx, dbq.InsertDocumentVersionParams{
		ProjectID:  projectID,
		DocumentID: row.ID,
		Version:    row.CurrentVersion,
		// The *stored* spelling, off the row this statement just wrote, not
		// in.Path: a write addressed under another casing reaches the existing
		// document and UpsertDocument leaves the path column alone, so the
		// caller's spelling is not this version's address. Recording it would put
		// a spelling in the history that no reader of the document ever sees.
		// TestMoveArea's "a version records the stored spelling and not the
		// callers" case pins it.
		Path:        row.Path,
		Title:       row.Title,
		Summary:     row.Summary,
		BodyMd:      row.BodyMd,
		Frontmatter: row.Frontmatter,
		Message:     in.Message,
		// Stated rather than defaulted: InsertDocumentVersion requires the
		// argument so that Delete's tombstone is the only row in the table that
		// could ever carry true, and so that a future snapshot-writing path
		// cannot inherit the wrong answer by saying nothing. TestDeleteArea's
		// "only the tombstone version is marked deleted" case reads all four
		// versions of one document back and pins which of them is the tombstone.
		Deleted:       false,
		AuthorUserID:  in.Actor.UserID,
		AuthorTokenID: in.Actor.TokenID,
	}); err != nil {
		// document_versions names its actor `author_`, not `updated_by_`
		// — a version is authored once and never edited — under the same
		// composite key shape, which is the prefix actorColumns was
		// missing.
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
	// advanced the version must re-read the tombstone in order to report the
	// version it has to merge onto. With the filter off (IncludeDeleted:
	// false), this read would find nothing and the arm below would turn the
	// caller's own version_conflict into an internal_error.
	// TestDocumentsArea's "a creation racing a create and delete is told the
	// tombstone" case stages exactly that race and covers it;
	// TestDocumentsArea's "a stale version cannot silently resurrect a
	// document" case does not reach this function at all — its stale write is
	// refused earlier, by writeWith's own conflictOn under the locked read.
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
type DeleteInput struct {
	Path            string
	Message         string
	ExpectedVersion *int32
	Actor           Actor
}

// Delete soft-deletes one document: the row keeps its path and its whole
// history, and a later write to the same path resurrects it.
func (s *Service) Delete(ctx context.Context, projectID uuid.UUID, in DeleteInput) (dbq.Document, error) {
	// One pass over every argument, as Write does: a caller whose path and
	// whose message are both wrong hears about both. TestDeleteArea's "every
	// problem with one delete is reported in one pass" case pins it.
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
		// The tombstone carries the document exactly as it stood: the same title,
		// summary, body and frontmatter the last live version had, with deleted
		// true. A tombstone that blanked them would make the history unreadable
		// at the one point a reader most wants to see what was lost.
		// TestDeleteArea's "a tombstones body is still readable as a version"
		// case pins all four.
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
		// not_found, like a path that was never here, because the recovery is the
		// same: there is nothing to delete. The message is what separates the
		// two, and both are asserted — TestDeleteArea's "deleting twice is not
		// found rather than a second tombstone" case and TestDeleteArea's
		// "deleting a document that was never there is not found naming the path"
		// case. A conflict would be the wrong shape: it would tell a caller to
		// merge onto a version and try again, and trying again can only produce
		// this same answer forever.
		return &MissingError{
			Path: "path",
			Message: fmt.Sprintf("the document at %q was already deleted; "+
				"write to the path to bring it back", in.Path),
		}
	}
	// IncludeCurrent is deliberately false: a caller deleting a document is
	// not merging prose, and echoing a body it asked to remove would be the
	// largest payload in the system attached to the one call that wanted none
	// of it. TestDeleteArea's "deleting with a stale version is a conflict"
	// case asserts the conflict carries the version and no body.
	return s.conflictOn(ctx, q, projectID, row, false)
}
