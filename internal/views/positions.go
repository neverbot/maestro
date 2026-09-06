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
//
// **What the server does about layout: nothing.** `layout_mode` is
// validated (views.go), stored (0008_views.sql) and
// returned, and **no server code reads the mode beyond validating and
// returning it** — nothing in this file branches on it, nothing in this
// package computes a coordinate, and `pinned` is likewise written, read
// back and never acted on here. The three modes are instructions to the
// *client*: `auto` lays every node out and ignores what is stored,
// `manual` uses every stored position and places the rest once, `mixed`
// fixes the pinned nodes and lays the rest out around them. Which engine
// draws that is sub-project 5's question.
//
// That claim is a claim about behaviour, so it is asserted rather than
// promised: TestTheLayoutModeChangesNothingTheServerAnswers saves one
// view under each of the three modes and requires the positions this
// package hands back to be identical in all three. A comment saying the
// server honours a mode it never sees is this project's first defect in
// its purest form, and a comment saying the server ignores one it
// secretly reads is the same defect with the sign flipped.
//
// **Addressed by entity key plus type key, never by id.** An agent that
// has never read this game can write a position from the two keys it
// used to create the entity, and the resolution is
// metamodel.EntityByKey's — the project-filtered lookup that already
// exists, with the two messages that already tell a wrong type key from
// a wrong entity key apart.
//
// **A position write does not advance the view's version.** The version
// guards the query document (Task 11); a drag is not an edit of that
// document, and making it one would hand every agent holding a version a
// conflict for a change to something it never wrote.
//
// **A position write publishes view.positions**, from SetPositions and
// from ClearPositions alike, once the write has landed. Task 13 left the
// decision to Task 15 because only two kinds were registered then and a
// third would have reached nobody; Task 15 registered it, on the
// argument the events design already makes for an invalidation — a
// browser holding a picture has no other way to learn the picture
// changed, and a drag is that situation exactly. events.go carries the
// payload, the gating and the reasoning.

// MaxPositions is the most positions one SetPositions call may carry.
//
// It is HardMaxNodes rather than a number of its own, and that is the
// judgement rather than a coincidence: a run cannot return more than
// HardMaxNodes nodes, so a call positioning more nodes than that is
// positioning something no run showed the caller. One rule, one place —
// if the node cap moves, this moves with it.
const MaxPositions = HardMaxNodes

// DefaultPinned is 0008_views.sql's column default, spelled here for the
// reason DefaultLayoutMode is: this package fills the value rather than
// letting the column default it, because every position this file writes
// names the column.
//
// True: a position written without saying otherwise is an explicit
// placement, not a spot a layout algorithm may move. PositionInput.Pinned
// is a *bool because `false` is a value a caller may deliberately mean,
// so a plain bool's zero value would silently store the opposite of the
// column default for every caller that said nothing.
const DefaultPinned = true

