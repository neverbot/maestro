package graph_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/graph"
	"github.com/neverbot/maestro/internal/testutil"
)

// fixture is one seeded game: a project, one entity type, one relation
// type, n entities and whatever edges the test asked for. The seeding is
// raw SQL on purpose -- this package must not depend on
// internal/metamodel, which is one of its two future callers -- and it
// follows internal/db/views_schema_test.go's seedViewGame.
type fixture struct {
	projectID uuid.UUID
	relTypeID uuid.UUID
	ids       []uuid.UUID
}

func seedGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string, n int, edges [][2]int) fixture {
	t.Helper()

	var f fixture
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (slug, name) VALUES ($1, $1) RETURNING id`, slug).Scan(&f.projectID); err != nil {
		t.Fatalf("insert project %s: %v", slug, err)
	}
	var entityTypeID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO entity_types (project_id, key, label, label_plural)
		 VALUES ($1, 'quest', 'Quest', 'Quests') RETURNING id`,
		f.projectID).Scan(&entityTypeID); err != nil {
		t.Fatalf("insert entity type in %s: %v", slug, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 VALUES ($1, 'requires', 'Requires') RETURNING id`,
		f.projectID).Scan(&f.relTypeID); err != nil {
		t.Fatalf("insert relation type in %s: %v", slug, err)
	}
	for i := 0; i < n; i++ {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO entities (project_id, entity_type_id, key, name)
			 VALUES ($1, $2, $3, $3) RETURNING id`,
			f.projectID, entityTypeID, fmt.Sprintf("e%d", i)).Scan(&id); err != nil {
			t.Fatalf("insert entity %d in %s: %v", i, slug, err)
		}
		f.ids = append(f.ids, id)
	}
	for _, e := range edges {
		if _, err := pool.Exec(ctx,
			`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
			 VALUES ($1, $2, $3, $4)`,
			f.projectID, f.relTypeID, f.ids[e[0]], f.ids[e[1]]); err != nil {
			t.Fatalf("insert edge %v in %s: %v", e, slug, err)
		}
	}
	return f
}

// run compiles a walk, executes it against the wrapper CTE the package
// says to read from, and returns the rows. Every behavioural test below
// goes through it, so no test can read the recursion directly and miss
// what ReadFrom applies.
type reached struct {
	id     uuid.UUID
	depth  int
	via    *uuid.UUID
	from   *uuid.UUID
	closed bool
}

