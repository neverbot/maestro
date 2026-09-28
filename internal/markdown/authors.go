package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file is the one read path from an audit column to a name.
type Author struct {
	Kind  string
	ID    *uuid.UUID
	Label string
}

// authorOf builds the unresolved half of an Author from one row's audit
// pair: which of the two columns is set, and the id in it.
func authorOf(userID, tokenID *uuid.UUID) Author {
	switch {
	case tokenID != nil:
		return Author{Kind: "token", ID: tokenID}
	case userID != nil:
		return Author{Kind: "user", ID: userID}
	default:
		return Author{}
	}
}

// Authors resolves a batch of audit pairs to labels, in the order they
// were given.
func (s *Service) Authors(ctx context.Context, projectID uuid.UUID, actors []Actor) ([]Author, error) {
	return s.authorsWith(ctx, s.q, projectID, actors)
}

// authorsWith is Authors against any queries handle, so a conflict built
// inside a write's transaction resolves the author of the version it is
// telling the caller to merge onto without leaving that transaction.
func (s *Service) authorsWith(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	actors []Actor,
) ([]Author, error) {
	authors := make([]Author, 0, len(actors))
	for _, actor := range actors {
		authors = append(authors, authorOf(actor.UserID, actor.TokenID))
	}
	if err := s.labelAuthors(ctx, q, projectID, authors); err != nil {
		return nil, err
	}
	return authors, nil
}

// labelAuthors fills in the Label of every author in the slice that
// names somebody, in place.
func (s *Service) labelAuthors(ctx context.Context, q *dbq.Queries, projectID uuid.UUID,
	authors []Author,
) error {
	seenUsers := map[uuid.UUID]bool{}
	seenTokens := map[uuid.UUID]bool{}
	// Never nil: pgx encodes a nil slice as SQL NULL, and `x = ANY(NULL)`
	// is NULL rather than false, so a nil array would silently match
	// nothing on a side that has ids and everything the caller expected
	// on neither. The same trap DeleteDocumentLinksExcept's `keep`
	// records.
	userIDs := []uuid.UUID{}
	tokenIDs := []uuid.UUID{}
	for _, author := range authors {
		if author.ID == nil {
			continue
		}
		switch author.Kind {
		case "user":
			if !seenUsers[*author.ID] {
				seenUsers[*author.ID] = true
				userIDs = append(userIDs, *author.ID)
			}
		case "token":
			if !seenTokens[*author.ID] {
				seenTokens[*author.ID] = true
				tokenIDs = append(tokenIDs, *author.ID)
			}
		}
	}
	if len(userIDs) == 0 && len(tokenIDs) == 0 {
		return nil
	}
	rows, err := q.ResolveAuthors(ctx, dbq.ResolveAuthorsParams{
		ProjectID: projectID, UserIds: userIDs, TokenIds: tokenIDs,
	})
	if err != nil {
		return fmt.Errorf("resolve authors: %w", err)
	}
	// Keyed by (kind, id) and not by id alone. A user id and a token id
	// are both uuids out of two different tables and nothing stops one
	// value appearing in both; keying on the id alone would let a
	// token's label be printed against a user.
	type ref struct {
		kind string
		id   uuid.UUID
	}
	labels := make(map[ref]string, len(rows))
	for _, row := range rows {
		labels[ref{kind: row.Kind, id: row.ID}] = row.Label
	}
	for i := range authors {
		if authors[i].ID == nil {
			continue
		}
		authors[i].Label = labels[ref{kind: authors[i].Kind, id: *authors[i].ID}]
	}
	return nil
}
