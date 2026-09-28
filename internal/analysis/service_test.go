package analysis

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/roles"
)

func TestService(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestAFailedUnitOfWorkLeavesNothingBehind pins withTx's contract, which
	// is the one the route CRUD rests on: a route's row and its whole ordered
	// step list are one change, so a step rewrite that landed beside a route
	// that rolled back would leave a walk nobody authored.
	t.Run("a failed unit of work leaves nothing behind", func(t *testing.T) {
		g := a.game(t)

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
		assert.Must(t, errors.Is(err, sentinel), "err = %v, want fn's own error returned unwrapped enough to match", err)
		if got := countRelationTypes(t, g, "rolled-back"); got != 0 {
			t.Fatalf("%d rows survived a rolled-back unit of work", got)
		}
	})

	// TestNothingIsPublishedWithoutAHub. The hub is optional — this
	// package's own tests run without one, and internal/web is where
	// publication is asserted end to end — so publish must be a no-op rather
	// than a nil dereference, and the alternative is a panic in a code path
	// only a production wiring reaches.
	t.Run("nothing is published without a hub", func(t *testing.T) {
		g := a.game(t)
		assert.Must(t, g.analysis.hub == nil, "this fixture is supposed to have no hub")
		g.analysis.publish(g.projectID, "route.checked", roles.Viewer, false,
			map[string]any{"key": "the-critical-path"})
	})

	// TestAPublishedEventCarriesItsGatingAsGiven pins that publish passes
	// minRole and humanOnly through rather than deriving them from the kind.
	t.Run("a published event carries its gating as given", func(t *testing.T) {
		g := a.game(t)
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
			assert.Must(t, event.Kind == "route.checked", "kind = %q", event.Kind)
			assert.Must(t, event.MinRole == string(roles.Editor) && event.HumanOnly, "event = %+v, want the gating this call site chose", event)
			assert.Must(t, event.ProjectID == g.projectID, "project = %s, want %s", event.ProjectID, g.projectID)
		default:
			t.Fatal("nothing was published to a subscriber of this game")
		}

		select {
		case event := <-viewer.C:
			t.Fatalf("a viewer received an editor-gated event: %+v", event)
		default:
		}
	})
}

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
