package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// gatedGame is the fixture most of this file uses: one entity type,
// three gating relation types declared with the three shapes the
// normalisation has to tell apart, and one inert one.
//
// The traits are declared rather than derived from roles, because the
// subject here is the walk and not the resolver, and a fixture that let
// the resolver choose would fail for two reasons at once.
func gatedGame(t *testing.T) game {
	t.Helper()
	g := newGame(t)
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "requires", "", []string{"prerequisite_of"})
	g.declareRelationType(t, "unlocks", "", []string{"unlocks"})
	g.declareRelationType(t, "connects_to", "", []string{"symmetric"})
	return g
}

// TestANormalisedWalkFollowsAPrerequisiteEdgeBackwardsAndAnUnlockEdgeForwards
// is the normalisation itself, asserted rather than assumed.
//
// The two halves are in one test on purpose: they are one rule seen from
// two sides, and the mutation that breaks one leaves the other green,
// which is what makes this discriminating rather than a smoke test.
// `requires` reads "A requires B" -- source depends on target, so the
// engine follows it target→source -- and `unlocks` reads "A unlocks B",
// followed source→target. Both are gates and after the walk nothing
// downstream knows which was which.
func TestANormalisedWalkFollowsAPrerequisiteEdgeBackwardsAndAnUnlockEdgeForwards(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		g.entity(t, "quest", key)
	}
	// b requires a: a is needed, b depends on it.
	g.edge(t, "requires", "quest", "b", "a")
	// c unlocks d: c is needed, d depends on it.
	g.edge(t, "unlocks", "quest", "c", "d")

	got := g.reach(t, Params{
		SeedEntities: []SeedRef{
			{EntityType: "quest", Key: "a"},
			{EntityType: "quest", Key: "c"},
		},
		IncludeUngated: boolPtr(false),
	})

	if !g.reached(t, got, "quest", "b") {
		t.Error("b requires a and a is a seed, so b must be reached: a prerequisite_of edge " +
			"is followed target→source, which is the reversal the whole engine rests on")
	}
	if !g.reached(t, got, "quest", "d") {
		t.Error("c unlocks d and c is a seed, so d must be reached: an unlocks edge is " +
			"followed source→target")
	}
	if got.SeedCount != 2 {
		t.Errorf("the walk started from %d entities, want the two seeds: a closure that "+
			"started nowhere reaches nothing and reads exactly like a healthy game",
			got.SeedCount)
	}
	if got.EdgesWalked != 2 {
		t.Errorf("the walk traversed %d edges, want 2", got.EdgesWalked)
	}
}

// TestANormalisedWalkWillNotFollowAGatingEdgeAgainstItsDirection is the
// other half of the same decision: a gate is directional, and an
// analysis that followed one backwards would report a game as healthy
// because it could reach everything from anywhere.
//
// **The control is what makes it a test.** Without the second half, this
// passes against a walk that reaches nothing at all.
func TestANormalisedWalkWillNotFollowAGatingEdgeAgainstItsDirection(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "x")
	g.entity(t, "quest", "y")
	g.edge(t, "unlocks", "quest", "x", "y") // x unlocks y

	fromY := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "y"}},
		IncludeUngated: boolPtr(false),
	})
	if g.reached(t, fromY, "quest", "x") {
		t.Error("the walk reached x from y across an `unlocks` edge pointing the other way: " +
			"the per-type direction is spliced into the recursion's JOIN precisely so an " +
			"edge excluded by direction is never traversed")
	}

	fromX := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "x"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, fromX, "quest", "y") {
		t.Fatal("the same edge did not carry the walk from x to y either, so the assertion " +
			"above passed over a walk that reaches nothing")
	}
}

// TestASymmetricEdgeIsFollowedBothWays pins the third arm of the
// predicate: a `connects_to` is one row and two directions.
func TestASymmetricEdgeIsFollowedBothWays(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "p")
	g.entity(t, "quest", "q")
	g.edge(t, "connects_to", "quest", "p", "q")

	fromP := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "p"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, fromP, "quest", "q") {
		t.Error("a symmetric edge was not followed source→target")
	}
	fromQ := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "q"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, fromQ, "quest", "p") {
		t.Error("a symmetric edge was not followed target→source, which is the only thing " +
			"the trait says")
	}
}

