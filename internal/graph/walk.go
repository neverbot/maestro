// Package graph emits the one bounded traversal Maestro walks a game's
// relations with: project-filtered in both terms of the recursion,
// depth-bounded, and guarded against expanding a node twice on the path
// it arrived by, while still handing the caller the edge that closes a
// cycle.
//
// It lives outside both of its callers so there is one walk and no drift:
// internal/views routes every traversal step deeper than one hop through
// WalkCTE, and internal/analysis runs its reachability closure through it
// with the gating direction in EdgePredicate.
//
// **Callers as of this commit:** internal/views and internal/analysis.
//
// **Not a caller yet:** none.
//
// Both lines are checked against the real import graph by
// TestThePackageCommentNamesItsCallersAndOnlyItsCallers, so the commit
// that adds a third importer is red until it is named above.
//
// What lives here is the SQL primitive. Policy — which relation types
// gate what, whether a container propagates reachability, what a step's
// result means — belongs to the caller.
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

	// EdgePredicate is an extra condition every hop's relation has to
	// satisfy before the walk will follow it, written as **the caller's
	// own SQL over the alias `r`, containing no caller text** -- the same
	// contract SeedSQL carries and for the same reason. It is numbered
	// from $1 and renumbered here, with EdgeArgs supplying its values, so
	// a caller can build it with its own numbering.
	//
	// It sits **inside** the recursive term, next to the relation-type
	// filter, because a relation the caller's predicate excludes is a
	// relation the walk must not traverse: applied outside, the walk would
	// still reach -- and hand back -- every node behind an excluded edge,
	// which is a wrong answer with no error rather than a narrower
	// picture. TestAnEdgePredicateIsAppliedInsideTheRecursion pins that
	// difference with a node reachable only through the excluded edge.
	//
	// An empty EdgePredicate adds no clause at all.
	EdgePredicate string
	EdgeArgs      []any

	// MinDepth and MaxDepth bound the hops. Depth 0 is the seed, which the
	// recursion always carries because the cycle guard needs it on the
	// path; MinDepth is applied after the recursion, in the CTE ReadFrom
	// names, so a walk with min 2 still walks depth 1 and does not hand
	// it back.
	//
	// Both are non-negative and MinDepth may not exceed MaxDepth. WalkCTE
	// panics rather than returning the empty answer with no error that a
	// negative or inverted bound would otherwise produce.
	MinDepth int
	MaxDepth int

	// MaxRows caps the rows a caller can read. Zero means no cap; a
	// negative value panics.
	//
	// It caps what comes back, not the work Postgres does: LIMIT is
	// illegal in a recursive term, so the cap is a LIMIT on the wrapper
	// CTE. The emitted limit is **MaxRows + 1**, so a caller reading
	// MaxRows+1 rows knows its answer was truncated — exactly-at-cap and
	// truncated-at-cap are otherwise the same answer.
	//
	// Which rows survive is ORDER BY depth: a truncated answer is a
	// prefix of the nearest hops, connected to the seed, rather than a
	// scatter of nodes whose own edges were dropped. Order within one
	// depth is unspecified.
	MaxRows int

	// CarryRelationPath adds a rel_path uuid[] column: the relation ids
	// walked to reach this row, in order, so a caller holding a closed row
	// can name every edge of the cycle and not only the closing one. path
	// carries node ids, and two relation types between the same pair of
	// nodes cannot be told apart from node ids alone.
	//
	// Opt-in, so a walk that does not ask for it emits the SQL text
	// internal/views' golden tests assert.
	//
	// The seed's rel_path is a zero-length array and never NULL: `||`
	// against NULL is NULL, which would make every path downstream NULL.
	CarryRelationPath bool
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

