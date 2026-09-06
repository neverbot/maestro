package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
	"github.com/neverbot/maestro/internal/realtime"
)

// levellingGame is the fixture most of these tests want: a gated game
// with four quests to walk through.
func levellingGame(t *testing.T) game {
	t.Helper()
	g := gatedGame(t)
	for _, key := range []string{"tutorial", "hogger", "defias", "deadmines"} {
		g.entity(t, "quest", key)
	}
	return g
}

func version(v int32) *int32 { return &v }

// upsert is the writer under test, with the error surfaced.
func (g game) upsert(t *testing.T, in RouteInput) Route {
	t.Helper()
	got, err := g.analysis.UpsertRoute(context.Background(), g.projectID, in)
	if err != nil {
		t.Fatalf("upsert route %q: %v", in.Key, err)
	}
	return got
}

func steps(keys ...string) []RouteStepInput {
	out := make([]RouteStepInput, 0, len(keys))
	for _, key := range keys {
		out = append(out, RouteStepInput{EntityType: "quest", Key: key})
	}
	return out
}

// TestARouteReadsBackEverythingItWasWrittenWith.
//
// `params` is a free jsonb blob written by an upsert, which is **the
// exact shape relations.fields had when it was write-only for a whole
// sub-project through nine review rounds**, and `note` is the field
// nobody has a reason to look at. Both are read back here, and again
// over the transport in Task 11.
func TestARouteReadsBackEverythingItWasWrittenWith(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	in := RouteInput{
		Key:             "levelling",
		Name:            "The levelling route",
		Description:     "How a fresh character reaches the Deadmines.\n\nTwo paragraphs.",
		ExpectedVersion: version(0),
		Params: RouteParams{
			Gate:                 GatingAll,
			IncludeUngated:       boolPtr(false),
			PropagateContainment: boolPtr(false),
			SeedEntities:         []SeedRef{{EntityType: "quest", Key: "tutorial"}},
			SeedEntityTypes:      []string{"quest"},
			MaxDepth:             7,
			ExcludeInvalid:       true,
		},
		Steps: []RouteStepInput{
			{EntityType: "quest", Key: "tutorial", Note: "character creation"},
			{EntityType: "quest", Key: "hogger", Note: "the wall every player meets"},
			{EntityType: "quest", Key: "defias"},
		},
	}
	g.upsert(t, in)

	got, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back: %v", err)
	}
	if got.Name != in.Name || got.Description != in.Description {
		t.Errorf("name/description read back as %q / %q", got.Name, got.Description)
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want 1 on a creation", got.Version)
	}
	if got.Params.Gate != GatingAll || got.Params.MaxDepth != 7 || !got.Params.ExcludeInvalid {
		t.Errorf("params read back as %+v", got.Params)
	}
	if got.Params.IncludeUngated == nil || *got.Params.IncludeUngated {
		t.Errorf("include_ungated read back as %v, want the stored false: a tri-state "+
			"that loses its false is a stored parameter nothing reads", got.Params.IncludeUngated)
	}
	if got.Params.PropagateContainment == nil || *got.Params.PropagateContainment {
		t.Errorf("propagate_containment read back as %v", got.Params.PropagateContainment)
	}
	if len(got.Params.SeedEntities) != 1 || got.Params.SeedEntities[0].Key != "tutorial" {
		t.Errorf("seed_entities read back as %v", got.Params.SeedEntities)
	}
	if len(got.Params.SeedEntityTypes) != 1 || got.Params.SeedEntityTypes[0] != "quest" {
		t.Errorf("seed_entity_types read back as %v", got.Params.SeedEntityTypes)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("read back %d steps, want three", len(got.Steps))
	}
	for i, want := range in.Steps {
		step := got.Steps[i]
		if int(step.Position) != i || step.EntityType != want.EntityType || step.Key != want.Key {
			t.Errorf("step %d read back as %+v, want %+v", i, step, want)
		}
		if step.Note != want.Note {
			t.Errorf("step %d note read back as %q, want %q: a note stored and never "+
				"read back is a field nothing would notice the loss of", i, step.Note, want.Note)
		}
		if step.EntityID == nil {
			t.Errorf("step %d resolved to no entity id", i)
		}
	}
}

