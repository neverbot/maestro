package views

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the projection: how a node presents itself, and the one
// hop a presentation attribute may take to find its value somewhere else.
type ResolvedProjection struct {
	Slots []ResolvedAttr
	// Fields are the declared field keys `project.fields` asked to be
	// carried on every node. They are the *narrow* half of the payload
	// rule: a run with include_fields off and this list non-empty gets
	// exactly these keys, which is what §5.5's token discipline is for.
	Fields []string
}

// ResolvedAttr is one projection slot.
type ResolvedAttr struct {
	// Name is the member the slot was written under, and is also the key
	// it comes back under in Node.Attrs. A renderer reads attrs["color_by"]
	// without knowing what the document put there.
	Name string
	// Attr is the scalar spelling: a built-in with its sigil, or a
	// declared field key. Empty when Related is set.
	Attr string
	// Related is the one-hop spelling.
	Related *ResolvedHop
}

// ResolvedHop is a one-hop related attribute with its types resolved.
type ResolvedHop struct {
	Hop *RelHop
	// Attr is what to read off the far entity: a built-in with its sigil,
	// or a declared field key. It defaults to @name — the far entity's
	// name is what "coloured by zone" means — and applyDefaults fills it,
	// so an empty one here is a hand-built *Resolved and is refused.
	Attr           string
	RelationTypeID *uuid.UUID
	// EntityTypeID narrows the far side. It is nil when the hop named no
	// type, which is a hop over every entity the relation reaches rather
	// than a refusal: a relation type whose target is always one type
	// needs no `type`, and the metamodel does not constrain a relation's
	// endpoints, so there is nothing to infer one from.
	EntityTypeID *uuid.UUID
}

// projectionScope is the set of entity types a projection's attributes
// are read against: every type the document draws nodes of.
type projectionScope struct {
	// subject names what the schemas belong to, for the refusal message.
	subject string
	names   []string
	schemas []metamodel.Schema
	// open marks a scope that cannot judge a key at all, because the
	// query reaches entities of a type it does not name — a traverse step
	// with no to_type. Refusing a key there would refuse a projection
	// over a type the document never had a chance to declare.
	open bool
	// unresolved marks a scope built from types that resolved to nothing.
	// The missing type is already a reported problem, and adding "and its
	// fields are not declared" to it is one refusal saying the same thing
	// twice.
	unresolved bool
}

// declares says whether a key is worth keeping, or why it is not.
func (sc projectionScope) declares(key string) error {
	if sc.open || sc.unresolved {
		return nil
	}
	for _, schema := range sc.schemas {
		for i := range schema {
			if schema[i].Key == key {
				return nil
			}
		}
	}
	return staleScope(DiagFieldMissing, "",
		"no field %q is declared on %s (%s): use types.get to see a type's "+
			"field_schema, or address a built-in with an @ sigil",
		key, sc.subject, strings.Join(sc.names, ", "))
}

// nodeScopeOf is the scope a node projection is judged against: the type
// of every selector, plus the destination types of every step. A step
// with no to_type opens the scope, because it reaches entities of any
// type and no schema applies.
func nodeScopeOf(cat *Catalogue, q *Query, st *staleness) projectionScope {
	sc := projectionScope{subject: "the entity types this query draws"}
	seen := map[uuid.UUID]bool{}
	add := func(ptr, key string) {
		row, ok := st.entityTypeAt(cat, ptr, key, false)
		if !ok || seen[row.ID] {
			return
		}
		seen[row.ID] = true
		sc.names = append(sc.names, row.Key)
		sc.schemas = append(sc.schemas, cat.schemas[row.ID])
	}
	for i, sel := range q.From {
		add(pointer("from", i, "type"), sel.Type)
	}
	for i, step := range q.Traverse {
		if len(step.ToType) == 0 {
			sc.open = true
			continue
		}
		for j, key := range step.ToType {
			add(pointer("traverse", i, "to_type", j), key)
		}
	}
	sc.unresolved = len(sc.names) == 0
	return sc
}