// TestAnAnnotationTypeIsNotFollowedAtAll pins the word that exists to
// mean "deliberately inert", with the control that makes it a statement
// about the trait and not about the fixture.
func TestAnAnnotationTypeIsNotFollowedAtAll(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "illustrates", "", []string{"annotation"})
	g.entity(t, "quest", "a")
	g.entity(t, "quest", "b")
	g.edge(t, "illustrates", "quest", "a", "b")

	inert := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "a"}},
		IncludeUngated: boolPtr(false),
	})
	if g.reached(t, inert, "quest", "b") {
		t.Error("an `annotation` edge carried reachability; the trait's whole meaning is " +
			"that no analysis follows it")
	}

	// The control: the same fixture, the same edge, the type redeclared
	// as a gate. If b is unreachable here too, the assertion above was
	// about the fixture and not about the trait.
	g.declareRelationType(t, "illustrates", "", []string{"unlocks"})
	gating := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "a"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, gating, "quest", "b") {
		t.Fatal("redeclaring the type as {unlocks} did not reach b either, so the " +
			"assertion above proved nothing about `annotation`")
	}
}

// containmentGame is a zone holding a quest, plus the seed the two
// containment tests start from.
func containmentGame(t *testing.T) game {
	t.Helper()
	g := newGame(t)
	g.declareEntityType(t, "zone")
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "contains", "", []string{"containment"})
	g.entity(t, "zone", "elwynn")
	g.entity(t, "quest", "wolves")
	_, err := g.meta.UpsertRelation(context.Background(), g.projectID, metamodel.RelationInput{
		TypeKey: "contains",
		Source:  metamodel.Ref{TypeKey: "zone", Key: "elwynn"},
		Target:  metamodel.Ref{TypeKey: "quest", Key: "wolves"},
	})
	if err != nil {
		t.Fatalf("write the containment edge: %v", err)
	}
	return g
}

// TestContainmentPropagatesReachabilityDownward and its twin are the two
// halves of propagate_containment.
//
// Both seed the container explicitly with include_ungated **off**, and
// that is not incidental: a contained entity has no gating in-edge once
// containment stops counting as one, so under the default seeding it
// would become a start point and be reached for a reason that has
// nothing to do with propagation. The pair would then be green either
// way, which is the "test that passes for the wrong reason" this plan
// names.
func TestContainmentPropagatesReachabilityDownward(t *testing.T) {
	t.Parallel()
	g := containmentGame(t)
	got := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "zone", Key: "elwynn"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, got, "quest", "wolves") {
		t.Error("reaching a zone must reach what it contains")
	}
}

func TestContainmentPropagationCanBeSwitchedOff(t *testing.T) {
	t.Parallel()
	g := containmentGame(t)
	got := g.reach(t, Params{
		SeedEntities:         []SeedRef{{EntityType: "zone", Key: "elwynn"}},
		IncludeUngated:       boolPtr(false),
		PropagateContainment: boolPtr(false),
	})
	if g.reached(t, got, "quest", "wolves") {
		t.Error("propagate_containment is off and the walk still descended into the zone")
	}
	if !g.reached(t, got, "zone", "elwynn") {
		t.Fatal("the seed itself is not in the closure, so the assertion above passed over " +
			"an empty walk")
	}
}

// TestUnderAllABothGatedEntityNeedsBothGatesReachable holds the two
// gating modes apart, in one test, so they cannot silently become one.
func TestUnderAllABothGatedEntityNeedsBothGatesReachable(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"gate-one", "gate-two", "x"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "requires", "quest", "x", "gate-one")
	g.edge(t, "requires", "quest", "x", "gate-two")

	seeds := []SeedRef{{EntityType: "quest", Key: "gate-one"}}
	any := g.reach(t, Params{SeedEntities: seeds, IncludeUngated: boolPtr(false)})
	if !g.reached(t, any, "quest", "x") {
		t.Error("under `any` one reachable gate is enough and x must be reached")
	}
	all := g.reach(t, Params{
		SeedEntities: seeds, IncludeUngated: boolPtr(false), Gating: GatingAll,
	})
	if g.reached(t, all, "quest", "x") {
		t.Error("under `all` x needs both of its gates and only one is reachable")
	}
	if !g.reached(t, all, "quest", "gate-one") {
		t.Fatal("the seed is not admitted under `all` either, so the assertion above " +
			"passed over an empty answer")
	}
}

