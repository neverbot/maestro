package views

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// Positions: where a human dragged each node of one view, and the layout
// contract the three calls in this file are the state of.
const MaxPositions = HardMaxNodes

// DefaultPinned is 0008_views.sql's column default, spelled here for the
// reason DefaultLayoutMode is: this package fills the value rather than
// letting the column default it, because every position this file writes
// names the column.
const DefaultPinned = true

// PositionInput is one node's coordinates in one view.
type PositionInput struct {
	EntityType string
	EntityKey  string
	X          float64
	Y          float64
	Pinned     *bool
}

// EntityAddress names one node by the two keys that identify it, for the
// calls that address a node without carrying coordinates.
type EntityAddress struct {
	EntityType string
	EntityKey  string
}

// Position is one stored position as it reads back.
type Position struct {
	EntityType string    `json:"entity_type"`
	EntityKey  string    `json:"entity_key"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	Pinned     bool      `json:"pinned"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SetPositions writes the coordinates of one or more nodes of one view.
func (s *Service) SetPositions(ctx context.Context, projectID uuid.UUID, viewKey string,
	positions []PositionInput,
) error {
	if problems := positionProblems(positions); len(problems) > 0 {
		return &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}
	view, err := s.ViewByKey(ctx, projectID, viewKey)
	if err != nil {
		return err
	}

	// Resolved before the transaction opens rather than inside it: the
	// lookups are reads of another domain's service, which holds the pool
	// and not this transaction's handle, and holding a write transaction
	// open across five thousand reads would take the row locks with it.
	// What that costs is a window in which an entity resolved here is
	// deleted before the write lands — 0008_views.sql's composite foreign
	// key refuses the row then, and the wrapped error names the position
	// it came from.
	ids := make([]uuid.UUID, len(positions))
	// seen is keyed by the resolved id and not by the spelling, so the
	// fold that decides whether two keys are one entity is Postgres's own
	// (entities_key_key is UNIQUE (project_id, entity_type_id,
	// lower(key))) rather than a second rule written here that could
	// disagree with it. Two spellings of one key in one call are two
	// coordinates for one node, of which the array order would silently
	// pick one; refusing says which two positions collide.
	seen := make(map[uuid.UUID]int, len(positions))
	for i, p := range positions {
		row, err := s.meta.EntityByKey(ctx, projectID, p.EntityType, p.EntityKey)
		if err != nil {
			return fmt.Errorf("%s: %w", pointer("positions", i), err)
		}
		if first, dup := seen[row.ID]; dup {
			return &metamodel.ValidationError{
				Code: metamodel.CodeInvalidInput,
				Fields: []metamodel.FieldError{{
					Path: pointer("positions", i),
					Message: fmt.Sprintf("addresses the same entity as %s, and keys are "+
						"matched without regard to case: one node cannot have two "+
						"positions in one view, so send it once",
						pointer("positions", first)),
				}},
			}
		}
		seen[row.ID] = i
		ids[i] = row.ID
	}

	err = s.withTx(ctx, func(q *dbq.Queries) error {
		for i, p := range positions {
			pinned := DefaultPinned
			if p.Pinned != nil {
				pinned = *p.Pinned
			}
			written, err := q.UpsertViewPosition(ctx, dbq.UpsertViewPositionParams{
				ViewID: view.ID, EntityID: ids[i], ProjectID: projectID,
				X: p.X, Y: p.Y, Pinned: pinned,
			})
			if err != nil {
				return fmt.Errorf("write %s: %w", pointer("positions", i), err)
			}
			// The statement's DO UPDATE is guarded on the stored row's own
			// project id, and a guard that matches nothing updates nothing
			// and reports zero.
			if written == 0 {
				return fmt.Errorf("write %s: the stored position belongs to another game",
					pointer("positions", i))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// After withTx has returned, never from inside fn: an event published
	// inside the transaction announces an arrangement that may still roll
	// back, and every subscriber's only reaction is to re-read.
	// Service.publish's own doc comment states the rule for the whole
	// package.
	s.publish(projectID, eventViewPositions, viewEventMinRole, viewEventHumanOnly,
		viewPlacementEvent{ID: view.ID, Key: view.Key})
	return nil
}

// ClearPositions drops stored positions, and returns how many rows went.
func (s *Service) ClearPositions(ctx context.Context, projectID uuid.UUID, viewKey string,
	entities []EntityAddress,
) (int64, error) {
	if problems := clearProblems(entities); len(problems) > 0 {
		return 0, &metamodel.ValidationError{
			Code: metamodel.CodeInvalidInput, Fields: problems,
		}
	}
	view, err := s.ViewByKey(ctx, projectID, viewKey)
	if err != nil {
		return 0, err
	}
	if entities == nil {
		removed, err := s.q.DeleteViewPositions(ctx, dbq.DeleteViewPositionsParams{
			ProjectID: projectID, ViewID: view.ID,
		})
		if err != nil {
			return 0, fmt.Errorf("clear view positions: %w", err)
		}
		// Announced whether or not a row went. A clear that removed
		// nothing changed nothing, so this is the one publish in the
		// package that could be argued away — and it is kept, because the
		// count this call answers with is the caller's own information
		// and not a subscriber's: a browser that has been told "re-read
		// this view's positions" and finds the same arrangement has lost
		// one read, while a browser not told has lost the picture. The
		// removal event next door refuses the same shape for the opposite
		// reason: a removal that removed nothing is an error there, so
		// nothing reaches the publish at all.
		s.publish(projectID, eventViewPositions, viewEventMinRole, viewEventHumanOnly,
			viewPlacementEvent{ID: view.ID, Key: view.Key})
		return removed, nil
	}

	var removed int64
	for i, a := range entities {
		row, err := s.meta.EntityByKey(ctx, projectID, a.EntityType, a.EntityKey)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", pointer("entities", i), err)
		}
		// One statement per entity rather than one over an array of ids:
		// clearProblems has already bounded the list by the same cap a
		// write is — asserted, because this comment claimed the bound
		// before anything applied it — the delete is a primary-key
		// lookup, and a caller naming one node is the common call. Not
		// in a transaction, deliberately — a delete of a row
		// that is already gone is a no-op, so a call that fails halfway
		// leaves an arrangement with fewer positions and no row it should
		// not have, which is the same state a caller retrying reaches.
		gone, err := s.q.DeleteViewPosition(ctx, dbq.DeleteViewPositionParams{
			ProjectID: projectID, ViewID: view.ID, EntityID: row.ID,
		})
		if err != nil {
			return 0, fmt.Errorf("clear %s: %w", pointer("entities", i), err)
		}
		removed += gone
	}
	s.publish(projectID, eventViewPositions, viewEventMinRole, viewEventHumanOnly,
		viewPlacementEvent{ID: view.ID, Key: view.Key})
	return removed, nil
}

// GetPositions reads one view's stored positions, addressed by the two
// keys they were written with.
func (s *Service) GetPositions(ctx context.Context, projectID uuid.UUID, viewKey string) (
	[]Position, error,
) {
	view, err := s.ViewByKey(ctx, projectID, viewKey)
	if err != nil {
		return nil, err
	}
	return s.positionsOf(ctx, projectID, view.ID)
}

// positionsOf is GetPositions once the view has already been resolved.
func (s *Service) positionsOf(ctx context.Context, projectID, viewID uuid.UUID) (
	[]Position, error,
) {
	rows, err := s.q.ListViewPositions(ctx, dbq.ListViewPositionsParams{
		ProjectID: projectID, ViewID: viewID,
	})
	if err != nil {
		return nil, fmt.Errorf("list view positions: %w", err)
	}
	out := make([]Position, 0, len(rows))
	for _, row := range rows {
		out = append(out, Position{
			EntityType: row.EntityType, EntityKey: row.EntityKey,
			X: row.X, Y: row.Y, Pinned: row.Pinned,
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}

// positionProblems judges one SetPositions call's own arguments, whole,
// so a caller with three bad positions hears about all three.
func positionProblems(positions []PositionInput) []metamodel.FieldError {
	if len(positions) == 0 {
		return []metamodel.FieldError{{
			Path: pointer("positions"),
			Message: "is empty: a call that sets no position changes nothing, and " +
				"reporting a change it did not make would be worse than refusing it",
		}}
	}
	if len(positions) > MaxPositions {
		return []metamodel.FieldError{{
			Path: pointer("positions"),
			Message: fmt.Sprintf("holds %d positions, and the most one call may carry is "+
				"%d: that is the most nodes a run of this view can return, so a longer "+
				"call is positioning something no run has shown you",
				len(positions), MaxPositions),
		}}
	}
	problems := make([]metamodel.FieldError, 0)
	for i, p := range positions {
		problems = append(problems, addressProblems(pointer("positions", i),
			EntityAddress{EntityType: p.EntityType, EntityKey: p.EntityKey})...)
		problems = append(problems, oneValue(pointer("positions", i, "x"),
			coordinateProblem(p.X))...)
		problems = append(problems, oneValue(pointer("positions", i, "y"),
			coordinateProblem(p.Y))...)
	}
	return problems
}

// clearProblems judges one ClearPositions call's own arguments, whole,
// the way positionProblems does for a write — one function per call so
// each says what its own list means, and the same three refusals in the
// same order so the two calls cannot drift apart.
func clearProblems(entities []EntityAddress) []metamodel.FieldError {
	if entities == nil {
		return nil
	}
	if len(entities) == 0 {
		return []metamodel.FieldError{{
			Path: pointer("entities"),
			Message: "is empty: send no list at all to clear every position of this " +
				"view, or name the entities to clear — an empty list is neither, " +
				"and clearing a whole arrangement is not something to do by accident",
		}}
	}
	if len(entities) > MaxPositions {
		return []metamodel.FieldError{{
			Path: pointer("entities"),
			Message: fmt.Sprintf("names %d entities, and the most one call may carry is "+
				"%d: the same cap a write takes, because this call resolves and deletes "+
				"one address at a time and a longer list is an unbounded loop on one "+
				"connection",
				len(entities), MaxPositions),
		}}
	}
	problems := make([]metamodel.FieldError, 0)
	for i, a := range entities {
		problems = append(problems, addressProblems(pointer("entities", i), a)...)
	}
	return problems
}

// addressProblems bounds the two keys that address a node, through
// metamodel.RowKeyProblems rather than through a rule written here.
func addressProblems(ptr string, a EntityAddress) []metamodel.FieldError {
	problems := metamodel.RowKeyProblems(ptr+"/entity_type", a.EntityType)
	return append(problems, metamodel.RowKeyProblems(ptr+"/entity_key", a.EntityKey)...)
}

// coordinateProblem refuses the three doubles a coordinate cannot be.
func coordinateProblem(v float64) string {
	switch {
	case math.IsNaN(v):
		return "is NaN: a coordinate must be a finite number"
	case math.IsInf(v, 0):
		return "is infinite: a coordinate must be a finite number"
	default:
		return ""
	}
}
