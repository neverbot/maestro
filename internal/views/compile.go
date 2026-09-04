package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/graph"
	"github.com/neverbot/maestro/internal/metamodel"
)

// frag is a piece of statement text this package wrote itself.
//
// It is a *defined* type over string, and that is the whole point: an
// untyped constant such as "SELECT " converts to it implicitly, while a
// `string` variable — which is what every caller value in this package is
// — does not. `b.write(set.Name)` therefore does not compile, and turning
// a caller's value into statement text requires spelling `frag(...)`,
// which is one grep and one review comment away from being caught.
// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere reads every
// non-test file of this package and refuses a conversion outside the five
// helpers below, so the guard is a test rather than a habit.
type frag string

// builder is the only thing in this package that appends to a statement.
//
// **It has no method that takes a caller's value as text**, and that is
// the whole design: bind() returns the placeholder and puts the value in
// the argument slice, write() takes a fragment, and a fragment is either
// a literal this file wrote or the output of bind. There is deliberately
// no writef with a %s a value could reach — a single fmt.Sprintf with a
// caller value in it is the injection this repository's only
// runtime-built SQL could have, and the way to not have it is to make it
// unspellable rather than to remember not to write it.
// TestNoCallerValueEverReachesTheStatementText is the behavioural
// assertion; the frag type and its own test are the construction.
type builder struct {
	sql  sqlText
	args []any
}

// sqlText is the statement being assembled, and it is a wrapper around
// strings.Builder rather than a strings.Builder because the difference is
// the guard. A bare strings.Builder field is package-visible and carries
// WriteString, so `b.sql.WriteString(set.Name)` would put a caller's
// value in the text with no frag conversion for a test to find. sqlText
// exposes one appender, it takes a fragment, and the raw buffer is
// reachable only as `.raw` — which
// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere refuses
// outside this type's own two methods.
type sqlText struct{ raw strings.Builder }

func (t *sqlText) append(literal frag) { t.raw.WriteString(string(literal)) }

func (t *sqlText) String() string { return t.raw.String() }

func (b *builder) write(literal frag) { b.sql.append(literal) }

// bind puts a value in the argument slice and hands back its placeholder.
// It is the only route from a Go value into an emitted statement.
func (b *builder) bind(v any) frag {
	b.args = append(b.args, v)
	return frag(fmt.Sprintf("$%d", len(b.args)))
}

// adopt splices the statement internal/graph wrote for one bounded walk
// into this one, and returns its CTE bodies as a fragment.
//
// **It is the one route by which text this package did not write becomes
// statement text**, and it is narrow on purpose: it takes a graph.Walk
// rather than a string, so the only thing it can convert is
// graph.WalkCTE's own output. What that output contains beyond graph's
// own literals is the seed and the edge predicate this builder handed it,
// both already fragments, and its values are bind arguments.
//
// **The renumbering.** WalkCTE numbers its arguments from $1 and puts the
// project id there, which is where this statement already keeps it, so
// $1 maps onto $1 and everything above it moves to the end of this
// builder's argument list. The identity of $1 is *checked* rather than
// trusted: a walk compiled for another project, or a builder whose $1 is
// not the project id, would otherwise emit `project_id = $1` filters
// against some unrelated value — the wrong-answer-without-an-error class
// this project keeps producing, and here it would be a wrong answer about
// which game a picture came from.
func (b *builder) adopt(w graph.Walk) (frag, error) {
	sql, args := graph.WalkCTE(w)
	if len(b.args) == 0 || b.args[0] != any(w.ProjectID) {
		return "", fmt.Errorf("views: this statement's $1 is not the project id the walk " +
			"was compiled for, so its project filters would read the wrong argument")
	}
	if len(args) == 0 || args[0] != any(w.ProjectID) {
		return "", fmt.Errorf("views: internal/graph no longer binds the project id at $1, " +
			"so a spliced walk cannot share this statement's $1")
	}
	// $1 stays; args[1] becomes $(len+1), which is $(2 + offset) with
	// offset one below the current count.
	offset := len(b.args) - 1
	b.args = append(b.args, args[1:]...)
	return frag(graph.Renumber(sql, offset)), nil
}

// sprintf is fmt.Sprintf with a fragment format and fragment arguments,
// so nothing but statement text this package produced can be
// interpolated. Its format string is an untyped constant at every call
// site, because a `string` variable does not convert to frag implicitly.
func sprintf(format frag, args ...frag) frag {
	parts := make([]any, len(args))
	for i, a := range args {
		parts[i] = string(a)
	}
	return frag(fmt.Sprintf(string(format), parts...))
}

// joinFrags is strings.Join over fragments.
func joinFrags(parts []frag, sep frag) frag {
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = string(p)
	}
	return frag(strings.Join(texts, string(sep)))
}

// cteName builds a generated CTE alias such as "s0" or "t3". The index is
// an int and the prefix is a constant, so no caller string can reach a
// name — which matters because a name is the one thing in a statement
// that cannot be a bind parameter.
func cteName(prefix frag, i int) frag {
	return frag(fmt.Sprintf("%s%d", prefix, i))
}

// The three CTE prefixes and the two collection points, spelled once.
const (
	seedPrefix frag = "s"
	stepPrefix frag = "t"
	// walkPrefix names the CTEs internal/graph emits for a multi-hop
	// step. It is a third prefix rather than the step's own name with a
	// suffix so that no generated name can ever collide with another:
	// "t1_w" is a name a step called t1_w would also want.
	walkPrefix frag = "w"
	nodeRows   frag = "node_rows"
	edgeRows   frag = "edge_rows"
)

// compileOptions are the parts of one *run* the compiler needs, as
// opposed to the parts of the query, which live on Resolved. The
// projection is not here for exactly that reason: it is written in the
// document, so it is resolved with the rest of it and read off Resolved.
type compileOptions struct {
	// IncludeFields selects the entity's and the relation's jsonb fields
	// into the result. It is off by default because a thousand-node
	// result with every field inlined is a five-figure token bill for a
	// picture the agent is not going to read.
	IncludeFields bool
}

// Compile turns a resolved query into one SELECT and its arguments.
//
// $1 is always the project id, in every CTE and every arm, and
// TestEveryTableReferenceIsProjectFiltered asserts it as text.
//
// **The projection travels as one jsonb column**, built per node arm by
// project.go: every slot the document asked for, plus the one hop a slot
// may take to read its value off a neighbour, plus the flag that says a
// hop had more than one candidate. An edge entry's label_from rides in
// the same column under the key "label". Nulls are stripped, so a slot
// that found nothing is absent rather than empty — "this quest has no
// zone" and "its zone is named the empty string" are different answers.
//
// A traversal step deeper than one hop is emitted through
// internal/graph's WalkCTE — see walk() — which owns the recursion, its
// project filter, its path guard and its depth bound. That package's
// arguments are spliced in by builder.adopt, which is why $1 means the
// project id in the walk's own text as well as in this compiler's.
//
// **What the emitted statement costs.** entities.fields is indexed by
// `gin (fields jsonb_path_ops)`, which serves containment and nothing
// else — no ranges, no ordering, not even key existence. So every leaf
// this compiler emits against a declared field is a scan of the rows the
// selector's entity_type_id filter left, with one exception: `contains`
// on a list<text> field is emitted as `fields @> jsonb_build_object(...)`
// and can use that index. That is accepted rather than hidden; Task 7's
// stats is what makes it measurable.
func Compile(r *Resolved, projectID uuid.UUID) (string, []any, error) {
	return compileWith(r, projectID, compileOptions{})
}

