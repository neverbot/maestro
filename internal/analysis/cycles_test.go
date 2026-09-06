package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/neverbot/maestro/internal/graph"
	"github.com/neverbot/maestro/internal/metamodel"
)

// cycles runs the report and fails the test on an error, which is what
// most of these want; the refusal tests call the service directly.
func (g game) cycles(t *testing.T, in CyclesInput) CyclesResult {
	t.Helper()
	got, err := g.analysis.Cycles(context.Background(), g.projectID, in)
	if err != nil {
		t.Fatalf("cycles: %v", err)
	}
	return got
}

// keys is the entity keys of one cycle, in the order the finding names
// them.
func (c Cycle) keys() []string {
	out := make([]string, 0, len(c.Entities))
	for _, node := range c.Entities {
		out = append(out, node.Key)
	}
	return out
}

// relationTypes is the relation type key of every edge of one cycle, in
// the order the finding names them.
func (c Cycle) relationTypes() []string {
	out := make([]string, 0, len(c.Edges))
	for _, edge := range c.Edges {
		out = append(out, edge.RelationType)
	}
	return out
}

// TestAnAcyclicGameReportsNoCyclesAndSaysHowManyEdgesItWalked is the
// negative half, and it is the load-bearing test of this file.
//
// **The assertion is not that the list is empty.** An empty findings
// list is what a walk that followed no edge at all returns too, and
// those two are the same JSON. So: empty *and* a non-zero
// `edges_walked` *and* a seed total that says the walk started
// somewhere.
//
// **Mutation:** filter the walk down to no relation types at all
// (`walk.RelationTypeIDs = nil` in cycleWalk). graph.Walk documents that
// an empty list follows no edge, so the findings stay empty, and this
// test goes red on edges_walked.
func TestAnAcyclicGameReportsNoCyclesAndSaysHowManyEdgesItWalked(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "unlocks", "quest", "a", "b")
	g.edge(t, "unlocks", "quest", "b", "c")
	g.edge(t, "unlocks", "quest", "c", "d")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 0 {
		t.Errorf("an acyclic game reported %d cycles: %v", len(got.Cycles), got.Cycles)
	}
	if got.EdgesWalked == 0 {
		t.Error("edges_walked = 0 over a game with three edges: an empty findings list " +
			"over a walk that followed nothing is the same JSON as an acyclic game, and " +
			"this count is the only thing that separates them")
	}
	if got.SeedTotal != 4 {
		t.Errorf("seed_total = %d, want the four entities the walk started from", got.SeedTotal)
	}
	if got.InvalidEdgesFollowed != 0 {
		t.Errorf("invalid_edges_followed = %d over a game with no flagged edge",
			got.InvalidEdgesFollowed)
	}
}

// TestASelfLoopIsReportedAsALengthOneCycle, including its single edge
// and its relation type key.
//
// A self-loop is the shape the earlier walk could not show at all -- it
// suppressed the closing row, so a self-loop came back with none of its
// edges -- which is why it is checked first.
func TestASelfLoopIsReportedAsALengthOneCycle(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "ouroboros")
	relation := g.edge(t, "unlocks", "quest", "ouroboros", "ouroboros")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("a self-loop produced %d findings, want one: %v", len(got.Cycles), got.Cycles)
	}
	cycle := got.Cycles[0]
	if cycle.Length != 1 || len(cycle.Entities) != 1 || cycle.Entities[0].Key != "ouroboros" {
		t.Errorf("the finding names %v at length %d, want the one entity", cycle.keys(), cycle.Length)
	}
	if len(cycle.Edges) != 1 || cycle.Edges[0].RelationID != relation {
		t.Errorf("the finding names edges %v, want the one relation %s", cycle.Edges, relation)
	}
	if cycle.Edges[0].RelationType != "unlocks" {
		t.Errorf("the edge names relation type %q, want unlocks", cycle.Edges[0].RelationType)
	}
}

// TestATwoCycleIsReportedOnceAndNotTwice is canonicalisation's core
// case: the loop is discovered from both of its ends and is one finding.
func TestATwoCycleIsReportedOnceAndNotTwice(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "chicken")
	g.entity(t, "quest", "egg")
	g.edge(t, "unlocks", "quest", "chicken", "egg")
	g.edge(t, "unlocks", "quest", "egg", "chicken")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("a two-cycle produced %d findings, want one: %v", len(got.Cycles), got.Cycles)
	}
	if got.Cycles[0].Length != 2 {
		t.Errorf("length = %d, want 2 over %v", got.Cycles[0].Length, got.Cycles[0].keys())
	}
}

