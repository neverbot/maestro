package markdown

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one page of documents.
const (
	// DefaultDocumentPage and MaxDocumentPage bound one page of List.
	DefaultDocumentPage int32 = 50
	MaxDocumentPage     int32 = 200
)

// ListFilter narrows a document listing.
type ListFilter struct {
	PathPrefix     string
	Kind           string
	EntityType     string
	EntityKey      string
	IncludeDeleted bool
	Cursor         string
	Limit          int32
}

// DocumentSummary is one row of a listing.
type DocumentSummary struct {
	ID             uuid.UUID
	Path           string
	Kind           string
	Title          string
	Summary        string
	CurrentVersion int32
	Deleted        bool

	// CreatedAt, UpdatedAt, CreatedBy and UpdatedBy are what make "what
	// changed lately" answerable from a listing.
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy Author
	UpdatedBy Author
}

// DocumentPage is one page of summaries plus the cursor for the next.
type DocumentPage struct {
	Documents  []DocumentSummary
	NextCursor string
}

// List returns one page of a game's documents, in path order.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, f ListFilter) (DocumentPage, error) {
	problems := checkPathPrefix(f.PathPrefix)
	problems = append(problems, checkShortText("kind", f.Kind, MaxKindLen)...)
	switch {
	case f.EntityType != "" && f.EntityKey == "":
		problems = append(problems, metamodel.FieldError{
			Path:    "entity_key",
			Message: "and entity_type must be given together: half an address is not a filter",
		})
	case f.EntityKey != "" && f.EntityType == "":
		problems = append(problems, metamodel.FieldError{
			Path:    "entity_type",
			Message: "and entity_key must be given together: half an address is not a filter",
		})
	case f.EntityType != "":
		// Bounded before either lookup runs, through the metamodel's own key rule
		// rather than a second copy of it, exactly as the link calls do.
		// entityAddressKeyProblems, not entityAddressProblems: List has no role
		// argument, and running the whole of entityAddressProblems here would
		// silently apply the role rule to a field this filter does not have (see
		// entityAddressKeyProblems' doc comment). An entity key holding an
		// invalid UTF-8 byte reaches Postgres as a byte sequence it refuses
		// outright, which would land on the default arm as internal_error over a
		// value the caller supplied. TestListArea's "an entity filter is bounded
		// before postgres sees it" case pins it.
		problems = append(problems, entityAddressKeyProblems("", f.EntityType, f.EntityKey)...)
	}
	if len(problems) > 0 {
		return DocumentPage{}, invalidInputProblems(problems)
	}

	params := dbq.ListDocumentsPageParams{
		ProjectID:      projectID,
		IncludeDeleted: f.IncludeDeleted,
		Limit:          paging.Size(f.Limit, DefaultDocumentPage, MaxDocumentPage),
	}
	entityPart := ""
	if f.EntityType != "" {
		entityID, err := s.resolveEntity(ctx, s.q, projectID, "", LinkTarget{
			EntityType: f.EntityType, EntityKey: f.EntityKey,
		})
		if err != nil {
			return DocumentPage{}, err
		}
		params.EntityID = &entityID
		entityPart = entityID.String()
	}
	if f.PathPrefix != "" {
		prefix := f.PathPrefix
		params.PathPrefix = &prefix
	}
	if f.Kind != "" {
		kind := f.Kind
		params.Kind = &kind
	}

	fingerprint := documentListingFingerprint(projectID, f, entityPart)
	after, err := paging.Decode(f.Cursor, fingerprint, refuseCursor)
	if err != nil {
		return DocumentPage{}, err
	}
	if after.ID != uuid.Nil {
		id := after.ID
		sort := after.Sort
		params.AfterID = &id
		params.AfterPath = &sort
	}

	rows, err := s.q.ListDocumentsPage(ctx, params)
	if err != nil {
		return DocumentPage{}, fmt.Errorf("list documents: %w", err)
	}

	page := DocumentPage{Documents: make([]DocumentSummary, 0, len(rows))}
	// Two audit pairs per row, resolved to labels in one round trip for
	// the whole page rather than one per row: see Service.Authors. The
	// order is (created, updated) per row and is unpacked the same way
	// below, which is the one thing this pairing has to get right.
	actors := make([]Actor, 0, 2*len(rows))
	for _, row := range rows {
		actors = append(actors,
			Actor{UserID: row.CreatedByUserID, TokenID: row.CreatedByTokenID},
			Actor{UserID: row.UpdatedByUserID, TokenID: row.UpdatedByTokenID})
	}
	authors, err := s.authorsWith(ctx, s.q, projectID, actors)
	if err != nil {
		return DocumentPage{}, err
	}
	for i, row := range rows {
		page.Documents = append(page.Documents, DocumentSummary{
			ID: row.ID, Path: row.Path, Kind: row.Kind, Title: row.Title,
			Summary: row.Summary, CurrentVersion: row.CurrentVersion,
			// pgtype.Timestamptz, not a pointer: a nil test does not
			// compile against the generated model (Task 4's correction
			// 2).
			Deleted:   row.DeletedAt.Valid,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
			CreatedBy: authors[2*i],
			UpdatedBy: authors[2*i+1],
		})
	}
	// paging.Size never returns a limit below one, so a full page is
	// never an empty one and there is no separate emptiness check.
	if len(rows) == int(params.Limit) {
		last := rows[len(rows)-1]
		page.NextCursor = paging.Encode(paging.Cursor{
			Sort: last.Path, ID: last.ID, Fingerprint: fingerprint,
		})
	}
	return page, nil
}

// documentListingFingerprint is the resolved listing one document cursor
// belongs to: the game, then this domain's listing, then every filter
// that changes which rows a page holds.
func documentListingFingerprint(projectID uuid.UUID, f ListFilter, entityID string) string {
	return paging.Fingerprint(projectID.String(), "documents",
		strings.ToLower(f.PathPrefix), strings.ToLower(f.Kind), entityID,
		fmt.Sprintf("%t", f.IncludeDeleted))
}

// checkPathPrefix bounds a prefix filter.
func checkPathPrefix(prefix string) []metamodel.FieldError {
	if prefix == "" {
		return nil
	}
	return checkShortText("path_prefix", prefix, MaxPathLen)
}
