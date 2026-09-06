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
	if err != nil {
		t.Fatalf("save view %q: %v", key, err)
	}
	return row
}

func mustGetPositions(t *testing.T, g *game, key string) []Position {
	t.Helper()
	got, err := g.views.GetPositions(context.Background(), g.projectID, key)
	if err != nil {
		t.Fatalf("get positions of %q: %v", key, err)
	}
	return got
}

// TestAPositionSurvivesAReload is this task's read-back, and `pinned` is
// the column it exists for.
//
// The plan names it as this task's write-only candidate: a column was
// write-only for a whole sub-project because every test asserted that the
// call succeeded and none read the row back, and Task 11's review then
// found the audit columns write-only in the very test written to prevent
// that. So the assertion is on all three values — x, y **and pinned** —
// off a second read rather than off the write, and one of the two nodes
// is written pinned:false so that a GetPositions hard-wiring
// DefaultPinned is red rather than green.
//
// The second half moves a node that is already placed, which is the
// common call: a drag is an ON CONFLICT, not an insert, and a second
// SetPositions must move the node rather than fail or add a row.
func TestAPositionSurvivesAReload(t *testing.T) {
	g, _ := newGame(t)
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
	if len(got) != len(want) {
		t.Fatalf("read back %d positions, want %d: %+v", len(got), len(want), got)
	}
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
		if got[i].UpdatedAt.IsZero() {
			t.Errorf("%s: updated_at is zero, want the time the node was placed",
				w.EntityKey)
		}
	}

	// A drag of a node that already has a position.
	moved := []PositionInput{
		{EntityType: "quest", EntityKey: "hogger", X: 99, Y: 1, Pinned: ptrBool(false)},
	}
	if err := g.views.SetPositions(ctx, g.projectID, "route", moved); err != nil {
		t.Fatalf("move a placed node: %v", err)
	}
	got = mustGetPositions(t, g, "route")
	if len(got) != 2 {
		t.Fatalf("after moving one node the view holds %d positions, want 2", len(got))
	}
	if got[1].X != 99 || got[1].Y != 1 || got[1].Pinned {
		t.Fatalf("hogger = %+v, want (99, 1) unpinned: a second write moves the node",
			got[1])
	}
}

// TestAPositionIsPinnedUnlessTheCallerSaysOtherwise pins the default at
// the level a caller meets it.
//
// PositionInput.Pinned is a *bool for the reason ViewInput.LayoutSeed is
// a *int32: false is a value a caller may deliberately mean, so a plain
// bool would store the opposite of 0008_views.sql's column default for
// every caller that said nothing, and no test that only ever writes the
// flag explicitly could see it.
func TestAPositionIsPinnedUnlessTheCallerSaysOtherwise(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")

	in := []PositionInput{{EntityType: "quest", EntityKey: "cook", X: 1, Y: 2}}
	if err := g.views.SetPositions(ctx, g.projectID, "route", in); err != nil {
		t.Fatalf("set positions: %v", err)
	}
	got := mustGetPositions(t, g, "route")
	if len(got) != 1 || !got[0].Pinned {
		t.Fatalf("positions = %+v, want one pinned node: a placement written without "+
			"saying otherwise is explicit, not a spot the layout may move", got)
	}
	if DefaultPinned != true {
		t.Fatalf("DefaultPinned = %v, want the column default in 0008_views.sql",
			DefaultPinned)
	}
}

// TestDeletingAnEntityDropsItsPositionAndKeepsTheView is the foreign
// key's deliberate action, observed through the service rather than
// through the schema test that pins the constraint itself.
//
// Both halves matter and neither implies the other: positions are a
// cache of a human's arrangement and never a claim that anything exists,
// so an entity that goes takes its coordinates with it — and takes
// nothing else, because a view that vanished when a designer deleted one
// quest would be the opposite of a saved artefact.
func TestDeletingAnEntityDropsItsPositionAndKeepsTheView(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	view := mustSaveView(t, g, "route")

	if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
		t.Fatalf("set positions: %v", err)
	}
	if err := g.meta.RemoveEntity(ctx, g.projectID, "quest", "hogger"); err != nil {
		t.Fatalf("remove hogger: %v", err)
	}

	got := mustGetPositions(t, g, "route")
	if len(got) != 1 || got[0].EntityKey != "defias" {
		t.Fatalf("positions = %+v, want defias alone: a deleted entity's position goes "+
			"with it", got)
	}
	after, err := g.views.ViewByKey(ctx, g.projectID, "route")
	if err != nil {
		t.Fatalf("the view must survive an entity deletion: %v", err)
	}
	if after.ID != view.ID || after.Version != view.Version {
		t.Fatalf("view = (%v, %d), want (%v, %d) untouched",
			after.ID, after.Version, view.ID, view.Version)
	}
}

