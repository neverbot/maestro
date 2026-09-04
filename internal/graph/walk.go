// Package graph emits the one bounded traversal Maestro walks a game's
// relations with: project-filtered in both terms of the recursion,
// depth-bounded, and guarded against revisiting a node on the path it
// arrived by.
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
type Walk struct {
	// Name is the CTE's name. It must be a Go-side constant or a
	// generated identifier such as "s3" -- never anything derived from a
	// caller's document. WalkCTE panics on a name that is not an
	// identifier, so a future caller that gets this wrong fails loudly at
	// its first test rather than quietly at somebody's database.
	// TestACTENameThatIsNotAnIdentifierPanics pins the panic.
	Name string

	ProjectID       uuid.UUID
	SeedSQL         string
	SeedArgs        []any
	RelationTypeIDs []uuid.UUID
	Direction       Direction

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
	MinDepth int
	MaxDepth int

	// MaxRows caps the rows a caller can read from the walk. Zero means
	// no cap.
	//
	// It is a cap on what comes back, **not** a bound on the work
	// Postgres does: LIMIT is not allowed in a recursive term, so the cap
	// is a LIMIT on the wrapper CTE. Postgres's recursion is demand
	// driven and in practice stops early under it, but nothing here
	// measures that and this comment does not claim it.
	// TestMaxRowsCapsWhatComesBack pins the cap on the rows returned, and
	// which rows survive it is unspecified -- there is no ORDER BY, so a
	// caller that hits the cap has a truncated answer and must say so
	// rather than treat it as the whole walk.
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
// applies MinDepth and the row cap, never the recursion itself.
func ReadFrom(w Walk) string { return w.Name + "_out" }

// WalkCTE returns the bodies of the CTEs one bounded walk needs -- ready
// to follow a `WITH RECURSIVE` -- and the bind arguments they use,
// numbered from $1. The caller reads its rows from ReadFrom(w).
//
// The columns it produces are (id, depth, path, via_relation, from_id):
// the node reached, how many hops away, the ids on the path that reached
// it, the relation walked to get there (null at depth 0) and the node it
// came from (null at depth 0). A caller that wants the edges a walk
// traversed reads via_relation; a caller that wants only the nodes reads
// id and ignores the rest. **One row is one edge traversal**, so a node
// reachable by two edges arrives twice: collapsing that is the caller's
// job, and it is the direction that loses no information -- a walk that
// deduplicated by node would drop one of the two edges between a pair
// joined in both directions, and a renderer cannot draw an edge it was
// never handed. TestDirectionAnyTraversesEachEdgeOnceFromEachNode pins
// the counts on exactly that pair.
//
// **Three things in the emitted statement are load-bearing:**
//
//   - project_id = $1 appears on every table reference, in the anchor and
//     in the recursive term, on the relation *and* on the entity at the
//     far end of it. An anchor-only filter seeds correctly and then lets
//     the walk leave the project through any edge whose far side lives
//     elsewhere. TestTheProjectFilterIsInBothTermsOfTheRecursion asserts
//     it as text, and TestAWalkCannotLeaveItsProjectThroughARogueEdge
//     asserts it behaviourally by forging, in its own throwaway database
//     with 0004_metamodel.sql's composite keys dropped, the two rows the
//     shipped schema makes impossible.
//   - NOT (... = ANY(path)) is the cycle guard, and it is a correctness
//     requirement rather than a defensive one: the core spec deliberately
//     allows prerequisite cycles as design errors to be surfaced, so a
//     cycle is legal content and a walk without the guard does not
//     terminate on exactly the games this engine exists to help.
//     TestAWalkTerminatesOnACycle pins termination and the node set.
//   - depth < $n sits in the recursive term, where it prunes, and not in
//     an outer WHERE, which would materialise the whole walk first.
//     TestDepthBoundsTheWalk pins what it reaches.
//
// **The shape this deliberately does not reuse, and why the plan's
// replacement is not the one that shipped.** ListEntitiesRelatedTo
// (internal/db/queries/metamodel.sql) selects the far end of an edge in
// the join condition, with two arms guarded by a scalar `direction`, so
// one arm is dead on every row. Under `any` both arms are live. The
// answer is *not* to emit one UNION ALL arm per direction: Postgres
// refuses that outright -- "recursive reference to query \"w\" must not
// appear more than once" -- and, written in the legal way that gets the
// same rows (a two-armed edge relation feeding one self-reference), a
// self-loop matches under both arms and the walk doubles at every level,
// measured at 2, 4 and 8 rows for depths 1, 2 and 3. So `any` is **one**
// arm: the near end matches either column and the far end is the scalar
// CASE of whichever matched, which cannot produce an edge twice from one
// node.
func WalkCTE(w Walk) (string, []any) {
	if !isIdentifier(w.Name) {
		panic(fmt.Sprintf("graph: a CTE name must be a lower-case identifier, got %q; a name "+
			"derived from a caller's document is how this becomes an injection", w.Name))
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
	near, far := "r.source_id = w.id", "r.target_id"
	switch w.Direction {
	case In:
		near, far = "r.target_id = w.id", "r.source_id"
	case Any:
		near = "(r.source_id = w.id OR r.target_id = w.id)"
		far = "CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END"
	}

	body := fmt.Sprintf(`%[1]s (id, depth, path, via_relation, from_id) AS (
    SELECT seed.id, 0, ARRAY[seed.id], NULL::uuid, NULL::uuid
    FROM (%[2]s) AS seed
    JOIN entities anchor ON anchor.id = seed.id AND anchor.project_id = $1
  UNION ALL
    SELECT %[3]s, w.depth + 1, w.path || (%[3]s), r.id, w.id
    FROM %[1]s w
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY(%[4]s::uuid[])
     AND %[5]s
    JOIN entities far
      ON far.id = (%[3]s)
     AND far.project_id = $1
    WHERE w.depth < %[6]s
      AND NOT (%[3]s) = ANY(w.path)
)`, w.Name, seed, far, types, near, maxDepth)

	// The wrapper is where MinDepth and the row cap live, because neither
	// may prune the recursion: a walk with min 2 has to pass through
	// depth 1 to get there, and LIMIT is not allowed in a recursive term
	// at all.
	minDepth := bind(w.MinDepth)
	limit := ""
	if w.MaxRows > 0 {
		limit = " LIMIT " + bind(w.MaxRows)
	}
	body += fmt.Sprintf(",\n%s AS (SELECT * FROM %s WHERE depth >= %s%s)",
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