func compileWith(r *Resolved, projectID uuid.UUID, opts compileOptions) (string, []any, error) {
	if r == nil || r.Query == nil {
		return "", nil, fmt.Errorf("views: a resolved query is required")
	}
	b := &builder{}
	b.bind(projectID) // $1, referenced by every clause
	c := &compiler{b: b, r: r, projectID: projectID, opts: opts, cte: map[string]cteRef{}}

	var ctes []frag
	for i := range r.Sets {
		cte, err := c.selector(i)
		if err != nil {
			return "", nil, err
		}
		ctes = append(ctes, cte)
	}
	for i := range r.Steps {
		cte, err := c.step(i)
		if err != nil {
			return "", nil, err
		}
		ctes = append(ctes, cte)
	}
	nodes, err := c.nodeUnion()
	if err != nil {
		return "", nil, err
	}
	edges, err := c.edgeUnion()
	if err != nil {
		return "", nil, err
	}
	ctes = append(ctes, nodes, edges)

	// WITH RECURSIVE whether or not this query has a multi-hop step in it.
	// RECURSIVE is a property of the WITH clause rather than of a single
	// CTE, it costs nothing when nothing recurses, and spelling it
	// conditionally would make the keyword one more thing that can be
	// wrong about a statement.
	b.write("WITH RECURSIVE ")
	b.write(joinFrags(ctes, ",\n"))
	b.write("\nSELECT 'node' AS kind, n.* FROM ")
	b.write(nodeRows)
	b.write(" n\nUNION ALL\nSELECT 'edge' AS kind, e.* FROM ")
	b.write(edgeRows)
	// Ordered by kind, then by the declaration rank of the nodes or edges
	// entry that produced the row, then by id. The rank is what makes
	// "which set does a node that appears in two of them belong to"
	// answerable in Go without a second query, and the id is what keeps
	// two runs of the same query in the same order.
	// TestANodeInTwoSetsComesBackOnceUnderTheFirstSetThatClaimedIt
	// asserts this line as text, because deleting it leaves the
	// behavioural half of that test green.
	b.write(" e")
	// The depth arm: zero rows or one, and the one says that at least one
	// walk had a hop left to make when its depth bound stopped it. It is a
	// statement-level fact rather than a property of any node, so it comes
	// back as its own row rather than as a column repeated on every other
	// one — and a query with no multi-hop step emits no arm at all, which
	// is why a flat query cannot report a depth it never measured.
	if len(c.probes) > 0 {
		b.write(sprintf("\nUNION ALL\nSELECT '%s' AS kind, %s\nWHERE %s",
			depthTruncatedKind, nullColumns, joinFrags(c.probes, " OR ")))
	}
	// The row arm, the same shape and for the same reason: a walk that
	// hit the row cap internal/graph applies handed this statement fewer
	// traversals than the graph holds, and neither the node cap nor the
	// edge cap can see that — four times the node cap in *edge
	// traversals* can collapse to a handful of nodes.
	if len(c.rowProbes) > 0 {
		b.write(sprintf("\nUNION ALL\nSELECT '%s' AS kind, %s\nWHERE %s",
			walkTruncatedKind, nullColumns, joinFrags(c.rowProbes, " OR ")))
	}
	b.write("\nORDER BY 1, 11, 2")
	return b.sql.String(), b.args, nil
}

// cteRef is one named intermediate result and what it can be read for.
type cteRef struct {
	name frag
	// step is true for a CTE produced by a traversal, which is the only
	// kind that carries the relation each of its rows was reached by, and
	// therefore the only kind an `edges: [{from_step: …}]` entry can draw.
	step bool
}

// compiler carries the state one Compile call threads through its parts.
type compiler struct {
	b         *builder
	r         *Resolved
	projectID uuid.UUID
	opts      compileOptions
	cte       map[string]cteRef
	// probes are the "is there a node or an edge past the depth this step
	// asked for that the picture does not already hold" tests, one per
	// multi-hop step. They are collected here and emitted as one arm of
	// the final statement, so a query with no walk in it carries no probe
	// at all rather than an EXISTS over nothing.
	probes []frag
	// rowProbes are the "did this walk's own row cap drop a traversal"
	// tests, one per multi-hop step, emitted as their own arm. They are
	// separate from probes because they answer a different question and
	// set different flags: a depth truncation is a bound the designer
	// declared, a row truncation is content the walk's internal cap threw
	// away.
	rowProbes []frag
}

// subPredicate compiles a predicate against a fresh argument list, so
// what comes back is numbered from $1 and can be handed to
// internal/graph, which renumbers it into the outer statement. Compiled
// against the outer builder instead, its placeholders would be
// renumbered a second time by adopt and point at the wrong values.
func (c *compiler) subPredicate(sc leafScope, p *ResolvedPredicate) (frag, []any, error) {
	outer := c.b
	sub := &builder{}
	c.b = sub
	sql, err := c.predicate(sc, p)
	c.b = outer
	if err != nil {
		return "", nil, err
	}
	return sql, sub.args, nil
}

// fieldsOf is the jsonb payload column, or a typed null when the run did
// not ask for it.
func (c *compiler) fieldsOf(alias frag) frag {
	if !c.opts.IncludeFields {
		return "NULL::jsonb"
	}
	return sprintf("%s.fields", alias)
}

// invalidFilter is the clause that keeps rows the metamodel flagged as no
// longer fitting their schema out of a picture, unless the document asked
// for them.
func (c *compiler) invalidFilter(alias frag) frag {
	if c.r.Query.IncludeInvalid {
		return ""
	}
	return sprintf("\n     AND %s.invalid = false", alias)
}

// selector emits one seed set:
//
//	s0 (id, set_name) AS (
//	    SELECT e.id, $n::text
//	    FROM entities e
//	    WHERE e.project_id = $1 AND e.entity_type_id = $k AND e.invalid = false
//	      AND lower(e.key) = ANY($m::text[]) AND (…)
//	)
//
// **The set name travels as a bind parameter**, not as SQL text, and so
// do the keys — which is what makes
// TestNoCallerValueEverReachesTheStatementText pass on a key that is
// perfectly legal. The compiler cannot tell a legal key from a crafted
// one and does not try.
func (c *compiler) selector(i int) (frag, error) {
	set := c.r.Sets[i]
	name := cteName(seedPrefix, i)
	c.cte[set.Name] = cteRef{name: name}
	if set.EntityTypeID == nil {
		// Unreachable through Resolve, which refuses an unresolved type
		// key, and reachable from a hand-built *Resolved.
		return "", invalidQuery(pointer("from", i, "type"),
			"did not resolve to an entity type, so there is nothing to select")
	}
	where := []frag{
		sprintf("e.project_id = $1"),
		sprintf("e.entity_type_id = %s", c.b.bind(*set.EntityTypeID)),
	}
	if !c.r.Query.IncludeInvalid {
		where = append(where, "e.invalid = false")
	}
	if keys := set.Selector.Keys; len(keys) > 0 {
		lowered := make([]string, 0, len(keys))
		for _, key := range keys {
			lowered = append(lowered, strings.ToLower(key))
		}
		where = append(where, sprintf("lower(e.key) = ANY(%s::text[])", c.b.bind(lowered)))
	}
	predicate, err := c.predicate(leafScope{alias: "e"}, set.Where)
	if err != nil {
		return "", err
	}
	where = append(where, predicate)
	return sprintf(`%s (id, set_name, depth) AS (
    SELECT e.id, %s::text, 0
    FROM entities e
    WHERE %s
)`, name, c.b.bind(set.Name), joinFrags(where, "\n      AND ")), nil
}