// TestAFourCycleIsReportedOnceRatherThanFourTimes is the case rotation
// exists for: four starting points, four discoveries, one finding.
func TestAFourCycleIsReportedOnceRatherThanFourTimes(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	keys := []string{"n1", "n2", "n3", "n4"}
	for _, key := range keys {
		g.entity(t, "quest", key)
	}
	for i, key := range keys {
		g.edge(t, "unlocks", "quest", key, keys[(i+1)%len(keys)])
	}

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("a four-cycle produced %d findings, want one: %v", len(got.Cycles), got.Cycles)
	}
	if len(got.Cycles[0].Entities) != 4 {
		t.Errorf("the finding names %v, want all four members once each", got.Cycles[0].keys())
	}
}

// TestACycleAndItsEdgesRotateTogether is the rotation assertion, and it
// reads the edges **out of the database** rather than out of the walk's
// own row, so a canonicalisation that moved the nodes and left the edges
// where they were is red.
//
// The invariant: edge i joins entity i to entity (i+1) mod n.
//
// **Mutation:** rotate only the node slice in `rotate` (leave `edges`
// as it was). This test goes red on the four-cycle -- edge 0 then joins
// the wrong pair -- and TestASelfLoopIsReportedAsALengthOneCycle stays
// green, which is the discrimination that proves this tests rotation
// rather than existence.
func TestACycleAndItsEdgesRotateTogether(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	keys := []string{"w", "x", "y", "z"}
	for _, key := range keys {
		g.entity(t, "quest", key)
	}
	for i, key := range keys {
		g.edge(t, "unlocks", "quest", key, keys[(i+1)%len(keys)])
	}

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("want one finding, got %d", len(got.Cycles))
	}
	cycle := got.Cycles[0]
	if len(cycle.Edges) != len(cycle.Entities) {
		t.Fatalf("%d edges for %d entities: a cycle has one edge per node",
			len(cycle.Edges), len(cycle.Entities))
	}
	for i, edge := range cycle.Edges {
		source, target := g.relationEnds(t, edge.RelationID)
		wantSource := cycle.Entities[i].Key
		wantTarget := cycle.Entities[(i+1)%len(cycle.Entities)].Key
		if source != wantSource || target != wantTarget {
			t.Errorf("edge %d is %s -> %s in the database, and the finding places it "+
				"between %s and %s: the nodes rotated and the edges did not",
				i, source, target, wantSource, wantTarget)
		}
	}
}

// relationEnds reads one relation's endpoints back as entity keys, which
// is what makes the rotation assertion an assertion about the database
// rather than about the walk's own row.
func (g game) relationEnds(t *testing.T, relationID uuid.UUID) (source, target string) {
	t.Helper()
	if err := g.pool.QueryRow(context.Background(),
		`SELECT s.key, tg.key FROM relations r
		 JOIN entities s ON s.id = r.source_id
		 JOIN entities tg ON tg.id = r.target_id
		 WHERE r.project_id = $1 AND r.id = $2`,
		g.projectID, relationID).Scan(&source, &target); err != nil {
		t.Fatalf("read relation %s back: %v", relationID, err)
	}
	return source, target
}