func run(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w graph.Walk) []reached {
	t.Helper()
	body, args := graph.WalkCTE(w)
	stmt := "WITH RECURSIVE " + body + "\nSELECT id, depth, via_relation, from_id, closed FROM " + graph.ReadFrom(w)
	rows, err := pool.Query(ctx, stmt, args...)
	if err != nil {
		t.Fatalf("the walk must run: %v\n%s", err, stmt)
	}
	defer rows.Close()
	var out []reached
	for rows.Next() {
		var r reached
		if err := rows.Scan(&r.id, &r.depth, &r.via, &r.from, &r.closed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func nodeSet(rows []reached) map[uuid.UUID]bool {
	set := map[uuid.UUID]bool{}
	for _, r := range rows {
		set[r.id] = true
	}
	return set
}

// edgeSet is the relations a walk actually handed back. It is asserted
// beside the node set wherever a cycle is involved, because the node set
// alone cannot see a missing edge -- which is exactly how the suppressed
// closing edge survived the first round of tests on this package.
func edgeSet(rows []reached) map[uuid.UUID]bool {
	set := map[uuid.UUID]bool{}
	for _, r := range rows {
		if r.via != nil {
			set[*r.via] = true
		}
	}
	return set
}

// relationIDs reads back the ids of the edges a fixture seeded, in the
// order they were seeded, so a test can name "the edge that closes the
// cycle" rather than infer it from a count.
func relationIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f fixture, edges [][2]int) []uuid.UUID {
	t.Helper()
	out := make([]uuid.UUID, 0, len(edges))
	for _, e := range edges {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT id FROM relations WHERE project_id = $1 AND source_id = $2 AND target_id = $3`,
			f.projectID, f.ids[e[0]], f.ids[e[1]]).Scan(&id); err != nil {
			t.Fatalf("read back edge %v: %v", e, err)
		}
		out = append(out, id)
	}
	return out
}

// seedWalk is the seed every behavioural test uses: one anchor entity,
// written with its own $1/$2 numbering so that every run also exercises
// renumber.
const seedWalk = "SELECT id FROM entities WHERE project_id = $1 AND id = $2"

// TestTheProjectFilterIsInBothTermsOfTheRecursion is the isolation
// assertion in its cheapest form, and it is a *text* assertion on
// purpose: no query document and no fixture is needed to state the rule,
// and it fails on the emitted statement rather than on a row that the
// shipped composite foreign keys make hard to forge.
// TestAWalkCannotLeaveItsProjectThroughARogueEdge is the behavioural
// half, and it forges them.
func TestTheProjectFilterIsInBothTermsOfTheRecursion(t *testing.T) {
	t.Parallel()
	sql, _ := graph.WalkCTE(graph.Walk{
		Name: "w", ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities WHERE false",
		RelationTypeIDs: []uuid.UUID{uuid.New()}, Direction: graph.Out, MinDepth: 1, MaxDepth: 3,
	})
	anchor, recursive, ok := strings.Cut(sql, "UNION ALL")
	if !ok {
		t.Fatalf("a bounded walk must be recursive:\n%s", sql)
	}
	for name, half := range map[string]string{"anchor": anchor, "recursive term": recursive} {
		if !strings.Contains(half, "project_id = $1") {
			t.Errorf("the %s does not filter on the project:\n%s", name, half)
		}
	}
	// The recursive term carries two of them, on the edge and on the
	// entity at its far end, and they stop different things: the first a
	// walk through another game's edge, the second a walk into another
	// game's entity. Counting them is what keeps a fix to one of the two
	// from reading as a fix to both.
	if n := strings.Count(recursive, "project_id = $1"); n != 2 {
		t.Errorf("the recursive term filters on the project %d time(s), want 2 "+
			"(once on the relation, once on the entity at the far end):\n%s", n, recursive)
	}
	// The anchor's own is the third position, and it is the one a
	// project-blind seed depends on entirely.
	// TestTheAnchorFiltersOnTheProjectEvenWhenTheSeedDoesNot is its
	// behavioural half.
	if n := strings.Count(anchor, "project_id = $1"); n != 1 {
		t.Errorf("the anchor filters on the project %d time(s), want 1 (on the entity the "+
			"seed named, whether or not the seed filtered):\n%s", n, anchor)
	}
}

// TestAWalkOverACycleReturnsEachNodeOnceAndTheClosingEdgeWithIt is the
// visited-path guard's test, and the counts are the half that pins it.
// Termination is *not*: the depth bound terminates the walk on its own,
// so removing the guard leaves this walk finishing in the same time with
// the same node set, and a test that asserted only "it came back, with
// three nodes" was green against an emitter with no guard at all. What
// the guard changes is how many times the cycle is walked -- once,
// instead of once per depth up to the bound: 4 rows here against 11,
// measured on this project's Postgres. The cost is the branching factor
// raised to the depth bound, not the length of the cycle.
//
// The edge set is the other half, and it is the one an earlier shape of
// this walk failed: suppressing the *row* that closes the cycle rather
// than only its expansion returned an n-cycle with n-1 of its n edges,
// so the one thing a designer needs to see about a prerequisite cycle --
// the edge that closes it -- was the one thing no caller could be handed.
func TestAWalkOverACycleReturnsEachNodeOnceAndTheClosingEdgeWithIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 2}, {2, 0}} // a -> b -> c -> a
	f := seedGraph(t, ctx, pool, "cycle", 3, edges)
	rels := relationIDs(t, ctx, pool, f, edges)

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 10, MaxRows: 1000,
	})
	got := nodeSet(rows)

	// Termination alone would also be satisfied by an empty answer, which
	// is why the node set is asserted: a cycle is legal content and the
	// walk must return all of it, once. Min depth is 0 here because the
	// seed is one of the three nodes and dropping depth 0 would make this
	// test read as an assertion that the cycle was cut.
	for name, id := range map[string]uuid.UUID{"a": f.ids[0], "b": f.ids[1], "c": f.ids[2]} {
		if !got[id] {
			t.Errorf("the walk must reach %s: a cycle is legal content, not a reason to stop early", name)
		}
	}
	if len(got) != 3 {
		t.Fatalf("the walk visited %d nodes and the graph has 3", len(got))
	}
	// a at depth 0, b at 1, c at 2, and a again at depth 3 over the edge
	// that closes the loop.
	if len(rows) != 4 {
		t.Fatalf("the walk returned %d rows for a three-node cycle, want 4 (the three nodes "+
			"plus the hop that closes the loop): without the visited-path guard a cycle is "+
			"re-walked once per level until the depth bound stops it, and the node set alone "+
			"cannot see that; with the closing row suppressed there would be 3", len(rows))
	}
	if e := edgeSet(rows); len(e) != 3 || !e[rels[0]] || !e[rels[1]] || !e[rels[2]] {
		t.Fatalf("the walk handed back %d of the cycle's 3 edges: c -> a is the edge that "+
			"closes the loop, and a renderer cannot draw an edge it was never handed", len(e))
	}
	var closing []reached
	for _, r := range rows {
		if r.closed {
			closing = append(closing, r)
		}
	}
	if len(closing) != 1 {
		t.Fatalf("exactly one hop closes a three-node cycle, %d rows say they did", len(closing))
	}
	if closing[0].id != f.ids[0] || closing[0].depth != 3 || *closing[0].via != rels[2] {
		t.Errorf("the closing hop must be a at depth 3 over c -> a, got %+v", closing[0])
	}
}

// TestATwoCycleReturnsItsReturnEdge is the smallest cycle that has a
// return edge distinct from the edge that reached it, and it is the
// shape the earlier walk lost most visibly: b was reached, and the edge
// back from b to a -- half the content of the cycle -- was suppressed
// along with its expansion.
func TestATwoCycleReturnsItsReturnEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 0}} // a -> b -> a
	f := seedGraph(t, ctx, pool, "twocycle", 2, edges)
	rels := relationIDs(t, ctx, pool, f, edges)

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 1, MaxDepth: 10, MaxRows: 1000,
	})
	if len(rows) != 2 {
		t.Fatalf("a two-cycle walked out from a is b then a again, got %d rows: %+v", len(rows), rows)
	}
	if rows[0].id != f.ids[1] || rows[0].depth != 1 || rows[0].closed || *rows[0].via != rels[0] {
		t.Errorf("the first hop is b at depth 1 over a -> b, open, got %+v", rows[0])
	}
	if rows[1].id != f.ids[0] || rows[1].depth != 2 || !rows[1].closed || *rows[1].via != rels[1] {
		t.Errorf("the second hop is a at depth 2 over b -> a, closed, got %+v", rows[1])
	}
}

// TestACycleThatExcludesTheSeedIsGuardedByTheWholePath is the one input
// that separates the accumulating path from a guard that only remembers
// the seed. Every other fixture in this file has its cycle pass through
// the seed, so a path that never grows -- w.path instead of
// w.path || far -- answers all of them identically. Here the cycle is
// b -> c -> d -> b and the seed is a, outside it: the shipped walk
// returns 5 rows, and the mutant walks the loop until the depth bound
// stops it at 11.
func TestACycleThatExcludesTheSeedIsGuardedByTheWholePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 1}} // a -> b -> c -> d -> b
	f := seedGraph(t, ctx, pool, "offseedcycle", 4, edges)
	rels := relationIDs(t, ctx, pool, f, edges)

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 10, MaxRows: 1000,
	})
	// a, b, c, d, and b again over the edge that closes a loop the seed
	// is not part of.
	if len(rows) != 5 {
		t.Fatalf("the walk returned %d rows, want 5: a guard that only remembers the seed "+
			"lets b -> c -> d -> b run until the depth bound and returns 11: %+v", len(rows), rows)
	}
	last := rows[len(rows)-1]
	if !last.closed || last.id != f.ids[1] || last.depth != 4 || *last.via != rels[3] {
		t.Errorf("the loop closes at b, depth 4, over d -> b, got %+v", last)
	}
	if e := edgeSet(rows); len(e) != 4 {
		t.Errorf("all four edges must be handed back, got %d", len(e))
	}
}

func TestDepthBoundsTheWalk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "chain", 5, [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}})

	for _, tc := range []struct{ maxDepth, want int }{{1, 1}, {2, 2}, {4, 4}} {
		got := nodeSet(run(t, ctx, pool, graph.Walk{
			Name: "w", ProjectID: f.projectID,
			SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
			RelationTypeIDs: []uuid.UUID{f.relTypeID},
			Direction:       graph.Out, MinDepth: 1, MaxDepth: tc.maxDepth, MaxRows: 1000,
		}))
		if len(got) != tc.want {
			t.Errorf("max_depth %d reached %d entities, want %d", tc.maxDepth, len(got), tc.want)
		}
	}
}

// TestDirectionAnyTraversesEachEdgeOnceFromEachNode is what is left of
// the plan's TestDirectionAnyDoesNotDoubleASymmetricEdge, and it asserts
// something different because the plan's premise did not survive
// contact with the shipped schema. A pair joined in both directions is
// two edges, not one hop counted twice: the walk hands back both, and a
// renderer that was handed one of them could not draw the other. What
// must not double is a single *edge* traversed from a single node, which
// is what the per-depth assertions below pin on the relation ids.
//
// It is also where the non-backtracking guard is visible. Under Any, the
// hop from b would otherwise re-traverse the very edge it arrived by and
// report a closed row for a on every edge in the graph; what closes here
// is the *other* edge of the pair, once from each of the two ways b was
// reached.
func TestDirectionAnyTraversesEachEdgeOnceFromEachNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 0}} // a -> b and b -> a
	f := seedGraph(t, ctx, pool, "symmetric", 2, edges)
	rels := relationIDs(t, ctx, pool, f, edges)

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Any, MinDepth: 1, MaxDepth: 4, MaxRows: 1000,
	})

	// Two open hops onto b, one per edge, and two closing hops back onto
	// a -- one from each of those, over the edge it did not arrive by.
	if len(rows) != 4 {
		t.Fatalf("a symmetric pair reached under any is two hops out and two that close, "+
			"got %d rows: %+v", len(rows), rows)
	}
	byDepth := map[int][]reached{}
	for _, r := range rows {
		if r.via == nil {
			t.Fatalf("a reached row must name the relation it walked: %+v", r)
		}
		byDepth[r.depth] = append(byDepth[r.depth], r)
	}
	for _, tc := range []struct {
		depth  int
		node   uuid.UUID
		closed bool
		what   string
	}{
		{1, f.ids[1], false, "the far node, once per edge"},
		{2, f.ids[0], true, "the seed again, once per way b was reached"},
	} {
		got := byDepth[tc.depth]
		if len(got) != 2 {
			t.Fatalf("depth %d must carry 2 rows (%s), got %d: %+v", tc.depth, tc.what, len(got), got)
		}
		seen := map[uuid.UUID]int{}
		for _, r := range got {
			if r.id != tc.node || r.closed != tc.closed {
				t.Errorf("at depth %d, want %s with closed = %v, got %+v", tc.depth, tc.what, tc.closed, r)
			}
			seen[*r.via]++
		}
		if len(seen) != 2 || seen[rels[0]] != 1 || seen[rels[1]] != 1 {
			t.Fatalf("at depth %d the two edges must be traversed once each, got %v: the walk "+
				"is counting one edge under both arms", tc.depth, seen)
		}
	}
	if len(byDepth) != 2 {
		t.Fatalf("the walk must stop after the hop that closes, got depths %v", byDepth)
	}
}

// TestASelfLoopIsReturnedOnceAndNotExpanded pins the one shape that can
// match an edge from both ends at once, and it is the test that makes
// the emitter's arm count observable.
//
// A self-loop is a cycle of length one. The walk hands it back -- once,
// closed, over the loop's own relation -- and does not expand from it: an
// earlier shape of this walk suppressed the row along with the expansion,
// so the single most obvious design error a game can contain was the one
// thing this package could not show.
//
// Because the row is now returned, the arm count is pinned here. A
// two-armed emitter -- the legal form of "one arm per direction", a
// two-armed edge relation feeding one self-reference -- matches this
// loop under both arms and returns it twice; that doubling was measured
// at 1, 2, 4 and 8 rows for depths 0 to 3 with the path guard removed,
// and while the row was suppressed no test in this package could see it.
// The count below is red against that emitter.
func TestASelfLoopIsReturnedOnceAndNotExpanded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 0}, {0, 1}}
	f := seedGraph(t, ctx, pool, "selfloop", 2, edges)
	rels := relationIDs(t, ctx, pool, f, edges)

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Any, MinDepth: 1, MaxDepth: 3, MaxRows: 1000,
	})

	// The ordinary edge is the positive control: an empty answer would
	// satisfy "the loop was returned once" without the walk having
	// worked at all. Both rows sit at depth 1, so they are matched by
	// relation rather than by position.
	byRelation := map[uuid.UUID][]reached{}
	for _, r := range rows {
		if r.via == nil {
			t.Fatalf("a reached row must name the relation it walked: %+v", r)
		}
		byRelation[*r.via] = append(byRelation[*r.via], r)
	}
	if len(rows) != 2 || len(byRelation) != 2 {
		t.Fatalf("the walk must return the loop once and the ordinary edge once, got %d rows "+
			"over %d relations -- two rows over the loop's relation is the two-armed emitter, "+
			"which matches a self-loop from both ends: %+v", len(rows), len(byRelation), rows)
	}
	loop := byRelation[rels[0]]
	if len(loop) != 1 || loop[0].id != f.ids[0] || loop[0].depth != 1 || !loop[0].closed {
		t.Fatalf("the self-loop must come back once, as the seed at depth 1, closed, got %+v", loop)
	}
	ordinary := byRelation[rels[1]]
	if len(ordinary) != 1 || ordinary[0].id != f.ids[1] || ordinary[0].closed {
		t.Fatalf("the ordinary edge must be walked to its far end, open, got %+v", ordinary)
	}
}

// TestAWalkCannotLeaveItsProjectThroughARogueEdge is the behavioural
// half of the isolation rule, and it has to forge its evidence:
// 0004_metamodel.sql's composite foreign keys put an edge, its type and
// both its endpoints in one game by construction, so the row this test
// needs cannot exist in a correct database. It drops those three
// constraints **in its own throwaway database** and writes two rows that
// the shipped schema refuses, one for each of the two filters in the
// recursive term. The invariant must not rest on another table's data
// being correct, and this is what says so with rows rather than with a
// comment.
func TestAWalkCannotLeaveItsProjectThroughARogueEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	mine := seedGraph(t, ctx, pool, "mine", 3, [][2]int{{0, 1}})
	theirs := seedGraph(t, ctx, pool, "theirs", 1, nil)

	for _, c := range []string{
		"relations_source_id_project_id_fkey",
		"relations_target_id_project_id_fkey",
		"relations_relation_type_id_project_id_fkey",
	} {
		if _, err := pool.Exec(ctx, "ALTER TABLE relations DROP CONSTRAINT "+c); err != nil {
			t.Fatalf("drop %s: %v", c, err)
		}
	}
	// An edge of this game, pointing at another game's entity.
	if _, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		mine.projectID, mine.relTypeID, mine.ids[0], theirs.ids[0]); err != nil {
		t.Fatalf("forge the cross-game edge: %v", err)
	}
	// An edge of the *other* game, between two of this game's entities.
	if _, err := pool.Exec(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id) VALUES ($1, $2, $3, $4)`,
		theirs.projectID, mine.relTypeID, mine.ids[0], mine.ids[2]); err != nil {
		t.Fatalf("forge the foreign edge: %v", err)
	}

	got := nodeSet(run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: mine.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{mine.projectID, mine.ids[0]},
		RelationTypeIDs: []uuid.UUID{mine.relTypeID},
		Direction:       graph.Any, MinDepth: 0, MaxDepth: 5, MaxRows: 1000,
	}))

	// The positive control is in the same assertion: the legitimate edge
	// must still be walked, so an empty answer cannot pass this test.
	want := map[uuid.UUID]string{mine.ids[0]: "the anchor", mine.ids[1]: "the entity one legitimate edge away"}
	for id, what := range want {
		if !got[id] {
			t.Errorf("the walk must reach %s", what)
		}
	}
	if got[theirs.ids[0]] {
		t.Error("the walk left the game through an edge whose far end lives elsewhere: " +
			"the entity join in the recursive term must filter on the project")
	}
	if got[mine.ids[2]] {
		t.Error("the walk followed another game's edge between two of this game's entities: " +
			"the relation join in the recursive term must filter on the project")
	}
	if len(got) != len(want) {
		t.Fatalf("the walk reached %d entities, want %d", len(got), len(want))
	}
}