// TestPositionsArePerViewAndNotPerEntity is the core spec's own
// requirement: the same zone sits at its real map coordinates in a World
// map view and wherever the algorithm put it in a Mage route view.
//
// Two views, one entity, two coordinates, neither disturbing the other —
// a table keyed on the entity alone would answer the second write to
// both.
func TestPositionsArePerViewAndNotPerEntity(t *testing.T) {
	g, _ := newGame(t)
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
		if len(got) != 1 || got[0].X != tc.x || got[0].Y != tc.y {
			t.Errorf("%s holds %+v, want hogger at (%v, %v)", tc.view, got, tc.x, tc.y)
		}
	}
}

// TestANonFinitePositionIsRefusedWithItsIndex refuses the three doubles
// a coordinate cannot be, at the caller's own path.
//
// Ahead of 0008_views.sql's CHECK, which is the backstop for a write
// path that does not come through here: the constraint answers with an
// untyped SQLSTATE 23514 over a value the caller itself supplied, and
// this package's standing rule is that nothing a caller can fix reports
// internal_error. All three are reported in one pass, at the member each
// belongs to, and nothing is stored.
func TestANonFinitePositionIsRefusedWithItsIndex(t *testing.T) {
	g, _ := newGame(t)
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
	if !errors.As(err, &ve) {
		t.Fatalf("err = %#v, want a *metamodel.ValidationError", err)
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input: these are the caller's own arguments", err)
	}
	want := []string{"/positions/1/x", "/positions/2/y", "/positions/3/x"}
	if len(ve.Fields) != len(want) {
		t.Fatalf("problems = %v, want one per non-finite coordinate at %v", ve.Fields, want)
	}
	for i, path := range want {
		if ve.Fields[i].Path != path {
			t.Errorf("problem %d at %q, want %q", i, ve.Fields[i].Path, path)
		}
	}
	if got := mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("stored %+v, want nothing: a refused call stores none of its positions",
			got)
	}
}

// TestAnEmptyOrOversizePositionsCallIsRefused holds the two bounds on
// the list itself.
//
// **Empty is refused rather than answered with success**, which is the
// silent no-op this package refuses everywhere else: a caller that set no
// position changed nothing and must not be told it did.
//
// **The cap is the node cap**, HardMaxNodes, because a call positioning
// more nodes than a run of the view can return is positioning something
// no run has shown the caller. The oversize list here names entities
// that do not exist, which is what proves the bound is answered *before*
// five thousand lookups are made: a call refused with not_found would
// mean the order is the other way round.
func TestAnEmptyOrOversizePositionsCallIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")

	err := g.views.SetPositions(ctx, g.projectID, "route", nil)
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/positions" {
		t.Fatalf("err = %#v, want one problem at /positions for an empty call", err)
	}

	oversize := make([]PositionInput, MaxPositions+1)
	for i := range oversize {
		oversize[i] = PositionInput{
			EntityType: "quest", EntityKey: fmt.Sprintf("ghost_%d", i), X: 1, Y: 1,
		}
	}
	err = g.views.SetPositions(ctx, g.projectID, "route", oversize)
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/positions" {
		t.Fatalf("err = %#v, want one problem at /positions for %d positions",
			err, len(oversize))
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want the cap answered before the addresses are resolved", err)
	}
	if MaxPositions != HardMaxNodes {
		t.Fatalf("MaxPositions = %d and HardMaxNodes = %d: the cap on a call is the node "+
			"cap, and one rule lives in one place", MaxPositions, HardMaxNodes)
	}
}