// WalkCTE returns the bodies of the CTEs one bounded walk needs, ready
// to follow a `WITH RECURSIVE`, and the bind arguments they use, numbered
// from $1. The caller reads its rows from ReadFrom(w).
//
// Columns: (id, depth, path, via_relation, from_id, closed) — the node
// reached, how many hops away, the ids on the path that reached it, the
// relation walked to get there (null at depth 0), the node it came from
// (null at depth 0), and whether this hop closed a cycle. rel_path is a
// seventh, between path and via_relation, only when CarryRelationPath is
// set.
//
// **One row is one edge traversal**, so a node reachable by two edges
// arrives twice; collapsing that is the caller's job. Deduplicating by
// node here would drop one of the two edges between a pair joined both
// ways, and a renderer cannot draw an edge it was never handed.
//
// **A cycle is content, not an error.** The hop onto a node already on
// the path is returned once with closed = true and is not expanded from,
// so an n-cycle comes back with all n of its edges and a self-loop with
// its own.
//
// Five things in the emitted statement are load-bearing:
//
//   - project_id = $1 on every table reference, in the anchor and in the
//     recursive term, on the relation and on the entity at its far end.
//     An anchor-only filter lets the walk leave the project through an
//     edge whose far side lives elsewhere, and SeedSQL is the caller's
//     own, so the anchor join is all that stands between a project-blind
//     seed and another game's entity.
//   - NOT w.closed in the recursive term. The depth bound is what makes
//     the walk terminate; this is what stops a cycle being re-walked once
//     per level until that bound. It is computed against the whole path,
//     not against the seed.
//   - depth < $n in the recursive term, where it prunes, rather than in
//     an outer WHERE, which would materialise the whole walk first.
//   - EdgePredicate, when the caller set one, ANDed into the same JOIN so
//     a condition on the relation prunes the recursion instead of
//     filtering its output.
//   - r.id IS DISTINCT FROM w.via_relation, which only fires under Any:
//     without it, arriving at b over a -> b and walking the same edge
//     back emits a closed row for a on every edge in the graph, and an
//     engine looking for prerequisite cycles finds one everywhere.
//
// Any is **one** arm, not one per direction: the near end matches either
// column and the far end is a CASE of whichever matched. Postgres refuses
// three UNION ALL arms outright (42P19), and the legal two-armed shape
// matches a self-loop twice and doubles the walk at every level.
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

	// The caller's own edge predicate, renumbered the same way and for the
	// same reason. It is parenthesised because it is joined to this
	// package's own conditions with AND, and a caller's `a OR b` would
	// otherwise swallow the relation-type filter beside it.
	edge := ""
	if w.EdgePredicate != "" {
		edge = "\n     AND (" + renumber(w.EdgePredicate, len(args)) + ")"
		args = append(args, w.EdgeArgs...)
	}

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
	// The three fragments CarryRelationPath adds, and the empty strings
	// it does not: a walk that did not ask for the column emits the
	// statement byte for byte as it shipped, which is what keeps
	// internal/views' golden files still.
	relPathCol, relPathSeed, relPathStep := "", "", ""
	if w.CarryRelationPath {
		relPathCol = ", rel_path"
		relPathSeed = ", ARRAY[]::uuid[]"
		relPathStep = ", w.rel_path || r.id"
	}

	body := fmt.Sprintf(`%[1]s (id, depth, path%[8]s, via_relation, from_id, closed) AS (
    SELECT seed.id, 0, ARRAY[seed.id]%[9]s, NULL::uuid, NULL::uuid, false
    FROM (%[2]s) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT %[3]s, w.depth + 1, w.path || (%[3]s)%[10]s, r.id, w.id, (%[3]s) = ANY(w.path)
    FROM %[1]s w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY(%[4]s::uuid[])
     AND r.id IS DISTINCT FROM w.via_relation
     AND %[5]s%[7]s
    JOIN entities far
      ON far.id = (%[3]s)
     AND far.project_id = $1
    WHERE w.depth < %[6]s
      AND NOT w.closed
)`, w.Name, seed, far, types, near, maxDepth, edge, relPathCol, relPathSeed, relPathStep)

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

// Renumber shifts a statement WalkCTE produced up by offset so it can be
// spliced into a larger one, **leaving $1 exactly where it is**.
//
// $1 is the project id in everything this package emits, and it is the
// project id in internal/views' statements too, so the two agree on that
// one position and the id is bound once rather than twice. That is not a
// convenience: `project_id = $1` on every table reference is the shape
// both packages' isolation guards assert as text, and a splice that
// renumbered $1 into $9 would leave those guards asserting a filter that
// no longer exists under the name they look for.
//
// It is exported for exactly one caller shape -- a compiler that owns the
// outer statement and wrote its own $1 as the same project id -- and it
// checks nothing, because it cannot: the caller is the only side that
// knows what its $1 holds. internal/views asserts the identity itself,
// in builder.adopt, rather than trusting this comment.
func Renumber(sql string, offset int) string { return shift(sql, offset, 2) }

// renumber shifts a seed statement's $1..$n up by offset, so a caller can
// write its seed with its own numbering. It rewrites only $N tokens and
// leaves everything else alone; a seed containing a literal "$1" inside a
// string would be rewritten, which is why SeedSQL is documented as the
// caller's own SQL with no caller text in it.
func renumber(sql string, offset int) string { return shift(sql, offset, 1) }

// shift is the renumbering both spellings share: every $N with N >= from
// moves up by offset, and everything else is copied through.
func shift(sql string, offset, from int) string {
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
		if n >= from {
			n += offset
		}
		fmt.Fprintf(&b, "$%d", n)
		i = j - 1
	}
	return b.String()
}