// resolveProjection turns the document's projection into the compiler's,
// reporting every problem at the pointer the caller wrote.
func resolveProjection(cat *Catalogue, q *Query, add func(ptr, message string),
	entityType func(ptr, key string) *dbq.EntityType,
	relationType func(ptr, key string) *dbq.RelationType,
	st *staleness) ResolvedProjection {
	var out ResolvedProjection
	if q.Project == nil {
		return out
	}
	scope := nodeScopeOf(cat, q, st)
	for _, attr := range projectionAttrs {
		ref := attr.Of(q.Project)
		if ref == nil {
			continue
		}
		ptr := pointer("project", attr.Name)
		if ref.Related == nil {
			if !strings.HasPrefix(ref.Attr, "@") {
				if err := scope.declares(ref.Attr); err != nil {
					add(ptr, err.Error())
					st.noteScope(err, ptr, ref.Attr)
					continue
				}
			}
			out.Slots = append(out.Slots, ResolvedAttr{Name: attr.Name, Attr: ref.Attr})
			continue
		}
		hop := ref.Related
		resolved := ResolvedHop{Hop: hop, Attr: hop.Attr}
		if row := relationType(ptr+"/related/via", hop.Via); row != nil {
			id := row.ID
			resolved.RelationTypeID = &id
		}
		var far *dbq.EntityType
		if hop.Type != "" {
			if far = entityType(ptr+"/related/type", hop.Type); far != nil {
				id := far.ID
				resolved.EntityTypeID = &id
			}
		}
		if !strings.HasPrefix(hop.Attr, "@") && hop.Attr != "" {
			// The far side is judged against the type the hop names, and
			// a hop that names none is open: nothing says what a relation
			// reaches.
			farScope := projectionScope{subject: "the entity type this hop reaches"}
			if far != nil {
				farScope.names = append(farScope.names, far.Key)
				farScope.schemas = append(farScope.schemas, cat.schemas[far.ID])
			}
			farScope.unresolved = len(farScope.names) == 0
			// A hop that names no type reaches entities of any type, so
			// no schema applies and no key can be judged.
			farScope.open = hop.Type == ""
			if err := farScope.declares(hop.Attr); err != nil {
				add(ptr+"/related/attr", err.Error())
				st.noteScope(err, ptr+"/related/attr", hop.Attr)
				continue
			}
		}
		out.Slots = append(out.Slots, ResolvedAttr{Name: attr.Name, Related: &resolved})
	}
	for i, key := range q.Project.Fields {
		if err := scope.declares(key); err != nil {
			at := pointer("project", "fields", i)
			add(at, err.Error())
			st.noteScope(err, at, key)
			continue
		}
		out.Fields = append(out.Fields, key)
	}
	return out
}

// resolveEdgeLabel judges an edges[] entry's label_from against the
// relation types that entry draws.
func resolveEdgeLabel(cat *Catalogue, rows []*dbq.RelationType, label, ptr string,
	add func(ptr, message string), st *staleness) string {
	if label == "" {
		return ""
	}
	sc := fieldScope{subject: "the relation types this entry draws", edge: true}
	scope := projectionScope{subject: "the relation types this entry draws"}
	for _, row := range rows {
		if row == nil {
			continue
		}
		scope.names = append(scope.names, row.Key)
		scope.schemas = append(scope.schemas, cat.schemas[row.ID])
	}
	scope.unresolved = len(scope.names) == 0
	if strings.HasPrefix(label, "@") {
		if _, ok := builtinTypes[label]; !ok {
			add(ptr, fmt.Sprintf("%q is not a built-in: the built-ins are %s",
				label, strings.Join(builtinNames, ", ")))
			return ""
		}
		if err := sc.builtin(label); err != nil {
			add(ptr, err.Error())
			return ""
		}
		return label
	}
	if err := scope.declares(label); err != nil {
		add(ptr, err.Error())
		st.noteScope(err, ptr, label)
		return ""
	}
	return label
}

// projectionAlias names the lateral join one related slot compiles to.
// It is a generated name from a constant prefix and an index, like every
// other alias this compiler writes, so no caller string can reach it.
const projectionPrefix frag = "p"

// projection emits the three fragments a node arm needs: the attrs
// payload, the ambiguity flag, and the lateral joins the related slots
// require.
func (c *compiler) projection(alias, typeAlias frag) (attrs, ambiguous, joins frag, err error) {
	var pairs, terms, lateral []frag
	for i, slot := range c.r.Projection.Slots {
		name := c.b.bind(slot.Name)
		if slot.Related == nil {
			value, err := c.attrValue(alias, typeAlias, slot.Attr, pointer("project", slot.Name))
			if err != nil {
				return "", "", "", err
			}
			pairs = append(pairs, sprintf("%s::text, %s", name, value))
			continue
		}
		hop := cteName(projectionPrefix, i)
		join, err := c.relatedHop(alias, hop, slot)
		if err != nil {
			return "", "", "", err
		}
		lateral = append(lateral, join)
		pairs = append(pairs, sprintf("%s::text, %s.value", name, hop))
		// A hop that found nothing has no row at all, so matches is null
		// and the node is not ambiguous. COALESCE says so in the column
		// rather than leaving it null.
		terms = append(terms, sprintf("COALESCE(%s.matches, 0) > 1", hop))
	}
	attrs = "NULL::jsonb"
	if len(pairs) > 0 {
		attrs = sprintf("jsonb_strip_nulls(jsonb_build_object(%s))", joinFrags(pairs, ", "))
	}
	ambiguous = "false"
	if len(terms) > 0 {
		ambiguous = sprintf("(%s)", joinFrags(terms, " OR "))
	}
	// Each join carries its own leading newline rather than being joined
	// by one, so a projection with no related slot contributes nothing at
	// all to the arm — an empty line inside a UNION arm is a diff in three
	// golden files for a slot nobody asked for.
	for i := range lateral {
		lateral[i] = sprintf("\n    %s", lateral[i])
	}
	return attrs, ambiguous, joinFrags(lateral, ""), nil
}