// step emits one traversal of exactly one hop:
//
//	t0 (id, set_name, from_id, via_relation) AS (
//	    SELECT far.id, $n::text, near.id, r.id
//	    FROM s0 near
//	    JOIN relations r ON r.project_id = $1 AND … AND (edge predicate)
//	    JOIN entities far ON far.id = … AND far.project_id = $1 AND …
//	    WHERE (node predicate)
//	)
//
// A step deeper than one hop is **not** emitted here: it goes through
// walk() and internal/graph, which owns the recursion, its project
// filter, its path guard and its depth bound. Emitting it here as a
// second, hand-written recursion is the drift internal/graph was
// extracted to prevent.
func (c *compiler) step(i int) (frag, error) {
	step := c.r.Steps[i]
	name := cteName(stepPrefix, i)
	if step.Name != "" {
		c.cte[step.Name] = cteRef{name: name, step: true}
	}
	from, ok := c.cte[step.FromSet]
	if !ok {
		// Unreachable through ParseQuery, which refuses a step that reads
		// a set no earlier selector or step declared.
		return "", invalidQuery(pointer("traverse", i, "from"),
			fmt.Sprintf("no set named %q was compiled before this step", step.FromSet))
	}
	if step.Step.Depth != nil && step.Step.Depth.Max > 1 {
		return c.walk(i, name, from)
	}
	return c.hop(i, name, from)
}

// hop emits a step of exactly one hop as a plain join. It is the shape
// above; walk() is the shape for anything deeper.
func (c *compiler) hop(i int, name frag, from cteRef) (frag, error) {
	step := c.r.Steps[i]

	var near, far frag
	switch step.Step.Direction {
	case DirectionOut:
		near, far = "r.source_id = near.id", "r.target_id"
	case DirectionIn:
		near, far = "r.target_id = near.id", "r.source_id"
	case DirectionAny:
		near = "(r.source_id = near.id OR r.target_id = near.id)"
		far = "CASE WHEN r.source_id = near.id THEN r.target_id ELSE r.source_id END"
	default:
		// Including the empty string, which applyDefaults fills. A
		// direction this compiler does not know would otherwise be
		// emitted as `out` and answer backwards without an error.
		return "", invalidQuery(pointer("traverse", i, "direction"),
			fmt.Sprintf("must be %q, %q or %q (got %q)",
				DirectionOut, DirectionIn, DirectionAny, step.Step.Direction))
	}

	edgeWhere, err := c.predicate(leafScope{alias: "r", edge: true}, step.EdgeWhere)
	if err != nil {
		return "", err
	}
	nodeWhere, err := c.predicate(leafScope{alias: "far"}, step.Where)
	if err != nil {
		return "", err
	}
	var toType frag
	if len(step.ToTypeIDs) > 0 {
		toType = sprintf("\n     AND far.entity_type_id = ANY(%s::uuid[])",
			c.b.bind(step.ToTypeIDs))
	}
	return sprintf(`%s (id, set_name, from_id, via_relation, depth) AS (
    SELECT %s, %s::text, near.id, r.id, near.depth + 1
    FROM %s near
    JOIN relations r
      ON r.project_id = $1
     AND r.relation_type_id = ANY(%s::uuid[])
     AND %s
     AND %s
    JOIN entities far
      ON far.id = (%s)
     AND far.project_id = $1%s%s
    WHERE %s
)`, name, far, c.b.bind(step.Name), from.name, c.b.bind(step.RelationTypeIDs),
		near, edgeWhere, far, c.invalidFilter("far"), toType, nodeWhere), nil
}

// walkDirection maps this language's direction onto internal/graph's.
//
// It is a translation rather than a cast even though the three strings
// are equal, because graph.WalkCTE **panics** on a direction it does not
// know — rightly, since a direction it defaulted would answer a walk
// backwards — and a panic is not how this package refuses a document.
// The refusal is the one hop() gives, with the same pointer.
func walkDirection(i int, d string) (graph.Direction, error) {
	switch d {
	case DirectionOut:
		return graph.Out, nil
	case DirectionIn:
		return graph.In, nil
	case DirectionAny:
		return graph.Any, nil
	}
	return "", invalidQuery(pointer("traverse", i, "direction"),
		fmt.Sprintf("must be %q, %q or %q (got %q)",
			DirectionOut, DirectionIn, DirectionAny, d))
}