// TestTheCycleReportReadsTheWalksClosingHopRatherThanReconstructingIt is
// the reliance, asserted.
//
// This file's whole premise is that graph.WalkCTE returns the
// cycle-closing hop exactly once, carrying the relation ids along the
// path -- a correction made to that package precisely because the
// earlier shape made the one thing this engine exists to find the one
// thing the walk could not show. The fixture is the case where
// reconstructing edges from consecutive node pairs is ambiguous: **two
// gating relation types between the same pair of entities**. The
// finding's edge ids must be the ones the walk's own rel_path carried,
// not one of the two edges that happen to join the pair.
func TestTheCycleReportReadsTheWalksClosingHopRatherThanReconstructingIt(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.declareRelationType(t, "also_unlocks", "", []string{"unlocks"})
	g.entity(t, "quest", "alpha")
	g.entity(t, "quest", "beta")
	// Two distinct gating types between one pair, in both directions:
	// four edges, and a two-cycle that can be closed four ways.
	g.edge(t, "unlocks", "quest", "alpha", "beta")
	g.edge(t, "also_unlocks", "quest", "alpha", "beta")
	g.edge(t, "unlocks", "quest", "beta", "alpha")
	g.edge(t, "also_unlocks", "quest", "beta", "alpha")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) == 0 {
		t.Fatal("no cycle found over four edges joining two entities in both directions")
	}
	// Every reported edge must be a real relation of this game, and the
	// pairs the report names must be the pairs the walk walked.
	reported := map[uuid.UUID]bool{}
	for _, cycle := range got.Cycles {
		for _, edge := range cycle.Edges {
			reported[edge.RelationID] = true
		}
	}

	// The walk, run directly, with the same shape cycleWalk builds.
	sem := g.semantics(t)
	gating, _ := cycleSemantics(sem)
	b := &binder{}
	seed := "SELECT e.id FROM entities e WHERE e.project_id = " + b.bind(g.projectID)
	p := Params{ProjectID: g.projectID, Semantics: gating}
	walk := p.normalisedWalk("probe", seed, b.args)
	walk.CarryRelationPath = true
	body, args := graph.WalkCTE(walk)
	statement := "WITH RECURSIVE " + body + `
SELECT w.path, w.rel_path, w.closed FROM ` + graph.ReadFrom(walk) + ` w WHERE w.closed`

	closing := map[uuid.UUID]bool{}
	rows := 0
	err := g.analysis.runInTx(context.Background(), time.Second, statement, args,
		func(r pgx.Rows) error {
			for r.Next() {
				var path, relPath []uuid.UUID
				var closed bool
				if err := r.Scan(&path, &relPath, &closed); err != nil {
					return err
				}
				rows++
				found, ok := cycleFromClosedRow(path, relPath)
				if !ok {
					continue
				}
				for _, id := range found.edges {
					closing[id] = true
				}
			}
			return r.Err()
		})
	if err != nil {
		t.Fatalf("run the walk directly: %v", err)
	}
	if rows == 0 {
		t.Fatal("graph.WalkCTE returned no closed row at all: this file reads closing " +
			"hops, so a walk that suppresses them makes every cycle report a false negative")
	}
	for id := range reported {
		if !closing[id] {
			t.Errorf("the report names relation %s, which no closing hop of the walk "+
				"carried: the edges of a finding come from rel_path and are never "+
				"reconstructed from consecutive node pairs", id)
		}
	}
}

// TestACycleThatDoesNotPassThroughTheSeedIsFound separates a guard
// computed against the whole path from one computed against the seed
// alone. internal/graph pins that property for itself; this package's
// own seeding could still lose it, and every other fixture's cycle
// passes through its seed.
func TestACycleThatDoesNotPassThroughTheSeedIsFound(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.declareEntityType(t, "prologue")
	for _, key := range []string{"loop-a", "loop-b", "loop-c"} {
		g.entity(t, "quest", key)
	}
	g.entity(t, "prologue", "start")
	g.edge(t, "unlocks", "quest", "loop-a", "loop-b")
	g.edge(t, "unlocks", "quest", "loop-b", "loop-c")
	g.edge(t, "unlocks", "quest", "loop-c", "loop-a")
	// The only seed is outside the loop, and it leads into it.
	if _, err := g.meta.UpsertRelation(context.Background(), g.projectID,
		metamodel.RelationInput{
			TypeKey: "unlocks",
			Source:  metamodel.Ref{TypeKey: "prologue", Key: "start"},
			Target:  metamodel.Ref{TypeKey: "quest", Key: "loop-a"},
		}); err != nil {
		t.Fatalf("write the entry edge: %v", err)
	}

	got := g.cycles(t, CyclesInput{EntityTypes: []string{"prologue"}})
	if len(got.Cycles) != 1 {
		t.Fatalf("seeded only outside the loop, the report names %d cycles, want the one: %v",
			len(got.Cycles), got.Cycles)
	}
	if got.Cycles[0].Length != 3 {
		t.Errorf("length = %d, want the three-node loop: %v",
			got.Cycles[0].Length, got.Cycles[0].keys())
	}
}

// TestOrderingTypesAreCheckedForCyclesToo -- `ordering` implies
// `acyclic`, so a `follows` loop is a finding.
func TestOrderingTypesAreCheckedForCyclesToo(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "chapter")
	g.declareRelationType(t, "follows", "", []string{"ordering"})
	for _, key := range []string{"one", "two"} {
		g.entity(t, "chapter", key)
	}
	g.edge(t, "follows", "chapter", "one", "two")
	g.edge(t, "follows", "chapter", "two", "one")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("an ordering loop produced %d findings, want one", len(got.Cycles))
	}
	if got.Cycles[0].relationTypes()[0] != "follows" {
		t.Errorf("the edges name %v, want follows", got.Cycles[0].relationTypes())
	}
}