// PositionInput is one node's coordinates in one view.
//
// EntityType and EntityKey address the node the way an agent thinks of
// it. Both are matched without regard to case, as every key in Maestro
// is, which is also why two spellings of one key inside one call are
// refused rather than silently collapsed — see SetPositions.
//
// X and Y are in the client's own coordinate space and mean nothing to
// this package beyond being finite. Nothing here scales, snaps or clamps
// them; the renderer catalogue's `snap` parameter is the client's grid,
// not the server's.
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
//
// It carries the keys rather than the entity id, so the answer to
// GetPositions is addressed in the same vocabulary the write took: a
// caller can round-trip it without ever having read the game's ids.
//
// UpdatedAt is when the node was last moved, and it is the one field
// here nothing renders. It is what lets a caller — and
// TestRunningAViewNeverRewritesPositions — tell "this arrangement is
// untouched" from "somebody re-dragged it to the same spot", which is
// the property that lets a query be edited without losing an afternoon
// of map work.
type Position struct {
	EntityType string    `json:"entity_type"`
	EntityKey  string    `json:"entity_key"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	Pinned     bool      `json:"pinned"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SetPositions writes the coordinates of one or more nodes of one view.
//
// **Three passes, in the order a caller can act on them**, which is the
// order UpsertView already refuses in: the arguments this call carries,
// then the addresses they name, then the write.
//
//  1. The arguments. Every problem is reported at once, addressed by
//     JSON pointer into the call's own `positions` array, with
//     invalid_input — the code metamodel and markdown answer a row's own
//     arguments with, at this package's own address style (Task 11's
//     finding 12). A non-finite coordinate is refused here, ahead of the
//     CHECK 0008_views.sql carries: the constraint answers with SQLSTATE
//     23514 over a value the caller itself supplied, which reaches an
//     agent as internal_error.
//  2. The addresses, through metamodel.EntityByKey, which is
//     project-filtered in SQL and already distinguishes a wrong type key
//     from a wrong entity key. A miss is not_found naming the position it
//     came from.
//  3. The write, in one transaction, so a call that names ten nodes and
//     misses on the tenth leaves the other nine unwritten rather than
//     half-dragging a picture.
//
// **An empty list is refused rather than answered with success.** A
// caller that sets no positions changed nothing, and reporting a change
// it did not make is the silent no-op this package refuses everywhere
// else. Removing positions is ClearPositions' job.
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
			//
			// **Unreachable from this call, and recorded as such rather
			// than dressed up.** The view was resolved inside this game and
			// so was every entity, and 0008_views.sql's composite key means
			// a row they collide with carries this game's project id — so
			// deleting these three lines leaves the whole package green,
			// measured. It stays because the alternative is discarding a
			// row count that can only be zero if an invariant of this
			// package has been broken, and a silent no-op is how the
			// cross-game overwrite this guard exists for got as far as a
			// green test run (views.sql says what it was). The guard
			// itself is load-bearing and is asserted by driving the
			// statement directly, in
			// TestPositionsOfAnotherGameAreNotReachable.
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
//
// **A nil list clears the whole view; an empty one is refused.** The
// distinction is markdown's WriteInput.Links rule — said nothing is not
// the same as said none — and here it is what stands between a caller
// whose list of dirty nodes came out empty and a wiped arrangement.
// Clearing everything is a thing a designer asks for, so it stays
// reachable; it is not something a caller should be able to do by
// accident.
//
// **The same three passes SetPositions makes, in the same order**: the
// arguments this call carries, then the addresses they name, then the
// write. The order is stated on both calls and was wrong here — the view
// was resolved before the arguments were judged, so a call naming a
// missing view *and* a malformed address heard about the view, while the
// same pair on SetPositions heard about the address. Now both answer the
// argument first, and TestAPositionCallRefusesItsArgumentsInTheSameOrder
// pins that they agree.
//
// **The list is capped at MaxPositions, exactly as a write is**, and for
// the reason a write is: one statement per entity is a lookup plus a
// delete per address, on one pooled connection, and an uncapped list is
// an unbounded loop a single call can park that connection on. Measured
// before the cap existed: fifty thousand addresses took 24.6 s and
// returned success. The cap is answered here, before a single address is
// resolved, which is what
// TestAnEmptyOrOversizeClearIsRefused asserts by naming entities that do
// not exist.
//
// An address that names no entity of this game is refused exactly as it
// is in SetPositions, and through the same lookup. An entity that exists
// and was never dragged is not: it has no row, and removing nothing from
// it is the answer, not an error — which is why the count is the answer
// this call gives back rather than a check it performs.
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
//
// The order is the entity type key then the entity key, which is stable
// across calls and independent of the order they were written in: a
// caller diffing two reads of one view is diffing arrangements rather
// than insertion orders.
//
// **Nothing here filters on pinned or on the view's layout mode**, and
// that is the layout contract this file's header states: every stored
// position comes back under all three modes, and which of them the
// client honours is the client's decision.
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
//
// It exists because RunView holds the row already and reads the same
// rows into Result.Positions: a second ViewByKey there would be a second
// lookup of a row this package is holding, and — worse — a second place
// that decides which columns a position is made of, which is how the two
// answers would start to differ.
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
//
// A nil list is every position of the view and is not a problem. An
// empty one is refused: said nothing is not said none, and here that
// distinction stands between a caller whose list of dirty nodes came out
// empty and a wiped arrangement.
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
//
// The same argument internal/markdown's entityAddressKeyProblems makes,
// and the same concrete thing it closes: the rule that decides which
// spellings can exist is the one that must decide which spellings can be
// asked for, and a key holding an invalid UTF-8 byte raises SQLSTATE
// 22021 out of the lookup — internal_error over a value the caller
// supplied, which this package's standing rule refuses.
func addressProblems(ptr string, a EntityAddress) []metamodel.FieldError {
	problems := metamodel.RowKeyProblems(ptr+"/entity_type", a.EntityType)
	return append(problems, metamodel.RowKeyProblems(ptr+"/entity_key", a.EntityKey)...)
}

// coordinateProblem refuses the three doubles a coordinate cannot be.
//
// NaN and both infinities, named at the caller's own path, ahead of
// 0008_views.sql's CHECK — which exists for a write path that does not
// come through here and answers with an untyped 23514 when it fires.
// They are refused rather than clamped because a NaN coordinate is a
// client's arithmetic that went wrong, and storing a number the client
// did not compute would hide that.
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
