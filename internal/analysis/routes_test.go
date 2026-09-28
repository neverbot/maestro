package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/metamodel"
	"github.com/neverbot/maestro/internal/paging"
	"github.com/neverbot/maestro/internal/realtime"
	"github.com/neverbot/maestro/internal/testutil"
)

// levellingGame is the fixture most of these tests want: a gated game
// with four quests to walk through.
func levellingGame(t *testing.T, a area) game {
	t.Helper()
	g := a.gated(t)
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
	assert.Must(t, err == nil, "upsert route %q: %v", in.Key, err)
	return got
}

func steps(keys ...string) []RouteStepInput {
	out := make([]RouteStepInput, 0, len(keys))
	for _, key := range keys {
		out = append(out, RouteStepInput{EntityType: "quest", Key: key})
	}
	return out
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	a := newArea(t)

	// TestARouteReadsBackEverythingItWasWrittenWith.
	t.Run("a route reads back everything it was written with", func(t *testing.T) {
		g := levellingGame(t, a)
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
		assert.Must(t, err == nil, "read the route back: %v", err)
		assert.Should(t, got.Name == in.Name && got.Description == in.Description, "name/description read back as %q / %q", got.Name, got.Description)
		assert.Should(t, got.Version == 1, "version = %d, want 1 on a creation", got.Version)
		assert.Should(t, got.Params.Gate == GatingAll && got.Params.MaxDepth == 7 && got.Params.ExcludeInvalid, "params read back as %+v", got.Params)
		assert.Should(t, got.Params.IncludeUngated != nil && !(*got.Params.IncludeUngated), "include_ungated read back as %v, want the stored false: a tri-state "+
			"that loses its false is a stored parameter nothing reads", got.Params.IncludeUngated)
		assert.Should(t, got.Params.PropagateContainment != nil && !(*got.Params.PropagateContainment), "propagate_containment read back as %v", got.Params.PropagateContainment)
		assert.Should(t, len(got.Params.SeedEntities) == 1 && got.Params.SeedEntities[0].Key == "tutorial", "seed_entities read back as %v", got.Params.SeedEntities)
		assert.Should(t, len(got.Params.SeedEntityTypes) == 1 && got.Params.SeedEntityTypes[0] == "quest", "seed_entity_types read back as %v", got.Params.SeedEntityTypes)
		assert.Must(t, len(got.Steps) == 3, "read back %d steps, want three", len(got.Steps))
		for i, want := range in.Steps {
			step := got.Steps[i]
			assert.Should(t, int(step.Position) == i && step.EntityType == want.EntityType && step.Key == want.Key, "step %d read back as %+v, want %+v", i, step, want)
			assert.Should(t, step.Note == want.Note, "step %d note read back as %q, want %q: a note stored and never "+
				"read back is a field nothing would notice the loss of", i, step.Note, want.Note)
			assert.Should(t, step.EntityID != nil, "step %d resolved to no entity id", i)
		}
	})

	// TestRouteParamsAreValidatedAtWriteTimeAndNotOnlyAtCheckTime.
	t.Run("route params are validated at write time and not only at check time", func(t *testing.T) {
		g := levellingGame(t, a)
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "broken", Name: "Broken", ExpectedVersion: version(0),
			Params: RouteParams{Gate: Gating("most")},
			Steps:  steps("tutorial"),
		})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "a nonsense gate answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "/params/gate", "the refusal names %v, want /params/gate", invalid.Fields)
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
			assert.Should(t, errors.As(err, &invalid) && len(invalid.Fields) == 1 && invalid.Fields[0].Path == probe.path, "a bound broken at %s answered %v", probe.path, err)
		}
	})

	// TestOmittingTheExpectedVersionIsRefusedRatherThanGuessed.
	t.Run("omitting the expected version is refused rather than guessed", func(t *testing.T) {
		g := levellingGame(t, a)
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "levelling", Name: "Levelling", Steps: steps("tutorial"),
		})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "an omitted expected_version answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "/expected_version", "the refusal names %v", invalid.Fields)
	})

	// TestAVersionClaimAgainstARouteThatIsGoneIsRefusedRatherThanRecreated.
	t.Run("a version claim against a route that is gone is refused rather than recreated", func(t *testing.T) {
		g := levellingGame(t, a)
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
		assert.Must(t, errors.Is(err, ErrNotFound), "a version claim against a removed route answered %v, want not_found", err)
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
	})

	// TestAStepListAboveTheCapIsRefusedAndNotTruncated.
	t.Run("a step list above the cap is refused and not truncated", func(t *testing.T) {
		g := levellingGame(t, a)
		long := make([]RouteStepInput, MaxRouteSteps+1)
		for i := range long {
			long[i] = RouteStepInput{EntityType: "quest", Key: "tutorial"}
		}
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "long", Name: "Long", ExpectedVersion: version(0), Steps: long,
		})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "a 501-step route answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "/steps", "the refusal names %v, want /steps", invalid.Fields)
		message := invalid.Fields[0].Message
		assert.Should(t, strings.Contains(message, "501") && strings.Contains(message, "500"), "the refusal names neither the count nor the cap: %s", message)
		if _, err := g.analysis.RouteByKey(context.Background(), g.projectID, "long"); !errors.Is(
			err, ErrNotFound) {
			t.Fatalf("a truncated route was stored: %v", err)
		}
	})

	// TestAStepKeyThatResolvesToNothingIsNotFoundAndNotATombstone.
	t.Run("a step key that resolves to nothing is not found and not a tombstone", func(t *testing.T) {
		g := levellingGame(t, a)
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "typo", Name: "Typo", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hoggre"),
		})
		assert.Must(t, errors.Is(err, ErrNotFound), "a step naming no entity answered %v, want not_found", err)
		assert.Should(t, strings.Contains(err.Error(), "steps[1]") && strings.Contains(err.Error(), "hoggre"), "the refusal names neither the position nor the key: %v", err)
		if _, err := g.analysis.RouteByKey(context.Background(), g.projectID, "typo"); !errors.Is(
			err, ErrNotFound) {
			t.Fatalf("the route was stored with a step nobody could resolve: %v", err)
		}
	})

	// TestARouteStepCannotResolveToAnotherGamesEntity -- the isolation half
	// of step resolution, over one database holding both games.
	t.Run("a route step cannot resolve to another games entity", func(t *testing.T) {
		mine := levellingGame(t, a)
		theirs := mine.sibling(t)
		theirs.declareEntityType(t, "quest")
		theirs.entity(t, "quest", "their-secret")

		_, err := mine.analysis.UpsertRoute(context.Background(), mine.projectID, RouteInput{
			Key: "borrowed", Name: "Borrowed", ExpectedVersion: version(0),
			Steps: steps("tutorial", "their-secret"),
		})
		assert.Must(t, errors.Is(err, ErrNotFound), "a step naming another game's entity answered %v, want not_found", err)
		// The control: the same key resolves in the game that owns it, so
		// this test cannot pass on a resolver that finds nothing at all.
		theirs.upsert(t, RouteInput{
			Key: "theirs", Name: "Theirs", ExpectedVersion: version(0),
			Steps: []RouteStepInput{{EntityType: "quest", Key: "their-secret"}},
		})
	})

	// TestARouteKeyFromAnotherGameIsNotFound.
	t.Run("a route key from another game is not found", func(t *testing.T) {
		mine := levellingGame(t, a)
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
	})

	// TestTheStepListIsReplacedWholesaleAndNotMerged.
	t.Run("the step list is replaced wholesale and not merged", func(t *testing.T) {
		g := levellingGame(t, a)
		first := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger", "defias", "deadmines"),
		})
		second := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(first.Version),
			Steps: steps("deadmines", "tutorial"),
		})
		assert.Should(t, second.Version == first.Version+1, "version = %d, want %d", second.Version, first.Version+1)
		assert.Must(t, len(second.Steps) == 2, "the route holds %d steps after a two-step rewrite: %+v",
			len(second.Steps), second.Steps)
		if second.Steps[0].Key != "deadmines" || second.Steps[1].Key != "tutorial" {
			t.Errorf("the rewritten steps are %v, want the new order in the new order",
				[]string{second.Steps[0].Key, second.Steps[1].Key})
		}
		if second.Steps[0].Position != 0 || second.Steps[1].Position != 1 {
			t.Errorf("positions are %d,%d, want a dense 0..n-1",
				second.Steps[0].Position, second.Steps[1].Position)
		}
	})

	// TestANeverCheckedRouteIsNeitherStaleNorHealthy pins all three states
	// in one test.
	t.Run("a never checked route is neither stale nor healthy", func(t *testing.T) {
		g := levellingGame(t, a)
		route := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger"),
		})
		assert.Must(t, route.Status == RouteNeverChecked, "status = %q on a fresh route, want %q", route.Status, RouteNeverChecked)
		assert.Should(t, route.LastCheckedDesignVersion == nil && route.LastCheckedAt == nil, "a never-checked route carries a verdict: %v / %v",
			route.LastCheckedDesignVersion, route.LastCheckedAt)

		// Checked against the current design version. routes.check is Task
		// 10; this writes the three columns it will write, which is the one
		// part of this row the CRUD deliberately does not touch.
		g.recordCheck(t, "levelling", route.DesignVersion)
		checked, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "read the route back: %v", err)
		assert.Must(t, checked.Status == RouteChecked, "status = %q after a check against the current design, want %q",
			checked.Status, RouteChecked)

		// Any write anywhere in the game moves design_version, and the
		// counter is coarse on purpose: this one touches an entity the route
		// does not even name.
		g.entity(t, "quest", "an-unrelated-quest")
		stale, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "read the route back: %v", err)
		assert.Must(t, stale.Status == RouteStale, "status = %q after a write to the game, want %q", stale.Status, RouteStale)
		assert.Should(t, stale.LastCheckedDesignVersion != nil && *stale.LastCheckedDesignVersion < stale.DesignVersion, "last_checked_design_version %v is not below the game's %d, so the "+
			"status above rests on nothing", stale.LastCheckedDesignVersion, stale.DesignVersion)
		// And the verdict is still there: stale means "about a game that has
		// changed", never "discarded".
		assert.Should(t, len(stale.LastCheck) != 0, "the stored verdict was lost when the route went stale")
	})

	// TestAnOrdinaryUpsertLeavesAStoredVerdictStanding.
	t.Run("an ordinary upsert leaves a stored verdict standing", func(t *testing.T) {
		g := levellingGame(t, a)
		route := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		g.recordCheck(t, "levelling", route.DesignVersion)

		after := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling, renamed", ExpectedVersion: version(route.Version),
			Steps: steps("tutorial", "hogger"),
		})
		assert.Must(t, len(after.LastCheck) != 0 && after.LastCheckedAt != nil && after.LastCheckedDesignVersion != nil, "an ordinary edit discarded the stored verdict: %+v", after)
	})

	// TestEditingARouteMakesItsOwnVerdictStale is the half design_version
	// cannot see.
	t.Run("editing a route makes its own verdict stale", func(t *testing.T) {
		g := levellingGame(t, a)
		route := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		g.recordCheck(t, "levelling", route.DesignVersion)
		checked, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "read the route back: %v", err)
		assert.Must(t, checked.Status == RouteChecked, "status = %q straight after a check, want %q", checked.Status, RouteChecked)

		edited := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(checked.Version),
			Steps: steps("tutorial", "hogger", "defias"),
		})
		assert.Must(t, edited.DesignVersion == checked.DesignVersion, "writing a route moved the design version from %d to %d: `routes` is "+
			"deliberately not one of the watched tables, and a route write that marked "+
			"every route in the game stale would be exactly the mechanism 0013 argues "+
			"against", checked.DesignVersion, edited.DesignVersion)
		assert.Must(t, edited.Status == RouteStale, "status = %q after the steps were rewritten under the verdict, want %q: "+
			"a verdict about a claim nobody makes any more must not read as green",
			edited.Status, RouteStale)
		// The control: the other clock still works on its own. A design
		// write with no route write is stale too.
		g.recordCheck(t, "levelling", edited.DesignVersion)
		g.entity(t, "quest", "an-unrelated-quest")
		moved, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "read the route back: %v", err)
		assert.Should(t, moved.Status == RouteStale, "status = %q after a write to the game, want %q", moved.Status, RouteStale)
	})

	// TestARenameLeavesARouteStepSpellingTheOldKey is the rename lesson,
	// carried one step along.
	t.Run("a rename leaves a route step spelling the old key", func(t *testing.T) {
		g := levellingGame(t, a)
		g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger"),
		})
		current, err := g.meta.EntityTypeByKey(context.Background(), g.projectID, "quest")
		assert.Must(t, err == nil, "read the entity type: %v", err)
		if _, err := g.meta.RenameEntityType(context.Background(), g.projectID,
			metamodel.RenameInput{From: "quest", To: "mission", ExpectedVersion: &current.Version},
		); err != nil {
			t.Fatalf("rename the entity type: %v", err)
		}

		route, err := g.analysis.RouteByKey(context.Background(), g.projectID, "levelling")
		assert.Must(t, err == nil, "read the route back after the rename: %v", err)
		for i, step := range route.Steps {
			assert.Should(t, step.EntityType == "quest", "step %d spells its type %q; a rename moves the catalogue row and "+
				"nothing else, and the stored spelling is what lets a check report the "+
				"key as moved rather than the step as missing", i, step.EntityType)
			// Resolution is by id, which the rename left standing, so the
			// step still points at real content.
			assert.Should(t, step.EntityID != nil, "step %d lost its entity id to a rename", i)
		}
	})

	// TestRemovingARouteRequiresTheVersionAndSaysWhy.
	t.Run("removing a route requires the version and says why", func(t *testing.T) {
		g := levellingGame(t, a)
		route := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial", "hogger"),
		})

		err := g.analysis.RemoveRoute(context.Background(), g.projectID, "levelling", nil)
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "a removal with no version answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "/expected_version", "the refusal names %v", invalid.Fields)
		message := invalid.Fields[0].Message
		for _, said := range []string{"authored", "verdict"} {
			assert.Should(t, strings.Contains(message, said), "the refusal does not say what would be lost (%q): %s", said, message)
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
		assert.Should(t, stepRows == 0, "%d step rows survived the route they belong to", stepRows)
	})

	// TestTheRouteListingCarriesHealthAndCountsButNoStepsOrVerdicts.
	t.Run("the route listing carries health and counts but no steps or verdicts", func(t *testing.T) {
		g := levellingGame(t, a)
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
		assert.Must(t, err == nil, "list routes: %v", err)
		assert.Must(t, len(page.Routes) == 2, "the listing names %d routes, want two", len(page.Routes))
		assert.Should(t, page.Routes[0].Key == "alpha" && page.Routes[1].Key == "beta", "the listing is ordered %v, want by name", page.Routes)
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
	})

	// TestARouteCursorFromAnotherGameIsRefused -- the behavioural half of
	// the fingerprint contract.
	t.Run("a route cursor from another game is refused", func(t *testing.T) {
		mine := levellingGame(t, a)
		for _, key := range []string{"one", "two", "three"} {
			mine.upsert(t, RouteInput{
				Key: key, Name: key, ExpectedVersion: version(0), Steps: steps("tutorial"),
			})
		}
		first, err := mine.analysis.ListRoutes(context.Background(), mine.projectID, "", 1)
		assert.Must(t, err == nil, "list routes: %v", err)
		assert.Must(t, first.NextCursor != "", "the first page issued no cursor, so there is nothing to carry")

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
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "another game's cursor answered %v, want invalid_input", err)
		assert.Should(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "cursor", "the refusal names %v, want the caller's own `cursor`", invalid.Fields)
	})

	// TestTheRouteListingFingerprintIsProjectIdFirstAndCarriesItsDomain is
	// the compositional half.
	t.Run("the route listing fingerprint is project id first and carries its domain", func(t *testing.T) {
		g := a.game(t)
		want := paging.Fingerprint(g.projectID.String(), "routes")
		if got := routeListingFingerprint(g.projectID); got != want {
			t.Fatalf("routeListingFingerprint = %q, want %q: the project id is first and the "+
				"domain follows it, which is internal/paging's contract", got, want)
		}
		assert.Must(t, routeListingFingerprint(g.projectID) !=
			paging.Fingerprint(g.projectID.String(), "views"), "this listing shares a fingerprint with another domain's over one game")
		assert.Must(t, routeListingFingerprint(g.projectID) !=
			routeListingFingerprint(g.sibling(t).projectID), "two games share a fingerprint, so one game's cursor pages the other")
	})

	// TestARouteListingWalksEveryRouteExactlyOnce.
	t.Run("a route listing walks every route exactly once", func(t *testing.T) {
		g := levellingGame(t, a)
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
			assert.Must(t, err == nil, "list routes: %v", err)
			for _, route := range page.Routes {
				seen[route.Key]++
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		assert.Must(t, len(seen) == len(want), "the walk saw %v, want the seven routes", seen)
		for key, count := range seen {
			assert.Should(t, count == 1 && want[key], "%q was seen %d times", key, count)
		}
	})

	// TestARouteUpsertAndRemovalArePublished, with the gating each carries.
	t.Run("a route upsert and removal are published", func(t *testing.T) {
		g := levellingGame(t, a)
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
		assert.Must(t, err == nil, "upsert: %v", err)
		got := requireEvent(t, sub)
		assert.Should(t, got.Kind == eventRouteUpserted, "kind = %q, want %q", got.Kind, eventRouteUpserted)
		payload, ok := got.Payload.(routeEvent)
		assert.Must(t, ok, "payload = %#v, want a routeEvent", got.Payload)
		assert.Should(t, payload.Key == "levelling" && payload.Version == route.Version && payload.ID == route.ID, "payload = %+v, want the route's identity and version", payload)

		if err := svc.RemoveRoute(context.Background(), g.projectID,
			"levelling", version(route.Version)); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if got := requireEvent(t, sub); got.Kind != eventRouteRemoved {
			t.Errorf("kind = %q, want %q", got.Kind, eventRouteRemoved)
		}
	})

	// TestARespellingOfAStoredRouteKeyIsNamedRatherThanSilentlyApplied.
	t.Run("a respelling of a stored route key is named rather than silently applied", func(t *testing.T) {
		g := levellingGame(t, a)
		stored := g.upsert(t, RouteInput{
			Key: "levelling", Name: "Levelling", ExpectedVersion: version(0),
			Steps: steps("tutorial"),
		})
		_, err := g.analysis.UpsertRoute(context.Background(), g.projectID, RouteInput{
			Key: "Levelling", Name: "Levelling", ExpectedVersion: version(stored.Version),
			Steps: steps("hogger"),
		})
		var invalid *metamodel.ValidationError
		assert.Must(t, errors.As(err, &invalid) && invalid.Code == CodeInvalidInput, "a respelling answered %v, want invalid_input", err)
		assert.Must(t, len(invalid.Fields) == 1 && invalid.Fields[0].Path == "/key", "the refusal names %v, want /key", invalid.Fields)
		if !strings.Contains(invalid.Fields[0].Message, "levelling") {
			t.Errorf("the refusal does not name the stored spelling: %s", invalid.Fields[0].Message)
		}
	})
}

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
func TestNoRouteEventIsPublishedWhenTheCommitFails(t *testing.T) {
	t.Parallel()
	g := levellingGame(t, area{pool: testutil.NewPool(t)})
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
	assert.Must(t, err != nil, "the commit must fail under the deferred constraint")
	var pgErr *pgconn.PgError
	assert.Must(t, errors.As(err, &pgErr) && pgErr.Code == "23503", "err = %v, want a deferred foreign-key violation at commit", err)
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
	assert.Must(t, stepRows == 0, "%d step rows survived a transaction that never committed", stepRows)
}