// TestAnAcyclicOnlyTypeWithNoGatingIsStillChecked is the `acyclic`
// trait's whole reason for existing: a type that gates nothing, orders
// nothing and contains nothing, and still must not loop. It is the entry
// most likely to be quietly dropped from the checked set, because every
// other trait in it has a second job.
func TestAnAcyclicOnlyTypeWithNoGatingIsStillChecked(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "item")
	g.declareRelationType(t, "variant_of", "", []string{"acyclic"})
	for _, key := range []string{"sword", "sword-of-fire", "sword-of-ice"} {
		g.entity(t, "item", key)
	}
	g.edge(t, "variant_of", "item", "sword", "sword-of-fire")
	g.edge(t, "variant_of", "item", "sword-of-fire", "sword-of-ice")
	g.edge(t, "variant_of", "item", "sword-of-ice", "sword")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("an acyclic-only type looping produced %d findings, want one: %v",
			len(got.Cycles), got.Cycles)
	}
	if got.Cycles[0].Length != 3 {
		t.Errorf("length = %d, want three", got.Cycles[0].Length)
	}
}

// TestASymmetricTypeProducesNoCycleFindings, with a control in the same
// test: a `requires` loop over the same three entities **is** a finding,
// so a run that found nothing at all cannot pass this.
func TestASymmetricTypeProducesNoCycleFindings(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	for _, key := range []string{"barrens", "durotar", "orgrimmar"} {
		g.entity(t, "quest", key)
	}
	g.edge(t, "connects_to", "quest", "barrens", "durotar")
	g.edge(t, "connects_to", "quest", "durotar", "orgrimmar")
	g.edge(t, "connects_to", "quest", "orgrimmar", "barrens")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 0 {
		t.Errorf("a ring of symmetric edges reported %d cycles: an adjacency is a map, "+
			"not a loop, and every connected game would fill this report", len(got.Cycles))
	}

	// The control: the same three entities, joined by a gating type.
	g.edge(t, "requires", "quest", "barrens", "durotar")
	g.edge(t, "requires", "quest", "durotar", "orgrimmar")
	g.edge(t, "requires", "quest", "orgrimmar", "barrens")
	withGate := g.cycles(t, CyclesInput{})
	if len(withGate.Cycles) != 1 {
		t.Fatalf("the control found %d cycles over a requires loop, want one: a run that "+
			"walks nothing at all passes the first half of this test", len(withGate.Cycles))
	}
	if withGate.Cycles[0].relationTypes()[0] != "requires" {
		t.Errorf("the control's edges name %v, want requires",
			withGate.Cycles[0].relationTypes())
	}
}

// TestAContainmentLoopIsReportedSeparatelyFromAPrerequisiteLoop builds
// one of each and asserts they land in different lists with different
// type keys. A hierarchy that is not one and a gate nobody can open are
// two reports with two fixes.
func TestAContainmentLoopIsReportedSeparatelyFromAPrerequisiteLoop(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.declareEntityType(t, "zone")
	g.declareRelationType(t, "contains", "", []string{"containment"})
	for _, key := range []string{"elwynn", "goldshire"} {
		g.entity(t, "zone", key)
	}
	g.edge(t, "contains", "zone", "elwynn", "goldshire")
	g.edge(t, "contains", "zone", "goldshire", "elwynn")
	g.entity(t, "quest", "a")
	g.entity(t, "quest", "b")
	g.edge(t, "requires", "quest", "a", "b")
	g.edge(t, "requires", "quest", "b", "a")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 || len(got.ContainmentCycles) != 1 {
		t.Fatalf("prerequisite cycles = %v, containment cycles = %v; want one of each in "+
			"its own list", got.Cycles, got.ContainmentCycles)
	}
	if got.Cycles[0].relationTypes()[0] != "requires" {
		t.Errorf("the prerequisite finding names %v", got.Cycles[0].relationTypes())
	}
	if got.ContainmentCycles[0].relationTypes()[0] != "contains" {
		t.Errorf("the containment finding names %v", got.ContainmentCycles[0].relationTypes())
	}
}