// TestMinDepthDropsTheNearHopsAfterWalkingThem pins both halves of the
// bound this package owns rather than leaving to its callers: the near
// hops do not come back, and the far ones do -- which they could not if
// MinDepth had pruned the recursion instead of filtering its result.
func TestMinDepthDropsTheNearHopsAfterWalkingThem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "mindepth", 4, [][2]int{{0, 1}, {1, 2}, {2, 3}})

	walk := graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 2, MaxDepth: 3, MaxRows: 1000,
	}
	got := nodeSet(run(t, ctx, pool, walk))
	for i, want := range map[int]bool{0: false, 1: false, 2: true, 3: true} {
		if got[f.ids[i]] != want {
			t.Errorf("with min depth 2, e%d present = %v, want %v", i, got[f.ids[i]], want)
		}
	}

	// The control: the same walk with min 1 hands back the hop that min 2
	// dropped, so the assertion above is about the bound and not about an
	// unreachable node.
	walk.MinDepth = 1
	if !nodeSet(run(t, ctx, pool, walk))[f.ids[1]] {
		t.Error("with min depth 1 the first hop must come back")
	}
}

// TestMaxRowsReturnsOneRowPastTheCapSoTruncationIsDetectable pins the
// cap and, more importantly, pins that a caller can tell a full answer
// from a truncated one. Exactly-at-cap and truncated-at-cap are
// otherwise the same answer, and MaxRows's own comment tells a caller
// that hitting the cap means a truncated answer it must report -- which
// it cannot do if the two look alike. This is the same cap+1 mechanism
// Task 7's truncation flags are built on.
func TestMaxRowsReturnsOneRowPastTheCapSoTruncationIsDetectable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "cap", 5, [][2]int{{0, 1}, {0, 2}, {0, 3}, {0, 4}})

	walk := graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 2, MaxRows: 2,
	}
	// Five rows are available and the cap is 2, so the walk hands back
	// three: the cap plus the one row that says "there was more".
	if n := len(run(t, ctx, pool, walk)); n != 3 {
		t.Errorf("max_rows 2 returned %d rows, want 3 (the cap plus one): without the extra "+
			"row a caller at the cap cannot tell a complete answer from a truncated one", n)
	}
	// The other side of the mechanism: a walk that fits under the cap
	// must return fewer than cap+1 rows, so the extra row means
	// truncation and nothing else.
	walk.MaxRows = 5
	if n := len(run(t, ctx, pool, walk)); n != 5 {
		t.Errorf("with the cap at 5 and five rows available the walk returned %d, want 5: "+
			"an answer that exactly fills the cap is not a truncated one", n)
	}
	// The control: uncapped, the same walk returns the anchor and its
	// four neighbours, so the cap above truncated something real.
	walk.MaxRows = 0
	if n := len(run(t, ctx, pool, walk)); n != 5 {
		t.Errorf("uncapped, the walk returned %d rows, want 5", n)
	}
}