// walk emits a step of more than one hop, as the two CTEs
// graph.WalkCTE produces plus one of this compiler's own reading them:
//
//	t0_w      the recursion
//	t0_w_out  the depth bound, the ordering and the row cap
//	t0        this step's own row shape, its output filters and its depth
//
// **Which filter goes inside the recursion and which outside is the whole
// design of this function**, and the two are not interchangeable:
//
//   - edge_where is a condition on the relation each hop walks, so it is
//     handed to graph.Walk as EdgePredicate and prunes the recursion. A
//     relation the document excluded is a relation the walk must not
//     follow; filtering it out of the *output* instead would still return
//     every node reachable behind it.
//   - to_type, where and the invalid-row exclusion are conditions on the
//     entity a hop reached, and they are applied here, outside. A walk
//     that pruned on them could not pass *through* a node of another type
//     to reach one of the right type, which is a picture the language
//     promises: "quests three steps up the prerequisite chain" does not
//     stop at the zone in the middle.
//
// TestAnEdgeWhereFiltersTheHopsAWalkFollows and
// TestAWalkDrawsOnlyItsDestinationTypeAndOnlyValidRows are the two sides.
//
// **The depth is absolute**, counted from the seed selector rather than
// from this step's own start, which is what makes Stats.MaxDepthReached
// answerable for a step that reads from another step. The walk counts
// from its own seed, so the seed row's own depth is added back: path[1]
// is the id the walk started from (Postgres arrays are 1-based).
//
// The from-set is grouped by id before that join, taking each seed's
// shortest depth. **Only the golden file observes it** — removing the
// grouping is red on testdata's expected statement and green everywhere
// else — and that is recorded rather than dressed up as a correctness
// guard: no behavioural test moves, because capOf deduplicates by id with
// ORDER BY id, rank, depth and therefore keeps the shallowest row of a
// node reached at two depths anyway. What it stops is this CTE holding
// one row per *spelling* of its seed, which is work and not an answer.
func (c *compiler) walk(i int, name frag, from cteRef) (frag, error) {
	step := c.r.Steps[i]
	direction, err := walkDirection(i, step.Step.Direction)
	if err != nil {
		return "", err
	}
	// The edge predicate is compiled against its own argument list, so it
	// arrives at graph.WalkCTE numbered from $1 like the seed and is
	// renumbered there. Compiled into this builder instead, its
	// placeholders would be renumbered a second time by adopt below.
	var edgeWhere frag
	var edgeArgs []any
	if step.EdgeWhere != nil {
		edgeWhere, edgeArgs, err = c.subPredicate(leafScope{alias: "r", edge: true}, step.EdgeWhere)
		if err != nil {
			return "", err
		}
	}
	// The two CTE names are built here as fragments, from a constant
	// prefix and an int, because this statement has to *reference* them
	// and a name is the one thing in a statement that cannot be a bind
	// parameter. graph.ReadFrom is asked for the second one rather than
	// assumed, so a change to that package's naming convention is a
	// refusal here instead of a statement referring to a CTE that no
	// longer exists.
	walkName := cteName(walkPrefix, i)
	outName := walkName + "_out"
	maxRows := c.r.Limits.MaxNodes * 4
	walk := graph.Walk{
		Name:      string(walkName),
		ProjectID: c.projectID,
		// DISTINCT because one row of a step is one edge traversal, so a
		// set can name the same entity several times: an anchor per
		// spelling would walk the whole graph below it once per spelling,
		// for rows the collection points then deduplicate anyway.
		SeedSQL:         string(sprintf("SELECT DISTINCT id FROM %s", from.name)),
		RelationTypeIDs: step.RelationTypeIDs,
		Direction:       direction,
		EdgePredicate:   string(edgeWhere),
		EdgeArgs:        edgeArgs,
		MinDepth:        step.Step.Depth.Min,
		// One hop *past* what the step asked for, which is how
		// Truncated.Depth is measured rather than inferred — the same
		// cap + 1 mechanism capOf uses for the node and edge caps. The
		// extra hop is dropped below and never reaches the picture; what
		// it buys is the difference between "the chain ends here" and "the
		// bound stopped here", which a designer cannot see any other way.
		//
		// **It is the widest level of the walk, and it costs about what
		// the whole walk below it costs.** Measured on this project's
		// Postgres, an eight-entity clique walked `any` at depth 1..4,
		// best of twenty-one runs: 36.1 ms as emitted, 18.3 ms with this
		// line reading Depth.Max — a factor of 1.97 on a dense graph,
		// because a breadth-first level of a clique is bigger than every
		// level before it put together. That is the price of the flag,
		// and it is stated here rather than left for a designer to
		// discover on a slow view: a one-hop step is not probed at all
		// (see Truncated.Depth) precisely because the same argument runs
		// the other way there. The probe's own predicate is free beside
		// it — the honest "is anything past the bound *new*" test below
		// measured 36.1 ms against 34.8 ms for the bare "is there a row
		// past the bound" it replaced, inside the run-to-run spread.
		MaxDepth: step.Step.Depth.Max + 1,
		// Four times the node cap, so a pathological branching factor
		// cannot build a giant intermediate before the outer limit
		// applies. Four rather than one because a walk legitimately
		// visits a node at several depths before the outer DISTINCT
		// collapses them.
		//
		// **The overflow row this asks for is read**, below, and it has
		// to be: internal/graph emits LIMIT MaxRows + 1 precisely so a
		// caller can tell a full walk from a truncated one, and a walk
		// row is an edge traversal, so four times the node cap can
		// collapse to far fewer nodes than the node cap — a picture
		// short of content with the node and the edge cap both unfired.
		// A cap whose overflow signal nothing reads is silent content
		// loss, which is this plan's worst failure mode.
		MaxRows: maxRows,
	}
	if graph.ReadFrom(walk) != string(outName) {
		return "", fmt.Errorf("views: internal/graph reads its walk from %q and this "+
			"statement refers to %q", graph.ReadFrom(walk), outName)
	}
	body, err := c.b.adopt(walk)
	if err != nil {
		return "", err
	}
	maxDepth := c.b.bind(step.Step.Depth.Max)
	// **The depth probe: is there anything past the bound that the
	// picture does not already hold?** Not "did the recursion produce a
	// row past the bound", which is a different and much weaker question:
	// on a dense or a cyclic graph deeper simple paths keep existing long
	// after they stop reaching anything new, so a complete picture — every
	// node and every edge of a clique, drawn — was reported cut short by
	// its depth bound. The predicate below asks for a node **or an edge**
	// past the bound that is not already inside it, which is the thing a
	// designer reads the flag as meaning.
	//
	// It reads the *recursion* rather than the wrapper, on both sides of
	// the comparison, because the wrapper applies MinDepth and the row
	// cap: a node the walk passed through below MinDepth is a node it
	// found, and re-finding it one hop past the bound is not new content.
	//
	// A relation is never null at a depth above zero, so the NOT IN over
	// via_relation is safe; the inner filter spells the exclusion anyway,
	// because a single null in a NOT IN list makes the whole test
	// unknowable and that is a flag stuck false.
	c.probes = append(c.probes, sprintf(`EXISTS (
        SELECT 1 FROM %[1]s deep
        WHERE deep.depth > %[2]s
          AND (deep.id NOT IN (SELECT id FROM %[1]s WHERE depth <= %[2]s)
            OR deep.via_relation NOT IN (SELECT via_relation FROM %[1]s
                                         WHERE depth <= %[2]s AND via_relation IS NOT NULL))
    )`, walkName, maxDepth))
	// **The row probe: did the walk's own row cap drop a traversal?**
	// internal/graph emits LIMIT MaxRows + 1 so that exactly this can be
	// asked, and this is the caller that asks it. The overflow row is
	// excluded from the picture below, by the same LIMIT, so the answer
	// is the cap + 1 mechanism used the way that package documents it
	// rather than an extra row quietly drawn.
	rowCap := c.b.bind(maxRows)
	c.rowProbes = append(c.rowProbes,
		sprintf("EXISTS (SELECT 1 FROM %s OFFSET %s)", outName, rowCap))

	nodeWhere, err := c.predicate(leafScope{alias: "far"}, step.Where)
	if err != nil {
		return "", err
	}
	var toType frag
	if len(step.ToTypeIDs) > 0 {
		toType = sprintf("\n     AND far.entity_type_id = ANY(%s::uuid[])",
			c.b.bind(step.ToTypeIDs))
	}
	return sprintf(`%s,
%s (id, set_name, from_id, via_relation, depth) AS (
    SELECT w.id, %s::text, w.from_id, w.via_relation, src.depth + w.depth
    FROM (SELECT * FROM %s LIMIT %s) w
    JOIN (SELECT id, MIN(depth) AS depth FROM %s GROUP BY id) src ON src.id = w.path[1]
    JOIN entities far
      ON far.id = w.id
     AND far.project_id = $1%s%s
    WHERE w.depth <= %s
      AND %s
)`, body, name, c.b.bind(step.Name), outName, rowCap, from.name,
		c.invalidFilter("far"), toType, maxDepth, nodeWhere), nil
}

// The column list both collection points produce, so the two arms of the
// final UNION ALL line up. A node fills the identity columns and leaves
// the endpoints null; an edge does the opposite.
// depth is last so that rank stays the eleventh column and the final
// ORDER BY does not move. It is the hops from a seed selector to the row,
// and it is what Stats.MaxDepthReached is read off: a walk that asked for
// four hops and found two must report two, which the declared depth of
// the step cannot say. Only node rows fill it — an edge's depth is the
// step's, and nothing reads it, so a from_step arm carries its step's
// depth and a between arm carries null.
const rowColumns frag = "(id, key, name, type_key, set_name, role, " +
	"source_id, target_id, fields, rank, depth, attrs, ambiguous)"