// TestRouteParamsAreValidatedAtWriteTimeAndNotOnlyAtCheckTime.
//
// A route whose `gate` is nonsense is a route every check from now on
// will refuse, and the caller who can fix it is the one writing it.
// `params` existing and being judged by nothing is the write-only-field
// defect in its route-shaped form.
func TestRouteParamsAreValidatedAtWriteTimeAndNotOnlyAtCheckTime(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "broken", Name: "Broken", ExpectedVersion: version(0),
		Params: RouteParams{Gate: Gating("most")},
		Steps:  steps("tutorial"),
	})
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("a nonsense gate answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "/params/gate" {
		t.Fatalf("the refusal names %v, want /params/gate", invalid.Fields)
	}
	if !strings.Contains(invalid.Fields[0].Message, string(GatingAll)) {
		t.Errorf("the refusal does not name what would be right: %s", invalid.Fields[0].Message)
	}
	// And nothing was stored, so a caller cannot find the broken route
	// waiting for it.
	if _, err := g.analysis.RouteByKey(context.Background(), g.projectID, "broken"); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("the refused route was stored anyway: %v", err)
	}
	// The other three bounds, at their own paths, so a rule applied to
	// one member of params and not the rest is red.
	for _, probe := range []struct {
		path string
		in   RouteParams
	}{
		{"/params/max_depth", RouteParams{MaxDepth: MaxMaxDepth + 1}},
		{"/params/seed_entities", RouteParams{SeedEntities: make([]SeedRef, MaxSeedKeys+1)}},
		{"/params/seed_entity_types", RouteParams{
			SeedEntityTypes: make([]string, MaxTypeKeys+1)}},
	} {
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "probe", Name: "Probe", ExpectedVersion: version(0), Params: probe.in,
		})
		if !errors.As(err, &invalid) || len(invalid.Fields) != 1 ||
			invalid.Fields[0].Path != probe.path {
			t.Errorf("a bound broken at %s answered %v", probe.path, err)
		}
	}
}

// TestOmittingTheExpectedVersionIsRefusedRatherThanGuessed.
//
// The guess a caller would want depends on whether the route exists,
// which is the very thing the argument asserts.
func TestOmittingTheExpectedVersionIsRefusedRatherThanGuessed(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "levelling", Name: "Levelling", Steps: steps("tutorial"),
	})
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("an omitted expected_version answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "/expected_version" {
		t.Fatalf("the refusal names %v", invalid.Fields)
	}
}

// TestAVersionClaimAgainstARouteThatIsGoneIsRefusedRatherThanRecreated.
//
// The reason is stronger for a route than for anything
// metamodel.RemovedError was written for: last_check,
// last_checked_design_version and every step row hang off the route's
// id, so a quiet re-creation would discard a designer's proved
// progression *and* its proof, under a new id, and answer success.
func TestAVersionClaimAgainstARouteThatIsGoneIsRefusedRatherThanRecreated(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	written := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})
	if err := g.analysis.RemoveRoute(context.Background(), g.projectID,
		"levelling", version(written.Version)); err != nil {
		t.Fatalf("remove the route: %v", err)
	}

	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(written.Version),
		Steps: steps("tutorial", "hogger"),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a version claim against a removed route answered %v, want not_found", err)
	}
	if errors.Is(err, ErrVersionConflict) {
		t.Fatal("it must not also read as version_conflict: telling a caller to merge " +
			"is the one instruction that cannot work against a row that is gone")
	}
	// The control: with expected_version 0 the same call creates, so the
	// refusal above is about the claim and not about the key.
	g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
}

