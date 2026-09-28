package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
)

// The bounds on one document search.
const (
	defaultSearchLimit int32 = 50
	maxSearchLimit     int32 = 200
)

// MaxIndexedChars is how much of each of a document's title, summary and
// body reaches the search vector: 0007_documents.sql wraps all three in
// `left(…, 131072)`, in *characters*, and the tail of anything longer is
// stored and re-read intact but is not findable by search.
const MaxIndexedChars = 131072

// DocumentHit is one search result: a document, whether its *title*
// satisfied the query, the rank it matched at, and the entities it is
// attached to.
type DocumentHit struct {
	ID             uuid.UUID
	Path           string
	Title          string
	Summary        string
	Kind           string
	Version        int32
	NameMatch      bool
	Rank           float32
	LinkedEntities []EntityLink
}

// SearchDocuments runs a full-text query over a game's current documents
// and returns the best matches first.
func (s *Service) SearchDocuments(ctx context.Context, projectID uuid.UUID, query, kind string, limit int32) ([]DocumentHit, error) {
	problems := checkShortText("kind", kind, MaxKindLen)
	if len(problems) > 0 {
		return nil, invalidInputProblems(problems)
	}
	if err := metamodel.CheckSearchQuery(query); err != nil {
		return nil, err
	}

	rows, err := s.q.SearchDocuments(ctx, dbq.SearchDocumentsParams{
		ProjectID: projectID,
		Query:     query,
		Kind:      kind,
		Limit:     paging.Size(limit, defaultSearchLimit, maxSearchLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}

	hits := make([]DocumentHit, 0, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, DocumentHit{
			ID: row.ID, Path: row.Path, Title: row.Title, Summary: row.Summary,
			Kind: row.Kind, Version: row.CurrentVersion,
			NameMatch: row.NameMatch, Rank: row.Rank,
			LinkedEntities: []EntityLink{},
		})
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return hits, nil
	}

	links, err := s.q.ListLinksForDocuments(ctx, dbq.ListLinksForDocumentsParams{
		ProjectID: projectID, DocumentIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("list links for search hits: %w", err)
	}
	byDocument := make(map[uuid.UUID][]EntityLink, len(ids))
	for _, link := range links {
		byDocument[link.DocumentID] = append(byDocument[link.DocumentID], EntityLink{
			EntityID: link.EntityID, EntityTypeKey: link.EntityTypeKey,
			EntityKey: link.EntityKey, EntityName: link.EntityName, Role: link.Role,
		})
	}
	for i := range hits {
		if attached, ok := byDocument[hits[i].ID]; ok {
			hits[i].LinkedEntities = attached
		}
	}
	return hits, nil
}

// There is deliberately no exported SearchLimit here. metamodel's is the
// one the surface calls, because the surface clamps once for both sides;
// a twin would be an exported accessor with no caller, which is how a
// shape gets pre-committed sight unseen.
