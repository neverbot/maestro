package views

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/google/uuid"
)

// chainOfQuests seeds a prerequisite chain of five quests — c1 requires
// c2 requires c3 requires c4 requires c5 — with min_level rising by ten
// along it, so a step's own `where` can be told apart from its depth
// bound.
//
// Five and not four: every depth assertion below has a negative half, and
// the node one hop past the bound is what that half names. A fixture
// whose chain ends exactly at the bound cannot tell "the walk stopped
// where it was told" from "the walk stopped because it ran out of
// content", which is the fixture-too-small-to-distinguish-any-policy
// shape this plan keeps finding.
func chainOfQuests(t *testing.T, g *game) {
	t.Helper()
	for i := 1; i <= 5; i++ {
		g.entity(t, "quest", fmt.Sprintf("c%d", i), fmt.Sprintf("Chain %d", i),
			map[string]any{"min_level": i * 10})
	}
	for i := 1; i < 5; i++ {
		g.relate(t, "requires", "quest", fmt.Sprintf("c%d", i), "quest", fmt.Sprintf("c%d", i+1))
	}
}

// runQuery is Run with the failure handled, for the tests below that are
// about what came back rather than about a refusal.
func runQuery(t *testing.T, g *game, doc string) Result {
	t.Helper()
	res, err := g.views.Run(t.Context(), g.projectID, RunRequest{Query: mustParse(t, doc)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

// sortedKeysOf is the node keys of a result, sorted, so an assertion can
// name the whole answer rather than probe it one membership at a time.
func sortedKeysOf(res Result) []string {
	out := keysOf(res.Nodes)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAMultiHopStepReachesTransitively is the feature: a step with a
// depth greater than one walks the relation, and the *fourth* quest is
// the assertion. Without the negative half the test passes against an
// unbounded walk, which is the failure mode a depth bound exists to
// prevent.
func TestAMultiHopStepReachesTransitively(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["c1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":3},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`)
	want := []string{"c2", "c3", "c4"}
	if got := sortedKeysOf(res); !equalStrings(got, want) {
		t.Fatalf("a three-hop walk reaches %v, got %v", want, got)
	}
	// The fourth ancestor is one hop past the bound. It is reachable, it
	// is of the right type, and nothing but the depth bound keeps it out.
	for _, n := range res.Nodes {
		if n.Key == "c5" {
			t.Fatalf("c5 is four hops away and the step asked for three")
		}
	}
	if res.Stats.MaxDepthReached != 3 {
		t.Errorf("the deepest node came back at three hops, stats say %d",
			res.Stats.MaxDepthReached)
	}
}

// TestACycleInContentIsDrawnRatherThanHung is why the walk lives in
// internal/graph. A prerequisite cycle is content the core spec
// deliberately allows — the analysis engine exists to report it — so a
// view of it must come back, with the edge that closes it, rather than
// spin until the statement budget cancels it.
func TestACycleInContentIsDrawnRatherThanHung(t *testing.T) {
	g, _ := newGame(t)
	for _, key := range []string{"x", "y", "z"} {
		g.entity(t, "quest", key, "Cycle "+key, nil)
	}
	g.relate(t, "requires", "quest", "x", "quest", "y")
	g.relate(t, "requires", "quest", "y", "quest", "z")
	g.relate(t, "requires", "quest", "z", "quest", "x")

	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["x"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":12},"as":"chain"}],
		"nodes":[{"set":"start"},{"set":"chain"}],
		"edges":[{"from_step":"chain"}],
		"limits":{"max_depth":12}}`)
	if got := sortedKeysOf(res); !equalStrings(got, []string{"x", "y", "z"}) {
		t.Fatalf("the three quests of the cycle come back once each, got %v", got)
	}
	// Three edges, and the third is the one that closes the cycle: an
	// earlier shape of the walk suppressed exactly that row, so the one
	// thing a designer needs to see about a prerequisite cycle was the one
	// thing no picture could show.
	if len(res.Edges) != 3 {
		t.Fatalf("a three-cycle has three edges, got %d", len(res.Edges))
	}
	closing := false
	byID := map[uuid.UUID]string{}
	for _, n := range res.Nodes {
		byID[n.ID] = n.Key
	}
	for _, e := range res.Edges {
		if byID[e.Source] == "z" && byID[e.Target] == "x" {
			closing = true
		}
	}
	if !closing {
		t.Errorf("the edge that closes the cycle (z requires x) must be drawn")
	}
	// x is the seed and is two hops from nothing: it is claimed by the
	// `start` entry at depth 0, and z, at two, is the furthest node the
	// picture holds. The closing hop onto x at depth 3 is an edge, not a
	// third distance to the same quest.
	if res.Stats.MaxDepthReached != 2 {
		t.Errorf("z is the furthest node at two hops and x is the seed at none, stats say %d",
			res.Stats.MaxDepthReached)
	}
	// **The counts alone do not see the path guard**, and this is where
	// that is said. The plan's own mutation for this test — break the
	// guard and watch it hang — neither hangs nor fails: the depth bound
	// terminates the walk on its own, the node and edge sets are
	// deduplicated, and the depth stat survives too because a node kept at
	// two distances keeps the shorter. What is left is this flag. Without
	// the guard the cycle is re-entered once per level to the bound and
	// past it, so a whole picture is reported partial — and that is the
	// assertion the guard is red under.
	if res.Truncated.Depth {
		t.Errorf("the whole cycle is drawn and there is nothing past it, so this picture " +
			"is not depth-truncated")
	}
}

// TestMinDepthDropsTheNearHops is the views-level half of the bound
// internal/graph applies after walking. The control at min 1 in the same
// test is what makes the negative half mean "dropped" rather than "never
// reached".
func TestMinDepthDropsTheNearHops(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	doc := `{"v":1,
		"from":[{"type":"quest","keys":["c1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":%d,"max":3},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`

	near := runQuery(t, g, fmt.Sprintf(doc, 1))
	if got := sortedKeysOf(near); !equalStrings(got, []string{"c2", "c3", "c4"}) {
		t.Fatalf("control: at min depth 1 the parent is reachable, got %v", got)
	}
	far := runQuery(t, g, fmt.Sprintf(doc, 2))
	if got := sortedKeysOf(far); !equalStrings(got, []string{"c3", "c4"}) {
		t.Fatalf("at min depth 2 the parent is walked through and not returned, got %v", got)
	}
}

// TestDirectionAnyWalksBothWaysWithoutDoubling is the views-level
// counterpart of internal/graph's own direction test, over real content:
// from the middle of the chain, `any` reaches both ways, and each quest
// comes back once.
func TestDirectionAnyWalksBothWaysWithoutDoubling(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["c3"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"any",
		             "depth":{"min":1,"max":2},"as":"around"}],
		"nodes":[{"set":"around"}],
		"edges":[{"from_step":"around"}]}`)
	// Two hops in both directions from c3 is c1, c2, c4 and c5 — the
	// whole chain but c3 itself, which the min depth of 1 drops.
	if got := sortedKeysOf(res); !equalStrings(got, []string{"c1", "c2", "c4", "c5"}) {
		t.Fatalf("`any` walks both ways two hops, got %v", got)
	}
	// Four edges, not eight: every edge of the chain is traversed once
	// from each end under `any`, and a walk that emitted one arm per
	// direction would hand each of them back twice.
	if len(res.Edges) != 4 {
		t.Fatalf("the four edges of the chain are drawn once each, got %d", len(res.Edges))
	}
}

// TestTruncatedDepthIsFlagged is the field Task 7 shipped false with a
// "not measured" note, measured.
//
// It is measured the way every other truncation flag in this package is:
// the walk is asked for one hop *more* than the step wants, the extra hop
// is dropped before the picture is built, and its existence is the flag.
// The control in the same test is a chain that ends exactly at the bound,
// which is the case an inferred flag ("the deepest node is at max depth")
// gets wrong.
func TestTruncatedDepthIsFlagged(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	doc := `{"v":1,
		"from":[{"type":"quest","keys":["c1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":%d},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`

	cut := runQuery(t, g, fmt.Sprintf(doc, 2))
	if !cut.Truncated.Depth {
		t.Errorf("the chain runs two hops past the bound and the flag says it did not")
	}
	if got := sortedKeysOf(cut); !equalStrings(got, []string{"c2", "c3"}) {
		t.Errorf("the hop past the bound must be dropped, not drawn: %v", got)
	}
	// The control: four hops reaches the end of the chain exactly, so
	// there is nothing past the bound and the flag must stay false. This
	// is the assertion that separates a measured flag from "the deepest
	// node sits at max_depth", which would report a whole picture partial.
	whole := runQuery(t, g, fmt.Sprintf(doc, 4))
	if whole.Truncated.Depth {
		t.Errorf("the chain ends exactly at the bound, so nothing was cut off: %+v",
			whole.Truncated)
	}
	if got := sortedKeysOf(whole); !equalStrings(got, []string{"c2", "c3", "c4", "c5"}) {
		t.Errorf("control: four hops reaches the whole chain, got %v", got)
	}
	// And a query with no walk in it at all must not claim a depth it
	// never measured.
	if flat := runQuery(t, g, `{"v":1,"from":[{"type":"quest"}]}`); flat.Truncated.Depth {
		t.Errorf("a query with no traversal cannot be depth-truncated")
	}
}

// TestAWalkStaysInsideOneGame is the isolation test, and it has to forge
// its evidence for the same reason internal/graph's does: 0004's
// composite foreign keys put an edge, its type and both its endpoints in
// one game by construction, so in a correct database every project filter
// this compiler emits is redundant and deleting one fails nothing.
//
// **That is exactly why this test exists here.** A recursive term is the
// first shape in this package whose filters are not redundant *by
// construction* — the rows it walks are found by the walk itself rather
// than by an id resolved in this game — so the invariant is asserted with
// rows the shipped schema makes impossible, in this test's own throwaway
// database, one row for each of the two filters in the recursive term.
func TestAWalkStaysInsideOneGame(t *testing.T) {
	g, other := newGame(t)
	ctx := context.Background()
	// A real edge of this game, so the walk has something to find: the
	// positive control below is that cook comes back.
	g.relate(t, "requires", "quest", "hogger", "quest", "cook")

	var requiresID uuid.UUID
	if err := g.pool.QueryRow(ctx,
		`SELECT id FROM relation_types WHERE project_id = $1 AND key = 'requires'`,
		g.projectID).Scan(&requiresID); err != nil {
		t.Fatalf("read this game's relation type: %v", err)
	}
	id := func(project uuid.UUID, key string) uuid.UUID {
		var out uuid.UUID
		if err := g.pool.QueryRow(ctx,
			`SELECT e.id FROM entities e JOIN entity_types et ON et.id = e.entity_type_id
			 WHERE e.project_id = $1 AND et.key = 'quest' AND e.key = $2`,
			project, key).Scan(&out); err != nil {
			t.Fatalf("read %s of %s: %v", key, project, err)
		}
		return out
	}
	for _, c := range []string{
		"relations_source_id_project_id_fkey",
		"relations_target_id_project_id_fkey",
		"relations_relation_type_id_project_id_fkey",
	} {
		if _, err := g.pool.Exec(ctx, "ALTER TABLE relations DROP CONSTRAINT "+c); err != nil {
			t.Fatalf("drop %s: %v", c, err)
		}
	}
	// An edge of this game pointing at another game's quest: what the
	// far-entity filter in the recursive term stops.
	foreignTarget := id(other.projectID, "defias")
	if _, err := g.pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4)`,
		g.projectID, requiresID, id(g.projectID, "hogger"), foreignTarget); err != nil {
		t.Fatalf("forge the cross-game edge: %v", err)
	}
	// An edge of the *other* game between two of this game's quests: what
	// the relation filter in the recursive term stops. Its far end is a
	// legitimate node of this game, so only the edge's own project says it
	// does not belong.
	if _, err := g.pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4)`,
		other.projectID, requiresID, id(g.projectID, "hogger"), id(g.projectID, "defias")); err != nil {
		t.Fatalf("forge the foreign edge: %v", err)
	}

	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["hogger"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":3},"as":"chain"}],
		"nodes":[{"set":"chain"}],
		"edges":[{"from_step":"chain"}]}`)

	// The positive control, in the same test: the walk does reach the one
	// quest a legitimate edge leads to. Without it every assertion below
	// is satisfied by a walk that returned nothing at all.
	if got := sortedKeysOf(res); !equalStrings(got, []string{"cook"}) {
		t.Fatalf("the walk must reach this game's own quest and nothing else, got %v", got)
	}
	for _, n := range res.Nodes {
		if n.ID == foreignTarget {
			t.Errorf("a quest of another game came back through the walk")
		}
	}
	// The edges are asserted too, because the node arms filter on the
	// project themselves: a walk that left the game would still have its
	// far node dropped there and would hand back the edge pointing at it.
	if len(res.Edges) != 1 {
		t.Fatalf("one legitimate edge is drawn, got %d", len(res.Edges))
	}
	mine := map[uuid.UUID]bool{}
	rows, err := g.pool.Query(ctx, `SELECT id FROM entities WHERE project_id = $1`, g.projectID)
	if err != nil {
		t.Fatalf("read this game's entities: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eid uuid.UUID
		if err := rows.Scan(&eid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		mine[eid] = true
	}
	for _, e := range res.Edges {
		if !mine[e.Source] || !mine[e.Target] {
			t.Errorf("an edge of this picture has an endpoint in another game: %+v", e)
		}
	}
}

// TestAWalkCTEsBindsAreRenumberedIntoTheOuterStatement is the test the
// renumbering owes. graph.WalkCTE numbers its own arguments from $1 and
// the compiler splices them into a statement that already has some, so a
// renumbering that is off by one does not fail — it compares the right
// column against the wrong value, and the wrong quest comes back with no
// error at all.
//
// The query carries a predicate bound *before* the walk (the selector's
// key) and one bound *after* it (the step's own where), with the walk's
// four arguments in between.
func TestAWalkCTEsBindsAreRenumberedIntoTheOuterStatement(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["c1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "to_type":"quest","depth":{"min":1,"max":3},"as":"chain",
		             "where":{"field":"min_level","op":"gte","value":30}}],
		"nodes":[{"set":"chain"}]}`)
	// c2 is one hop away and fails the predicate; c5 passes it and is out
	// of depth. Both halves have to hold at once, which is what a
	// misnumbered bind breaks.
	if got := sortedKeysOf(res); !equalStrings(got, []string{"c3", "c4"}) {
		t.Fatalf("the selector's key and the step's own predicate must both be answered "+
			"against the value they were bound with, got %v", got)
	}
}

// TestAnEdgeWhereFiltersTheHopsAWalkFollows is the views-level half of
// internal/graph's EdgePredicate placement: a condition on the relation
// prunes the recursion, so a quest reachable only through an excluded
// edge is not reached — rather than reached and then filtered out of the
// picture, which is what applying it to the walk's output would do.
func TestAnEdgeWhereFiltersTheHopsAWalkFollows(t *testing.T) {
	g, _ := newGame(t)
	for _, key := range []string{"e1", "e2", "e3"} {
		g.entity(t, "quest", key, "Edge "+key, nil)
	}
	g.relate(t, "requires", "quest", "e1", "quest", "e2")
	g.relate(t, "Guards", "quest", "e2", "quest", "e3")

	doc := `{"v":1,
		"from":[{"type":"quest","keys":["e1"],"as":"start"}],
		"traverse":[{"from":"start","via":["requires","guards"],"direction":"out",
		             "depth":{"min":1,"max":3},"as":"chain"%s}],
		"nodes":[{"set":"chain"}]}`

	both := runQuery(t, g, fmt.Sprintf(doc, ""))
	if got := sortedKeysOf(both); !equalStrings(got, []string{"e2", "e3"}) {
		t.Fatalf("control: following both relation types reaches e3, got %v", got)
	}
	only := runQuery(t, g, fmt.Sprintf(doc,
		`,"edge_where":{"field":"@type","op":"eq","value":"requires"}`))
	if got := sortedKeysOf(only); !equalStrings(got, []string{"e2"}) {
		t.Fatalf("e3 is reachable only over the excluded edge, so an edge_where applied to "+
			"the walk's recursion must not reach it at all, got %v", got)
	}
}

// TestAWalkDrawsOnlyItsDestinationTypeAndOnlyValidRows is the multi-hop
// half of the filters a one-hop step already applies. They are applied to
// the walk's *output* — a quest of the wrong type is walked through and
// not drawn — which is the difference between them and edge_where above,
// and it is asserted here rather than assumed: the zone in the middle of
// this fixture is what a walk that filtered its recursion by to_type
// could not walk past.
func TestAWalkDrawsOnlyItsDestinationTypeAndOnlyValidRows(t *testing.T) {
	g, _ := newGame(t)
	ctx := context.Background()
	g.entity(t, "quest", "t1", "Through 1", nil)
	g.entity(t, "zone", "middle", "The Middle", nil)
	g.entity(t, "quest", "t2", "Through 2", nil)
	g.entity(t, "quest", "t3", "Through 3", nil)
	// A relation type constrains neither of its endpoints, so `requires`
	// legitimately runs quest -> zone -> quest here.
	g.relate(t, "requires", "quest", "t1", "zone", "middle")
	g.relate(t, "requires", "zone", "middle", "quest", "t2")
	g.relate(t, "requires", "quest", "t2", "quest", "t3")
	if _, err := g.pool.Exec(ctx,
		`UPDATE entities SET invalid = true WHERE project_id = $1 AND key = 't3'`,
		g.projectID); err != nil {
		t.Fatalf("flag t3 invalid: %v", err)
	}

	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["t1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out","to_type":"quest",
		             "depth":{"min":1,"max":3},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`)
	if got := sortedKeysOf(res); !equalStrings(got, []string{"t2"}) {
		t.Fatalf("the walk passes through the zone to reach t2, draws neither the zone nor "+
			"the invalid t3, got %v", got)
	}
	// The two controls: the zone is reachable and is drawn when the step
	// asks for it, and t3 is drawn when the query asks for invalid rows.
	// Without them "got only t2" is also what a broken walk returns.
	open := runQuery(t, g, `{"v":1,"include_invalid":true,
		"from":[{"type":"quest","keys":["t1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":3},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`)
	if got := sortedKeysOf(open); !equalStrings(got, []string{"middle", "t2", "t3"}) {
		t.Fatalf("control: with no to_type and include_invalid the walk draws all three, got %v", got)
	}
}