// TestAStepListAboveTheCapIsRefusedAndNotTruncated.
//
// A route silently cut to five hundred steps is a proof of a
// progression that stops halfway and says it holds.
func TestAStepListAboveTheCapIsRefusedAndNotTruncated(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	long := make([]RouteStepInput, MaxRouteSteps+1)
	for i := range long {
		long[i] = RouteStepInput{EntityType: "quest", Key: "tutorial"}
	}
	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "long", Name: "Long", ExpectedVersion: version(0), Steps: long,
	})
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("a 501-step route answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "/steps" {
		t.Fatalf("the refusal names %v, want /steps", invalid.Fields)
	}
	message := invalid.Fields[0].Message
	if !strings.Contains(message, "501") || !strings.Contains(message, "500") {
		t.Errorf("the refusal names neither the count nor the cap: %s", message)
	}
	if _, err := g.analysis.RouteByKey(context.Background(), g.projectID, "long"); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("a truncated route was stored: %v", err)
	}
}

// TestAStepKeyThatResolvesToNothingIsNotFoundAndNotATombstone.
//
// A tombstone is what a *deletion* leaves behind. Accepting one on write
// would let an agent author a route out of typos and be told it holds.
func TestAStepKeyThatResolvesToNothingIsNotFoundAndNotATombstone(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "typo", Name: "Typo", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hoggre"),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a step naming no entity answered %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), "steps[1]") || !strings.Contains(err.Error(), "hoggre") {
		t.Errorf("the refusal names neither the position nor the key: %v", err)
	}
	if _, err := g.analysis.RouteByKey(context.Background(), g.projectID, "typo"); !errors.Is(
		err, ErrNotFound) {
		t.Fatalf("the route was stored with a step nobody could resolve: %v", err)
	}
}

// TestARouteStepCannotResolveToAnotherGamesEntity -- the isolation half
// of step resolution, over one database holding both games.
func TestARouteStepCannotResolveToAnotherGamesEntity(t *testing.T) {
	t.Parallel()
	mine := levellingGame(t)
	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.entity(t, "quest", "their-secret")

	_, err := mine.analysis.UpsertRoute(context.Background(), mine.projectID, RouteInput{
		Key: "borrowed", Name: "Borrowed", ExpectedVersion: version(0),
		Steps: steps("tutorial", "their-secret"),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a step naming another game's entity answered %v, want not_found", err)
	}
	// The control: the same key resolves in the game that owns it, so
	// this test cannot pass on a resolver that finds nothing at all.
	theirs.upsert(t, RouteInput{
		Key: "theirs", Name: "Theirs", ExpectedVersion: version(0),
		Steps: []RouteStepInput{{EntityType: "quest", Key: "their-secret"}},
	})
}

// TestARouteKeyFromAnotherGameIsNotFound.
func TestARouteKeyFromAnotherGameIsNotFound(t *testing.T) {
	t.Parallel()
	mine := levellingGame(t)
	mine.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
	theirs := mine.sibling(t)
	if _, err := theirs.analysis.RouteByKey(
		context.Background(), theirs.projectID, "levelling"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another game's route key answered %v, want not_found", err)
	}
	// The control, so a lookup that finds nothing at all cannot pass.
	if _, err := mine.analysis.RouteByKey(
		context.Background(), mine.projectID, "levelling"); err != nil {
		t.Fatalf("the owning game can no longer read its own route: %v", err)
	}
}

// TestTheStepListIsReplacedWholesaleAndNotMerged.
//
// Steps are positional and an upsert replaces them: a merge would leave
// a route holding steps its author had deleted, which is a claim nobody
// made.
func TestTheStepListIsReplacedWholesaleAndNotMerged(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	first := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger", "defias", "deadmines"),
	})
	second := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(first.Version),
		Steps: steps("deadmines", "tutorial"),
	})
	if second.Version != first.Version+1 {
		t.Errorf("version = %d, want %d", second.Version, first.Version+1)
	}
	if len(second.Steps) != 2 {
		t.Fatalf("the route holds %d steps after a two-step rewrite: %+v",
			len(second.Steps), second.Steps)
	}
	if second.Steps[0].Key != "deadmines" || second.Steps[1].Key != "tutorial" {
		t.Errorf("the rewritten steps are %v, want the new order in the new order",
			[]string{second.Steps[0].Key, second.Steps[1].Key})
	}
	if second.Steps[0].Position != 0 || second.Steps[1].Position != 1 {
		t.Errorf("positions are %d,%d, want a dense 0..n-1",
			second.Steps[0].Position, second.Steps[1].Position)
	}
}

