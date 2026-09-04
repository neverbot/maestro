package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

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
// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere reads this
// file's own syntax tree and refuses a conversion outside the four
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

// The two CTE prefixes and the two collection points, spelled once.
const (
	seedPrefix frag = "s"
	stepPrefix frag = "t"
	nodeRows   frag = "node_rows"
	edgeRows   frag = "edge_rows"
)

// compileOptions are the parts of one *run* the compiler needs, as
// opposed to the parts of the query, which live on Resolved. There is one
// today; Task 9's projection adds to it.
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
// **What it does not do yet**, so this comment does not claim a compiler
// that is finished: a traversal step deeper than one hop is refused
// rather than emitted (Task 8 routes it through internal/graph), the
// projection's label, colour and grouping attributes are resolved but not
// applied (Task 9), an edge entry's label_from is not read, and no bound
// from Resolved.Limits appears in the statement (Task 7). Each of those
// is a refusal or a documented absence, never a silently wrong answer,
// except the projection, which is an absence a designer can see.
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
	c := &compiler{b: b, r: r, opts: opts, cte: map[string]cteRef{}}

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

	// WITH RECURSIVE even though nothing recurses yet: Task 8's multi-hop
	// steps arrive as recursive CTEs in this same list, and RECURSIVE is
	// a property of the WITH clause rather than of a single CTE.
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
	b.write(" e\nORDER BY 1, 11, 2")
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
	b    *builder
	r    *Resolved
	opts compileOptions
	cte  map[string]cteRef
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
	return sprintf(`%s (id, set_name) AS (
    SELECT e.id, %s::text
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
// A step deeper than one hop is **refused**, with the pointer that names
// it, rather than emitted as a one-hop walk that would answer the wrong
// question in silence. Task 8 replaces the refusal with a
// graph.WalkCTE call.
func (c *compiler) step(i int) (frag, error) {
	step := c.r.Steps[i]
	name := cteName(stepPrefix, i)
	if step.Name != "" {
		c.cte[step.Name] = cteRef{name: name, step: true}
	}
	if step.Step.Depth != nil && step.Step.Depth.Max > 1 {
		return "", invalidQuery(pointer("traverse", i, "depth"),
			fmt.Sprintf("asks for %d hops and this build walks one: multi-hop traversal is "+
				"not implemented yet — use depth 1, or several steps chained by \"from\"",
				step.Step.Depth.Max))
	}
	from, ok := c.cte[step.FromSet]
	if !ok {
		// Unreachable through ParseQuery, which refuses a step that reads
		// a set no earlier selector or step declared.
		return "", invalidQuery(pointer("traverse", i, "from"),
			fmt.Sprintf("no set named %q was compiled before this step", step.FromSet))
	}

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
	return sprintf(`%s (id, set_name, from_id, via_relation) AS (
    SELECT %s, %s::text, near.id, r.id
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

// The column list both collection points produce, so the two arms of the
// final UNION ALL line up. A node fills the identity columns and leaves
// the endpoints null; an edge does the opposite.
const rowColumns frag = "(id, key, name, type_key, set_name, role, " +
	"source_id, target_id, fields, rank)"

// emptyRow is the typed nothing a collection point emits when the
// document asked for no nodes or no edges at all. Its column types have
// to be spelled, because a UNION of two untyped nulls has no type.
const emptyRow frag = "SELECT NULL::uuid, NULL::text, NULL::text, NULL::text, NULL::text, " +
	"NULL::text, NULL::uuid, NULL::uuid, NULL::jsonb, NULL::integer WHERE false"

// nodeUnion collects the sets the document asked to draw. Each arm joins
// its set back to entities and entity_types for the identity a renderer
// needs, and UNION rather than UNION ALL is what stops a node reached by
// two edges of one step arriving twice.
func (c *compiler) nodeUnion() (frag, error) {
	var arms []frag
	for i, entry := range c.r.Query.Nodes {
		ref, ok := c.cte[entry.Set]
		if !ok {
			return "", invalidQuery(pointer("nodes", i, "set"),
				fmt.Sprintf("no set named %q is declared", entry.Set))
		}
		arms = append(arms, sprintf(`SELECT e.id, e.key, e.name, et.key, %s.set_name, %s::text,
           NULL::uuid, NULL::uuid, %s, %s::integer
    FROM %s
    JOIN entities e ON e.id = %s.id AND e.project_id = $1
    JOIN entity_types et ON et.id = e.entity_type_id AND et.project_id = $1`,
			ref.name, c.b.bind(entry.Role), c.fieldsOf("e"), c.b.bind(i),
			ref.name, ref.name))
	}
	if len(arms) == 0 {
		arms = append(arms, emptyRow)
	}
	return sprintf("%s %s AS (\n    %s\n)", nodeRows, rowColumns,
		joinFrags(arms, "\n  UNION\n    ")), nil
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
	return sprintf("%s %s AS (\n    %s\n)", edgeRows, rowColumns,
		joinFrags(arms, "\n  UNION\n    ")), nil
}

func (c *compiler) edge(i int) (frag, error) {
	spec := c.r.Edges[i]
	rank := c.b.bind(i)
	fields := c.fieldsOf("r")
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
           r.source_id, r.target_id, %s, %s::integer
    FROM %s
    JOIN relations r ON r.id = %s.via_relation AND r.project_id = $1
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1`,
			fields, rank, ref.name, ref.name), nil
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
           r.source_id, r.target_id, %s, %s::integer
    FROM relations r
    JOIN relation_types rt ON rt.id = r.relation_type_id AND rt.project_id = $1
    WHERE r.project_id = $1
      AND r.relation_type_id = ANY(%s::uuid[])
      AND %s`, fields, rank, c.b.bind(spec.RelationTypeIDs), endpoints), nil
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
