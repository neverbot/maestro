package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// EntityCounts is how many entities one type holds, and how many of
// those no longer fit the schema the type declares.
//
// Invalid is the number a designer has to act on: a schema edit that
// narrows a type does not delete the rows that stop fitting, it marks
// them (see Service.revalidateEntitiesOfType), and nothing tells anyone
// how many there are unless something counts them.
type EntityCounts struct {
	Total   int64
	Invalid int64
}

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
// There is no invalid count here, and that is the domain's asymmetry
// rather than an omission: an entity carries a version and is
// re-validated when its type's schema changes, while an edge has
// neither (metamodel.RelationInput's own doc comment records that
// decision), so there is no such thing as an edge marked invalid to
// count.
func (s *Service) RelationCountsByType(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]int64, error) {
	rows, err := s.q.CountRelationsPerType(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count relations per type: %w", err)
	}
	counts := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		counts[row.RelationTypeID] = row.Total
	}
	return counts, nil
}