// TestANeverCheckedRouteIsNeitherStaleNorHealthy pins all three states
// in one test.
//
// Never checked, stale and checked are three different facts, and
// collapsing any pair either alarms a designer wrongly or reassures them
// wrongly. One test, because a state asserted in isolation passes on an
// implementation that only ever answers that one.
func TestANeverCheckedRouteIsNeitherStaleNorHealthy(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	route := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})
	if route.Status != RouteNeverChecked {
		t.Fatalf("status = %q on a fresh route, want %q", route.Status, RouteNeverChecked)
	}
	if route.LastCheckedDesignVersion != nil || route.LastCheckedAt != nil {
		t.Errorf("a never-checked route carries a verdict: %v / %v",
			route.LastCheckedDesignVersion, route.LastCheckedAt)
	}

	// Checked against the current design version. routes.check is Task
	// 10; this writes the three columns it will write, which is the one
	// part of this row the CRUD deliberately does not touch.
	g.recordCheck(t, "levelling", route.DesignVersion)
	checked, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back: %v", err)
	}
	if checked.Status != RouteChecked {
		t.Fatalf("status = %q after a check against the current design, want %q",
			checked.Status, RouteChecked)
	}

	// Any write anywhere in the game moves design_version, and the
	// counter is coarse on purpose: this one touches an entity the route
	// does not even name.
	g.entity(t, "quest", "an-unrelated-quest")
	stale, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back: %v", err)
	}
	if stale.Status != RouteStale {
		t.Fatalf("status = %q after a write to the game, want %q", stale.Status, RouteStale)
	}
	if stale.LastCheckedDesignVersion == nil ||
		*stale.LastCheckedDesignVersion >= stale.DesignVersion {
		t.Errorf("last_checked_design_version %v is not below the game's %d, so the "+
			"status above rests on nothing", stale.LastCheckedDesignVersion, stale.DesignVersion)
	}
	// And the verdict is still there: stale means "about a game that has
	// changed", never "discarded".
	if len(stale.LastCheck) == 0 {
		t.Error("the stored verdict was lost when the route went stale")
	}
}

// recordCheck writes the three verdict columns routes.check will write.
// It is raw SQL because the checker is Task 10 and this task's writer
// deliberately never touches those columns -- which is itself asserted,
// by TestAnOrdinaryUpsertLeavesAStoredVerdictStanding.
func (g game) recordCheck(t *testing.T, key string, atDesignVersion int64) {
	t.Helper()
	if _, err := g.pool.Exec(context.Background(),
		`UPDATE routes SET last_check = '{"verdict":"ok"}'::jsonb, last_checked_at = now(),
		        last_checked_design_version = $3
		 WHERE project_id = $1 AND lower(key) = lower($2)`,
		g.projectID, key, atDesignVersion); err != nil {
		t.Fatalf("record a check on %q: %v", key, err)
	}
}

// TestAnOrdinaryUpsertLeavesAStoredVerdictStanding.
//
// The three verdict columns are not in the upsert's SET list, and that
// is the decision this table exists for: an edit to a route's name must
// not silently discard a verdict somebody proved. A caller that said
// nothing about a check would be saying "never checked" if they were
// listed there.
func TestAnOrdinaryUpsertLeavesAStoredVerdictStanding(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	route := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
	g.recordCheck(t, "levelling", route.DesignVersion)

	after := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling, renamed", ExpectedVersion: version(route.Version),
		Steps: steps("tutorial", "hogger"),
	})
	if len(after.LastCheck) == 0 || after.LastCheckedAt == nil ||
		after.LastCheckedDesignVersion == nil {
		t.Fatalf("an ordinary edit discarded the stored verdict: %+v", after)
	}
}