// TestMaxDepthReachedIsWhatTheWalkReachedNotWhatItAskedFor is the
// arithmetic Task 6 shipped, replaced.
//
// A set's depth used to be its source set's depth plus the step's
// *declared* max, which is exact only while every step is one hop: a
// walk that asked for four and found two would have reported four, and a
// designer reading "max_depth_reached: 4" would conclude the bound was
// binding when it was not — the same over-report the node cap was fixed
// for. The depth now travels on the row.
func TestMaxDepthReachedIsWhatTheWalkReachedNotWhatItAskedFor(t *testing.T) {
	g, _ := newGame(t)
	g.entity(t, "quest", "m1", "Middle 1", nil)
	g.entity(t, "quest", "m2", "Middle 2", nil)
	g.relate(t, "requires", "quest", "m1", "quest", "m2")

	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["m1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":4},"as":"chain"}],
		"nodes":[{"set":"chain"}]}`)
	if got := sortedKeysOf(res); !equalStrings(got, []string{"m2"}) {
		t.Fatalf("the chain is one hop long, got %v", got)
	}
	if res.Stats.MaxDepthReached != 1 {
		t.Errorf("the walk asked for four hops and found one; stats say %d",
			res.Stats.MaxDepthReached)
	}
}

// TestAWalkFromAWalkCountsItsDepthFromTheSeed is the case the depth
// column exists for. A step reading from another step starts at whatever
// depth its own seed row sits at, and internal/graph counts from its own
// anchor — so the seed row's depth is added back, per row, through the
// path the walk carries. A picture of a chain would otherwise report
// every node past the second step as one or two hops away.
func TestAWalkFromAWalkCountsItsDepthFromTheSeed(t *testing.T) {
	g, _ := newGame(t)
	chainOfQuests(t, g)
	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["c1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":2},"as":"first"},
		            {"from":"first","via":"requires","direction":"out",
		             "depth":{"min":1,"max":2},"as":"second"}],
		"nodes":[{"set":"second"}],
		"limits":{"max_depth":4}}`)
	// The second walk starts from c2 and c3 and reaches c3, c4 and c5.
	if got := sortedKeysOf(res); !equalStrings(got, []string{"c3", "c4", "c5"}) {
		t.Fatalf("a walk from a walk reaches the rest of the chain, got %v", got)
	}
	// c5 is four hops from the seed: two through the first walk, two more
	// through the second. Counted from the second walk's own anchor it
	// would be two.
	if res.Stats.MaxDepthReached != 4 {
		t.Errorf("the deepest node is four hops from the seed selector, stats say %d",
			res.Stats.MaxDepthReached)
	}
}

