package markdown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
// Task 6 adds a Links field to this struct. It is deliberately not here
// yet: a field no caller sets is a shape pre-committed sight unseen.
type WriteInput struct {
	Path            string
	Content         string
	Kind            string
	Message         string
	ExpectedVersion *int32
	IncludeCurrent  bool
	Actor           Actor
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
	// Every problem in one pass: a caller whose path and whose kind are
	// both wrong hears about both, rather than fixing one, calling again
	// and learning about the other.
	// TestEveryProblemWithOneWriteIsReportedInOnePass pins it.
	problems := pathProblems(in.Path)
	problems = append(problems, checkShortText("kind", in.Kind, MaxKindLen)...)
	problems = append(problems, checkShortText("message", in.Message, MaxMessageLen)...)
	if in.ExpectedVersion == nil {
		problems = append(problems, metamodel.FieldError{
			Path: "expected_version",
			Message: "is required: pass 0 to create a document, or the version you read " +
				"to update the one that is there",
		})
	}
	if len(problems) > 0 {
		return dbq.Document{}, invalidInputProblems(problems)
	}

	// The content is checked after the arguments above and reported on
	// its own, not folded into that pass: SplitContent's refusals are
	// ordered among themselves (encoding, then controls, then the
	// frontmatter block) and reporting one of those beside a bad path
	// would claim a completeness this function does not have — a
	// document with two frontmatter problems still hears about one.
	content, err := SplitContent(in.Path, in.Content)
	if err != nil {
		return dbq.Document{}, err
	}

	var written dbq.Document
	err = s.withTx(ctx, func(q *dbq.Queries) error {
		var err error
		written, err = s.writeWith(ctx, q, projectID, in, content)
		return err
	})
	if err != nil {
		return dbq.Document{}, err
	}
	s.publish(projectID, eventDocumentWritten, documentEventMinRole, documentEventHumanOnly,
		DocumentEvent{ID: written.ID, Path: written.Path, Version: written.CurrentVersion})
	return written, nil
}

// writeWith does the work against any queries handle, so Task 6's revert
// shares one implementation with this one. Every caller runs it inside a
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
		if expected != existing.CurrentVersion {
			return dbq.Document{}, conflictOn(existing, in.IncludeCurrent)
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
		// (TestTwoConcurrentCreationsOfOnePathLeaveOneWinner).
		return dbq.Document{}, s.conflictAfterFailedUpsert(ctx, q, projectID, in)
	}
	if err != nil {
		if mapped := oversizeForIndex(err); errors.Is(mapped, ErrInvalidInput) {
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
		ProjectID:     projectID,
		DocumentID:    row.ID,
		Version:       row.CurrentVersion,
		Title:         row.Title,
		Summary:       row.Summary,
		BodyMd:        row.BodyMd,
		Frontmatter:   row.Frontmatter,
		Message:       in.Message,
		AuthorUserID:  in.Actor.UserID,
		AuthorTokenID: in.Actor.TokenID,
	}); err != nil {
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
	row, err := q.GetDocumentByPath(ctx, dbq.GetDocumentByPathParams{
		ProjectID: projectID, Path: in.Path, IncludeDeleted: true,
	})
	if err != nil {
		return fmt.Errorf("re-read document after a failed upsert: %w", err)
	}
	if row.Path != in.Path {
		return pathRespellingError(in.Path, row.Path)
	}
	return conflictOn(row, in.IncludeCurrent)
}

// conflictOn builds the conflict a caller must merge onto, carrying the
// current document when the caller asked for it.
func conflictOn(row dbq.Document, include bool) error {
	return &ConflictError{
		Current:     row.CurrentVersion,
		Include:     include,
		Title:       row.Title,
		BodyMD:      row.BodyMd,
		Frontmatter: json.RawMessage(row.Frontmatter),
	}
}

// Read returns one document in full, by path.
//
// A soft-deleted document is not found. Deletion is soft so that nothing
// is lost and a mistaken removal is recoverable (spec §3), not so that
// every reader has to filter — a caller that wants the deleted row asks
// the listing for it (Task 8) or reads a version (Task 6). Nothing sets
// deleted_at before Task 4, so no test here exercises that arm yet.
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