// TestATruncatedWalkIsOrderedByDepth pins that a truncated answer is a
// connected prefix of the nearest hops rather than an arbitrary scatter
// of nodes whose own edges were dropped -- which is not something a
// designer can be shown.
//
// It has two halves and they pin different things. The behavioural half
// below passes on an emitter with no ORDER BY at all, because Postgres
// evaluates a recursive CTE breadth-first and therefore already yields
// rows in depth order; it is the positive control that the prefix really
// is the near hops. The *text* half is what pins the ordering as a
// contract rather than as an executor detail, and it is red when the
// ORDER BY is removed.
func TestATruncatedWalkIsOrderedByDepth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "orderedcap", 5, [][2]int{{0, 1}, {1, 2}, {2, 3}, {3, 4}})

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 4, MaxRows: 2,
	})
	if len(rows) != 3 {
		t.Fatalf("the cap of 2 over a five-node chain must return 3 rows, got %d", len(rows))
	}
	for i, r := range rows {
		if r.depth != i || r.id != f.ids[i] {
			t.Errorf("row %d is %+v; a truncated walk is the nearest hops in order, so it "+
				"must be e%d at depth %d", i, r, i, i)
		}
	}

	sql, _ := graph.WalkCTE(graph.Walk{
		Name: "w", ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities WHERE false",
		RelationTypeIDs: []uuid.UUID{uuid.New()}, Direction: graph.Out, MaxDepth: 3, MaxRows: 2,
	})
	_, wrapper, ok := strings.Cut(sql, graph.ReadFrom(graph.Walk{Name: "w"})+" AS ")
	if !ok {
		t.Fatalf("the walk must emit a wrapper CTE:\n%s", sql)
	}
	order, limit := strings.Index(wrapper, "ORDER BY depth"), strings.Index(wrapper, "LIMIT ")
	if order < 0 || limit < 0 || order > limit {
		t.Errorf("the wrapper must order by depth before it limits, so that what survives the "+
			"cap is the near hops rather than whatever the executor happened to emit first:\n%s",
			wrapper)
	}
}