// nullColumns is one row of typed nothings, spelled once because two
// arms need it: the empty collection point below, and the depth arm of
// the final statement, which carries no graph element at all. The types
// have to be spelled, because a UNION of two untyped nulls has no type.
const nullColumns frag = "NULL::uuid, NULL::text, NULL::text, NULL::text, NULL::text, " +
	"NULL::text, NULL::uuid, NULL::uuid, NULL::jsonb, NULL::integer, NULL::integer, " +
	"NULL::jsonb, NULL::boolean"

// emptyRow is the typed nothing a collection point emits when the
// document asked for no nodes or no edges at all.
const emptyRow frag = "SELECT " + nullColumns + " WHERE false"

// depthTruncatedKind is the first column of the row that says a walk was
// cut short by its depth bound. It is not a graph element, so it is
// neither "node" nor "edge"; execute.go reads it and sets the flag.
const depthTruncatedKind frag = "depth_truncated"

// walkTruncatedKind is the first column of the row that says a walk hit
// the row cap it carries internally, so the traversals this statement was
// handed are a prefix of the ones the graph holds. It is neither a node
// nor an edge either; execute.go reads it and sets both element flags,
// because a dropped walk row is a (node, edge) pair and nothing here can
// say which of the two the picture actually came up short of.
const walkTruncatedKind frag = "walk_truncated"

// capOf is the body both collection points carry: the arms, deduplicated
// by id, ordered the way the result is, and cut at one row more than the
// cap.
//
// **The extra row is how truncation is detected rather than inferred.**
// With a plain LIMIT n, a result of exactly n rows and a graph that
// happens to hold n are the same answer, so the flag could only ever be a
// guess. With n + 1, the collection point that came back full says so by
// arriving one row over, and Run trims it.
//
// **The DISTINCT ON is what makes `max_nodes` a cap on nodes rather than
// on rows**, and it is here rather than only in Go because the LIMIT is
// here. UNION collapses two *identical* rows, but the same entity drawn
// by two `nodes` entries differs in set_name and rank, so it survives as
// two rows; before this, a graph of three quests declared as two
// overlapping sets arrived as six rows and was reported truncated at a
// cap of three — with the identical three nodes coming back either way.
// Counting rows never *under*-reported, so no truncated result was ever
// called complete, but an over-report is a designer told their picture is
// partial when it is whole, and the trim in Go could then also deliver
// fewer nodes than the cap allowed.
//
// The dedupe keeps `ORDER BY id, rank`: the lowest rank per id, which is
// the same "first entry that claimed it" rule the Go deduplication in
// execute.go applies, so the two cannot disagree about which set a node
// belongs to. The outer ordering is the one the final statement applies
// (rank, then id), and it has to be here as well as there: without it the
// rows the LIMIT keeps are whichever Postgres produced first, so a
// truncated result would drop a different arbitrary third of the graph on
// every run.
//
// Row count and node count are now the same number, which is what lets
// execute.go read `rows > cap` as an exact answer in both directions.
func (c *compiler) capOf(arms frag, limit int) frag {
	return sprintf(`    SELECT capped.*
    FROM (
        SELECT DISTINCT ON (all_rows.id) all_rows.*
        FROM (
    %s
        ) AS all_rows %s
        ORDER BY all_rows.id, all_rows.rank, all_rows.depth
    ) AS capped
    ORDER BY capped.rank, capped.id
    LIMIT %s`, arms, rowColumns, c.b.bind(limit+1))
}

// nodeUnion collects the sets the document asked to draw. Each arm joins
// its set back to entities and entity_types for the identity a renderer
// needs, and UNION rather than UNION ALL is what stops a node reached by
// two edges of one step arriving twice.
func (c *compiler) nodeUnion() (frag, error) {
	// The projection is compiled once and spelled into every arm: the
	// aliases it reads (e, et) are the same in each of them and its
	// placeholders are the same arguments, so a second compilation would
	// bind the same values a second time for no answer.
	attrs, ambiguous, laterals, err := c.projection("e", "et")
	if err != nil {
		return "", err
	}
	// The same, for the same reason: `project.fields` binds one key per
	// entry, and binding them again per arm would put the same value in
	// the argument list as many times as the document draws sets.
	payload := c.payload("e")
	var arms []frag
	for i, entry := range c.r.Query.Nodes {
		ref, ok := c.cte[entry.Set]
		if !ok {
			return "", invalidQuery(pointer("nodes", i, "set"),
				fmt.Sprintf("no set named %q is declared", entry.Set))
		}
		arms = append(arms, sprintf(`SELECT e.id, e.key, e.name, et.key, %s.set_name, %s::text,
           NULL::uuid, NULL::uuid, %s, %s::integer, %s.depth, %s, %s
    FROM %s
    JOIN entities e ON e.id = %s.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1%s`,
			ref.name, c.b.bind(entry.Role), payload, c.b.bind(i),
			ref.name, attrs, ambiguous, ref.name, ref.name, laterals))
	}
	if len(arms) == 0 {
		arms = append(arms, emptyRow)
	}
	return sprintf("%s %s AS (\n%s\n)", nodeRows, rowColumns,
		c.capOf(joinFrags(arms, "\n  UNION\n    "), c.r.Limits.MaxNodes)), nil
}

// edgeUnion collects the relations the document asked to draw, in the two
// spellings §2.2 gives them: the relations a step actually walked, and
// relations of a named type drawn *between* two sets already in the
// result.
func (c *compiler) edgeUnion() (frag, error) {
	var arms []frag
	for i := range c.r.Query.Edges {
		arm, err := c.edge(i)
		if err != nil {
			return "", err
		}
		arms = append(arms, arm)
	}
	if len(arms) == 0 {
		arms = append(arms, emptyRow)
	}
	return sprintf("%s %s AS (\n%s\n)", edgeRows, rowColumns,
		c.capOf(joinFrags(arms, "\n  UNION\n    "), c.r.Limits.MaxEdges)), nil
}