// TestTheSameEntityTwiceInOneCallIsRefused refuses two coordinates for
// one node.
//
// Silently keeping the last would let the array's order decide where a
// node lands, which is a picture a caller cannot predict from its own
// call. The second spelling here differs only in case: keys are matched
// without regard to case, so the two entries address one entity, and the
// check is keyed on the **resolved id** rather than on the text — the
// fold that decides whether two keys are one entity is Postgres's, not a
// second rule written in Go that could disagree with it.
func TestTheSameEntityTwiceInOneCallIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")

	in := []PositionInput{
		{EntityType: "quest", EntityKey: "hogger", X: 1, Y: 1},
		{EntityType: "QUEST", EntityKey: "Hogger", X: 2, Y: 2},
	}
	err := g.views.SetPositions(ctx, g.projectID, "route", in)
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 {
		t.Fatalf("err = %#v, want one problem naming the collision", err)
	}
	if ve.Fields[0].Path != "/positions/1" {
		t.Fatalf("problem at %q, want /positions/1", ve.Fields[0].Path)
	}
	if got := mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("stored %+v, want nothing", got)
	}
}

// TestAPositionForAnEntityThisGameDoesNotHaveIsRefused names the
// position that is wrong, and tells a wrong type key from a wrong entity
// key apart.
//
// Both messages are metamodel.EntityByKey's, which is the point: the
// judgement that decides whether an address names anything already
// exists, is project-filtered in SQL, and is not restated here.
func TestAPositionForAnEntityThisGameDoesNotHaveIsRefused(t *testing.T) {
	g, _ := newGame(t)
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
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %#v, want not_found", err)
			}
			if got := err.Error(); !strings.Contains(got, "/positions/1") || !strings.Contains(got, tc.wants) {
				t.Fatalf("err = %q, want it to name /positions/1 and %q", got, tc.wants)
			}
			if got := mustGetPositions(t, g, "route"); len(got) != 0 {
				t.Fatalf("stored %+v, want nothing: the good position of a refused call "+
					"is not written either", got)
			}
		})
	}
}

// TestNothingIsStoredWhenOneWriteOfManyFails is where the transaction
// around the writes is observed.
//
// Every refusal the product itself can produce happens *before* the
// transaction opens — the arguments, then the addresses — so no call a
// caller can make leaves a write to roll back, and moving the loop out of
// withTx onto the pool turns no test red. That is the shape Task 11's
// refs finding had, and it is closed the same way: a constraint the
// second write violates, added to this test's own throwaway database, so
// the first write has something to be rolled back from. Without the
// transaction the first node stays placed and this test is red with one
// stored position.
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

// TestRunningAViewNeverRewritesPositions is what lets a query be edited,
// and a game grown, without losing an afternoon of map work.
//
// The run is executed twice with content added in between, and the
// stored rows are compared **including updated_at**: the coordinates
// alone would be equal even if every run rewrote each row to the value it
// already held, and the trigger on view_positions is what makes the
// timestamp the thing that would move.
func TestRunningAViewNeverRewritesPositions(t *testing.T) {
	g, _ := newGame(t)
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
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(result.Nodes) != 4 {
		t.Fatalf("the second run drew %d nodes, want the four quests: the fixture must "+
			"actually have grown between the runs", len(result.Nodes))
	}

	after := mustGetPositions(t, g, "route")
	if len(after) != len(before) {
		t.Fatalf("positions = %d after the runs, want %d: a run never writes one",
			len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("%s moved: %+v, want %+v — including updated_at, which is the half "+
				"a rewrite to the same coordinates would move",
				before[i].EntityKey, after[i], before[i])
		}
	}
}