// TestEditingARouteMakesItsOwnVerdictStale is the half design_version
// cannot see.
//
// `routes` is deliberately not one of the four tables the design-version
// triggers watch, so rewriting a route's steps moves no counter at all
// -- and a status decided on that counter alone would report a verdict
// about steps nobody has any more as `checked`. That is a stale green
// arriving through the one door design_version does not watch, and it is
// exactly what this engine exists not to produce. routeStatus compares
// the route's own updated_at against last_checked_at as well.
//
// The control is in the same test: a *design* write with no route write
// is stale for the other reason, so a status that had collapsed into one
// clock would fail one half or the other.
func TestEditingARouteMakesItsOwnVerdictStale(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	route := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
	g.recordCheck(t, "levelling", route.DesignVersion)
	checked, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back: %v", err)
	}
	if checked.Status != RouteChecked {
		t.Fatalf("status = %q straight after a check, want %q", checked.Status, RouteChecked)
	}

	edited := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(checked.Version),
		Steps: steps("tutorial", "hogger", "defias"),
	})
	if edited.DesignVersion != checked.DesignVersion {
		t.Fatalf("writing a route moved the design version from %d to %d: `routes` is "+
			"deliberately not one of the watched tables, and a route write that marked "+
			"every route in the game stale would be exactly the mechanism 0013 argues "+
			"against", checked.DesignVersion, edited.DesignVersion)
	}
	if edited.Status != RouteStale {
		t.Fatalf("status = %q after the steps were rewritten under the verdict, want %q: "+
			"a verdict about a claim nobody makes any more must not read as green",
			edited.Status, RouteStale)
	}
	// The control: the other clock still works on its own. A design
	// write with no route write is stale too.
	g.recordCheck(t, "levelling", edited.DesignVersion)
	g.entity(t, "quest", "an-unrelated-quest")
	moved, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back: %v", err)
	}
	if moved.Status != RouteStale {
		t.Errorf("status = %q after a write to the game, want %q", moved.Status, RouteStale)
	}
}

// TestARenameLeavesARouteStepSpellingTheOldKey is the rename lesson,
// carried one step along.
//
// internal/metamodel/rename.go's header is the authority: a rename moves
// the catalogue row and nothing else, and it deliberately does not tidy
// view_refs.ref_key, because the stored row and the catalogue disagreeing
// on the spelling is what makes resolution fall to the by-id arm and
// report `type_renamed` rather than `missing`. A route step is the same
// shape: it stores entity_id as the resolution path plus
// (entity_type_key, entity_key) as a tombstone pair a rename must not
// touch.
//
// **Mutation:** have RenameEntityType tidy route_steps.entity_type_key.
// This test goes red on the stored spelling, and Task 10's
// TestARouteWhoseTypeWasRenamedIsStillOkAndSaysTheKeyMoved goes red on
// the diagnostic -- the second half of the same rule, which is why that
// half is named here rather than left to be discovered.
func TestARenameLeavesARouteStepSpellingTheOldKey(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})
	current, err := g.meta.EntityTypeByKey(context.Background(), g.projectID, "quest")
	if err != nil {
		t.Fatalf("read the entity type: %v", err)
	}
	if _, err := g.meta.RenameEntityType(context.Background(), g.projectID,
		metamodel.RenameInput{From: "quest", To: "mission", ExpectedVersion: &current.Version},
	); err != nil {
		t.Fatalf("rename the entity type: %v", err)
	}

	route, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
	if err != nil {
		t.Fatalf("read the route back after the rename: %v", err)
	}
	for i, step := range route.Steps {
		if step.EntityType != "quest" {
			t.Errorf("step %d spells its type %q; a rename moves the catalogue row and "+
				"nothing else, and the stored spelling is what lets a check report the "+
				"key as moved rather than the step as missing", i, step.EntityType)
		}
		// Resolution is by id, which the rename left standing, so the
		// step still points at real content.
		if step.EntityID == nil {
			t.Errorf("step %d lost its entity id to a rename", i)
		}
	}
}