// TestTheAllFixpointTerminatesOnACyclicGraphAndSaysWhy is the case the
// fixpoint's bound exists for, and the reason its answer points at
// another analysis rather than at itself.
func TestTheAllFixpointTerminatesOnACyclicGraphAndSaysWhy(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"start", "loop-a", "loop-b"} {
		g.entity(t, "quest", key)
	}
	// The loop is reachable under `any` -- start unlocks it -- so it is
	// in the candidate set the fixpoint iterates over. A loop the walk
	// never reached would leave the fixpoint with nothing to hold back
	// and this test green for the wrong reason.
	g.edge(t, "unlocks", "quest", "start", "loop-a")
	// A prerequisite loop: each waits for the other, so under `all`
	// neither can ever be admitted.
	g.edge(t, "requires", "quest", "loop-a", "loop-b")
	g.edge(t, "requires", "quest", "loop-b", "loop-a")

	got := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "start"}},
		IncludeUngated: boolPtr(false),
		Gating:         GatingAll,
	})
	if !g.reached(t, got, "quest", "start") {
		t.Fatal("the seed is not admitted, so nothing below is being tested")
	}
	if g.reached(t, got, "quest", "loop-a") || g.reached(t, got, "quest", "loop-b") {
		t.Error("a mutually gating pair was admitted under `all`; each of them waits for " +
			"the other and neither can ever be satisfied")
	}
	if !strings.Contains(got.Note, "analysis.cycles") {
		t.Errorf("the answer does not name analysis.cycles as where to look; it says %q", got.Note)
	}
	if got.Passes == 0 {
		t.Error("the fixpoint reports no passes at all, so its own bound is unasserted")
	}
}

// TestAnInvalidEdgeStillGatesUnlessTheCallerExcludesIt is this package's
// disagreement with internal/views, asserted on one fixture run twice.
func TestAnInvalidEdgeStillGatesUnlessTheCallerExcludesIt(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "a")
	g.entity(t, "quest", "b")
	g.edge(t, "unlocks", "quest", "a", "b")
	g.invalidate(t, "unlocks")

	followed := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "a"}},
		IncludeUngated: boolPtr(false),
	})
	if !g.reached(t, followed, "quest", "b") {
		t.Error("an invalid edge was not followed by default; `invalid` means a row's " +
			"fields stopped fitting, and this walk reads only endpoints and relation type")
	}
	if followed.InvalidEdgesFollowed != 1 {
		t.Errorf("invalid_edges_followed = %d, want 1: a verdict that rests on flagged "+
			"rows must say so", followed.InvalidEdgesFollowed)
	}

	excluded := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "a"}},
		IncludeUngated: boolPtr(false),
		ExcludeInvalid: true,
	})
	if g.reached(t, excluded, "quest", "b") {
		t.Error("exclude_invalid was set and the invalid edge was followed anyway")
	}
	if excluded.InvalidEdgesFollowed != 0 {
		t.Errorf("invalid_edges_followed = %d under exclude_invalid, want 0",
			excluded.InvalidEdgesFollowed)
	}
}

// TestASeedKeyThatDoesNotResolveIsNotFoundAndNotAnEmptySeedSet is the
// single most damaging wrong answer this engine could produce, refused.
func TestASeedKeyThatDoesNotResolveIsNotFoundAndNotAnEmptySeedSet(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "real")

	_, err := g.analysis.Reach(context.Background(), Params{
		ProjectID: g.projectID,
		Semantics: g.semantics(t),
		SeedEntities: []SeedRef{
			{EntityType: "quest", Key: "real"},
			{EntityType: "quest", Key: "ghost"},
		},
		IncludeUngated: boolPtr(false),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not_found: a seed key that resolves to nothing must be "+
			"named, never dropped, or 'you gave me a bad key' becomes 'your entire game "+
			"is unreachable'", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the refusal does not name the key that failed: %v", err)
	}
	// The control: the good key in the same call resolves, so the
	// refusal above is about the bad one and not about the call shape.
	good := g.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "real"}},
		IncludeUngated: boolPtr(false),
	})
	if good.SeedCount != 1 {
		t.Fatalf("the good key alone seeded %d entities, want 1", good.SeedCount)
	}
}