// TestTheAnchorFiltersOnTheProjectEvenWhenTheSeedDoesNot is the third
// project-filter position, and the one the text assertion alone was
// holding. SeedSQL is documented as the caller's own SQL and this
// package does not control whether it filters -- so the anchor join is
// the only thing between a project-blind seed and another game's entity.
// The seed here deliberately selects by key across every game.
func TestTheAnchorFiltersOnTheProjectEvenWhenTheSeedDoesNot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	mine := seedGraph(t, ctx, pool, "anchormine", 2, [][2]int{{0, 1}})
	theirs := seedGraph(t, ctx, pool, "anchortheirs", 2, [][2]int{{0, 1}})

	got := nodeSet(run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: mine.projectID,
		SeedSQL: "SELECT id FROM entities WHERE key = $1", SeedArgs: []any{"e0"},
		RelationTypeIDs: []uuid.UUID{mine.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 3, MaxRows: 1000,
	}))

	// The positive control is in the same assertion: this game's seed and
	// the hop away from it must still be there, so an empty answer cannot
	// pass.
	for id, what := range map[uuid.UUID]string{
		mine.ids[0]: "its own seed", mine.ids[1]: "the entity one hop from it",
	} {
		if !got[id] {
			t.Errorf("the walk must reach %s", what)
		}
	}
	for id, what := range map[uuid.UUID]string{
		theirs.ids[0]: "another game's entity of the same key",
		theirs.ids[1]: "the entity one hop beyond it",
	} {
		if got[id] {
			t.Errorf("the walk seeded on %s: the anchor join is the only project filter a "+
				"project-blind seed passes through, and it must not be droppable", what)
		}
	}
	if len(got) != 2 {
		t.Fatalf("the walk reached %d entities, want 2", len(got))
	}
}

