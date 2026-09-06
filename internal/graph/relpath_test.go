package graph_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/neverbot/maestro/internal/graph"
	"github.com/neverbot/maestro/internal/testutil"
)

// carried is one row of a walk that asked for CarryRelationPath. It is a
// separate scan from `reached` on purpose: the column is opt-in, so a
// helper that always scanned it would make every other test in this
// package depend on the field this one exists to keep optional.
type carried struct {
	id      uuid.UUID
	depth   int
	relPath []uuid.UUID
	closed  bool
}

func runCarrying(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w graph.Walk) []carried {
	t.Helper()
	w.CarryRelationPath = true
	body, args := graph.WalkCTE(w)
	stmt := "WITH RECURSIVE " + body +
		"\nSELECT id, depth, rel_path, closed FROM " + graph.ReadFrom(w)
	rows, err := pool.Query(ctx, stmt, args...)
	if err != nil {
		t.Fatalf("the walk must run: %v\n%s", err, stmt)
	}
	defer rows.Close()
	var out []carried
	for rows.Next() {
		var c carried
		if err := rows.Scan(&c.id, &c.depth, &c.relPath, &c.closed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestARelationPathNamesEveryEdgeOfATwoCycleAndNotOnlyTheClosingOne is
// the field's reason for existing, in its smallest form: the closed row
// already carries the *closing* edge in via_relation, and a caller
// reporting a cycle has to name every edge in it.
func TestARelationPathNamesEveryEdgeOfATwoCycleAndNotOnlyTheClosingOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 0}}
	f := seedGraph(t, ctx, pool, "relpath-two-cycle", 2, edges)
	rel := relationIDs(t, ctx, pool, f, edges)

	rows := runCarrying(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID, SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID}, Direction: graph.Out, MaxDepth: 5,
	})

	var closed *carried
	for i := range rows {
		if rows[i].closed {
			closed = &rows[i]
		}
	}
	if closed == nil {
		t.Fatalf("the walk returned no closed row at all, so nothing below is being tested: %+v", rows)
	}
	if closed.id != f.ids[0] {
		t.Errorf("the closed row is %v, want the seed %v", closed.id, f.ids[0])
	}
	if len(closed.relPath) != 2 {
		t.Fatalf("the closed row's rel_path has %d ids, want both edges of the two-cycle: %v",
			len(closed.relPath), closed.relPath)
	}
	if closed.relPath[0] != rel[0] || closed.relPath[1] != rel[1] {
		t.Errorf("rel_path = %v, want the outbound edge then the closing one %v", closed.relPath, rel)
	}
}

// TestARelationPathDistinguishesTwoEdgesBetweenTheSamePair is what
// justifies the column over reconstructing edges in Go from consecutive
// node pairs. Two relation types join a to b, both are followed, and both
// closed rows walk the identical node pair -- so a Go-side reconstruction
// from (a, b) would have to guess which edge each cycle used, and the two
// cycles are two different findings with two different fixes.
func TestARelationPathDistinguishesTwoEdgesBetweenTheSamePair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}, {1, 0}}
	f := seedGraph(t, ctx, pool, "relpath-parallel-types", 2, edges)
	first := relationIDs(t, ctx, pool, f, edges)

	// A second gating type between the same pair, pointing the same way
	// as the first pair does, so the walk has two distinct two-cycles
	// over one node pair.
	var second uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO relation_types (project_id, key, label)
		 VALUES ($1, 'unlocks', 'Unlocks') RETURNING id`, f.projectID).Scan(&second); err != nil {
		t.Fatalf("insert second relation type: %v", err)
	}
	var ab, ba uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		f.projectID, second, f.ids[0], f.ids[1]).Scan(&ab); err != nil {
		t.Fatalf("insert a->b of the second type: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO relations (project_id, relation_type_id, source_id, target_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		f.projectID, second, f.ids[1], f.ids[0]).Scan(&ba); err != nil {
		t.Fatalf("insert b->a of the second type: %v", err)
	}

	rows := runCarrying(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID, SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID, second}, Direction: graph.Out, MaxDepth: 5,
	})

	paths := map[string]bool{}
	for _, r := range rows {
		if !r.closed {
			continue
		}
		if len(r.relPath) != 2 {
			t.Errorf("a closed row's rel_path has %d ids, want 2: %v", len(r.relPath), r.relPath)
			continue
		}
		paths[r.relPath[0].String()+" "+r.relPath[1].String()] = true
	}
	// Four cycles over the one node pair: each of the two a->b edges
	// closed by each of the two b->a edges.
	want := []string{
		first[0].String() + " " + first[1].String(),
		first[0].String() + " " + ba.String(),
		ab.String() + " " + first[1].String(),
		ab.String() + " " + ba.String(),
	}
	if len(paths) != len(want) {
		t.Fatalf("the walk returned %d distinct closed rel_paths, want %d: %v", len(paths), len(want), paths)
	}
	for _, w := range want {
		if !paths[w] {
			t.Errorf("no closed row carries the rel_path %q; got %v", w, paths)
		}
	}
}

// TestTheRelationPathIsEmptyAtTheSeedAndNotNull pins the seed's own
// value. `||` against NULL is NULL in Postgres, so a NULL seed would make
// every path below it NULL -- an empty answer with no error, which is
// this repository's recurring defect.
func TestTheRelationPathIsEmptyAtTheSeedAndNotNull(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testutil.NewPool(t)
	edges := [][2]int{{0, 1}}
	f := seedGraph(t, ctx, pool, "relpath-seed", 2, edges)

	rows := runCarrying(t, ctx, pool, graph.Walk{
		Name: "w", ProjectID: f.projectID, SeedSQL: seedWalk, SeedArgs: []any{f.projectID, f.ids[0]},
		RelationTypeIDs: []uuid.UUID{f.relTypeID}, Direction: graph.Out, MaxDepth: 3,
	})
	if len(rows) != 2 {
		t.Fatalf("the walk returned %d rows, want the seed and its one hop: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.depth == 0 {
			if r.relPath == nil {
				t.Errorf("the seed's rel_path is NULL, which makes every path below it NULL")
			}
			if len(r.relPath) != 0 {
				t.Errorf("the seed's rel_path is %v, want a zero-length array", r.relPath)
			}
			continue
		}
		// The positive control: the hop below the seed did accumulate,
		// so an all-empty column cannot pass this test.
		if len(r.relPath) != 1 {
			t.Errorf("the depth-1 row's rel_path is %v, want exactly the edge it walked", r.relPath)
		}
	}
}