// TestAMutuallyExclusivePairIsReportedWithItsRelationTypeSoItCanBeReclassified
// is the false positive this report is designed to survive.
//
// "Choosing the Horde locks the Alliance" is two `requires_not` edges: a
// legitimate two-cycle in a type that reads like a prerequisite, whose
// fix is a trait declaration and not a change to the content. The
// finding therefore has to name `requires_not` on both edges, or the
// designer has no way to tell this loop from a real one.
func TestAMutuallyExclusivePairIsReportedWithItsRelationTypeSoItCanBeReclassified(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "faction")
	g.declareRelationType(t, "requires_not", "", []string{"prerequisite_of"})
	g.entity(t, "faction", "horde")
	g.entity(t, "faction", "alliance")
	g.edge(t, "requires_not", "faction", "horde", "alliance")
	g.edge(t, "requires_not", "faction", "alliance", "horde")

	got := g.cycles(t, CyclesInput{})
	if len(got.Cycles) != 1 {
		t.Fatalf("want the one two-cycle, got %d: %v", len(got.Cycles), got.Cycles)
	}
	types := got.Cycles[0].relationTypes()
	if len(types) != 2 || types[0] != "requires_not" || types[1] != "requires_not" {
		t.Errorf("the finding names %v, want requires_not on both edges: without the "+
			"type a designer cannot tell mutual exclusion from a prerequisite loop", types)
	}
}

// TestATruncatedCycleReportKeepsTheShortestCycles.
//
// Findings are ordered shortest first because graph.WalkCTE truncates
// with ORDER BY depth: a truncated answer is a connected prefix of the
// nearest hops, so the shortest cycles are the ones a truncated report
// can actually name -- and they are the most fixable.
func TestATruncatedCycleReportKeepsTheShortestCycles(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "short-a")
	g.entity(t, "quest", "short-b")
	g.edge(t, "unlocks", "quest", "short-a", "short-b")
	g.edge(t, "unlocks", "quest", "short-b", "short-a")
	for ring := range 3 {
		var keys []string
		for i := range 8 {
			key := string(rune('p'+ring)) + string(rune('0'+i))
			g.entity(t, "quest", key)
			keys = append(keys, key)
		}
		for i, key := range keys {
			g.edge(t, "unlocks", "quest", key, keys[(i+1)%len(keys)])
		}
	}

	full := g.cycles(t, CyclesInput{})
	if len(full.Cycles) != 4 {
		t.Fatalf("the fixture holds one 2-cycle and three 8-cycles; the full report names "+
			"%d: %v", len(full.Cycles), full.Cycles)
	}
	got := g.cycles(t, CyclesInput{MaxResults: 1})
	if len(got.Cycles) != 1 {
		t.Fatalf("max_results 1 returned %d findings", len(got.Cycles))
	}
	if got.Cycles[0].Length != 2 {
		t.Errorf("the truncated report kept a cycle of length %d, want the 2-cycle: a "+
			"truncated answer that keeps arbitrary loops is less useful than one that "+
			"keeps the fixable ones", got.Cycles[0].Length)
	}
	if !got.Truncated {
		t.Error("truncated is false on a report that dropped three findings: a capped " +
			"report that does not say so reads as the whole list of what is wrong")
	}
	if full.Truncated {
		t.Error("truncated is true on a report that named every cycle it found")
	}
}

// TestCyclesOverAGameWithNoTraitsRefusesRatherThanReportingHealth.
//
// A clean bill of health from an engine that had nothing to read is the
// worst output this package can produce, and cycles is the analysis
// where believing it costs the most.
func TestCyclesOverAGameWithNoTraitsRefusesRatherThanReportingHealth(t *testing.T) {
	t.Parallel()
	g := newGame(t)
	g.declareEntityType(t, "quest")
	g.declareRelationType(t, "mentions", "", nil)
	_, err := g.analysis.Cycles(context.Background(), g.projectID, CyclesInput{})
	if err == nil {
		t.Fatal("a game that declares nothing was reported acyclic")
	}
	if !isSemanticsUndeclared(err) {
		t.Fatalf("err = %v, want semantics_undeclared", err)
	}
}

// isSemanticsUndeclared is the refusal this package answers an
// undeclared game with, asserted by sentinel rather than by message.
func isSemanticsUndeclared(err error) bool {
	return err != nil && strings.Contains(err.Error(), CodeSemanticsUndeclared)
}