// TestTheLayoutModeChangesNothingTheServerAnswers is the assertion
// behind positions.go's headline claim: no server code reads
// layout_mode beyond validating and returning it.
//
// A comment claiming the server honours a mode it never sees is this
// project's first defect in its purest form, and the inverse — a comment
// saying it is ignored while something quietly reads it — is the same
// defect with the sign flipped. Only a test can tell the two apart, so
// one view is saved under each of the three modes with one arrangement
// under it, and every mode must answer with the same positions,
// unpinned rows included. The stored mode is read back in the same loop,
// so the test cannot pass because the mode never actually changed.
func TestTheLayoutModeChangesNothingTheServerAnswers(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	row := mustSaveView(t, g, "route")
	if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
		t.Fatalf("set positions: %v", err)
	}
	baseline := mustGetPositions(t, g, "route")
	if len(baseline) != 2 || baseline[0].Pinned == baseline[1].Pinned {
		t.Fatalf("baseline = %+v, want two positions differing in pinned: a fixture "+
			"where every node is pinned cannot tell mixed from manual", baseline)
	}

	version := row.Version
	for _, mode := range []string{LayoutAuto, LayoutManual, LayoutMixed} {
		in := saveable("route", questsOnly)
		in.LayoutMode = mode
		in.ExpectedVersion = ptrInt32(version)
		saved, err := g.views.UpsertView(ctx, g.projectID, in)
		if err != nil {
			t.Fatalf("save %s: %v", mode, err)
		}
		version = saved.Version
		if saved.LayoutMode != mode {
			t.Fatalf("stored layout_mode = %q, want %q", saved.LayoutMode, mode)
		}
		got := mustGetPositions(t, g, "route")
		if len(got) != len(baseline) {
			t.Fatalf("%s: %d positions, want %d", mode, len(got), len(baseline))
		}
		for i := range baseline {
			if got[i] != baseline[i] {
				t.Errorf("%s: %s = %+v, want %+v — the mode is a contract with the "+
					"client and nothing here reads it",
					mode, baseline[i].EntityKey, got[i], baseline[i])
			}
		}
	}
}

// TestClearingPositions holds the three shapes of a clear: every
// position of a view, the ones a caller names, and the empty list that is
// refused rather than read as either.
//
// **A nil list clears the view and an empty one is refused.** Said
// nothing is not the same as said none — markdown's WriteInput.Links
// draws the same distinction — and here that is what stands between a
// caller whose list of dirty nodes came out empty and a wiped
// arrangement.
func TestClearingPositions(t *testing.T) {
	g, _ := newGame(t)
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
	if err != nil {
		t.Fatalf("clear one: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	got := mustGetPositions(t, g, "route")
	if len(got) != 1 || got[0].EntityKey != "defias" {
		t.Fatalf("positions = %+v, want defias standing", got)
	}

	// An entity that exists and was never dragged: nothing to remove is
	// an answer, not an error.
	removed, err = g.views.ClearPositions(ctx, g.projectID, "route",
		[]EntityAddress{{EntityType: "quest", EntityKey: "cook"}})
	if err != nil || removed != 0 {
		t.Fatalf("clearing an unplaced node = (%d, %v), want (0, nil)", removed, err)
	}

	set()
	if removed, err = g.views.ClearPositions(ctx, g.projectID, "route", nil); err != nil {
		t.Fatalf("clear every position: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want both positions", removed)
	}
	if got = mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("positions = %+v, want none", got)
	}

	set()
	_, err = g.views.ClearPositions(ctx, g.projectID, "route", []EntityAddress{})
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/entities" {
		t.Fatalf("err = %#v, want one problem at /entities for an empty list", err)
	}
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
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "/entities/1") {
		t.Fatalf("err = %#v, want not_found naming /entities/1", err)
	}
}

// TestAPositionCallNamesTheViewItCannotFind: every one of the three
// calls resolves the view by key, in this game, before it does anything
// else.
func TestAPositionCallNamesTheViewItCannotFind(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	err := g.views.SetPositions(ctx, g.projectID, "nowhere", placed())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("set: err = %#v, want not_found", err)
	}
	if _, err := g.views.ClearPositions(ctx, g.projectID, "nowhere", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("clear: err = %#v, want not_found", err)
	}
	if _, err := g.views.GetPositions(ctx, g.projectID, "nowhere"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: err = %#v, want not_found", err)
	}
}