// TestAWalkWithNoRelationTypesReachesOnlyItsSeed pins what an empty type
// list means, which is the difference between "no edges are followed"
// and "every edge is followed" -- a difference an emitter can get wrong
// by dropping the ANY clause when the list is empty.
func TestAWalkWithNoRelationTypesReachesOnlyItsSeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "notypes", 2, [][2]int{{0, 1}})

	got := nodeSet(run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: nil,
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 3, MaxRows: 1000,
	}))
	if len(got) != 1 || !got[f.ids[0]] {
		t.Fatalf("a walk along no relation type reaches its seed and nothing else, got %v", got)
	}
}

// TestDirectionInWalksTheOtherWay is the arm the chain tests never
// exercise: with Out and In sharing an emitter, a swapped pair of
// columns answers every Out test correctly and every In question
// backwards.
func TestDirectionInWalksTheOtherWay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "inward", 3, [][2]int{{0, 1}, {1, 2}})

	got := nodeSet(run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[2]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.In, MinDepth: 1, MaxDepth: 2, MaxRows: 1000,
	}))
	for i, want := range map[int]bool{0: true, 1: true, 2: false} {
		if got[f.ids[i]] != want {
			t.Errorf("walking in from e2, e%d present = %v, want %v", i, got[f.ids[i]], want)
		}
	}
}

