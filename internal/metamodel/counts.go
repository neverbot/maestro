package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TypeCounts is how many rows one declared type holds, and how many of
// those no longer fit the schema it declares.
//
// Invalid is the number a designer has to act on: a schema edit that
// narrows a type does not delete the rows that stop fitting, it marks
// them (see Service.revalidate), and nothing tells anyone how many there
// are unless something counts them.
//
// **One type for both tables, not two.** Entities and edges are judged
// against a field schema by the same Schema.Validate, flagged by the same
// sweep and counted by the same FILTER, so a caller that has learned to
// read one has learned to read the other. Two structs differing in
// nothing would be two places for the next column to be added to one of.
type TypeCounts struct {
	Total   int64
	Invalid int64
}

// EntityCounts is TypeCounts under the name every existing caller knows
// it by. It is an alias rather than a second struct: the two were never
// going to differ, and an alias means a caller holding an EntityCounts
// and one holding a RelationCounts hold the same value.
type EntityCounts = TypeCounts

// RelationCounts is TypeCounts for a relation type. Named so that a
// caller reading a game summary does not have to remember which of the
// two halves of the page is spelled generically.
type RelationCounts = TypeCounts

// EntityCountsByType counts a game's entities, grouped by their type.
//
// A type with no entities is absent from the map rather than present as
// a zero: every caller already holds the type list this is joined onto
// — the counts are meaningless without it — and a missing key reads as
// none, which is the same answer with one fewer row on the wire.
//
// This exists so a game's home page costs one query for every count on
// it, regardless of how much content the game holds. Counting through
// ListEntities instead would make the page's cost grow with the game,
// which is exactly what a summary is for avoiding.
func (s *Service) EntityCountsByType(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]EntityCounts, error) {
	rows, err := s.q.CountEntitiesPerType(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count entities per type: %w", err)
	}
	counts := make(map[uuid.UUID]EntityCounts, len(rows))
	for _, row := range rows {
		counts[row.EntityTypeID] = EntityCounts{Total: row.Total, Invalid: row.Invalid}
	}
	return counts, nil
}

// RelationCountsByType counts a game's edges, grouped by their relation
// type. See EntityCountsByType for why an absent key means none.
//
// **It carries an invalid count, and until 0009 it could not.** This
// comment used to record the absence as the domain's own asymmetry: an
// entity was re-validated when its type's schema changed and an edge was
// not, because `relations` had no column to record a verdict in. That
// asymmetry is gone — a relation type carries a field schema exactly as
// an entity type does, and an edge's fields are now judged by the same
// sweep — so the two halves of a game summary answer the same question.
func (s *Service) RelationCountsByType(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]RelationCounts, error) {
	rows, err := s.q.CountRelationsPerType(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count relations per type: %w", err)
	}
	counts := make(map[uuid.UUID]RelationCounts, len(rows))
	for _, row := range rows {
		counts[row.RelationTypeID] = RelationCounts{Total: row.Total, Invalid: row.Invalid}
	}
	return counts, nil
}