// TestPositionsOfAnotherGameAreNotReachable, with its positive control.
//
// Both games seed the same keys, so nothing here discriminates by
// spelling: azeroth and outland each hold a view called "route" and a
// quest called "hogger", and only the project filters keep the two
// arrangements apart.
//
// The four statements are driven **directly** as well as through the
// service, because the service resolves the view by key inside the game
// first, which masks every filter under it — the only way to observe a
// filter a service path has already made redundant, exactly as
// TestTheViewQueriesAddressingARowByIdAreScopedToTheProject does for the
// view queries. UpsertViewPosition has no filter to drive: it is an
// INSERT, and its isolation is 0008_views.sql's two composite foreign
// keys, so it is asserted as the refusal they raise.
func TestPositionsOfAnotherGameAreNotReachable(t *testing.T) {
	azeroth, outland := newGame(t)
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
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("ListViewPositions returned %d of azeroth's rows to outland", len(rows))
	}
	gone, err := outland.views.q.DeleteViewPositions(ctx, dbq.DeleteViewPositionsParams{
		ProjectID: outland.projectID, ViewID: mine.ID,
	})
	if err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if gone != 0 {
		t.Errorf("DeleteViewPositions removed %d of azeroth's rows for outland", gone)
	}
	hogger, err := azeroth.meta.EntityByKey(ctx, azeroth.projectID, "quest", "hogger")
	if err != nil {
		t.Fatalf("read hogger: %v", err)
	}
	gone, err = outland.views.q.DeleteViewPosition(ctx, dbq.DeleteViewPositionParams{
		ProjectID: outland.projectID, ViewID: mine.ID, EntityID: hogger.ID,
	})
	if err != nil {
		t.Fatalf("delete one: %v", err)
	}
	if gone != 0 {
		t.Errorf("DeleteViewPosition removed %d of azeroth's rows for outland", gone)
	}
	// The write takes two mechanisms and both are asserted, because the
	// first alone was not enough: with only the composite foreign keys,
	// an insert that *conflicts* with a stored row is an UPDATE of that
	// row, project_id is not in its SET list, every key stays satisfied,
	// and outland silently moved azeroth's node. That is what this
	// statement's guard is for, and it is why the control below compares
	// coordinates rather than counting rows — the count was two either
	// way, which is how the first version of this test passed.
	//
	// cook has no stored position, so it takes the insert path and the
	// keys refuse it.
	cook, err := azeroth.meta.EntityByKey(ctx, azeroth.projectID, "quest", "cook")
	if err != nil {
		t.Fatalf("read cook: %v", err)
	}
	_, err = outland.views.q.UpsertViewPosition(ctx, dbq.UpsertViewPositionParams{
		ViewID: mine.ID, EntityID: cook.ID, ProjectID: outland.projectID,
		X: 1, Y: 1, Pinned: true,
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("inserting into azeroth's view from outland: err = %#v, want 23503", err)
	}
	// hogger has one, so it takes the conflict path and meets the guard.
	written, err := outland.views.q.UpsertViewPosition(ctx, dbq.UpsertViewPositionParams{
		ViewID: mine.ID, EntityID: hogger.ID, ProjectID: outland.projectID,
		X: 1, Y: 1, Pinned: true,
	})
	if err != nil {
		t.Errorf("overwriting azeroth's position from outland: err = %v, want no error "+
			"and no row", err)
	}
	if written != 0 {
		t.Errorf("the guarded DO UPDATE wrote %d rows for outland, want 0", written)
	}

	// The positive control: azeroth's arrangement survived all of it,
	// coordinates included.
	got := mustGetPositions(t, azeroth, "route")
	if len(got) != 2 {
		t.Fatalf("azeroth reads %+v, want its two positions", got)
	}
	if got[1].X != 12.5 || got[1].Y != -40.25 || !got[1].Pinned {
		t.Fatalf("azeroth's hogger = %+v, want (12.5, -40.25) pinned: another game must "+
			"not move it", got[1])
	}
}

// TestAPositionIsWrittenIntoTheViewItNames is the control the isolation
// test above needs and cannot carry: with two games holding the same
// keys, a call that wrote into the *other* game's view would be caught,
// but so would a call that wrote nothing at all. This one asserts the
// row lands in the right game's row by reading it back through the other
// game's service as well.
func TestAPositionIsWrittenIntoTheViewItNames(t *testing.T) {
	azeroth, outland := newGame(t)
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
}