// TestASeedsOwnBindsAreRenumbered is the unit half of the renumbering
// Task 8 depends on: a seed written with its own $1 and $2 must not
// collide with the project id this package binds first. The behavioural
// half is every test above, each of which passes its project id twice --
// once for the walk and once inside the seed -- and would return the
// wrong anchor if the shift were off by one.
func TestASeedsOwnBindsAreRenumbered(t *testing.T) {
	t.Parallel()
	sql, args := graph.WalkCTE(graph.Walk{
		Name: "w", ProjectID: uuid.New(), SeedSQL: seedWalk,
		SeedArgs:        []any{uuid.New(), uuid.New()},
		RelationTypeIDs: []uuid.UUID{uuid.New()}, Direction: graph.Out, MaxDepth: 2,
	})
	if !strings.Contains(sql, "SELECT id FROM entities WHERE project_id = $2 AND id = $3") {
		t.Errorf("the seed's own binds were not shifted past the walk's own:\n%s", sql)
	}
	// One argument per placeholder: the project id, the seed's two, the
	// relation type list, the depth bound and the minimum depth.
	if len(args) != 6 {
		t.Errorf("the walk bound %d arguments, want 6: %v", len(args), args)
	}
}

// TestAnUnknownDirectionPanics covers the zero value too, which is the
// one a caller reaches by building a graph.Walk and forgetting the
// field.
func TestAnUnknownDirectionPanics(t *testing.T) {
	t.Parallel()
	for _, d := range []graph.Direction{"", "outgoing", "OUT", "both"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("a direction of %q must panic rather than be walked as %q", d, graph.Out)
				}
			}()
			graph.WalkCTE(graph.Walk{
				Name: "w", ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities", Direction: d,
			})
		}()
	}
	// The control: the three this package knows do not panic.
	for _, d := range []graph.Direction{graph.Out, graph.In, graph.Any} {
		graph.WalkCTE(graph.Walk{
			Name: "w", ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities", Direction: d,
		})
	}
}

