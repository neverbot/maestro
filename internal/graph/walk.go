// Package graph emits the one bounded traversal Maestro walks a game's
// relations with: project-filtered in both terms of the recursion,
// depth-bounded, and guarded against *expanding* a node twice on the
// path it arrived by -- while still handing the caller the edge that
// closes a cycle.
//
// It exists in neither of its callers on purpose. The views sub-project's
// query language compiles traversal steps into it; the analysis engine
// will compile its reachability closure into it. Both specs asked for
// "one walk, two callers, no drift", neither said who owned it, and
// deciding after both were written is how the drift happens. The
// precedent is internal/paging, extracted for exactly this reason: a copy
// of a cursor is a copy of its bugs, and a copy of a walk is a copy of a
// missing project filter.
//
// **As of this commit there is no caller at all.** internal/views reaches
// it in Task 8 of the views plan and internal/analysis does not exist
// yet. That is stated rather than implied, because a package comment
// claiming callers it cannot point at is the "documentation claiming more
// than the code does" defect this repository has produced nineteen times.
//
// What lives here is the SQL primitive. Policy -- which relation types
// gate what, whether a container propagates reachability, what a step's
// result means -- belongs to the caller, and this package has no opinion
// about any of it.
package graph

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Direction is which way an edge is followed, relative to the node the
// walk is standing on.
type Direction string

const (
	// Out follows an edge from its source to its target.
	Out Direction = "out"
	// In follows an edge from its target to its source.
	In Direction = "in"
	// Any follows an edge from either end to the other. It is one arm
	// over the union of both matches, never two arms -- see WalkCTE.
	Any Direction = "any"
)

// Walk describes one bounded traversal.
//
// SeedSQL is a SELECT returning one uuid column, spliced in as the
// recursion's anchor. **It is the caller's own SQL and never a caller's
// text**: internal/views builds it from a compiled selector whose every
// value is already a bind parameter. This package cannot check that, so
// it is stated as the contract and nothing here tests it; the views side
// is where it can be, and must be, pinned.
//
// The seed is **not** required to filter on the project: the anchor join
// this package emits does that itself, and
// TestTheAnchorFiltersOnTheProjectEvenWhenTheSeedDoesNot pins it with a
// deliberately project-blind seed.
type Walk struct {
	// Name is the CTE's name. It must be a Go-side constant or a
	// generated identifier such as "s3" -- never anything derived from a
	// caller's document. WalkCTE panics on a name that is not an
	// identifier, so a future caller that gets this wrong fails loudly at
	// its first test rather than quietly at somebody's database.
	// TestACTENameThatIsNotAnIdentifierPanics pins the panic.
	Name string

	ProjectID uuid.UUID
	SeedSQL   string
	SeedArgs  []any

	// RelationTypeIDs is the set of relation types an edge may carry to
	// be followed. It is the one thing this walk trusts another table
	// for: the emitted statement compares relations.relation_type_id
	// against the list and **does not join relation_types**, so there is
	// no position at which a relation type belonging to another game
	// could be filtered out. What makes that safe is 0004_metamodel.sql's
	// composite foreign key relations_relation_type_id_project_id_fkey,
	// which puts an edge and its type in the same game by construction --
	// verified against the shipped migration, and the reason
	// TestAWalkCannotLeaveItsProjectThroughARogueEdge has to drop that
	// constraint in a throwaway database before it can forge its rows. A
	// caller passing ids it resolved in another game gets an empty walk,
	// not another game's edges.
	//
	// An empty list follows no edge at all -- not every edge, which is
	// what dropping the clause would mean.
	// TestAWalkWithNoRelationTypesReachesOnlyItsSeed pins the difference.
	RelationTypeIDs []uuid.UUID

	Direction Direction

	// MinDepth and MaxDepth bound the hops. Depth 0 is the seed itself,
	// which the recursion always carries because the guard needs it on
	// the path; MinDepth is applied *after* the recursion has finished,
	// in the CTE ReadFrom names, so a walk with min 2 still walks through
	// depth 1 and simply does not hand it back.
	//
	// MinDepth is honoured here rather than left to the caller on
	// purpose: it is a bound, bounds are this package's subject, and a
	// bound each caller applies for itself is the drift this package was
	// extracted to prevent. TestMinDepthDropsTheNearHopsAfterWalkingThem
	// pins both halves.
	//
	// Both are non-negative and MinDepth may not exceed MaxDepth; WalkCTE
	// panics otherwise. A negative MaxDepth is a walk that returns its
	// seed and nothing else, a MinDepth above MaxDepth is a walk that
	// returns nothing at all, and both are the empty answer with no error
	// that this repository keeps producing -- the same reason the CTE
	// name and the direction panic. TestANegativeOrInvertedBoundPanics
	// pins all four shapes.
	MinDepth int
	MaxDepth int

	// MaxRows caps the rows a caller can read from the walk. Zero means
	// no cap; a negative value panics rather than meaning "no cap", which
	// is what an unchecked LIMIT would have made it.
	//
	// It is a cap on what comes back, **not** a bound on the work
	// Postgres does: LIMIT is not allowed in a recursive term, so the cap
	// is a LIMIT on the wrapper CTE. Postgres's recursion is demand
	// driven and in practice stops early under it, but nothing here
	// measures that and this comment does not claim it.
	//
	// The emitted LIMIT is **MaxRows + 1**, so a caller that reads
	// MaxRows+1 rows knows its answer was truncated and one that reads
	// MaxRows or fewer knows it was not. Exactly-at-cap and
	// truncated-at-cap are otherwise the same answer, and a walk that
	// cannot say which it is gets reported to a designer as complete.
	// This is the same cap+1 mechanism Task 7's truncation flags use, and
	// it is here rather than there because the truncation happens here.
	// TestMaxRowsReturnsOneRowPastTheCapSoTruncationIsDetectable pins it,
	// with an uncapped control in the same test.
	//
	// Which rows survive the cap is **ORDER BY depth**: a truncated
	// answer is a prefix of the nearest hops, connected to the seed,
	// rather than an arbitrary scatter of nodes whose own edges were
	// dropped. Order within one depth is unspecified.
	// TestATruncatedWalkIsOrderedByDepth pins the ordering.
	MaxRows int
}

