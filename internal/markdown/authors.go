package markdown

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
)

// This file is the one read path from an audit column to a name.
//
// Every write in this domain records who made it — documents carry
// created_by and updated_by, versions carry an author pair — and until
// this file existed none of it came back out. A history entry carried an
// author id and nothing that resolved it, so the reading view rendered
// every token as "an agent" because it had no other choice, and a
// designer looking at ten versions by three agents saw "an agent" ten
// times.

// Author is who wrote a row, resolved to something a reader can name.
//
// **Kind and Label are two different facts and both are on the wire.**
// Kind ("user" or "token") is what tells a designer's edit from an
// agent's, which is why document_versions carries two nullable author
// columns rather than one synthetic user per agent. Label is the name
// itself — a user's display name, a token's label — and it is what stops
// a page rendering a raw uuid or a bare category.
//
// **The zero Author is the honest answer for a row that records
// nobody**, and it is reachable: both audit columns are ON DELETE SET
// NULL, so a user or a token that is really gone leaves the row naming
// no one. Kind is then empty, ID is nil and Label is empty, and a client
// says whatever it says about an unknown author — the reading view's
// "a former member". A kind naming nothing would be worse than no kind.
//
// **A present kind and id with an empty Label is a different case
// again** and is deliberately distinguishable: it means the row names
// somebody the resolver could not find, which today means a user or
// token deleted between the row being read and the labels being
// resolved. A client that renders Label unconditionally would print an
// empty name; one that checks it falls back to its own wording. Both
// need to be able to tell it from the zero value, which is why the
// absence is spelled as an empty Label rather than as a missing pair.
type Author struct {
	Kind  string
	ID    *uuid.UUID
	Label string
}

// authorOf builds the unresolved half of an Author from one row's audit
// pair: which of the two columns is set, and the id in it.
//
// Exactly one of the two is set on a row written through this server.
// Both nil is a row whose author is no longer on file and answers the
// zero Author.
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
//
// **One round trip for the whole batch, never one per row.** A listing
// is fifty documents with two audit pairs each and a history page is
// fifty versions; resolving those a row at a time would be the N+1 that
// this whole change exists to remove — the cost of answering "what
// changed lately" was one history call per document before it, and
// replacing that with one label call per row would not be an
// improvement.
//
// It is exported because the single-document answers are assembled in
// internal/web from a dbq.Document that Read and Write already return,
// so that surface needs the resolver even though the page-shaped answers
// resolve their own (List and History below). One resolver, four
// callers, rather than a second join per answer shape.
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
//
// It takes the slice rather than returning one because its callers build
// their answers row by row — a listing's summaries, a history page's
// versions — and pairing a label back onto the wrong row by getting an
// index wrong is the failure metamodel.upsertedEntity's type key exists
// to prevent. Here the pairing is by id and not by position, so it
// cannot slip.
//
// The ids are deduplicated before the query: a page of fifty versions of
// one document written by one agent is fifty copies of one token id, and
// sending them all would ask Postgres to match fifty times against one
// row. Nothing depends on the deduplication for correctness — the answer
// is a map either way — so it is a cost decision and it is stated as
// one.
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