// TestAWalkFromASetThatReachedANodeTwiceCountsTheShorterPath is why the
// from-set is grouped by id before a walk's depth is added back to it.
//
// One row of a step is one edge traversal, so a set can hold the same
// quest at two depths — here d3, reached from d1 directly and through d2.
// Joined ungrouped, the walk that reads from it would produce one row per
// spelling of its seed, and the deeper spelling would be reported as the
// depth the picture reached. The seed's depth is its *shortest* path, and
// the grouping is what makes that a single number.
func TestAWalkFromASetThatReachedANodeTwiceCountsTheShorterPath(t *testing.T) {
	g, _ := newGame(t)
	for _, key := range []string{"d1", "d2", "d3", "d4"} {
		g.entity(t, "quest", key, "Diamond "+key, nil)
	}
	g.relate(t, "requires", "quest", "d1", "quest", "d2")
	g.relate(t, "requires", "quest", "d2", "quest", "d3")
	g.relate(t, "requires", "quest", "d1", "quest", "d3")
	g.relate(t, "requires", "quest", "d3", "quest", "d4")

	res := runQuery(t, g, `{"v":1,
		"from":[{"type":"quest","keys":["d1"],"as":"start"}],
		"traverse":[{"from":"start","via":"requires","direction":"out",
		             "depth":{"min":1,"max":2},"as":"first"},
		            {"from":"first","via":"requires","direction":"out",
		             "depth":{"min":1,"max":1},"as":"second"}],
		"nodes":[{"set":"second"}],
		"limits":{"max_depth":4}}`)
	if got := sortedKeysOf(res); !equalStrings(got, []string{"d3", "d4"}) {
		t.Fatalf("the second walk reaches d3 (from d2) and d4 (from d3), got %v", got)
	}
	// d4 is two hops from the seed the short way (d1 -> d3 -> d4) and
	// three the long way. The picture reports the graph's distance, not
	// the longest spelling of it that happened to be walked.
	if res.Stats.MaxDepthReached != 2 {
		t.Errorf("the deepest node is two hops from the seed, stats say %d",
			res.Stats.MaxDepthReached)
	}
}