// isIdentifier reports whether s is a bare lower-case SQL identifier, the
// only thing this package will splice into a statement as a name.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// ReadFrom is the CTE a caller reads its result out of: the wrapper that
// applies MinDepth, the ordering and the row cap, never the recursion
// itself.
func ReadFrom(w Walk) string { return w.Name + "_out" }

// WalkCTE returns the bodies of the CTEs one bounded walk needs -- ready
// to follow a `WITH RECURSIVE` -- and the bind arguments they use,
// numbered from $1. The caller reads its rows from ReadFrom(w).
//
// The columns it produces are (id, depth, path, via_relation, from_id,
// closed): the node reached, how many hops away, the ids on the path that
// reached it, the relation walked to get there (null at depth 0), the
// node it came from (null at depth 0), and whether this hop closed a
// cycle. A caller that wants the edges a walk traversed reads
// via_relation; a caller that wants only the nodes reads id and ignores
// the rest. **One row is one edge traversal**, so a node reachable by two
// edges arrives twice: collapsing that is the caller's job, and it is the
// direction that loses no information -- a walk that deduplicated by node
// would drop one of the two edges between a pair joined in both
// directions, and a renderer cannot draw an edge it was never handed.
// TestDirectionAnyTraversesEachEdgeOnceFromEachNode pins the counts on
// exactly that pair.
//
// **What a walk returns for a cycle, which eleven tasks need to read.**
// A cycle is legal content -- the core spec allows prerequisite cycles
// deliberately, so that they can be surfaced as design errors, and
// surfacing one means drawing the edge that closes it. So the hop onto a
// node already on the path is **returned**, once, with closed = true, and
// is **not expanded from**. Concretely, walking out from a:
//
//   - a self-loop a -> a returns one row: a at depth 1, closed, via the
//     loop's own relation. TestASelfLoopIsReturnedOnceAndNotExpanded.
//   - a two-cycle a -> b -> a returns two rows: b at depth 1 and a at
//     depth 2, closed, via the return edge -- which the earlier shape of
//     this walk never handed to anybody. TestATwoCycleReturnsItsReturnEdge.
//   - a three-cycle a -> b -> c -> a returns four rows at min depth 0:
//     a, b, c and a again at depth 3, closed, via c -> a. Three distinct
//     relations, three distinct nodes.
//     TestAWalkOverACycleReturnsEachNodeOnceAndTheClosingEdgeWithIt.
//
// The earlier shape suppressed the *row*, not just the recursion, which
// meant an n-cycle came back with n-1 of its n edges and a self-loop came
// back with none. That contradicted this walk's own justification for its
// row shape -- see the paragraph above about a renderer that cannot draw
// an edge it was never handed -- and it made the one thing the analysis
// engine exists to find, a prerequisite cycle, the one thing this walk
// could not show.
//
// **Three things in the emitted statement are load-bearing:**
//
//   - project_id = $1 appears on every table reference, in the anchor and
//     in the recursive term, on the relation *and* on the entity at the
//     far end of it. An anchor-only filter seeds correctly and then lets
//     the walk leave the project through any edge whose far side lives
//     elsewhere. TestTheProjectFilterIsInBothTermsOfTheRecursion asserts
//     it as text -- counting all three positions, the anchor's included
//     -- and TestAWalkCannotLeaveItsProjectThroughARogueEdge asserts the
//     two in the recursive term behaviourally by forging, in its own
//     throwaway database with 0004_metamodel.sql's composite keys
//     dropped, the two rows the shipped schema makes impossible.
//     TestTheAnchorFiltersOnTheProjectEvenWhenTheSeedDoesNot is the
//     behavioural half of the third: SeedSQL is documented as the
//     caller's own and this package does not control whether it filters,
//     so the anchor join is the only thing between a project-blind seed
//     and another game's entity.
//   - NOT w.closed in the recursive term is the cycle guard, and it is a
//     correctness requirement rather than a defensive one. It is **not**
//     what makes the walk terminate -- the depth bound below does that,
//     and removing the guard leaves this package's cycle test finishing
//     with the same node set. What it stops is the cycle being re-walked
//     once per level until that bound is reached: four rows rather than
//     eleven for a three-node cycle at max depth 10.
//     TestAWalkOverACycleReturnsEachNodeOnceAndTheClosingEdgeWithIt pins
//     the node set, the row count and the edge set, and only the counts
//     are red without the guard. The guard is computed against the
//     **whole** path and not against the seed alone:
//     TestACycleThatExcludesTheSeedIsGuardedByTheWholePath is the input
//     that separates the two, because every other fixture's cycle passes
//     through the seed.
//   - depth < $n sits in the recursive term, where it prunes, and not in
//     an outer WHERE, which would materialise the whole walk first.
//     TestDepthBoundsTheWalk pins what it reaches.
//
// A fourth line is load-bearing only under Any:
// r.id IS DISTINCT FROM w.via_relation, which stops a walk re-traversing
// the relation it just arrived by. Under Out and In it can never fire.
// Under Any it is what keeps every single edge from reading as a
// two-cycle: without it, arriving at b over a -> b and then walking the
// same edge backwards would emit a closed row for a on every edge in the
// graph, and an analysis engine looking for prerequisite cycles would
// find one everywhere. The path guard used to hide this, because the
// backtrack always lands on the previous node; now that a closing hop is
// returned rather than suppressed, it has to be excluded on purpose.
// TestASelfLoopIsReturnedOnceAndNotExpanded and
// TestDirectionAnyTraversesEachEdgeOnceFromEachNode both fail without it.
//
// **The shape this deliberately does not reuse, and why the plan's
// replacement is not the one that shipped.** ListEntitiesRelatedTo
// (internal/db/queries/metamodel.sql) selects the far end of an edge in
// the join condition, with two arms guarded by a scalar `direction`, so
// one arm is dead on every row. Under `any` both arms are live. The
// answer is *not* to emit one UNION ALL arm per direction: Postgres
// refuses that outright -- SQLSTATE 42P19, "recursive reference to query
// \"w\" must not appear within its non-recursive term", because a third
// branch makes the first two the non-recursive term -- and, written in
// the legal way that gets the same rows (a two-armed edge relation
// feeding one self-reference), a self-loop matches under both arms and
// the walk doubles at every level: measured on this project's Postgres
// at 1, 2, 4 and 8 rows for depths 0 to 3, against 1, 1, 1, 1 for the
// arm below, both with the guard removed. So `any` is **one** arm: the
// near end matches either column and the far end is the scalar CASE of
// whichever matched, which cannot produce an edge twice from one node.
//
// The arm count is now **observable with the guard in place**, which it
// was not while a closing hop was suppressed: a self-loop under Any is
// exactly the edge a two-armed shape matches twice, and it now comes back
// as a returned row rather than as nothing, so the second copy is a
// second row. TestASelfLoopIsReturnedOnceAndNotExpanded asserts the one
// row and is red -- with two rows over one relation -- against the
// two-armed emitter, which is what that test could not do before.
func WalkCTE(w Walk) (string, []any) {
	if !isIdentifier(w.Name) {
		panic(fmt.Sprintf("graph: a CTE name must be a lower-case identifier, got %q; a name "+
			"derived from a caller's document is how this becomes an injection", w.Name))
	}
	switch {
	case w.MinDepth < 0 || w.MaxDepth < 0:
		panic(fmt.Sprintf("graph: depth bounds are non-negative, got min %d max %d; a negative "+
			"bound is an empty or seed-only answer with no error, which is this repository's "+
			"recurring defect", w.MinDepth, w.MaxDepth))
	case w.MinDepth > w.MaxDepth:
		panic(fmt.Sprintf("graph: min depth %d is above max depth %d; that walk returns nothing "+
			"and says nothing about why", w.MinDepth, w.MaxDepth))
	case w.MaxRows < 0:
		panic(fmt.Sprintf("graph: max rows is non-negative, got %d; a negative cap is no cap "+
			"at all, which is the opposite of what a caller asking for one wants", w.MaxRows))
	}
	args := []any{w.ProjectID}
	bind := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	// The seed's own arguments are re-bound in order, so a caller can
	// build SeedSQL with its own numbering and have it renumbered here.
	seed := renumber(w.SeedSQL, len(args))
	args = append(args, w.SeedArgs...)

	types := bind(w.RelationTypeIDs)
	maxDepth := bind(w.MaxDepth)

	// near is the end of the edge the walk is standing on; far is the end
	// it moves to. For a single direction both are plain columns. For Any
	// they are one match over either column and the scalar CASE of
	// whichever matched -- one arm, so no edge is ever traversed twice
	// from the same node.
	var near, far string
	switch w.Direction {
	case Out:
		near, far = "r.source_id = w.id", "r.target_id"
	case In:
		near, far = "r.target_id = w.id", "r.source_id"
	case Any:
		near = "(r.source_id = w.id OR r.target_id = w.id)"
		far = "CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END"
	default:
		// Including the zero value: a direction this package does not
		// know is a caller's mistake, and defaulting it to Out would
		// answer every such walk backwards-compatibly and wrongly, which
		// is the wrong-answer-without-an-error class this repository
		// keeps producing. TestAnUnknownDirectionPanics pins it.
		panic(fmt.Sprintf("graph: unknown direction %q; the three are %q, %q and %q",
			w.Direction, Out, In, Any))
	}

	// A closed row's path ends with a node it already contains -- that is
	// what closed means -- and it is never expanded from, so every path
	// the recursion carries forward is simple and the recursion is finite
	// for that reason as well as for the depth bound.
	body := fmt.Sprintf(`%[1]s (id, depth, path, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid, false
    FROM (%[2]s) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT %[3]s, w.depth + 1, w.path || (%[3]s), r.id, w.id, (%[3]s) = ANY(w.path)
    FROM %[1]s w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY(%[4]s::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND %[5]s
    JOIN entities far
      ON far.id = (%[3]s)
     AND far.project_id = $1
    WHERE w.depth < %[6]s
      AND NOT w.closed
)`, w.Name, seed, far, types, near, maxDepth)

	// The wrapper is where MinDepth, the ordering and the row cap live,
	// because none of them may prune the recursion: a walk with min 2 has
	// to pass through depth 1 to get there, and LIMIT is not allowed in a
	// recursive term at all. The cap is MaxRows + 1 so that the caller can
	// tell a full answer from a truncated one, and the ORDER BY is what
	// makes the truncated one a connected prefix of nearest hops.
	minDepth := bind(w.MinDepth)
	limit := ""
	if w.MaxRows > 0 {
		limit = " LIMIT " + bind(w.MaxRows+1)
	}
	body += fmt.Sprintf(",\n%s AS (SELECT * FROM %s WHERE depth >= %s ORDER BY depth%s)",
		ReadFrom(w), w.Name, minDepth, limit)

	return body, args
}

// renumber shifts a seed statement's $1..$n up by offset, so a caller can
// write its seed with its own numbering. It rewrites only $N tokens and
// leaves everything else alone; a seed containing a literal "$1" inside a
// string would be rewritten, which is why SeedSQL is documented as the
// caller's own SQL with no caller text in it.
func renumber(sql string, offset int) string {
	var b strings.Builder
	for i := 0; i < len(sql); i++ {
		if sql[i] != '$' {
			b.WriteByte(sql[i])
			continue
		}
		j := i + 1
		for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			continue
		}
		n := 0
		for _, c := range sql[i+1 : j] {
			n = n*10 + int(c-'0')
		}
		fmt.Fprintf(&b, "$%d", n+offset)
		i = j - 1
	}
	return b.String()
}
