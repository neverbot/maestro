package markdown

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one page of history, in the shape internal/metamodel's
// listings already use: asking for nothing is no opinion and gets the
// default, asking for too much is an opinion and gets the cap.
const (
	// DefaultHistoryPage and MaxHistoryPage bound one page of History.
	DefaultHistoryPage int32 = 50
	MaxHistoryPage     int32 = 200
)

// HistoryFilter selects one document's history.
type HistoryFilter struct {
	Path   string
	Cursor string
	Limit  int32
}

// HistoryPage is one page of version metadata plus the cursor for the
// next.
type HistoryPage struct {
	Versions   []VersionSummary
	NextCursor string
}

// VersionSummary is one row of a history: what changed, when, and who
// changed it.
type VersionSummary struct {
	Version int32

	// Path is the address this version was written at, which is not
	// necessarily the address the document is at now.
	Path      string
	Title     string
	Summary   string
	Message   string
	Deleted   bool
	CreatedAt time.Time
	Author    Author
}

// History returns one page of a document's version metadata, newest
// first.
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
	// One round trip for the whole page: fifty versions of one document
	// written by one agent are fifty copies of one token id, and a label
	// call per row would be the N+1 that answering "who changed this"
	// cost before this listing carried an author at all.
	actors := make([]Actor, 0, len(rows))
	for _, row := range rows {
		actors = append(actors, Actor{UserID: row.AuthorUserID, TokenID: row.AuthorTokenID})
	}
	authors, err := s.authorsWith(ctx, s.q, projectID, actors)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{Versions: make([]VersionSummary, 0, len(rows))}
	for i, row := range rows {
		page.Versions = append(page.Versions, VersionSummary{
			Version: row.Version, Path: row.Path,
			Title: row.Title, Summary: row.Summary,
			Message: row.Message, Deleted: row.Deleted,
			CreatedAt: row.CreatedAt.Time, Author: authors[i],
		})
	}
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
func historyFingerprint(projectID, documentID uuid.UUID) string {
	return paging.Fingerprint(projectID.String(), "document_versions", documentID.String())
}

// ReadVersion returns one past version in full, body included.
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
func (s *Service) Revert(ctx context.Context, projectID uuid.UUID, in RevertInput) (dbq.Document, error) {
	// One pass over every argument, as Write and Delete do: TestVersionsArea's
	// "every problem with one revert is reported in one pass" case pins that
	// four problems arrive as four fields of one refusal.
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
			return s.conflictOn(ctx, q, projectID, existing, in.IncludeCurrent)
		}

		// Read the version to restore **before** anything is written, so a revert
		// to a version that does not exist leaves the document exactly as it
		// stood. Moving this below writeWith would still roll the transaction
		// back, but the version numbering would have advanced in a way a reader
		// of this function could not see. TestVersionsArea's "reverting to a
		// missing version names the argument" case asserts both the argument path
		// and that nothing was written.
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
	// After the commit, never inside it: Service.publish's own comment carries
	// the argument. TestVersionsArea's "no revert is announced when the revert
	// is refused" case is the half this package can reach on its own.
	s.publish(projectID, eventDocumentReverted, documentEventMinRole, documentEventHumanOnly,
		RevertEvent{ID: written.ID, Path: written.Path, Version: written.CurrentVersion, FromVersion: from})
	return written, nil
}