// payload is the node's fields column: the whole jsonb when the run asked
// for it, exactly the keys `project.fields` named when it did not, and a
// typed null when it asked for neither.
func (c *compiler) payload(alias frag) frag {
	if c.opts.IncludeFields {
		// The whole payload is a superset of any list, so an explicit list
		// is not subtracted from it: a run that asked for everything gets
		// everything.
		return sprintf("%s.fields", alias)
	}
	if len(c.r.Projection.Fields) == 0 {
		return "NULL::jsonb"
	}
	pairs := make([]frag, 0, len(c.r.Projection.Fields))
	for _, key := range c.r.Projection.Fields {
		bound := c.b.bind(key)
		pairs = append(pairs, sprintf("%s::text, %s.fields -> %s", bound, alias, bound))
	}
	// Stripped of its nulls like attrs, and for the same reason: a node
	// that does not carry one of the named keys carries nothing under it,
	// rather than a null a renderer has to tell from a stored one.
	return sprintf("jsonb_strip_nulls(jsonb_build_object(%s))", joinFrags(pairs, ", "))
}

// attrValue is one scalar attribute as jsonb, read off the node's own
// row. A built-in is a column; anything else is a declared field key,
// which resolution has already checked is declared by something the query
// draws.
func (c *compiler) attrValue(alias, typeAlias frag, attr string, ptr string) (frag, error) {
	if !strings.HasPrefix(attr, "@") {
		if attr == "" {
			// Unreachable through ParseQuery, which refuses an empty
			// reference, and reachable from a hand-built *Resolved.
			return "", invalidQuery(ptr, "is empty: name a declared field key, a built-in "+
				"such as @name, or a one-hop related attribute")
		}
		return sprintf("%s.fields -> %s", alias, c.b.bind(attr)), nil
	}
	switch attr {
	case AttrName:
		return sprintf("to_jsonb(%s.name)", alias), nil
	case AttrKey:
		return sprintf("to_jsonb(%s.key)", alias), nil
	case AttrInvalid:
		return sprintf("to_jsonb(%s.invalid)", alias), nil
	case AttrCreatedAt:
		return sprintf("to_jsonb(%s.created_at)", alias), nil
	case AttrType:
		// The type's *key*, not its id: a colour keyed by a uuid is a
		// colour a designer cannot name. typeAlias is the entity_types
		// join the node arm already carries, so this costs no second
		// lookup.
		return sprintf("to_jsonb(%s.key)", typeAlias), nil
	}
	return "", invalidQuery(ptr, fmt.Sprintf("%q is not a built-in: the built-ins are %s",
		attr, strings.Join(builtinNames, ", ")))
}