func TestACTENameThatIsNotAnIdentifierPanics(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "w; DROP TABLE relations", "W", "1w", "a-b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("a CTE name of %q must panic: a name this package splices "+
						"into a statement is never a caller's text", name)
				}
			}()
			// Direction is set: without it this test would panic on the
			// direction instead and stay green with the name guard gone,
			// which is a test passing for the wrong reason.
			graph.WalkCTE(graph.Walk{
				Name: name, ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities", Direction: graph.Out,
			})
		}()
	}
}

// TestANegativeOrInvertedBoundPanics puts the bounds beside the two
// mistakes this package already refuses loudly. A negative row cap means
// no cap at all, a negative depth means the seed and nothing else, and a
// minimum above the maximum means nothing at all -- three empty or
// wrong answers with no error, which is this repository's recurring
// class and the stated reason the CTE name and the direction panic.
func TestANegativeOrInvertedBoundPanics(t *testing.T) {
	t.Parallel()
	for what, w := range map[string]graph.Walk{
		"a negative min depth":  {Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out, MinDepth: -1, MaxDepth: 3},
		"a negative max depth":  {Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out, MaxDepth: -1},
		"a min depth above max": {Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out, MinDepth: 3, MaxDepth: 2},
		"a negative row cap":    {Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out, MaxDepth: 3, MaxRows: -1},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s must panic rather than answer an empty or seed-only walk "+
						"with no error", what)
				}
			}()
			graph.WalkCTE(w)
		}()
	}
	// The control: the bounds a caller legitimately writes, including the
	// degenerate-but-meaningful "seed only" (min 0, max 0) and "no cap".
	for _, w := range []graph.Walk{
		{Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out},
		{Name: "w", SeedSQL: "SELECT id FROM entities", Direction: graph.Out, MinDepth: 2, MaxDepth: 2, MaxRows: 1},
	} {
		graph.WalkCTE(w)
	}
}
