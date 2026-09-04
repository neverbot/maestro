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
	id    uuid.UUID
	depth int
	via   *uuid.UUID
	from  *uuid.UUID
}

func run(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w graph.Walk) []reached {
	t.Helper()
	body, args := graph.WalkCTE(w)
	stmt := "WITH RECURSIVE " + body + "\nSELECT id, depth, via_relation, from_id FROM " + graph.ReadFrom(w)
	rows, err := pool.Query(ctx, stmt, args...)
	if err != nil {
		t.Fatalf("the walk must run: %v\n%s", err, stmt)
	}
	defer rows.Close()
	var out []reached
	for rows.Next() {
		var r reached
		if err := rows.Scan(&r.id, &r.depth, &r.via, &r.from); err != nil {
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
}

func TestAWalkTerminatesOnACycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "cycle", 3, [][2]int{{0, 1}, {1, 2}, {2, 0}}) // a -> b -> c -> a

	got := nodeSet(run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Out, MinDepth: 0, MaxDepth: 10, MaxRows: 1000,
	}))

	// Termination alone would also be satisfied by an empty answer, which
	// is why the node set is asserted: a cycle is legal content and the
	// walk must return all of it, once. Min depth is 0 here because the
	// third node closes the cycle onto the seed, and the guard is what
	// stops the seed being handed back a second time -- so a walk from a
	// that dropped depth 0 would answer b and c, and this test would then
	// be asserting the cycle was cut rather than walked.
	for name, id := range map[string]uuid.UUID{"a": f.ids[0], "b": f.ids[1], "c": f.ids[2]} {
		if !got[id] {
			t.Errorf("the walk must reach %s: a cycle is legal content, not a reason to stop early", name)
		}
	}
	if len(got) != 3 {
		t.Fatalf("the walk visited %d nodes and the graph has 3", len(got))
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
// is what the second half asserts on the relation ids.
func TestDirectionAnyTraversesEachEdgeOnceFromEachNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "symmetric", 2, [][2]int{{0, 1}, {1, 0}}) // a -> b and b -> a

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Any, MinDepth: 1, MaxDepth: 4, MaxRows: 1000,
	})

	if len(rows) != 2 {
		t.Fatalf("a symmetric pair must be reached once per edge, got %d rows: %+v", len(rows), rows)
	}
	if got := nodeSet(rows); len(got) != 1 || !got[f.ids[1]] {
		t.Fatalf("both rows must be the far node of the pair, got %v", got)
	}
	seen := map[uuid.UUID]int{}
	for _, r := range rows {
		if r.via == nil {
			t.Fatalf("a reached row must name the relation it walked: %+v", r)
		}
		if r.depth != 1 {
			t.Errorf("the far node of a two-node pair is one hop away, got depth %d", r.depth)
		}
		seen[*r.via]++
	}
	if len(seen) != 2 {
		t.Fatalf("the two edges must be traversed once each, got %v: the walk is "+
			"counting one edge under both arms", seen)
	}
}

// TestASelfLoopIsNotTraversed pins what the visited-path guard does to
// the one shape that can match an edge from both ends at once.
//
// It does not pin the arm count, and no test can: with the guard in
// place a two-armed emitter returns exactly these rows too, because the
// second copy of a self-loop is excluded by the same path test as the
// first. The doubling is real and was measured -- 2, 4 and 8 rows at
// depths 1, 2 and 3, from one self-loop, with the guard removed -- which
// is why the emitter is one-armed; it is simply not observable while the
// guard holds, and the commit message carries the measurement rather
// than a test pretending to.
func TestASelfLoopIsNotTraversed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	f := seedGraph(t, ctx, pool, "selfloop", 2, [][2]int{{0, 0}, {0, 1}})

	rows := run(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID,
		SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID},
		Direction:       graph.Any, MinDepth: 1, MaxDepth: 3, MaxRows: 1000,
	})

	// The positive control: an empty answer would satisfy "the loop was
	// not walked" without the walk having worked at all.
	if len(rows) != 1 || rows[0].id != f.ids[1] {
		t.Fatalf("the walk must reach the other end of the ordinary edge and nothing else, got %+v", rows)
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

func TestMaxRowsCapsWhatComesBack(t *testing.T) {
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
	if n := len(run(t, ctx, pool, walk)); n != 2 {
		t.Errorf("max_rows 2 returned %d rows", n)
	}
	// The control: uncapped, the same walk returns the anchor and its
	// four neighbours, so the cap above truncated something real.
	walk.MaxRows = 0
	if n := len(run(t, ctx, pool, walk)); n != 5 {
		t.Errorf("uncapped, the walk returned %d rows, want 5", n)
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
			graph.WalkCTE(graph.Walk{Name: name, ProjectID: uuid.New(), SeedSQL: "SELECT id FROM entities"})
		}()
	}
}
