package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/realtime"
)

func ptrBool(v bool) *bool { return &v }

// placed is the two-position call most tests in this file start from:
// one node whose pinned flag is written explicitly false, one that says
// nothing and takes the default. The asymmetry is deliberate — a fixture
// where every node is pinned cannot tell a stored flag from a hard-wired
// DefaultPinned.
func placed() []PositionInput {
	return []PositionInput{
		{EntityType: "quest", EntityKey: "hogger", X: 12.5, Y: -40.25},
		{EntityType: "quest", EntityKey: "defias", X: 0, Y: 0, Pinned: ptrBool(false)},
	}
}

// mustSaveView saves a view whose query resolves and whose renderer can
// draw it, for the tests here that need a view to hang positions on and
// do not care what it draws.
func mustSaveView(t *testing.T, g *game, key string) dbq.View {
	t.Helper()
	row, err := g.views.UpsertView(context.Background(), g.projectID, saveable(key, questsOnly))
	assert.Must(t, err == nil, "save view %q: %v", key, err)
	return row
}

func mustGetPositions(t *testing.T, g *game, key string) []Position {
	t.Helper()
	got, err := g.views.GetPositions(context.Background(), g.projectID, key)
	assert.Must(t, err == nil, "get positions of %q: %v", key, err)
	return got
}