func (c *compiler) edge(i int) (frag, error) {
	spec := c.r.Edges[i]
	rank := c.b.bind(i)
	fields := c.fieldsOf("r")
	label, err := c.edgeLabel(&spec, "r", "rt")
	if err != nil {
		return "", err
	}
	if spec.Spec.FromStep != "" {
		ref, ok := c.cte[spec.Spec.FromStep]
		if !ok {
			return "", invalidQuery(pointer("edges", i, "from_step"),
				fmt.Sprintf("no set named %q is declared", spec.Spec.FromStep))
		}
		if !ref.step {
			// A selector walks no relation, so there is nothing for this
			// entry to draw. Answering with an empty edge set would be the
			// silent-empty-picture failure §3 calls the most expensive one.
			return "", invalidQuery(pointer("edges", i, "from_step"),
				fmt.Sprintf("names the selector %q, which walks no relations: name a traverse "+
					"step, or draw relations between sets with \"via\" and \"between\"",
					spec.Spec.FromStep))
		}
		return sprintf(`SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, %s, %s::integer, %s.depth, %s, NULL::boolean
    FROM %s
    JOIN relations r ON r.id = %s.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1`,
			fields, rank, ref.name, label, ref.name, ref.name), nil
	}

	side := func(j int) (frag, error) {
		ref, ok := c.cte[spec.Spec.Between[j]]
		if !ok {
			return "", invalidQuery(pointer("edges", i, "between", j),
				fmt.Sprintf("no set named %q is declared", spec.Spec.Between[j]))
		}
		return ref.name, nil
	}
	if len(spec.Spec.Between) != 2 {
		return "", invalidQuery(pointer("edges", i, "between"),
			fmt.Sprintf("must name exactly two sets (got %d)", len(spec.Spec.Between)))
	}
	left, err := side(0)
	if err != nil {
		return "", err
	}
	right, err := side(1)
	if err != nil {
		return "", err
	}
	forward := sprintf("(r.source_id IN (SELECT id FROM %s) AND "+
		"r.target_id IN (SELECT id FROM %s))", left, right)
	backward := sprintf("(r.source_id IN (SELECT id FROM %s) AND "+
		"r.target_id IN (SELECT id FROM %s))", right, left)
	var endpoints frag
	switch spec.Spec.Direction {
	case DirectionOut:
		endpoints = forward
	case DirectionIn:
		endpoints = backward
	case DirectionAny:
		endpoints = sprintf("(%s OR %s)", forward, backward)
	default:
		// Including the empty string, which applyDefaults fills — the
		// same rule a step follows, rather than the silent "out" this arm
		// used to read an empty direction as.
		return "", invalidQuery(pointer("edges", i, "direction"),
			fmt.Sprintf("must be %q, %q or %q (got %q)",
				DirectionOut, DirectionIn, DirectionAny, spec.Spec.Direction))
	}
	return sprintf(`SELECT r.id, NULL::text, NULL::text, rt.key, NULL::text, NULL::text,
           r.source_id, r.target_id, %s, %s::integer, NULL::integer, %s, NULL::boolean
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY(%s::uuid[])
      AND %s`, fields, rank, label, c.b.bind(spec.RelationTypeIDs), endpoints), nil
}

// leafScope is what a predicate is being compiled against: the alias its
// leaves address, and whether that alias is a relation.
//
// The distinction is not cosmetic. A relation has no key, no name and no
// invalid flag — 0004_metamodel.sql gives it an id, a type, two
// endpoints, its fields and its timestamps and nothing else — so
// @name, @key and @invalid have no column to compile against on an edge.
// Resolution refuses them there (fieldScope.edge), and this is the second
// half of the same rule, for a *Resolved a Go caller built by hand.
type leafScope struct {
	alias frag
	edge  bool
}

// predicate compiles one resolved tree into a parenthesised boolean
// expression. An absent tree is `true`, which is what makes a selector
// with no `where` a selector over the whole type.
func (c *compiler) predicate(sc leafScope, p *ResolvedPredicate) (frag, error) {
	if p == nil {
		return "true", nil
	}
	switch {
	case p.All != nil:
		return c.combine(sc, p.All, " AND ")
	case p.Any != nil:
		return c.combine(sc, p.Any, " OR ")
	case p.Not != nil:
		inner, err := c.predicate(sc, p.Not)
		if err != nil {
			return "", err
		}
		return sprintf("NOT (%s)", inner), nil
	case p.Leaf != nil:
		return c.leaf(sc, p.Leaf)
	}
	return "true", nil
}