// relatedHop emits one slot's LEFT JOIN LATERAL.
func (c *compiler) relatedHop(alias, name frag, slot ResolvedAttr) (frag, error) {
	hop := slot.Related
	ptr := pointer("project", slot.Name, "related")
	if hop.RelationTypeID == nil {
		// Unreachable through Resolve, which reports an unknown relation
		// type; reachable from a hand-built *Resolved.
		return "", invalidQuery(ptr+"/via",
			"did not resolve to a relation type, so there is nothing to follow")
	}
	var anchor, target frag
	switch hop.Hop.Direction {
	case DirectionOut:
		anchor = sprintf("rel.source_id = %s.id", alias)
		target = "far.id = rel.target_id"
	case DirectionIn:
		anchor = sprintf("rel.target_id = %s.id", alias)
		target = "far.id = rel.source_id"
	case DirectionAny:
		anchor = sprintf("(rel.source_id = %s.id OR rel.target_id = %s.id)", alias, alias)
		target = sprintf("far.id = CASE WHEN rel.source_id = %s.id "+
			"THEN rel.target_id ELSE rel.source_id END", alias)
	default:
		// Including the empty string, which applyDefaults fills — the
		// same rule a step and an edge entry follow, rather than a
		// direction defaulted here and answered backwards in silence.
		return "", invalidQuery(ptr+"/direction",
			fmt.Sprintf("must be %q, %q or %q (got %q)",
				DirectionOut, DirectionIn, DirectionAny, hop.Hop.Direction))
	}
	var typeFilter, typeJoin frag
	if hop.EntityTypeID != nil {
		typeFilter = sprintf("\n                         AND far.entity_type_id = %s",
			c.b.bind(*hop.EntityTypeID))
	}
	attr := hop.Attr
	if attr == "" {
		// Unreachable through ParseQuery, which fills it; reachable from
		// a hand-built *Resolved. It is refused rather than defaulted
		// here, so the default lives in exactly one place.
		return "", invalidQuery(ptr+"/attr",
			fmt.Sprintf("is empty: name a declared field key or a built-in such as %s", AttrName))
	}
	// groupBy is what makes matches count far *entities*. far.id is the
	// primary key, so grouping by it leaves far.* and far.fields -> $n
	// selectable by functional dependency; fet.key is not dependent on it
	// and has to be named when the @type spelling brings the join in.
	var groupBy frag = "far.id, far.name"
	if attr == AttrType {
		typeJoin = "\n        JOIN entity_types fet ON fet.id = far.entity_type_id" +
			"\n                             AND fet.project_id = $1"
		groupBy += ", fet.key"
	}
	value, err := c.attrValue("far", "fet", attr, ptr+"/attr")
	if err != nil {
		return "", err
	}
	return sprintf(`LEFT JOIN LATERAL (
        SELECT %s AS value, count(*) OVER () AS matches
        FROM relations rel
        JOIN entities far ON %s
                         AND far.project_id = $1%s%s%s
        WHERE rel.project_id = $1
          AND rel.relation_type_id = %s%s
          AND %s
        GROUP BY %s
        ORDER BY far.name, far.id
        LIMIT 1
    ) AS %s ON true`, value, target, typeFilter, c.invalidFilterHop(), typeJoin,
		c.b.bind(*hop.RelationTypeID), c.invalidFilterHopEdge(), anchor, groupBy, name), nil
}

// invalidFilterHop keeps a far entity the metamodel flagged as no longer
// fitting its schema from colouring a node, unless the document asked for
// invalid rows. It is the same rule invalidFilter applies to a step's
// destination, at the indentation this join uses: a picture that excludes
// invalid quests should not be coloured by an invalid zone.
func (c *compiler) invalidFilterHop() frag {
	if c.r.Query.IncludeInvalid {
		return ""
	}
	return "\n                         AND far.invalid = false"
}

// invalidFilterHopEdge is the same rule for the *relation* this hop
// follows, which since 0009 can be flagged too.
func (c *compiler) invalidFilterHopEdge() frag {
	if c.r.Query.IncludeInvalid {
		return ""
	}
	return "\n          AND rel.invalid = false"
}

// edgeLabel is the label an edges[] entry asked to be drawn on each
// relation it draws, as jsonb under the key "label".
func (c *compiler) edgeLabel(spec *ResolvedEdge, relationAlias, typeAlias frag) (frag, error) {
	if spec.LabelFrom == "" {
		return "NULL::jsonb", nil
	}
	var value frag
	switch {
	case spec.LabelFrom == AttrType:
		value = sprintf("to_jsonb(%s.key)", typeAlias)
	case spec.LabelFrom == AttrCreatedAt:
		value = sprintf("to_jsonb(%s.created_at)", relationAlias)
	case spec.LabelFrom == AttrInvalid:
		// Since 0009 an edge has this column, so it can be drawn on one:
		// a designer auditing a schema edit wants the broken edges
		// labelled as such in the picture, not only findable through a
		// listing. fieldScope.builtin is what lets resolution reach here.
		value = sprintf("to_jsonb(%s.invalid)", relationAlias)
	case strings.HasPrefix(spec.LabelFrom, "@"):
		// Unreachable through Resolve, which refuses the built-ins a
		// relation has no column for; reachable from a hand-built
		// *Resolved.
		return "", invalidQuery("", fmt.Sprintf(
			"%s cannot be drawn on a relation: an edge carries its type, its creation time, "+
				"its validity and its declared fields, and has no key or name of its own",
			spec.LabelFrom))
	default:
		value = sprintf("%s.fields -> %s", relationAlias, c.b.bind(spec.LabelFrom))
	}
	return sprintf("jsonb_strip_nulls(jsonb_build_object(%s::text, %s))",
		c.b.bind(edgeLabelKey), value), nil
}

// edgeLabelKey is the key an edge's label comes back under. Node.Attrs
// and Edge.Label are different shapes on the wire — a renderer draws one
// string on an edge and reads a map on a node — so the wire name is spelt
// once here and read once in execute.go.
const edgeLabelKey = "label"