func TestPositionsArea(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestPositionsArea's "a position survives a reload" case is this task's
	// read-back, and `pinned` is the column it exists for.
	t.Run("a position survives a reload", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions: %v", err)
		}

		got := mustGetPositions(t, g, "route")
		want := []Position{
			{EntityType: "quest", EntityKey: "defias", X: 0, Y: 0, Pinned: false},
			{EntityType: "quest", EntityKey: "hogger", X: 12.5, Y: -40.25, Pinned: true},
		}
		assert.Must(t, len(got) == len(want), "read back %d positions, want %d: %+v", len(got), len(want), got)
		for i, w := range want {
			if got[i].EntityType != w.EntityType || got[i].EntityKey != w.EntityKey {
				t.Fatalf("position %d addresses %s/%s, want %s/%s", i,
					got[i].EntityType, got[i].EntityKey, w.EntityType, w.EntityKey)
			}
			if got[i].X != w.X || got[i].Y != w.Y {
				t.Errorf("%s: (x, y) = (%v, %v), want (%v, %v)",
					w.EntityKey, got[i].X, got[i].Y, w.X, w.Y)
			}
			if got[i].Pinned != w.Pinned {
				t.Errorf("%s: pinned = %v, want %v — the flag the whole mixed layout mode "+
					"turns on, written and read back", w.EntityKey, got[i].Pinned, w.Pinned)
			}
			assert.Should(t, !got[i].UpdatedAt.IsZero(), "%s: updated_at is zero, want the time the node was placed",
				w.EntityKey)
		}

		// A drag of a node that already has a position.
		moved := []PositionInput{
			{EntityType: "quest", EntityKey: "hogger", X: 99, Y: 1, Pinned: ptrBool(false)},
		}
		if err := g.views.SetPositions(ctx, g.projectID, "route", moved); err != nil {
			t.Fatalf("move a placed node: %v", err)
		}
		got = mustGetPositions(t, g, "route")
		assert.Must(t, len(got) == 2, "after moving one node the view holds %d positions, want 2", len(got))
		if got[1].X != 99 || got[1].Y != 1 || got[1].Pinned {
			t.Fatalf("hogger = %+v, want (99, 1) unpinned: a second write moves the node",
				got[1])
		}
	})

	// TestPositionsArea's "a position is pinned unless the caller says
	// otherwise" case pins the default at the level a caller meets it.
	t.Run("a position is pinned unless the caller says otherwise", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		in := []PositionInput{{EntityType: "quest", EntityKey: "cook", X: 1, Y: 2}}
		if err := g.views.SetPositions(ctx, g.projectID, "route", in); err != nil {
			t.Fatalf("set positions: %v", err)
		}
		got := mustGetPositions(t, g, "route")
		assert.Must(t, len(got) == 1 && got[0].Pinned, "positions = %+v, want one pinned node: a placement written without "+
			"saying otherwise is explicit, not a spot the layout may move", got)
		assert.Must(t, DefaultPinned == true, "DefaultPinned = %v, want the column default in 0008_views.sql",
			DefaultPinned)
	})

	// TestPositionsArea's "deleting an entity drops its position and keeps the
	// view" case is the foreign key's deliberate action, observed through the
	// service rather than through the schema test that pins the constraint
	// itself.
	t.Run("deleting an entity drops its position and keeps the view", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		view := mustSaveView(t, g, "route")

		if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions: %v", err)
		}
		if err := g.meta.RemoveEntity(ctx, g.projectID, "quest", "hogger"); err != nil {
			t.Fatalf("remove hogger: %v", err)
		}

		got := mustGetPositions(t, g, "route")
		assert.Must(t, len(got) == 1 && got[0].EntityKey == "defias", "positions = %+v, want defias alone: a deleted entity's position goes "+
			"with it", got)
		after, err := g.views.ViewByKey(ctx, g.projectID, "route")
		assert.Must(t, err == nil, "the view must survive an entity deletion: %v", err)
		assert.Must(t, after.ID == view.ID && after.Version == view.Version, "view = (%v, %d), want (%v, %d) untouched",
			after.ID, after.Version, view.ID, view.Version)
	})

	// TestPositionsArea's "positions are per view and not per entity" case is
	// the core spec's own requirement: the same zone sits at its real map
	// coordinates in a World map view and wherever the algorithm put it in a
	// Mage route view.
	t.Run("positions are per view and not per entity", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "world_map")
		mustSaveView(t, g, "mage_route")

		set := func(view string, x, y float64) {
			t.Helper()
			in := []PositionInput{{EntityType: "quest", EntityKey: "hogger", X: x, Y: y}}
			if err := g.views.SetPositions(ctx, g.projectID, view, in); err != nil {
				t.Fatalf("set positions on %s: %v", view, err)
			}
		}
		set("world_map", 10, 20)
		set("mage_route", 300, 400)
		// Written after the other view's, so a shared row would have been
		// overwritten by it.
		set("world_map", 10, 20)

		for _, tc := range []struct {
			view string
			x, y float64
		}{{"world_map", 10, 20}, {"mage_route", 300, 400}} {
			got := mustGetPositions(t, g, tc.view)
			assert.Should(t, len(got) == 1 && got[0].X == tc.x && got[0].Y == tc.y, "%s holds %+v, want hogger at (%v, %v)", tc.view, got, tc.x, tc.y)
		}
	})

	// TestPositionsArea's "a non finite position is refused with its index"
	// case refuses the three doubles a coordinate cannot be, at the caller's
	// own path.
	t.Run("a non finite position is refused with its index", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		in := []PositionInput{
			{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 2},
			{EntityType: "quest", EntityKey: "defias", X: math.NaN(), Y: 0},
			{EntityType: "quest", EntityKey: "cook", X: 0, Y: math.Inf(1)},
			{EntityType: "zone", EntityKey: "elwynn", X: math.Inf(-1), Y: 0},
		}
		err := g.views.SetPositions(ctx, g.projectID, "route", in)
		var ve *metamodel.ValidationError
		assert.Must(t, errors.As(err, &ve), "err = %#v, want a *metamodel.ValidationError", err)
		assert.Must(t, errors.Is(err, ErrInvalidInput), "err = %v, want invalid_input: these are the caller's own arguments", err)
		want := []string{"/positions/1/x", "/positions/2/y", "/positions/3/x"}
		assert.Must(t, len(ve.Fields) == len(want), "problems = %v, want one per non-finite coordinate at %v", ve.Fields, want)
		for i, path := range want {
			if ve.Fields[i].Path != path {
				t.Errorf("problem %d at %q, want %q", i, ve.Fields[i].Path, path)
			}
		}
		if got := mustGetPositions(t, g, "route"); len(got) != 0 {
			t.Fatalf("stored %+v, want nothing: a refused call stores none of its positions",
				got)
		}
	})

	// TestPositionsArea's "an empty or oversize positions call is refused"
	// case holds the two bounds on the list itself.
	t.Run("an empty or oversize positions call is refused", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		err := g.views.SetPositions(ctx, g.projectID, "route", nil)
		var ve *metamodel.ValidationError
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/positions", "err = %#v, want one problem at /positions for an empty call", err)

		oversize := make([]PositionInput, MaxPositions+1)
		for i := range oversize {
			oversize[i] = PositionInput{
				EntityType: "quest", EntityKey: fmt.Sprintf("ghost_%d", i), X: 1, Y: 1,
			}
		}
		err = g.views.SetPositions(ctx, g.projectID, "route", oversize)
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/positions", "err = %#v, want one problem at /positions for %d positions",
			err, len(oversize))
		assert.Must(t, !errors.Is(err, ErrNotFound), "err = %v, want the cap answered before the addresses are resolved", err)
		assert.Must(t, MaxPositions == HardMaxNodes, "MaxPositions = %d and HardMaxNodes = %d: the cap on a call is the node "+
			"cap, and one rule lives in one place", MaxPositions, HardMaxNodes)
	})

	// TestPositionsArea's "the same entity twice in one call is refused" case
	// refuses two coordinates for one node.
	t.Run("the same entity twice in one call is refused", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		in := []PositionInput{
			{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 1},
			{EntityType: "QUEST", EntityKey: "Hogger", X: 2, Y: 2},
		}
		err := g.views.SetPositions(ctx, g.projectID, "route", in)
		var ve *metamodel.ValidationError
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1, "err = %#v, want one problem naming the collision", err)
		if ve.Fields[0].Path != "/positions/1" {
			t.Fatalf("problem at %q, want /positions/1", ve.Fields[0].Path)
		}
		if got := mustGetPositions(t, g, "route"); len(got) != 0 {
			t.Fatalf("stored %+v, want nothing", got)
		}
	})

	// TestPositionsArea's "a position for an entity this game does not have is
	// refused" case names the position that is wrong, and tells a wrong type
	// key from a wrong entity key apart.
	t.Run("a position for an entity this game does not have is refused", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		for _, tc := range []struct {
			name, typeKey, key, wants string
		}{
			{"a key of a type this game has", "quest", "hoger", `no entity "hoger"`},
			{"a type this game does not have", "mount", "hogger", `no entity type "mount"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				in := []PositionInput{
					{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 1},
					{EntityType: tc.typeKey, EntityKey: tc.key, X: 2, Y: 2},
				}
				err := g.views.SetPositions(ctx, g.projectID, "route", in)
				assert.Must(t, errors.Is(err, ErrNotFound), "err = %#v, want not_found", err)
				if got := err.Error(); !strings.Contains(got, "/positions/1") || !strings.Contains(got, tc.wants) {
					t.Fatalf("err = %q, want it to name /positions/1 and %q", got, tc.wants)
				}
				if got := mustGetPositions(t, g, "route"); len(got) != 0 {
					t.Fatalf("stored %+v, want nothing: the good position of a refused call "+
						"is not written either", got)
				}
			})
		}
	})

	// TestPositionsArea's "running a view never rewrites positions" case is
	// what lets a query be edited, and a game grown, without losing an
	// afternoon of map work.
	t.Run("running a view never rewrites positions", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")
		if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions: %v", err)
		}
		before := mustGetPositions(t, g, "route")

		if _, err := g.views.RunView(ctx, g.projectID, "route", RunRequest{}); err != nil {
			t.Fatalf("first run: %v", err)
		}
		g.entity(t, "quest", "stockade", "The Stockade",
			map[string]any{"min_level": 24, "rank": "rare", "tags": []any{"dungeon"}})
		result, err := g.views.RunView(ctx, g.projectID, "route", RunRequest{})
		assert.Must(t, err == nil, "second run: %v", err)
		assert.Must(t, len(result.Nodes) == 4, "the second run drew %d nodes, want the four quests: the fixture must "+
			"actually have grown between the runs", len(result.Nodes))

		after := mustGetPositions(t, g, "route")
		assert.Must(t, len(after) == len(before), "positions = %d after the runs, want %d: a run never writes one",
			len(after), len(before))
		for i := range before {
			if after[i] != before[i] {
				t.Errorf("%s moved: %+v, want %+v — including updated_at, which is the half "+
					"a rewrite to the same coordinates would move",
					before[i].EntityKey, after[i], before[i])
			}
		}
	})

	// TestPositionsArea's "the layout mode changes nothing the server answers"
	// case is the assertion behind positions.go's headline claim: no server
	// code reads layout_mode beyond validating and returning it.
	t.Run("the layout mode changes nothing the server answers", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		row := mustSaveView(t, g, "route")
		if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions: %v", err)
		}
		baseline := mustGetPositions(t, g, "route")
		assert.Must(t, len(baseline) == 2 && baseline[0].Pinned != baseline[1].Pinned, "baseline = %+v, want two positions differing in pinned: a fixture "+
			"where every node is pinned cannot tell mixed from manual", baseline)

		version := row.Version
		for _, mode := range []string{LayoutAuto, LayoutManual, LayoutMixed} {
			in := saveable("route", questsOnly)
			in.LayoutMode = mode
			in.ExpectedVersion = ptrInt32(version)
			saved, err := g.views.UpsertView(ctx, g.projectID, in)
			assert.Must(t, err == nil, "save %s: %v", mode, err)
			version = saved.Version
			assert.Must(t, saved.LayoutMode == mode, "stored layout_mode = %q, want %q", saved.LayoutMode, mode)
			got := mustGetPositions(t, g, "route")
			assert.Must(t, len(got) == len(baseline), "%s: %d positions, want %d", mode, len(got), len(baseline))
			for i := range baseline {
				if got[i] != baseline[i] {
					t.Errorf("%s: %s = %+v, want %+v — the mode is a contract with the "+
						"client and nothing here reads it",
						mode, baseline[i].EntityKey, got[i], baseline[i])
				}
			}
		}
	})

	// TestPositionsArea's "clearing positions" case holds the three shapes of
	// a clear: every position of a view, the ones a caller names, and the
	// empty list that is refused rather than read as either.
	t.Run("clearing positions", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")
		set := func() {
			t.Helper()
			if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
				t.Fatalf("set positions: %v", err)
			}
		}

		set()
		removed, err := g.views.ClearPositions(ctx, g.projectID, "route",
			[]EntityAddress{{EntityType: "quest", EntityKey: "hogger"}})
		assert.Must(t, err == nil, "clear one: %v", err)
		assert.Must(t, removed == 1, "removed = %d, want 1", removed)
		got := mustGetPositions(t, g, "route")
		assert.Must(t, len(got) == 1 && got[0].EntityKey == "defias", "positions = %+v, want defias standing", got)

		// An entity that exists and was never dragged: nothing to remove is
		// an answer, not an error.
		removed, err = g.views.ClearPositions(ctx, g.projectID, "route",
			[]EntityAddress{{EntityType: "quest", EntityKey: "cook"}})
		assert.Must(t, err == nil && removed == 0, "clearing an unplaced node = (%d, %v), want (0, nil)", removed, err)

		set()
		if removed, err = g.views.ClearPositions(ctx, g.projectID, "route", nil); err != nil {
			t.Fatalf("clear every position: %v", err)
		}
		assert.Must(t, removed == 2, "removed = %d, want both positions", removed)
		if got = mustGetPositions(t, g, "route"); len(got) != 0 {
			t.Fatalf("positions = %+v, want none", got)
		}

		set()
		_, err = g.views.ClearPositions(ctx, g.projectID, "route", []EntityAddress{})
		var ve *metamodel.ValidationError
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/entities", "err = %#v, want one problem at /entities for an empty list", err)
		if got = mustGetPositions(t, g, "route"); len(got) != 2 {
			t.Fatalf("positions = %+v, want both still there: an empty list clears nothing",
				got)
		}

		// An address that names nothing is refused at its own index, through
		// the same lookup a write goes through.
		_, err = g.views.ClearPositions(ctx, g.projectID, "route", []EntityAddress{
			{EntityType: "quest", EntityKey: "hogger"},
			{EntityType: "quest", EntityKey: "ghost"},
		})
		assert.Must(t, errors.Is(err, ErrNotFound) && strings.Contains(err.Error(), "/entities/1"), "err = %#v, want not_found naming /entities/1", err)
	})

	// TestPositionsArea's "a position call names the view it cannot find"
	// case: every one of the three calls resolves the view by key, in this
	// game, before it does anything else.
	t.Run("a position call names the view it cannot find", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()

		err := g.views.SetPositions(ctx, g.projectID, "nowhere", placed())
		assert.Should(t, errors.Is(err, ErrNotFound), "set: err = %#v, want not_found", err)
		if _, err := g.views.ClearPositions(ctx, g.projectID, "nowhere", nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("clear: err = %#v, want not_found", err)
		}
		if _, err := g.views.GetPositions(ctx, g.projectID, "nowhere"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get: err = %#v, want not_found", err)
		}
	})

	// TestPositionsArea's "positions of another game are not reachable" case,
	// with its positive control.
	t.Run("positions of another game are not reachable", func(t *testing.T) {
		azeroth, outland := a.games(t)
		ctx := context.Background()
		mine := mustSaveView(t, azeroth, "route")
		mustSaveView(t, outland, "route")

		if err := azeroth.views.SetPositions(ctx, azeroth.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions in azeroth: %v", err)
		}

		// Through the service: one key, two games, two answers.
		if got := mustGetPositions(t, outland, "route"); len(got) != 0 {
			t.Fatalf("outland reads %+v, want nothing: the key it shares with azeroth names "+
				"its own view", got)
		}
		if got := mustGetPositions(t, azeroth, "route"); len(got) != 2 {
			t.Fatalf("azeroth reads %+v, want its own two positions", got)
		}

		// Directly, with azeroth's view id under outland's project id.
		rows, err := outland.views.q.ListViewPositions(ctx, dbq.ListViewPositionsParams{
			ProjectID: outland.projectID, ViewID: mine.ID,
		})
		assert.Must(t, err == nil, "list: %v", err)
		assert.Should(t, len(rows) == 0, "ListViewPositions returned %d of azeroth's rows to outland", len(rows))
		gone, err := outland.views.q.DeleteViewPositions(ctx, dbq.DeleteViewPositionsParams{
			ProjectID: outland.projectID, ViewID: mine.ID,
		})
		assert.Must(t, err == nil, "delete all: %v", err)
		assert.Should(t, gone == 0, "DeleteViewPositions removed %d of azeroth's rows for outland", gone)
		hogger, err := azeroth.meta.EntityByKey(ctx, azeroth.projectID, "quest", "hogger")
		assert.Must(t, err == nil, "read hogger: %v", err)
		gone, err = outland.views.q.DeleteViewPosition(ctx, dbq.DeleteViewPositionParams{
			ProjectID: outland.projectID, ViewID: mine.ID, EntityID: hogger.ID,
		})
		assert.Must(t, err == nil, "delete one: %v", err)
		assert.Should(t, gone == 0, "DeleteViewPosition removed %d of azeroth's rows for outland", gone)
		// The write takes two mechanisms and both are asserted, because the
		// first alone was not enough: with only the composite foreign keys,
		// an insert that *conflicts* with a stored row is an UPDATE of that
		// row, project_id is not in its SET list, every key stays satisfied,
		// and outland silently moved azeroth's node. That is what this
		// statement's guard is for, and it is why the control below compares
		// coordinates rather than counting rows — the count was two either
		// way, which is how the first version of this test passed.
		cook, err := azeroth.meta.EntityByKey(ctx, azeroth.projectID, "quest", "cook")
		assert.Must(t, err == nil, "read cook: %v", err)
		_, err = outland.views.q.UpsertViewPosition(ctx, dbq.UpsertViewPositionParams{
			ViewID: mine.ID, EntityID: cook.ID, ProjectID: outland.projectID,
			X: 1, Y: 1, Pinned: true,
		})
		var pgErr *pgconn.PgError
		assert.Should(t, errors.As(err, &pgErr) && pgErr.Code == "23503", "inserting into azeroth's view from outland: err = %#v, want 23503", err)
		// hogger has one, so it takes the conflict path and meets the guard.
		written, err := outland.views.q.UpsertViewPosition(ctx, dbq.UpsertViewPositionParams{
			ViewID: mine.ID, EntityID: hogger.ID, ProjectID: outland.projectID,
			X: 1, Y: 1, Pinned: true,
		})
		assert.Should(t, err == nil, "overwriting azeroth's position from outland: err = %v, want no error "+
			"and no row", err)
		assert.Should(t, written == 0, "the guarded DO UPDATE wrote %d rows for outland, want 0", written)

		// The positive control: azeroth's arrangement survived all of it,
		// coordinates included.
		got := mustGetPositions(t, azeroth, "route")
		assert.Must(t, len(got) == 2, "azeroth reads %+v, want its two positions", got)
		if got[1].X != 12.5 || got[1].Y != -40.25 || !got[1].Pinned {
			t.Fatalf("azeroth's hogger = %+v, want (12.5, -40.25) pinned: another game must "+
				"not move it", got[1])
		}
	})

	// TestPositionsArea's "a position is written into the view it names" case
	// is the control the isolation test above needs and cannot carry: with two
	// games holding the same keys, a call that wrote into the *other* game's
	// view would be caught, but so would a call that wrote nothing at all.
	// This one asserts the row lands in the right game's row by reading it
	// back through the other game's service as well.
	t.Run("a position is written into the view it names", func(t *testing.T) {
		azeroth, outland := a.games(t)
		ctx := context.Background()
		mustSaveView(t, azeroth, "route")
		mustSaveView(t, outland, "route")

		in := []PositionInput{{EntityType: "quest", EntityKey: "hogger", X: 7, Y: 7}}
		if err := outland.views.SetPositions(ctx, outland.projectID, "route", in); err != nil {
			t.Fatalf("set positions in outland: %v", err)
		}
		if got := mustGetPositions(t, outland, "route"); len(got) != 1 || got[0].X != 7 {
			t.Fatalf("outland reads %+v, want its own hogger at 7", got)
		}
		if got := mustGetPositions(t, azeroth, "route"); len(got) != 0 {
			t.Fatalf("azeroth reads %+v, want nothing: outland's write is outland's", got)
		}
	})

	// TestPositionsArea's "clearing one view leaves another views arrangement
	// standing" case is the clear path's half of the core spec's per-view
	// requirement, and it was the missing half.
	t.Run("clearing one view leaves another views arrangement standing", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")
		mustSaveView(t, g, "map")
		for _, key := range []string{"route", "map"} {
			if err := g.views.SetPositions(ctx, g.projectID, key, placed()); err != nil {
				t.Fatalf("set positions in %q: %v", key, err)
			}
		}

		// One named node, cleared out of one view only.
		removed, err := g.views.ClearPositions(ctx, g.projectID, "route",
			[]EntityAddress{{EntityType: "quest", EntityKey: "hogger"}})
		assert.Must(t, err == nil, "clear one: %v", err)
		assert.Must(t, removed == 1, "removed = %d, want 1: the same node in another view is not this "+
			"call's to remove", removed)
		if got := mustGetPositions(t, g, "map"); len(got) != 2 {
			t.Fatalf("map holds %+v after route cleared one node, want both its own "+
				"positions", got)
		}

		// The whole view, cleared out of one view only.
		removed, err = g.views.ClearPositions(ctx, g.projectID, "route", nil)
		assert.Must(t, err == nil, "clear every position of route: %v", err)
		assert.Must(t, removed == 1, "removed = %d, want 1: route had one position left, and map's two are "+
			"not route's to clear", removed)
		if got := mustGetPositions(t, g, "route"); len(got) != 0 {
			t.Fatalf("route holds %+v after its own clear, want none", got)
		}
		got := mustGetPositions(t, g, "map")
		assert.Must(t, len(got) == 2, "map holds %+v after route was cleared whole, want both its own "+
			"positions: clearing one view must not wipe another's arrangement", got)
		// Coordinates, not a count: the numbers are what a designer loses.
		if got[1].X != 12.5 || got[1].Y != -40.25 {
			t.Fatalf("map's hogger = %+v, want (12.5, -40.25)", got[1])
		}
	})

	// TestPositionsArea's "an empty or oversize clear is refused" case is the
	// twin of TestPositionsArea's "an empty or oversize positions call is
	// refused" case, and the cap half of it was a bound stated in prose and
	// absent from the code.
	t.Run("an empty or oversize clear is refused", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")

		_, err := g.views.ClearPositions(ctx, g.projectID, "route", []EntityAddress{})
		var ve *metamodel.ValidationError
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/entities", "err = %#v, want one problem at /entities for an empty list", err)

		oversize := make([]EntityAddress, MaxPositions+1)
		for i := range oversize {
			oversize[i] = EntityAddress{
				EntityType: "quest", EntityKey: fmt.Sprintf("ghost_%d", i),
			}
		}
		_, err = g.views.ClearPositions(ctx, g.projectID, "route", oversize)
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/entities", "err = %#v, want one problem at /entities for %d addresses",
			err, len(oversize))
		assert.Must(t, !errors.Is(err, ErrNotFound), "err = %v, want the cap answered before the addresses are resolved", err)
	})

	// TestPositionsArea's "a position call refuses its arguments in the same
	// order" case pins the one thing the two calls' doc comments both claim
	// and only one of them did.
	t.Run("a position call refuses its arguments in the same order", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()

		var ve *metamodel.ValidationError
		err := g.views.SetPositions(ctx, g.projectID, "nosuchview",
			[]PositionInput{{EntityType: "quest", EntityKey: "", X: 1, Y: 1}})
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/positions/0/entity_key", "set: err = %#v, want invalid_input at /positions/0/entity_key", err)

		_, err = g.views.ClearPositions(ctx, g.projectID, "nosuchview",
			[]EntityAddress{{EntityType: "quest", EntityKey: ""}})
		assert.Must(t, errors.As(err, &ve) && len(ve.Fields) == 1 && ve.Fields[0].Path == "/entities/0/entity_key", "clear: err = %#v, want invalid_input at /entities/0/entity_key: the "+
			"two calls state the same order and must refuse in it", err)
		assert.Must(t, !errors.Is(err, ErrNotFound), "clear: err = %v, want the arguments judged before the view is "+
			"resolved", err)
	})

	// TestPositionsArea's "a position write publishes its invalidation" case
	// is Task 15's decision, asserted rather than described: a drag reaches
	// every subscriber a query edit reaches, because a browser holding a
	// picture has no other way to learn the arrangement moved under it.
	t.Run("a position write publishes its invalidation", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		hub := realtime.NewHub()
		svc := New(g.pool, hub)

		row, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
		assert.Must(t, err == nil, "save the view: %v", err)

		viewer := hub.Subscribe(g.projectID, "viewer", false)
		defer hub.Unsubscribe(viewer)
		agent := hub.Subscribe(g.projectID, "viewer", true)
		defer hub.Unsubscribe(agent)

		for _, step := range []struct {
			what string
			do   func() error
		}{
			{"a drag", func() error {
				return svc.SetPositions(ctx, g.projectID, "route", placed())
			}},
			{"a clear of one node", func() error {
				_, err := svc.ClearPositions(ctx, g.projectID, "route",
					[]EntityAddress{{EntityType: "quest", EntityKey: "hogger"}})
				return err
			}},
			{"a clear of the whole view", func() error {
				_, err := svc.ClearPositions(ctx, g.projectID, "route", nil)
				return err
			}},
		} {
			if err := step.do(); err != nil {
				t.Fatalf("%s: %v", step.what, err)
			}
			for who, sub := range map[string]*realtime.Subscription{"viewer": viewer, "agent": agent} {
				got := receive(t, sub)
				assert.Must(t, got.Kind == "view.positions", "%s after %s got %q, want view.positions", who, step.what, got.Kind)
				assertPositionsPayload(t, who, got, row.ID, "route")
			}
		}
	})

	// TestPositionsArea's "no position event is published when the write is
	// refused" case is the control the test above cannot be without: a publish
	// placed before the write, or outside the transaction's error check,
	// announces an arrangement that never landed, and every subscriber's
	// reaction is to re-read a picture that did not change.
	t.Run("no position event is published when the write is refused", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		hub := realtime.NewHub()
		svc := New(g.pool, hub)

		if _, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly)); err != nil {
			t.Fatalf("save the view: %v", err)
		}
		sub := hub.Subscribe(g.projectID, "owner", false)
		defer hub.Unsubscribe(sub)

		for _, tc := range []struct {
			why string
			do  func() error
		}{
			{"the call named no position at all", func() error {
				return svc.SetPositions(ctx, g.projectID, "route", nil)
			}},
			{"the call named an entity this game does not have", func() error {
				return svc.SetPositions(ctx, g.projectID, "route",
					[]PositionInput{{EntityType: "quest", EntityKey: "nosuchquest", X: 1, Y: 1}})
			}},
			{"the call named a view this game does not have", func() error {
				return svc.SetPositions(ctx, g.projectID, "nosuchview", placed())
			}},
			{"the clear named an entity this game does not have", func() error {
				_, err := svc.ClearPositions(ctx, g.projectID, "route",
					[]EntityAddress{{EntityType: "quest", EntityKey: "nosuchquest"}})
				return err
			}},
			{"the clear named a view this game does not have", func() error {
				_, err := svc.ClearPositions(ctx, g.projectID, "nosuchview", nil)
				return err
			}},
		} {
			t.Run(tc.why, func(t *testing.T) {
				if err := tc.do(); err == nil {
					t.Fatalf("the call was accepted; this test needs it refused")
				}
				requireNothing(t, sub, tc.why)
			})
		}
	})

	// TestPositionsArea's "a saved views run carries its arrangement and an ad
	// hoc one does not" case is the positions member of the run envelope,
	// which Task 13 deferred to Task 15 on the ground that nothing read one
	// before Task 16.
	t.Run("a saved views run carries its arrangement and an ad hoc one does not", func(t *testing.T) {
		g, _ := a.games(t)
		ctx := context.Background()
		mustSaveView(t, g, "route")
		if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
			t.Fatalf("set positions: %v", err)
		}

		saved, err := g.views.RunView(ctx, g.projectID, "route", RunRequest{})
		assert.Must(t, err == nil, "RunView: %v", err)
		assert.Must(t, len(saved.Positions) == 2, "Positions = %+v, want the two that were dragged", saved.Positions)
		// The drawn nodes outnumber the placed ones, which is what makes the
		// absence assertion below mean something: an implementation padding
		// the list to one entry per node would have three.
		assert.Must(t, len(saved.Nodes) > len(saved.Positions), "the view draws %d nodes and %d are placed; this test needs an unplaced one",
			len(saved.Nodes), len(saved.Positions))
		placedKeys := map[string]Position{}
		for _, p := range saved.Positions {
			placedKeys[p.EntityType+"/"+p.EntityKey] = p
		}
		hogger, ok := placedKeys["quest/hogger"]
		assert.Must(t, ok && hogger.X == 12.5 && hogger.Y == -40.25 && hogger.Pinned, "quest/hogger = %+v, want the coordinates it was dragged to", hogger)
		// defias was dragged *to the origin* with pinned false, so a run that
		// invented a default for unplaced nodes would be indistinguishable
		// from one that read this row — which is why it is here and why the
		// third quest is checked for absence rather than for (0, 0).
		defias, ok := placedKeys["quest/defias"]
		assert.Must(t, ok && defias.X == 0 && defias.Y == 0 && !defias.Pinned, "quest/defias = %+v, want the origin it was dragged to, unpinned", defias)
		// And the node nobody dragged is absent rather than at the origin.
		// Named rather than counted: a count is the same either way once the
		// list is two long, and it is the *identity* of the missing one that
		// says an implementation did not invent a coordinate for it.
		undragged := 0
		for _, n := range saved.Nodes {
			if _, drawn := placedKeys[n.Type+"/"+n.Key]; drawn {
				continue
			}
			undragged++
			assert.Must(t, n.Key != "hogger" && n.Key != "defias", "%s/%s was dragged and is missing from the arrangement", n.Type, n.Key)
		}
		assert.Must(t, undragged != 0, "every drawn node was dragged; this test cannot see an invented coordinate")

		// The ad-hoc twin: the same document, run inline, carries nothing.
		q, err := ParseQuery([]byte(questsOnly))
		assert.Must(t, err == nil, "ParseQuery: %v", err)
		adhoc, err := g.views.Run(ctx, g.projectID, RunRequest{Query: q})
		assert.Must(t, err == nil, "Run: %v", err)
		assert.Must(t, len(adhoc.Positions) == 0, "an inline query answered with %+v: an ad-hoc document has no saved "+
			"view for a position to belong to", adhoc.Positions)
		assert.Must(t, len(adhoc.Nodes) == len(saved.Nodes), "the two runs drew %d and %d nodes; the control only holds if they "+
			"draw the same picture", len(adhoc.Nodes), len(saved.Nodes))
	})
}

// TestNothingIsStoredWhenOneWriteOfManyFails is where the transaction
// around the writes is observed.
func TestNothingIsStoredWhenOneWriteOfManyFails(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")

	if _, err := g.pool.Exec(ctx,
		`ALTER TABLE view_positions ADD CONSTRAINT no_666 CHECK (x <> 666)`); err != nil {
		t.Fatalf("add the constraint this test refuses with: %v", err)
	}
	in := []PositionInput{
		{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 1},
		{EntityType: "quest", EntityKey: "defias", X: 666, Y: 2},
	}
	if err := g.views.SetPositions(ctx, g.projectID, "route", in); err == nil {
		t.Fatalf("the second write must fail the constraint")
	}
	if got := mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("stored %+v, want nothing: the writes of one call are one change", got)
	}
}

// assertPositionsPayload checks the {id, key} a view.positions event
// carries, and — the half worth having — that it carries nothing else.
// A coordinate on this payload would be a value a client could render
// instead of re-reading, and publication order is not commit order, so
// two drags in flight would leave it rendering the earlier one for good.
// A version would be worse still: a position write deliberately does not
// advance one, so it could not have moved.
func assertPositionsPayload(t *testing.T, who string, e realtime.Event,
	wantID uuid.UUID, wantKey string,
) {
	t.Helper()
	raw, err := json.Marshal(e.Payload)
	assert.Must(t, err == nil, "%s: marshal payload: %v", who, err)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: decode payload: %v", who, err)
	}
	assert.Must(t, len(got) == 2, "%s: payload = %v, want the id and the key and nothing else", who, got)
	assert.Must(t, got["id"] == wantID.String() && got["key"] == wantKey, "%s: payload = %v, want {%s, %q}", who, got, wantID, wantKey)
}

// TestNoPositionEventIsPublishedWhenTheCommitFails is the one placement
// the refusal test above cannot catch, and it is the reason this file
// pays for a throwaway constraint.
func TestNoPositionEventIsPublishedWhenTheCommitFails(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	if _, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly)); err != nil {
		t.Fatalf("save the view: %v", err)
	}
	if _, err := g.pool.Exec(ctx,
		`ALTER TABLE view_positions ADD CONSTRAINT zz_fail_at_commit
		   FOREIGN KEY (view_id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(g.projectID, "owner", false)
	defer hub.Unsubscribe(sub)

	if err := svc.SetPositions(ctx, g.projectID, "route", placed()); err == nil {
		t.Fatal("the commit was accepted; this test needs it to fail")
	}
	// The control that the failure is the commit's and not an earlier
	// refusal: nothing is stored, so every statement did run.
	if got := mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("%d positions survived a failed commit", len(got))
	}
	requireNothing(t, sub, "the transaction did not commit")
}
