package analysis

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

// TestAFailedUnitOfWorkLeavesNothingBehind pins withTx's contract, which
// is the one the route CRUD rests on: a route's row and its whole ordered
// step list are one change, so a step rewrite that landed beside a route
// that rolled back would leave a walk nobody authored.
//
// It asserts the rollback through a write this package can already make
// — a route row — with the positive control that the identical write
// commits when fn returns nil. Without that control, a withTx that
// never wrote anything at all would pass the rollback half.
func TestAFailedUnitOfWorkLeavesNothingBehind(t *testing.T) {
	g := newGame(t)

	// The control: the same statement, committed.
	if err := g.analysis.withTx(t.Context(), func(q *dbq.Queries) error {
		return writeRelationType(t.Context(), q, g.projectID, "kept")
	}); err != nil {
		t.Fatalf("the control must commit: %v", err)
	}
	if got := countRelationTypes(t, g, "kept"); got != 1 {
		t.Fatalf("the committed row is not there (%d rows), so the rollback half "+
			"below would pass against a function that writes nothing", got)
	}

	sentinel := errors.New("the unit of work failed")
	err := g.analysis.withTx(t.Context(), func(q *dbq.Queries) error {
		if err := writeRelationType(t.Context(), q, g.projectID, "rolled-back"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want fn's own error returned unwrapped enough to match", err)
	}
	if got := countRelationTypes(t, g, "rolled-back"); got != 0 {
		t.Fatalf("%d rows survived a rolled-back unit of work", got)
	}
}

// TestNothingIsPublishedWithoutAHub. The hub is optional — this
// package's own tests run without one, and internal/web is where
// publication is asserted end to end — so publish must be a no-op rather
// than a nil dereference, and the alternative is a panic in a code path
// only a production wiring reaches.
func TestNothingIsPublishedWithoutAHub(t *testing.T) {
	g := newGame(t)
	if g.analysis.hub != nil {
		t.Fatal("this fixture is supposed to have no hub")
	}
	g.analysis.publish(g.projectID, "route.checked", roles.Viewer, false,
		map[string]any{"key": "the-critical-path"})
}

// TestAPublishedEventCarriesItsGatingAsGiven pins that publish passes
// minRole and humanOnly through rather than deriving them from the kind.
//
// They are passed explicitly at every call site in this repository, so
// that each site shows the gating it chose instead of inheriting one from
// a table three files away — and a publish that quietly substituted a
// default would make every one of those choices decorative.
func TestAPublishedEventCarriesItsGatingAsGiven(t *testing.T) {
	g := newGame(t)
	hub := realtime.NewHub()
	g.analysis.hub = hub

	// Subscribed as an editor and as a human, which are the two gates the
	// publish below states: a viewer subscription would be filtered out
	// by MinRole and a token one by HumanOnly, and either would leave
	// this test asserting the gates rather than the pass-through.
	sub := hub.Subscribe(g.projectID, string(roles.Editor), false)
	defer hub.Unsubscribe(sub)

	// The gates are real, and this is the control that says so: the same
	// event does not reach a viewer.
	viewer := hub.Subscribe(g.projectID, string(roles.Viewer), false)
	defer hub.Unsubscribe(viewer)

	g.analysis.publish(g.projectID, "route.checked", roles.Editor, true,
		map[string]any{"key": "the-critical-path"})

	select {
	case event := <-sub.C:
		if event.Kind != "route.checked" {
			t.Fatalf("kind = %q", event.Kind)
		}
		if event.MinRole != string(roles.Editor) || !event.HumanOnly {
			t.Fatalf("event = %+v, want the gating this call site chose", event)
		}
		if event.ProjectID != g.projectID {
			t.Fatalf("project = %s, want %s", event.ProjectID, g.projectID)
		}
	default:
		t.Fatal("nothing was published to a subscriber of this game")
	}

	select {
	case event := <-viewer.C:
		t.Fatalf("a viewer received an editor-gated event: %+v", event)
	default:
	}
}

// writeRelationType writes one row through the generated queries the
// transaction hands out. This task ships no route CRUD — Task 9 does —
// and withTx's contract is worth pinning before the code that depends on
// it exists rather than after, so the write under test is whatever row
// this package can already reach.
func writeRelationType(ctx context.Context, q *dbq.Queries, projectID uuid.UUID, key string) error {
	_, err := q.UpsertRelationType(ctx, dbq.UpsertRelationTypeParams{
		ProjectID:     projectID,
		Key:           key,
		Label:         key,
		SourceTypeIds: []uuid.UUID{},
		TargetTypeIds: []uuid.UUID{},
		FieldSchema:   []byte(`[]`),
	})
	return err
}

// countRelationTypes counts this game's relation types with one key, read
// outside every transaction the test staged.
func countRelationTypes(t *testing.T, g game, key string) int {
	t.Helper()
	var n int
	if err := g.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM relation_types WHERE project_id = $1 AND key = $2`,
		g.projectID, key).Scan(&n); err != nil {
		t.Fatalf("count relation types: %v", err)
	}
	return n
}
