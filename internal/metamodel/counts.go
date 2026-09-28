package metamodel

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TypeCounts is how many rows one declared type holds, and how many of
// those no longer fit the schema it declares.
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