// TestASeedKeyFromAnotherGameIsNotFound is the isolation case the spec
// makes mandatory, and it needs two games in one database.
func TestASeedKeyFromAnotherGameIsNotFound(t *testing.T) {
	t.Parallel()
	mine := gatedGame(t)
	mine.entity(t, "quest", "mine")
	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.entity(t, "quest", "theirs")

	_, err := mine.analysis.Reach(context.Background(), Params{
		ProjectID:      mine.projectID,
		Semantics:      mine.semantics(t),
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "theirs"}},
		IncludeUngated: boolPtr(false),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not_found for another game's entity key", err)
	}
	// The control: the same call shape against this game's own key
	// works, so the refusal is about the game and not about the key.
	ok := mine.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "mine"}},
		IncludeUngated: boolPtr(false),
	})
	if ok.SeedCount != 1 {
		t.Fatalf("this game's own seed resolved to %d entities, want 1", ok.SeedCount)
	}
}

// TestAnEmptySeedSetWithUngatedOffIsRefused pins O8's answer: an
// existing code names this recovery, so no new one ships.
func TestAnEmptySeedSetWithUngatedOffIsRefused(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "a")

	_, err := g.analysis.Reach(context.Background(), Params{
		ProjectID:      g.projectID,
		Semantics:      g.semantics(t),
		IncludeUngated: boolPtr(false),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	var invalid *metamodel.ValidationError
	if !errors.As(err, &invalid) || len(invalid.Fields) != 1 ||
		invalid.Fields[0].Path != "seed_entities" {
		t.Fatalf("the refusal is not reported at seed_entities: %v", err)
	}
	for _, source := range []string{"seed_entities", "seed_entity_types", "seed_route"} {
		if !strings.Contains(err.Error(), source) {
			t.Errorf("the refusal does not name the seed source %q: %v", source, err)
		}
	}
	// The control: the same call with the default seeding answers rather
	// than refusing, so the refusal is about the empty set and not about
	// the call.
	if got := g.reach(t, Params{}); got.SeedCount == 0 {
		t.Fatal("include_ungated on its default seeded nothing, so this game's ungated " +
			"entity is not being found and the refusal above proves nothing")
	}
}

// TestASeedRouteContributesItsStepsAsSeeds is O2's answer: no
// start_sets table, because a route already is an ordered list of this
// game's entities.
func TestASeedRouteContributesItsStepsAsSeeds(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"first", "second", "behind"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "unlocks", "quest", "second", "behind")
	g.route(t, "levelling", []SeedRef{
		{EntityType: "quest", Key: "first"},
		{EntityType: "quest", Key: "second"},
	})

	got := g.reach(t, Params{SeedRoute: "levelling", IncludeUngated: boolPtr(false)})
	if got.SeedCount != 2 {
		t.Errorf("the route contributed %d seeds, want its two steps", got.SeedCount)
	}
	if !g.reached(t, got, "quest", "behind") {
		t.Error("the walk did not continue from a route step, so the steps were counted " +
			"and not used")
	}
	if len(got.Seeds) != 2 || got.Seeds[0].Key != "first" || got.Seeds[1].Key != "second" {
		t.Errorf("the resolved seed set is %v, want the route's two steps by key", got.Seeds)
	}
}