// TestClearingOneViewLeavesAnotherViewsArrangementStanding is the clear
// path's half of the core spec's per-view requirement, and it was the
// missing half.
//
// TestPositionsArePerViewAndNotPerEntity asserts it for the *write*: two
// views, one entity, two coordinates, neither disturbing the other.
// Nothing asserted it for the clear, and the comments on
// DeleteViewPositions and DeleteViewPosition call the view filter
// load-bearing — a claim about behaviour, so worth what the test behind
// it is worth. There was no test behind it: with the view_id filter
// deleted from either statement the whole package stayed green, while
// clearing "route" took "map" down with it.
//
//	unmutated:  ClearPositions(route, nil) removed=2 ; route now=0 ; map now=2
//	mutated:    ClearPositions(route, nil) removed=4 ; route now=0 ; map now=0
//
// Both clears are covered, because they are two statements: the
// whole-view clear and the single-entity one.
func TestClearingOneViewLeavesAnotherViewsArrangementStanding(t *testing.T) {
	g, _ := newGame(t)
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
	if err != nil {
		t.Fatalf("clear one: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1: the same node in another view is not this "+
			"call's to remove", removed)
	}
	if got := mustGetPositions(t, g, "map"); len(got) != 2 {
		t.Fatalf("map holds %+v after route cleared one node, want both its own "+
			"positions", got)
	}

	// The whole view, cleared out of one view only.
	removed, err = g.views.ClearPositions(ctx, g.projectID, "route", nil)
	if err != nil {
		t.Fatalf("clear every position of route: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1: route had one position left, and map's two are "+
			"not route's to clear", removed)
	}
	if got := mustGetPositions(t, g, "route"); len(got) != 0 {
		t.Fatalf("route holds %+v after its own clear, want none", got)
	}
	got := mustGetPositions(t, g, "map")
	if len(got) != 2 {
		t.Fatalf("map holds %+v after route was cleared whole, want both its own "+
			"positions: clearing one view must not wipe another's arrangement", got)
	}
	// Coordinates, not a count: the numbers are what a designer loses.
	if got[1].X != 12.5 || got[1].Y != -40.25 {
		t.Fatalf("map's hogger = %+v, want (12.5, -40.25)", got[1])
	}
}

// TestAnEmptyOrOversizeClearIsRefused is the twin of
// TestAnEmptyOrOversizePositionsCallIsRefused, and the cap half of it
// was a bound stated in prose and absent from the code.
//
// ClearPositions' own comment said the list was "bounded by the same cap
// a write is". Nothing bounded it — the cap lived in the write's
// argument check, which the clear never called — and the call is one
// lookup plus one delete per address on a single pooled connection:
//
//	ClearPositions with 50000 addresses: removed=0 err=<nil> elapsed=24.604468167s
//
// The oversize list here names entities that **do not exist**, exactly
// as the write's test does, so a not_found answer would mean the cap is
// applied after five thousand lookups rather than before the first.
func TestAnEmptyOrOversizeClearIsRefused(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")

	_, err := g.views.ClearPositions(ctx, g.projectID, "route", []EntityAddress{})
	var ve *metamodel.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/entities" {
		t.Fatalf("err = %#v, want one problem at /entities for an empty list", err)
	}

	oversize := make([]EntityAddress, MaxPositions+1)
	for i := range oversize {
		oversize[i] = EntityAddress{
			EntityType: "quest", EntityKey: fmt.Sprintf("ghost_%d", i),
		}
	}
	_, err = g.views.ClearPositions(ctx, g.projectID, "route", oversize)
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Path != "/entities" {
		t.Fatalf("err = %#v, want one problem at /entities for %d addresses",
			err, len(oversize))
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want the cap answered before the addresses are resolved", err)
	}
}