// TestCyclesOverAnotherGamesTokenIsScopeViolation -- the isolation half.
//
// The two games live in **one** database, which is what makes this an
// assertion: testutil.NewPool builds a throwaway database per call, so
// two separately built fixtures could never leak into each other.
func TestCyclesOverAnotherGamesTokenIsScopeViolation(t *testing.T) {
	t.Parallel()
	mine := gatedGame(t)
	mine.entity(t, "quest", "loop-a")
	mine.entity(t, "quest", "loop-b")
	mine.edge(t, "unlocks", "quest", "loop-a", "loop-b")
	mine.edge(t, "unlocks", "quest", "loop-b", "loop-a")

	theirs := mine.sibling(t)
	theirs.declareEntityType(t, "quest")
	theirs.declareRelationType(t, "unlocks", "", []string{"unlocks"})
	theirs.entity(t, "quest", "peaceful")

	got := theirs.cycles(t, CyclesInput{})
	if len(got.Cycles) != 0 {
		t.Fatalf("the second game's report names %v, which belong to the first",
			got.Cycles)
	}
	// The control: the first game still reports its own loop, so a run
	// that walked nothing at all cannot pass the assertion above.
	if len(mine.cycles(t, CyclesInput{}).Cycles) != 1 {
		t.Fatal("the first game stopped reporting its own cycle, so the isolation " +
			"assertion above proves nothing")
	}
}

// TestATimedOutAnalysisIsRetryableAndSaysWhichBoundToLower.
//
// **No `analysis_timeout` code**, which errors.go argues at length and
// TestNoTimeoutCodeShips pins from the vocabulary side. This is the
// behavioural half: a run that exhausts its budget answers `retryable`
// -- SQLSTATE 57014 is already in metamodel's retryableSQLStates -- with
// a message naming the budget it spent and the four arguments that
// narrow a run.
//
// The budget is set through the package-private knob, which is settable
// from nowhere but this package's own tests.
func TestATimedOutAnalysisIsRetryableAndSaysWhichBoundToLower(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	keys := make([]string, 0, 60)
	for i := range 60 {
		key := "q" + string(rune('a'+i/10)) + string(rune('0'+i%10))
		g.entity(t, "quest", key)
		keys = append(keys, key)
	}
	// A dense ring plus chords, so a walk to the default depth over it has
	// real work to do inside a budget of one millisecond. One millisecond
	// and not less: Postgres takes the setting in milliseconds and reads a
	// zero as *no timeout at all*, which runInTx refuses outright rather
	// than running unbounded.
	for i, key := range keys {
		g.edge(t, "unlocks", "quest", key, keys[(i+1)%len(keys)])
		g.edge(t, "unlocks", "quest", key, keys[(i+7)%len(keys)])
		g.edge(t, "unlocks", "quest", key, keys[(i+23)%len(keys)])
	}
	g.analysis.statementTimeout = time.Millisecond

	_, err := g.analysis.Cycles(context.Background(), g.projectID, CyclesInput{})
	if err == nil {
		t.Fatal("a walk under a one-millisecond budget completed, so this test measures " +
			"nothing")
	}
	if !metamodel.IsRetryable(err) {
		t.Fatalf("err = %v; a timed-out analysis is retryable -- change nothing and "+
			"resend -- and a wrap that loses the PgError loses that answer", err)
	}
	message := err.Error()
	if !strings.Contains(message, "1ms") {
		t.Errorf("the message does not name the budget it exhausted: %s", message)
	}
	for _, bound := range []string{
		"max_depth", "entity_types", "relation_types", "seed_entity_types",
	} {
		if !strings.Contains(message, bound) {
			t.Errorf("the message does not name %q, which is one of the four arguments "+
				"that narrow a run: %s", bound, message)
		}
	}
}

// TestACycleReportRefusesADepthAboveItsCapRatherThanClamping, and the
// same for max_results. A run computed under a bound the caller did not
// ask for reads exactly like a complete one.
func TestACycleReportRefusesABoundAboveItsCapRatherThanClamping(t *testing.T) {
	t.Parallel()
	g := gatedGame(t)
	g.entity(t, "quest", "solo")
	for _, probe := range []struct {
		path string
		in   CyclesInput
	}{
		{"max_depth", CyclesInput{MaxDepth: MaxMaxDepth + 1}},
		{"max_results", CyclesInput{MaxResults: MaxMaxResults + 1}},
	} {
		_, err := g.analysis.Cycles(context.Background(), g.projectID, probe.in)
		var invalid *metamodel.ValidationError
		if !errors.As(err, &invalid) || invalid.Code != CodeLimitExceeded {
			t.Fatalf("%s above its cap answered %v, want limit_exceeded", probe.path, err)
		}
		if len(invalid.Fields) != 1 || invalid.Fields[0].Path != probe.path {
			t.Errorf("the refusal names %v, want the caller's own argument %q",
				invalid.Fields, probe.path)
		}
	}
}