// TestASeedRouteFromAnotherGameIsNotFound is the isolation half of the
// same feature, and it is a separate test because the route lookup is a
// different statement from the entity lookup.
func TestASeedRouteFromAnotherGameIsNotFound(t *testing.T) {
	t.Parallel()
	mine := gatedGame(t)
	mine.entity(t, "quest", "mine")
	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.declareRelationType(t, "unlocks", "", []string{"unlocks"})
	theirs.entity(t, "quest", "theirs")
	theirs.route(t, "their-route", []SeedRef{{EntityType: "quest", Key: "theirs"}})

	_, err := mine.analysis.Reach(context.Background(), Params{
		ProjectID:      mine.projectID,
		Semantics:      mine.semantics(t),
		SeedRoute:      "their-route",
		IncludeUngated: boolPtr(false),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want not_found for another game's route key", err)
	}
	// The control: the route resolves for the game that owns it.
	got := theirs.reach(t, Params{SeedRoute: "their-route", IncludeUngated: boolPtr(false)})
	if got.SeedCount != 1 {
		t.Fatalf("the owning game's route seeded %d entities, want 1", got.SeedCount)
	}
}

// TestAReachWalkCannotLeaveItsProjectThroughARogueEdge is this package's
// own copy of internal/graph's forgery, and it is not delegated to that
// package's test: this one composes a seed and an edge predicate around
// the walk, and the composition is what could lose the filter.
func TestAReachWalkCannotLeaveItsProjectThroughARogueEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mine := gatedGame(t)
	mine.entity(t, "quest", "seed")
	mine.entity(t, "quest", "near")
	mine.entity(t, "quest", "far")
	mine.edge(t, "unlocks", "quest", "seed", "near")

	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.entity(t, "quest", "elsewhere")

	for _, c := range []string{
		"relations_source_id_project_id_fkey",
		"relations_target_id_project_id_fkey",
		"relations_relation_type_id_project_id_fkey",
	} {
		if _, err := mine.pool.Exec(ctx, "ALTER TABLE relations DROP CONSTRAINT "+c); err != nil {
			t.Fatalf("drop %s: %v", c, err)
		}
	}
	ids := func(g game, typeKey, key string) uuid.UUID {
		row, err := g.meta.EntityByKey(ctx, g.projectID, typeKey, key)
		if err != nil {
			t.Fatalf("read back %s: %v", key, err)
		}
		return row.ID
	}
	var unlocksID uuid.UUID
	if err := mine.pool.QueryRow(ctx,
		`SELECT id FROM relation_types WHERE project_id = $1 AND key = 'unlocks'`,
		mine.projectID).Scan(&unlocksID); err != nil {
		t.Fatalf("read the relation type id: %v", err)
	}
	// An edge of this game pointing at another game's entity, and an
	// edge of the other game between two of this game's entities.
	if _, err := mine.pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4)`,
		mine.projectID, unlocksID, ids(mine, "quest", "seed"),
		ids(theirs, "quest", "elsewhere")); err != nil {
		t.Fatalf("forge the cross-game edge: %v", err)
	}
	if _, err := mine.pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4)`,
		theirs.projectID, unlocksID, ids(mine, "quest", "seed"),
		ids(mine, "quest", "far")); err != nil {
		t.Fatalf("forge the foreign edge: %v", err)
	}

	got := mine.reach(t, Params{
		SeedEntities:   []SeedRef{{EntityType: "quest", Key: "seed"}},
		IncludeUngated: boolPtr(false),
	})
	if !mine.reached(t, got, "quest", "near") {
		t.Fatal("the legitimate edge was not walked, so nothing below is being tested")
	}
	if got.Reached[ids(theirs, "quest", "elsewhere")] {
		t.Error("the closure left the game through an edge whose far end lives elsewhere")
	}
	if mine.reached(t, got, "quest", "far") {
		t.Error("the closure followed another game's edge between two of this game's entities")
	}
}

// TestADepthAboveTheCapIsRefusedRatherThanClamped keeps the refusal the
// bounds file argues reachable from the one call that takes a depth.
func TestADepthAboveTheCapIsRefusedRatherThanClamped(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "a")
	_, err := g.analysis.Reach(context.Background(), Params{
		ProjectID: g.projectID, Semantics: g.semantics(t), MaxDepth: MaxMaxDepth + 1,
	})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want limit_exceeded naming the cap", err)
	}
	if !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("the refusal does not name the argument at fault: %v", err)
	}
}

// TestADepthLimitedWalkSaysSo pins the flag the unreachable report turns
// into a per-entity reason, with a control run whose bound is not hit.
func TestADepthLimitedWalkSaysSo(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "unlocks", "quest", "a", "b")
	g.edge(t, "unlocks", "quest", "b", "c")
	g.edge(t, "unlocks", "quest", "c", "d")

	seeds := []SeedRef{{EntityType: "quest", Key: "a"}}
	short := g.reach(t, Params{SeedEntities: seeds, IncludeUngated: boolPtr(false), MaxDepth: 2})
	if !short.DepthLimited {
		t.Error("a walk that stopped at its bound does not say so")
	}
	if g.reached(t, short, "quest", "d") {
		t.Error("the walk reached past its depth bound")
	}
	long := g.reach(t, Params{SeedEntities: seeds, IncludeUngated: boolPtr(false), MaxDepth: 10})
	if long.DepthLimited {
		t.Error("a walk that ran out of graph before its bound reports itself depth-limited")
	}
	if !g.reached(t, long, "quest", "d") {
		t.Fatal("the unbounded control did not reach the end of the chain either")
	}
}