// TestRemovingARouteRequiresTheVersionAndSaysWhy.
//
// This is the **inversion** of views.remove, which takes none, and the
// message has to carry the reason: a reader who has just read that file
// will otherwise read this as an omission.
func TestRemovingARouteRequiresTheVersionAndSaysWhy(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	route := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})

	err := g.analysis.RemoveRoute(context.Background(), g.projectID, "levelling", nil)
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("a removal with no version answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "/expected_version" {
		t.Fatalf("the refusal names %v", invalid.Fields)
	}
	message := invalid.Fields[0].Message
	for _, said := range []string{"authored", "verdict"} {
		if !strings.Contains(message, said) {
			t.Errorf("the refusal does not say what would be lost (%q): %s", said, message)
		}
	}
	// A stale version is a conflict rather than a silent success.
	if err := g.analysis.RemoveRoute(context.Background(), g.projectID,
		"levelling", version(route.Version+7)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("a stale version answered %v, want version_conflict", err)
	}
	// And the route is still there, which is what makes the two refusals
	// above refusals rather than reports.
	if _, err := g.analysis.RouteByKey(
		context.Background(), g.projectID, "levelling"); err != nil {
		t.Fatalf("a refused removal removed the route anyway: %v", err)
	}
	// The control: with the right version it goes, and its steps with
	// it.
	if err := g.analysis.RemoveRoute(context.Background(), g.projectID,
		"levelling", version(route.Version)); err != nil {
		t.Fatalf("remove the route: %v", err)
	}
	var stepRows int
	if err := g.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM route_steps WHERE project_id = $1`, g.projectID).
		Scan(&stepRows); err != nil {
		t.Fatalf("count the steps: %v", err)
	}
	if stepRows != 0 {
		t.Errorf("%d step rows survived the route they belong to", stepRows)
	}
}

// TestTheRouteListingCarriesHealthAndCountsButNoStepsOrVerdicts.
//
// ListViewsPage's discipline, carried here: a listing says what is here
// and what needs attention, and routes.get is where a walk and its proof
// come from.
func TestTheRouteListingCarriesHealthAndCountsButNoStepsOrVerdicts(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	first := g.upsert(t, RouteInput{
		Key: "alpha", Name: "A first route", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger", "defias"),
	})
	g.upsert(t, RouteInput{
		Key: "beta", Name: "B second route", ExpectedVersion: version(0),
		Steps: steps("deadmines"),
	})
	g.recordCheck(t, "alpha", first.DesignVersion)
	// A write to the *game* after the check, so alpha is stale for the
	// design-version reason. Writing beta would not have done it:
	// `routes` is not one of the watched tables.
	g.entity(t, "quest", "an-unrelated-quest")

	page, err := g.analysis.ListRoutes(context.Background(), g.projectID, "", 0)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if len(page.Routes) != 2 {
		t.Fatalf("the listing names %d routes, want two", len(page.Routes))
	}
	if page.Routes[0].Key != "alpha" || page.Routes[1].Key != "beta" {
		t.Errorf("the listing is ordered %v, want by name", page.Routes)
	}
	if page.Routes[0].StepCount != 3 || page.Routes[1].StepCount != 1 {
		t.Errorf("step counts are %d and %d, want 3 and 1",
			page.Routes[0].StepCount, page.Routes[1].StepCount)
	}
	// alpha was checked against the design version at the time, and
	// writing beta moved it, so alpha is stale and beta was never
	// checked -- two of the three states, in a listing.
	if page.Routes[0].Status != RouteStale {
		t.Errorf("alpha's status is %q, want %q", page.Routes[0].Status, RouteStale)
	}
	if page.Routes[1].Status != RouteNeverChecked {
		t.Errorf("beta's status is %q, want %q", page.Routes[1].Status, RouteNeverChecked)
	}
}

// TestARouteCursorFromAnotherGameIsRefused -- the behavioural half of
// the fingerprint contract.
func TestARouteCursorFromAnotherGameIsRefused(t *testing.T) {
	t.Parallel()
	mine := levellingGame(t)
	for _, key := range []string{"one", "two", "three"} {
		mine.upsert(t, RouteInput{
			Key: key, Name: key, ExpectedVersion: version(0), Steps: steps("tutorial"),
		})
	}
	first, err := mine.analysis.ListRoutes(context.Background(), mine.projectID, "", 1)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("the first page issued no cursor, so there is nothing to carry")
	}

	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.entity(t, "quest", "their-quest")
	theirs.upsert(t, RouteInput{
		Key: "their-route", Name: "Their route", ExpectedVersion: version(0),
		Steps: []RouteStepInput{{EntityType: "quest", Key: "their-quest"}},
	})
	_, err = theirs.analysis.ListRoutes(
		context.Background(), theirs.projectID, first.NextCursor, 1)
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("another game's cursor answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "cursor" {
		t.Errorf("the refusal names %v, want the caller's own `cursor`", invalid.Fields)
	}
}

// TestTheRouteListingFingerprintIsProjectIdFirstAndCarriesItsDomain is
// the compositional half.
//
// This listing takes **no filter**, which is exactly why the
// compositional assertion matters more here than elsewhere: the
// behavioural test above rests on the project id alone, so it would pass
// on a fingerprint that carried the project id and nothing else -- and
// then a cursor from another domain's listing over the same game would
// page this one perfectly and answer a different question.
func TestTheRouteListingFingerprintIsProjectIdFirstAndCarriesItsDomain(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	want := paging.Fingerprint(g.projectID.String(), "routes")
	if got := routeListingFingerprint(g.projectID); got != want {
		t.Fatalf("routeListingFingerprint = %q, want %q: the project id is first and the "+
			"domain follows it, which is internal/paging's contract", got, want)
	}
	if routeListingFingerprint(g.projectID) ==
		paging.Fingerprint(g.projectID.String(), "views") {
		t.Fatal("this listing shares a fingerprint with another domain's over one game")
	}
	if routeListingFingerprint(g.projectID) ==
		routeListingFingerprint(g.sibling(t).projectID) {
		t.Fatal("two games share a fingerprint, so one game's cursor pages the other")
	}
}

// TestARouteListingWalksEveryRouteExactlyOnce.
func TestARouteListingWalksEveryRouteExactlyOnce(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	want := map[string]bool{}
	for i := range 7 {
		key := "route-" + string(rune('a'+i))
		// Every route shares one name, which is what makes the id in the
		// keyset load-bearing: with ties in the sort key, a comparison
		// that disagreed with the order would skip or repeat rows here.
		g.upsert(t, RouteInput{
			Key: key, Name: "Same name", ExpectedVersion: version(0), Steps: steps("tutorial"),
		})
		want[key] = true
	}
	seen := map[string]int{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := g.analysis.ListRoutes(context.Background(), g.projectID, cursor, 3)
		if err != nil {
			t.Fatalf("list routes: %v", err)
		}
		for _, route := range page.Routes {
			seen[route.Key]++
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("the walk saw %v, want the seven routes", seen)
	}
	for key, count := range seen {
		if count != 1 || !want[key] {
			t.Errorf("%q was seen %d times", key, count)
		}
	}
}

// TestARouteUpsertAndRemovalArePublished, with the gating each carries.
//
// The gating is this file's own decision (events.go argues it), so it is
// asserted rather than assumed: MinRole empty means every member of the
// game including a viewer, and HumanOnly false means an agent hears it
// too -- an agent looping over check and upsert is the subscriber whose
// next write is judged against a version another writer just moved.
func TestARouteUpsertAndRemovalArePublished(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	hub := realtime.NewHub()
	svc := New(g.pool, hub)
	// A viewer's subscription, and not an owner's: the gating claim is
	// that a viewer hears this, and a subscription at owner would pass
	// whatever MinRole said.
	sub := hub.Subscribe(g.projectID, "viewer", false)
	defer hub.Unsubscribe(sub)

	route, err := svc.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := requireEvent(t, sub)
	if got.Kind != eventRouteUpserted {
		t.Errorf("kind = %q, want %q", got.Kind, eventRouteUpserted)
	}
	payload, ok := got.Payload.(routeEvent)
	if !ok {
		t.Fatalf("payload = %#v, want a routeEvent", got.Payload)
	}
	if payload.Key != "levelling" || payload.Version != route.Version || payload.ID != route.ID {
		t.Errorf("payload = %+v, want the route's identity and version", payload)
	}

	if err := svc.RemoveRoute(context.Background(), g.projectID,
		"levelling", version(route.Version)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := requireEvent(t, sub); got.Kind != eventRouteRemoved {
		t.Errorf("kind = %q, want %q", got.Kind, eventRouteRemoved)
	}
}

// requireEvent reads one event or fails, so a test asserting a publish
// cannot pass by timing out.
func requireEvent(t *testing.T, sub *realtime.Subscription) realtime.Event {
	t.Helper()
	select {
	case got := <-sub.C:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no event was published")
		return realtime.Event{}
	}
}

// requireNoEvent is the other half.
func requireNoEvent(t *testing.T, sub *realtime.Subscription, why string) {
	t.Helper()
	select {
	case got := <-sub.C:
		t.Fatalf("%s, but %q was published: %+v", why, got.Kind, got.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNoRouteEventIsPublishedWhenTheCommitFails is the one placement a
// refusal test cannot catch.
//
// A publish written as the last statement inside the transaction's
// callback differs from the correct one only by the commit that follows,
// and a rolled-back write and a refused write look identical from
// outside. So the commit, and only the commit, is made to fail: a
// deferred foreign key from routes.id to projects.id is satisfied by
// nothing -- a route's id is not a project id -- but being DEFERRABLE
// INITIALLY DEFERRED it is checked at COMMIT, so every statement inside
// the transaction succeeds and the commit raises 23503. testutil's
// per-test database is what makes installing such a constraint safe.
//
// internal/metamodel's and internal/views' tests of the same name are the
// model; this construction is copied because it is the only one that
// works.
func TestNoRouteEventIsPublishedWhenTheCommitFails(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	ctx := context.Background()
	hub := realtime.NewHub()
	svc := New(g.pool, hub)

	// Added while routes is empty: ADD CONSTRAINT validates the rows
	// already stored.
	if _, err := g.pool.Exec(ctx,
		`ALTER TABLE routes ADD CONSTRAINT zz_fail_at_commit
		   FOREIGN KEY (id) REFERENCES projects (id) DEFERRABLE INITIALLY DEFERRED`); err != nil {
		t.Fatalf("install the deferred constraint: %v", err)
	}

	sub := hub.Subscribe(g.projectID, "owner", false)
	defer hub.Unsubscribe(sub)

	_, err := svc.UpsertRoute(ctx, g.projectID, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial", "hogger"),
	})
	if err == nil {
		t.Fatal("the commit must fail under the deferred constraint")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("err = %v, want a deferred foreign-key violation at commit", err)
	}
	requireNoEvent(t, sub, "the transaction never committed")

	if _, err := svc.RouteByKey(ctx, g.projectID, "levelling"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RouteByKey = %v, want not_found: nothing was stored", err)
	}
	// The steps go with it, which is the other half of "one change":
	// they are written inside the same transaction, so a commit that
	// fails takes them too.
	var stepRows int
	if err := g.pool.QueryRow(ctx,
		`SELECT count(*) FROM route_steps WHERE project_id = $1`, g.projectID).
		Scan(&stepRows); err != nil {
		t.Fatalf("count the steps: %v", err)
	}
	if stepRows != 0 {
		t.Fatalf("%d step rows survived a transaction that never committed", stepRows)
	}
}

// TestARespellingOfAStoredRouteKeyIsNamedRatherThanSilentlyApplied.
//
// Keys are matched without regard to case, so a second spelling is a
// rewrite of the handle a designer bookmarks rather than a new route.
func TestARespellingOfAStoredRouteKeyIsNamedRatherThanSilentlyApplied(t *testing.T) {
	t.Parallel()
	g := levellingGame(t)
	stored := g.upsert(t, RouteInput{
		Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
		Steps: steps("tutorial"),
	})
	_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
		Key: "Levelling", Name: "Levelling", ExpectedVersion: version(stored.Version),
		Steps: steps("hogger"),
	})
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || invalid.Code != CodeInvalidInput {
		t.Fatalf("a respelling answered %v, want invalid_input", err)
	}
	if len(invalid.Fields) != 1 || invalid.Fields[0].Path != "/key" {
		t.Fatalf("the refusal names %v, want /key", invalid.Fields)
	}
	if !strings.Contains(invalid.Fields[0].Message, "levelling") {
		t.Errorf("the refusal does not name the stored spelling: %s", invalid.Fields[0].Message)
	}
}