// TestAPositionCallRefusesItsArgumentsInTheSameOrder pins the one thing
// the two calls' doc comments both claim and only one of them did.
//
// Both say: the arguments this call carries, then the addresses they
// name, then the write. ClearPositions resolved the view first, so one
// call answered a missing view where the other answered a malformed
// address, from the same pair of bad arguments:
//
//	Set   -> invalid_input: /positions/0/entity_key: is required
//	Clear -> not_found: no view "nosuchview" in this game
//
// A rule two calls state and one follows is worth less than no rule, so
// the assertion is that they agree rather than that either is right.
func TestAPositionCallRefusesItsArgumentsInTheSameOrder(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()

	var ve *metamodel.ValidationError
	err := g.views.SetPositions(ctx, g.projectID, "nosuchview",
		[]PositionInput{{EntityType: "quest", EntityKey: "", X: 1, Y: 1}})
	if !errors.As(err, &ve) || len(ve.Fields) != 1 ||
		ve.Fields[0].Path != "/positions/0/entity_key" {
		t.Fatalf("set: err = %#v, want invalid_input at /positions/0/entity_key", err)
	}

	_, err = g.views.ClearPositions(ctx, g.projectID, "nosuchview",
		[]EntityAddress{{EntityType: "quest", EntityKey: ""}})
	if !errors.As(err, &ve) || len(ve.Fields) != 1 ||
		ve.Fields[0].Path != "/entities/0/entity_key" {
		t.Fatalf("clear: err = %#v, want invalid_input at /entities/0/entity_key: the "+
			"two calls state the same order and must refuse in it", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("clear: err = %v, want the arguments judged before the view is "+
			"resolved", err)
	}
}

// TestAPositionWritePublishesItsInvalidation is Task 15's decision,
// asserted rather than described: a drag reaches every subscriber a
// query edit reaches, because a browser holding a picture has no other
// way to learn the arrangement moved under it.
//
// The two subscribers are the two a wrong gating would silently cut
// out — a viewer, excluded by any MinRole above viewer, and a token
// caller, excluded by HumanOnly regardless of role. They are the same
// pair TestViewEventsReachEveryMemberOfTheGameIncludingAgents uses for
// view.upserted, which is the claim: the gating of this kind *is* that
// one's, not a second decision that happens to agree today.
//
// All three write paths publish. A test that only drove SetPositions
// would leave both clears free to go quiet, and a wiped arrangement is
// the invalidation that matters most.
func TestAPositionWritePublishesItsInvalidation(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	row, err := svc.UpsertView(ctx, g.projectID, saveable("route", questsOnly))
	if err != nil {
		t.Fatalf("save the view: %v", err)
	}

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
			if got.Kind != "view.positions" {
				t.Fatalf("%s after %s got %q, want view.positions", who, step.what, got.Kind)
			}
			assertPositionsPayload(t, who, got, row.ID, "route")
		}
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
	if err != nil {
		t.Fatalf("%s: marshal payload: %v", who, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: decode payload: %v", who, err)
	}
	if len(got) != 2 {
		t.Fatalf("%s: payload = %v, want the id and the key and nothing else", who, got)
	}
	if got["id"] != wantID.String() || got["key"] != wantKey {
		t.Fatalf("%s: payload = %v, want {%s, %q}", who, got, wantID, wantKey)
	}
}

// TestNoPositionEventIsPublishedWhenTheWriteIsRefused is the control the
// test above cannot be without: a publish placed before the write, or
// outside the transaction's error check, announces an arrangement that
// never landed, and every subscriber's reaction is to re-read a picture
// that did not change.
//
// Three refusals, one per pass SetPositions and ClearPositions make: the
// call's own arguments, the addresses they name, and the view itself.
func TestNoPositionEventIsPublishedWhenTheWriteIsRefused(t *testing.T) {
	g, _ := newGame(t)
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
}

// TestNoPositionEventIsPublishedWhenTheCommitFails is the one placement
// the refusal test above cannot catch, and it is the reason this file
// pays for a throwaway constraint.
//
// Every refusal SetPositions can produce happens *before* its
// transaction opens — the arguments, then the addresses, then the view —
// so a publish written as the last statement inside the transaction's
// callback, or above the error check that follows it, differs from the
// correct one by nothing a refusal can reach. A rolled-back write and a
// refused write look identical from outside, and the difference is a
// subscriber told to re-read an arrangement the database threw away.
//
// A deferred foreign key from view_positions.view_id to projects.id is
// satisfied by nothing — a view's id is not a project id — and being
// DEFERRABLE INITIALLY DEFERRED it is checked at COMMIT, so every
// statement inside the transaction succeeds and only the commit fails.
// views_test.go's TestNoViewEventIsPublishedWhenTheCommitFails is the
// model, and testutil.NewPool's per-test database is what makes it safe.
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

// TestASavedViewsRunCarriesItsArrangementAndAnAdHocOneDoesNot is the
// positions member of the run envelope, which Task 13 deferred to Task
// 15 on the ground that nothing read one before Task 16.
//
// Three claims, and the second and third are the ones a naive
// implementation would get wrong:
//
//   - a saved run carries the arrangement, addressed by the same two
//     keys GetPositions answers with, so a client matches a position to
//     a node without ever reading an id;
//   - a node nobody dragged is **absent**, not returned at the origin —
//     (0, 0) is a place a designer may deliberately have chosen, and the
//     fixture drags one node there on purpose so the two cannot be
//     confused;
//   - an ad-hoc run carries none at all, because an inline query has no
//     saved view for a position to belong to, exactly as it has no
//     staleness.
func TestASavedViewsRunCarriesItsArrangementAndAnAdHocOneDoesNot(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	mustSaveView(t, g, "route")
	if err := g.views.SetPositions(ctx, g.projectID, "route", placed()); err != nil {
		t.Fatalf("set positions: %v", err)
	}

	saved, err := g.views.RunView(ctx, g.projectID, "route", RunRequest{})
	if err != nil {
		t.Fatalf("RunView: %v", err)
	}
	if len(saved.Positions) != 2 {
		t.Fatalf("Positions = %+v, want the two that were dragged", saved.Positions)
	}
	// The drawn nodes outnumber the placed ones, which is what makes the
	// absence assertion below mean something: an implementation padding
	// the list to one entry per node would have three.
	if len(saved.Nodes) <= len(saved.Positions) {
		t.Fatalf("the view draws %d nodes and %d are placed; this test needs an unplaced one",
			len(saved.Nodes), len(saved.Positions))
	}
	placedKeys := map[string]Position{}
	for _, p := range saved.Positions {
		placedKeys[p.EntityType+"/"+p.EntityKey] = p
	}
	hogger, ok := placedKeys["quest/hogger"]
	if !ok || hogger.X != 12.5 || hogger.Y != -40.25 || !hogger.Pinned {
		t.Fatalf("quest/hogger = %+v, want the coordinates it was dragged to", hogger)
	}
	// defias was dragged *to the origin* with pinned false, so a run that
	// invented a default for unplaced nodes would be indistinguishable
	// from one that read this row — which is why it is here and why the
	// third quest is checked for absence rather than for (0, 0).
	defias, ok := placedKeys["quest/defias"]
	if !ok || defias.X != 0 || defias.Y != 0 || defias.Pinned {
		t.Fatalf("quest/defias = %+v, want the origin it was dragged to, unpinned", defias)
	}
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
		if n.Key == "hogger" || n.Key == "defias" {
			t.Fatalf("%s/%s was dragged and is missing from the arrangement", n.Type, n.Key)
		}
	}
	if undragged == 0 {
		t.Fatal("every drawn node was dragged; this test cannot see an invented coordinate")
	}

	// The ad-hoc twin: the same document, run inline, carries nothing.
	q, err := ParseQuery([]byte(questsOnly))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	adhoc, err := g.views.Run(ctx, g.projectID, RunRequest{Query: q})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(adhoc.Positions) != 0 {
		t.Fatalf("an inline query answered with %+v: an ad-hoc document has no saved "+
			"view for a position to belong to", adhoc.Positions)
	}
	if len(adhoc.Nodes) != len(saved.Nodes) {
		t.Fatalf("the two runs drew %d and %d nodes; the control only holds if they "+
			"draw the same picture", len(adhoc.Nodes), len(saved.Nodes))
	}
}