// combine is `all` and `any` over their children.
//
// **An empty list compiles to `true` under both spellings**, which for
// `any` reads "no condition matches nothing" as "no condition filters
// nothing". Pre-existing from Task 6, where it only ever widened a
// projection; since Task 8 the same `true` can be an `edge_where`, where
// it prunes a recursion, and "follow every edge" is arguably the wrong
// reading of "follow edges satisfying none of these". It is recorded
// rather than changed, because the change belongs with the validation
// pass that would refuse an empty list outright — Task 15's — and a
// silent flip of the identity element between tasks is worse than either
// reading. Say why before changing it.
func (c *compiler) combine(sc leafScope, children []ResolvedPredicate, sep frag) (frag, error) {
	if len(children) == 0 {
		return "true", nil
	}
	parts := make([]frag, 0, len(children))
	for i := range children {
		part, err := c.predicate(sc, &children[i])
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	return sprintf("(%s)", joinFrags(parts, sep)), nil
}

// operandOf is one leaf's left-hand side: the SQL expression holding the
// value, the guard that has to hold before that expression may be
// evaluated, and the expression that says the value is present at all.
type operandOf struct {
	alias frag
	value frag
	guard frag
	exist frag
	// path is the jsonb value itself, for the operators that work on a
	// container rather than on a scalar. Empty for a column.
	path frag
	// key is the bound placeholder for a declared field's key, reused
	// rather than re-bound so one leaf costs one argument.
	key frag
	// fold compares case-insensitively, which is what a *row key* is:
	// entities_key_key is UNIQUE (project_id, entity_type_id, lower(key)),
	// so folding one side only would refuse a spelling the write path
	// accepted.
	fold bool
}

func (c *compiler) leaf(sc leafScope, leaf *ResolvedLeaf) (frag, error) {
	leaf, err := c.withParam(leaf)
	if err != nil {
		return "", err
	}
	if leaf.Field.Builtin && leaf.Field.Key == AttrType {
		return c.typeLeaf(sc, leaf)
	}
	op, err := c.operand(sc, leaf)
	if err != nil {
		return "", err
	}
	return c.compare(op, leaf)
}

// withParam replaces a {"param": …} operand with the value this run
// bound for it. Resolution has already checked that the parameter is
// declared and that its declared type matches the field's, so what is
// left here is the one thing only a run can know: whether it has a value
// at all. A parameter declared without a default and left unbound is
// refused rather than compiled as NULL, which would draw nothing.
func (c *compiler) withParam(leaf *ResolvedLeaf) (*ResolvedLeaf, error) {
	ref, ok := leaf.Value.(ParamRef)
	if !ok {
		return leaf, nil
	}
	value, bound := c.r.Params[ref.Key]
	if !bound {
		return nil, invalidQuery(leaf.Pointer+"/value",
			fmt.Sprintf("the parameter %q has no value for this run: give it a default in "+
				"params, or supply it when running the view", ref.Key))
	}
	copied := *leaf
	copied.Value = value
	return &copied, nil
}

// operand builds the left-hand side of a leaf, for a built-in column or
// for a declared jsonb field.
//
// **The type guard comes before the cast, always.** internal/metamodel
// marks an entity invalid when a schema change makes its values not fit
// and leaves the values in place (MarkEntitiesOfTypeInvalid), so a field
// declared `number` today may hold a string written yesterday. Without
// jsonb_typeof the cast raises SQLSTATE 22P02 and the whole view fails on
// one stale row — which is exactly the row include_invalid exists to let
// a designer look at.
func (c *compiler) operand(sc leafScope, leaf *ResolvedLeaf) (operandOf, error) {
	if leaf.Field.Builtin {
		column, err := builtinColumn(sc, leaf.Field.Key)
		if err != nil {
			return operandOf{}, invalidQuery(leaf.Pointer+"/field", err.Error())
		}
		return operandOf{
			alias: sc.alias,
			value: column,
			exist: sprintf("(%s IS NOT NULL)", column),
			fold:  leaf.Field.Key == AttrKey,
		}, nil
	}
	key := c.b.bind(leaf.Field.Key)
	path := sprintf("(%s.fields -> %s)", sc.alias, key)
	op := operandOf{
		alias: sc.alias,
		path:  path,
		key:   key,
		exist: sprintf("(%s.fields ? %s)", sc.alias, key),
	}
	switch leaf.Type {
	case metamodel.FieldNumber:
		op.value = sprintf("(%s.fields ->> %s)::numeric", sc.alias, key)
		op.guard = sprintf("jsonb_typeof(%s) = 'number'", path)
	case metamodel.FieldBool:
		op.value = sprintf("%s::boolean", path)
		op.guard = sprintf("jsonb_typeof(%s) = 'boolean'", path)
	case metamodel.FieldListText:
		op.value = path
		op.guard = sprintf("jsonb_typeof(%s) = 'array'", path)
	default:
		// text, longtext and enum are all one jsonb string.
		op.value = sprintf("(%s.fields ->> %s)", sc.alias, key)
		op.guard = sprintf("jsonb_typeof(%s) = 'string'", path)
	}
	return op, nil
}

// builtinColumn maps an @-sigil built-in onto the column that holds it.
// @type is not here: it is a foreign key rather than a value, and
// typeLeaf handles it.
func builtinColumn(sc leafScope, name string) (frag, error) {
	if sc.edge {
		if name == AttrCreatedAt {
			return sprintf("%s.created_at", sc.alias), nil
		}
		return "", fmt.Errorf("%s cannot be compared on a relation: an edge carries its type, "+
			"its creation time and its declared fields, and has no key, name or invalid flag "+
			"of its own — compare %s or %s here", name, AttrType, AttrCreatedAt)
	}
	switch name {
	case AttrName:
		return sprintf("%s.name", sc.alias), nil
	case AttrKey:
		return sprintf("%s.key", sc.alias), nil
	case AttrInvalid:
		return sprintf("%s.invalid", sc.alias), nil
	case AttrCreatedAt:
		return sprintf("%s.created_at", sc.alias), nil
	}
	return "", fmt.Errorf("%q is not a built-in: the built-ins are %s",
		name, strings.Join(builtinNames, ", "))
}

// typeLeaf compiles @type.
//
// **A row holds its type as an id, so eq, neq and in compile against
// entity_type_id and a key that names no declared type is refused** —
// not answered with an empty picture. Task 4 left this decision here and
// recorded both halves of it: the alternative, comparing the key as text,
// turns `@type eq "qeust"` into a view that draws nothing and says
// nothing, which §3 calls the most expensive failure mode this language
// has. The refusal is raised by the compiler rather than by resolution,
// so a caller that wants to catch it before running has to compile —
// which views.validate (Task 15) does.
//
// The pattern operators cannot be answered by an id, so they compile
// against the type's key through a scalar subquery, project-filtered like
// every other table reference here. @type still contributes no TypeRef:
// whether a value is a structural dependency is view_refs' question, and
// Task 11 owns it.
func (c *compiler) typeLeaf(sc leafScope, leaf *ResolvedLeaf) (frag, error) {
	var column, table frag = "entity_type_id", "entity_types"
	lookup := func(key string) (uuid.UUID, bool) {
		row, ok := c.r.Cat.EntityTypes[strings.ToLower(key)]
		return row.ID, ok
	}
	if sc.edge {
		column, table = "relation_type_id", "relation_types"
		lookup = func(key string) (uuid.UUID, bool) {
			row, ok := c.r.Cat.RelationTypes[strings.ToLower(key)]
			return row.ID, ok
		}
	}
	idOf := func(raw any) (uuid.UUID, error) {
		key, ok := raw.(string)
		if !ok {
			return uuid.Nil, invalidQuery(leaf.Pointer+"/value",
				fmt.Sprintf("%s is compared with a type key, which is text (got %T)",
					AttrType, raw))
		}
		id, found := lookup(key)
		if !found {
			return uuid.Nil, invalidQuery(leaf.Pointer+"/value",
				fmt.Sprintf("no type %q in this game: %s compares against a declared type, and "+
					"a name this game does not have would draw nothing and say nothing", key,
					AttrType))
		}
		return id, nil
	}

	switch leaf.Op {
	case OpEq, OpNeq:
		id, err := idOf(leaf.Value)
		if err != nil {
			return "", err
		}
		var comparison frag = "="
		if leaf.Op == OpNeq {
			comparison = "<>"
		}
		return sprintf("(%s.%s %s %s)", sc.alias, column, comparison, c.b.bind(id)), nil
	case OpIn:
		ids := make([]uuid.UUID, 0, len(leaf.Values))
		for _, raw := range leaf.Values {
			id, err := idOf(raw)
			if err != nil {
				return "", err
			}
			ids = append(ids, id)
		}
		return sprintf("(%s.%s = ANY(%s::uuid[]))", sc.alias, column, c.b.bind(ids)), nil
	case OpExists:
		// Every row has a type: the column is NOT NULL. So this is the
		// caller's own operand, and `exists: false` is an empty set.
		return sprintf("(true = %s::boolean)", c.b.bind(leaf.Value)), nil
	}
	// contains, starts_with and matches ask about the spelling of the
	// key, which the row does not hold.
	value := sprintf("(SELECT ty.key FROM %s ty WHERE ty.id = %s.%s AND ty.project_id = $1)",
		table, sc.alias, column)
	return c.compare(operandOf{value: value, exist: sprintf("(%s IS NOT NULL)", value)}, leaf)
}

// compare joins a leaf's operand to its operator and its bound values.
func (c *compiler) compare(op operandOf, leaf *ResolvedLeaf) (frag, error) {
	guarded := func(expr frag) frag {
		if op.guard == "" {
			return sprintf("(%s)", expr)
		}
		return sprintf("(%s AND %s)", op.guard, expr)
	}
	value := op.value
	fold := func(raw any) any { return raw }
	if op.fold {
		value = sprintf("lower(%s)", op.value)
		fold = func(raw any) any {
			if s, ok := raw.(string); ok {
				return strings.ToLower(s)
			}
			return raw
		}
	}

	switch leaf.Op {
	case OpExists:
		return sprintf("(%s = %s::boolean)", op.exist, c.b.bind(leaf.Value)), nil

	case OpEmpty:
		// COALESCE, because a row that does not carry the field at all is
		// neither an empty list nor a non-empty one, and `empty: false`
		// must not draw it.
		return sprintf("(COALESCE(%s AND jsonb_array_length(%s) = 0, false) = %s::boolean)",
			op.guard, op.path, c.b.bind(leaf.Value)), nil

	case OpLengthEq, OpLengthGte, OpLengthLte:
		comparison, err := sqlComparison(lengthComparison(leaf.Op))
		if err != nil {
			return "", err
		}
		return guarded(sprintf("jsonb_array_length(%s) %s %s::numeric",
			op.path, comparison, c.b.bind(leaf.Value))), nil

	case OpContains:
		if leaf.Type == metamodel.FieldListText {
			// Containment rather than a scan, and containment over the
			// whole `fields` column rather than over one path, because
			// that is the shape entities_fields_idx — gin (fields
			// jsonb_path_ops) — can actually serve. It is the one leaf in
			// this compiler an index answers.
			text, ok := leaf.Value.(string)
			if !ok {
				return "", invalidQuery(leaf.Pointer+"/value",
					fmt.Sprintf("an element of a list<text> field is text (got %T)", leaf.Value))
			}
			return sprintf("(%s.fields @> jsonb_build_object(%s::text, "+
				"jsonb_build_array(%s::text)))", op.alias, op.key, c.b.bind(text)), nil
		}
		return guarded(sprintf(`%s ILIKE %s ESCAPE '\'`,
			value, c.b.bind(likePattern(leaf.Op, leaf.Value)))), nil

	case OpContainsAny, OpContainsAll:
		var operator frag = "?|"
		if leaf.Op == OpContainsAll {
			operator = "?&"
		}
		texts, err := textList(leaf.Values, leaf.Pointer)
		if err != nil {
			return "", err
		}
		return guarded(sprintf("%s %s %s::text[]", op.path, operator, c.b.bind(texts))), nil

	case OpBetween:
		if len(leaf.Values) != 2 {
			return "", invalidQuery(leaf.Pointer+"/value",
				fmt.Sprintf("between takes exactly two values (got %d)", len(leaf.Values)))
		}
		return guarded(sprintf("%s BETWEEN %s AND %s",
			value, c.b.bind(fold(leaf.Values[0])), c.b.bind(fold(leaf.Values[1])))), nil

	case OpIn:
		list, cast, err := typedList(leaf.Type, leaf.Values, fold, leaf.Pointer)
		if err != nil {
			return "", err
		}
		return guarded(sprintf("%s = ANY(%s%s)", value, c.b.bind(list), cast)), nil

	case OpStartsWith, OpMatches:
		return guarded(sprintf(`%s ILIKE %s ESCAPE '\'`,
			value, c.b.bind(likePattern(leaf.Op, leaf.Value)))), nil

	default:
		comparison, err := sqlComparison(leaf.Op)
		if err != nil {
			return "", err
		}
		return guarded(sprintf("%s %s %s", value, comparison, c.b.bind(fold(leaf.Value)))), nil
	}
}

// sqlComparison maps the six scalar comparisons onto their operators and
// **refuses anything else**, because an operator that reached here
// without a comparison is a compiler bug rather than a caller's mistake.
//
// It returns an error rather than panicking: Compile is exported, takes a
// *Resolved a Go caller may have built by hand, and a panic in a query
// engine reached from an MCP tool takes the process with it. The message
// says which of the two it is.
func sqlComparison(op Operator) (frag, error) {
	switch op {
	case OpEq:
		return "=", nil
	case OpNeq:
		return "<>", nil
	case OpLt:
		return "<", nil
	case OpLte:
		return "<=", nil
	case OpGt:
		return ">", nil
	case OpGte:
		return ">=", nil
	}
	return "", fmt.Errorf("views: the operator %q reached the comparison emitter, which knows "+
		"only eq, neq, lt, lte, gt and gte; this is a compiler bug, not a caller's mistake", op)
}

// lengthComparison is the scalar comparison a length operator counts with.
func lengthComparison(op Operator) Operator {
	switch op {
	case OpLengthEq:
		return OpEq
	case OpLengthGte:
		return OpGte
	case OpLengthLte:
		return OpLte
	}
	return op
}

// escapeLike walks a caller's operand once, escaping the three characters
// LIKE reads — backslash, per-cent and underscore, so that `starts_with:
// "50%"` matches a name starting "50%" rather than a name starting "50",
// with ESCAPE '\\' in the emitted pattern saying which character escapes
// — and, when glob is set, mapping `*` and `?` onto their LIKE
// equivalents *as it goes*.
//
// One pass rather than chained replacers, because the chained version was
// wrong: it escaped the caller's characters and then ran a second
// replacer that turned every `\%` back into a bare `%`, undoing the
// escape it had just written. `matches "50%"` came out as the pattern
// `50%` — a prefix match on "50" — and `matches "a_b"` as `a_b`, matching
// any character between the a and the b. A single pass cannot undo its
// own work, because it never looks at a character it already wrote.
func escapeLike(s string, glob bool) string {
	var out strings.Builder
	out.Grow(len(s) + 4)
	for _, r := range s {
		switch {
		case glob && r == '*':
			out.WriteByte('%')
		case glob && r == '?':
			out.WriteByte('_')
		case r == '\\' || r == '%' || r == '_':
			out.WriteByte('\\')
			out.WriteRune(r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// likePattern turns one text operand into the ILIKE pattern its operator
// means. `matches` is the literal-anchored glob the operator table
// promises: `*` becomes `%` and `?` becomes `_`, every other character is
// a literal — including a per-cent or an underscore the caller wrote,
// which is escaped — and there is no regex operator here.
func likePattern(op Operator, raw any) string {
	value, ok := raw.(string)
	if !ok {
		value = fmt.Sprint(raw)
	}
	switch op {
	case OpContains:
		return "%" + escapeLike(value, false) + "%"
	case OpStartsWith:
		return escapeLike(value, false) + "%"
	case OpMatches:
		return escapeLike(value, true)
	}
	return escapeLike(value, false)
}

// textList narrows a resolved operand list to the []string a text array
// bind needs.
func textList(values []any, ptr string) ([]string, error) {
	out := make([]string, 0, len(values))
	for i, raw := range values {
		text, ok := raw.(string)
		if !ok {
			return nil, invalidQuery(pointer(ptr, "value", i),
				fmt.Sprintf("must be text (got %T)", raw))
		}
		out = append(out, text)
	}
	return out, nil
}

// typedList binds an `in` operand list as an array of the field's own
// type, with the cast that keeps Postgres from having to guess. A list
// bound as `any` would arrive as text and compare a number against its
// own spelling.
func typedList(typ metamodel.FieldType, values []any, fold func(any) any,
	ptr string) (any, frag, error) {
	switch typ {
	case metamodel.FieldNumber:
		out := make([]float64, 0, len(values))
		for i, raw := range values {
			number, ok := raw.(float64)
			if !ok {
				return nil, "", invalidQuery(pointer(ptr, "value", i),
					fmt.Sprintf("must be a number (got %T)", raw))
			}
			out = append(out, number)
		}
		return out, "::numeric[]", nil
	case metamodel.FieldBool:
		out := make([]bool, 0, len(values))
		for i, raw := range values {
			flag, ok := raw.(bool)
			if !ok {
				return nil, "", invalidQuery(pointer(ptr, "value", i),
					fmt.Sprintf("must be true or false (got %T)", raw))
			}
			out = append(out, flag)
		}
		return out, "::boolean[]", nil
	case TypeTimestamp:
		out := make([]time.Time, 0, len(values))
		for i, raw := range values {
			at, ok := raw.(time.Time)
			if !ok {
				return nil, "", invalidQuery(pointer(ptr, "value", i),
					fmt.Sprintf("must be a timestamp (got %T)", raw))
			}
			out = append(out, at)
		}
		return out, "::timestamptz[]", nil
	default:
		texts := make([]string, 0, len(values))
		for i, raw := range values {
			text, ok := fold(raw).(string)
			if !ok {
				return nil, "", invalidQuery(pointer(ptr, "value", i),
					fmt.Sprintf("must be text (got %T)", raw))
			}
			texts = append(texts, text)
		}
		return texts, "::text[]", nil
	}
}
