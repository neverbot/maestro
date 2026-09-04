package views

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/db/dbq"
	"github.com/neverbot/maestro/internal/metamodel"
)

// The two kinds of thing a query can name, spelled once. They are the
// values view_refs.kind takes (Task 11) and the values Task 12's
// staleness report groups by.
const (
	KindEntityType   = "entity_type"
	KindRelationType = "relation_type"
)

// Catalogue is one game's declared vocabulary, read once and reused by
// resolution, by compilation and by the staleness report.
//
// It is loaded through internal/metamodel's own ListEntityTypes and
// ListRelationTypes, which take a project id and filter on it in SQL,
// rather than one lookup per key. Three reasons: a query with eight steps
// would otherwise make a dozen round trips inside a call that is already
// the most expensive one in the product; staleness (Task 12) needs
// exactly this data, so a run and its staleness report read the game once
// between them; and going through the accessors that shipped keeps one
// implementation of "a lookup is scoped to the caller's game" instead of
// a copy of it here.
//
// Keys are folded to lower case in the maps because the database folds
// them: entity_types_key_key is UNIQUE (project_id, lower(key)). The
// *stored* spelling is kept on the row, which is what a rename diagnostic
// compares against. TestACatalogueFoldsCaseOnBothSides pins both sides of
// that folding.
//
// **It carries the id of the game it was read for**, and every entry
// point that takes a catalogue takes that id too and refuses a mismatch.
// A catalogue with no identity makes this package's whole isolation story
// rest on callers pairing two arguments correctly, which is the one thing
// reading through one scoped path was supposed to remove: a query
// resolved against another game's catalogue would come back with that
// game's type ids, and Task 12 would compare a saved view against the
// wrong game's vocabulary and answer "not stale".
// TestACatalogueFromAnotherGameIsRefused pins it.
type Catalogue struct {
	ProjectID     uuid.UUID
	EntityTypes   map[string]dbq.EntityType
	RelationTypes map[string]dbq.RelationType
	schemas       map[uuid.UUID]metamodel.Schema
}

// LoadCatalogue reads one game's vocabulary.
func (s *Service) LoadCatalogue(ctx context.Context, projectID uuid.UUID) (*Catalogue, error) {
	types, err := s.meta.ListEntityTypes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	relTypes, err := s.meta.ListRelationTypes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cat := &Catalogue{
		ProjectID:     projectID,
		EntityTypes:   make(map[string]dbq.EntityType, len(types)),
		RelationTypes: make(map[string]dbq.RelationType, len(relTypes)),
		schemas:       make(map[uuid.UUID]metamodel.Schema, len(types)+len(relTypes)),
	}
	for _, row := range types {
		cat.EntityTypes[strings.ToLower(row.Key)] = row
		schema, err := metamodel.ParseSchema(row.FieldSchema)
		if err != nil {
			return nil, fmt.Errorf("parse field schema of entity type %q: %w", row.Key, err)
		}
		cat.schemas[row.ID] = schema
	}
	for _, row := range relTypes {
		cat.RelationTypes[strings.ToLower(row.Key)] = row
		schema, err := metamodel.ParseSchema(row.FieldSchema)
		if err != nil {
			return nil, fmt.Errorf("parse field schema of relation type %q: %w", row.Key, err)
		}
		cat.schemas[row.ID] = schema
	}
	return cat, nil
}

// TypeRef is one reference from a query to a declared type, with the
// pointer that addresses it. It is what Task 11 writes into view_refs and
// what Task 12's staleness report reads back.
//
// A reference that resolved to nothing is still a reference: Key and
// Pointer are filled and ID is nil. That is the whole point of the list —
// a stale view has to be able to say *which part of itself* broke, and a
// pointer plus the key the document spells is exactly that.
// TestAReferenceThatDoesNotResolveIsStillListedWithItsKey pins it.
type TypeRef struct {
	Kind    string // KindEntityType or KindRelationType
	Key     string // as the query spells it
	ID      *uuid.UUID
	Pointer string
}

// ResolvedSet is one seed selector, with its type key turned into an id.
//
// It carries no resolved entity ids: a selector's `keys` shortcut
// compiles to `lower(e.key) = ANY($n)` over the keys themselves, bound as
// a text array beside the project filter, so there is nothing for this
// pass to look up and no round trip to spend. Selector is kept so the
// compiler reads the keys from the document rather than from a copy.
type ResolvedSet struct {
	Name         string
	EntityTypeID *uuid.UUID
	Where        *ResolvedPredicate
	Selector     *Selector
}

// ResolvedStep is one traversal with its relation types resolved.
type ResolvedStep struct {
	Step            *Step
	Name            string
	FromSet         string
	RelationTypeIDs []uuid.UUID
	ToTypeIDs       []uuid.UUID
	Where           *ResolvedPredicate
	EdgeWhere       *ResolvedPredicate
}

// ResolvedEdge is one edges[] entry with its relation type keys turned
// into ids.
//
// Only the `via`/`between` spelling has anything to resolve — the
// `from_step` spelling names a set, which is resolved by being compiled.
// It is resolved here rather than in the compiler for the reason
// LoadCatalogue exists at all: a second place that turns a key into an id
// is a second place that can forget the project filter, and a reference
// the compiler resolved privately would be missing from Refs, so
// deleting a relation type a view draws edges with would report the view
// as fine. TestAnEdgeEntrysRelationTypeIsResolvedAndListed pins both
// halves.
type ResolvedEdge struct {
	Spec            *EdgeSpec
	RelationTypeIDs []uuid.UUID
	// LabelFrom is the field key or built-in this entry draws on each
	// relation, checked against the relation types the entry actually
	// draws — the step's, for the from_step spelling. Empty when the
	// entry asked for no label, which is the default: an unasked-for
	// label on every edge is text a renderer has to hide again.
	LabelFrom string
}

// ResolvedPredicate is a predicate whose every leaf carries the declared
// type of the field it names and a value already coerced to that type.
type ResolvedPredicate struct {
	All  []ResolvedPredicate
	Any  []ResolvedPredicate
	Not  *ResolvedPredicate
	Leaf *ResolvedLeaf
}

// ResolvedLeaf is one comparison, ready to compile. Value is a Go value
// the compiler binds as a parameter and never spells into SQL; a Value of
// type ParamRef stands in for one the caller supplies at run time.
//
// What Value holds, for Task 6 to bind without guessing: a value coerced
// to Type for the scalar operators, a bool for exists and empty, and a
// float64 for the length operators — the last two compare against the
// operand's own type rather than the field's, and each is guarded here.
// Values, not Value, carries the operands of the list and pair operators.
type ResolvedLeaf struct {
	Field   FieldRef
	Type    metamodel.FieldType
	Op      Operator
	Value   any
	Values  []any
	Pointer string
}

// Resolved is a query with every name turned into something the compiler
// can bind.
//
// Params holds **declared defaults only**, each coerced to its
// parameter's declared type. A parameter declared without a default has
// no entry here at all, and a value the caller supplies at run time never
// passes through this pass: Task 7 coerces it with coerceParam and
// overlays it on this map.
type Resolved struct {
	Query  *Query
	Cat    *Catalogue
	Sets   []ResolvedSet
	Steps  []ResolvedStep
	Edges  []ResolvedEdge
	Refs   []TypeRef
	Params map[string]any
	Limits ResolvedLimits
	// Projection is how each node presents itself: the attribute slots
	// the document asked for, each already known to name something, and
	// the one hop a slot may take to read its value off a neighbour.
	Projection ResolvedProjection
}

// ResolvedLimits are the bounds this run will use: the query's overrides
// where it gave them, the defaults otherwise, both already known to be
// within the hard caps because ParseQuery refused anything else.
type ResolvedLimits struct {
	MaxDepth int
	MaxNodes int
	MaxEdges int
}

// Resolve turns a parsed query into one the compiler can emit SQL for,
// against one game.
//
// **Nothing it produces is ever concatenated into SQL.** Every type key
// becomes a uuid, every field key becomes a declared type plus the key
// the compiler binds as a jsonb path parameter, and every value becomes a
// Go value. After this pass the compiler has nothing left that a caller
// controls except bind parameters — which is the property that makes the
// injection question answerable rather than a matter of care. **Nothing
// here pins it**, and cannot: it is a statement about emitted SQL and
// this pass emits none, so the assertion belongs to Task 6's compiler.
//
// **Every lookup it makes is scoped to the caller's game**, because the
// only catalogue it can see is the one LoadCatalogue read for that
// project. TestAKeyFromAnotherGameDoesNotResolve and
// TestARelationTypeFromAnotherGameDoesNotResolve pin both statements,
// each with a positive control in the same test.
//
// It collects every problem in one pass. An agent writing a five-step
// traversal against an unfamiliar game gets all five mistakes at once;
// TestEveryProblemInOneQueryIsReportedInOnePass pins it.
//
// The problems come back in **document order**, because the pass walks
// the document in that order and nothing sorts them afterwards. Sorting
// by pointer string is what an earlier version did, and it reads
// /from/10 before /from/2 — a list that jumps about in a query long
// enough for the order to matter.
// TestProblemsAreReportedInDocumentOrderNotPointerOrder pins it.
func (s *Service) Resolve(ctx context.Context, projectID uuid.UUID, q *Query) (*Resolved, error) {
	cat, err := s.LoadCatalogue(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return ResolveAgainst(projectID, cat, q)
}

// ErrWrongGame is returned when a catalogue is paired with a game it was
// not read for. It is a programming error rather than a query problem —
// no document a designer or an agent can write reaches it — so it carries
// no field pointer and is not a *QueryError.
var ErrWrongGame = errors.New("this catalogue belongs to another game")

// ResolveAgainst is Resolve with the catalogue already in hand, for a
// caller that has just read it — Task 12's staleness report, and Task 7's
// execution, which resolves and compiles in one pass over one read.
//
// It takes the game the query is being resolved for as well as the
// catalogue, and refuses the pair when they disagree: see Catalogue.
func ResolveAgainst(projectID uuid.UUID, cat *Catalogue, q *Query) (*Resolved, error) {
	if err := cat.belongsTo(projectID); err != nil {
		return nil, err
	}
	r, problems := resolveInto(cat, q)
	if len(problems) > 0 {
		return nil, invalidQueryProblems(problems)
	}
	return r, nil
}

// belongsTo is the check every entry point taking a catalogue makes
// first.
func (c *Catalogue) belongsTo(projectID uuid.UUID) error {
	if c.ProjectID != projectID {
		return fmt.Errorf("%w: it was read for %s and this call is for %s",
			ErrWrongGame, c.ProjectID, projectID)
	}
	return nil
}

// ReferencesOf is the dependency list alone, for a query that may not
// resolve at all. Task 12 reads it: a saved view whose relation type was
// deleted has to report *which* reference died, and ResolveAgainst
// answers such a query with a refusal rather than with a Resolved to read
// the list off. It is the same pass, so the list cannot drift from the
// one a successful resolve produces.
func ReferencesOf(projectID uuid.UUID, cat *Catalogue, q *Query) ([]TypeRef, error) {
	if err := cat.belongsTo(projectID); err != nil {
		return nil, err
	}
	r, _ := resolveInto(cat, q)
	return r.Refs, nil
}

// resolveInto is the pass itself, always returning what it built alongside
// whatever it could not resolve.
func resolveInto(cat *Catalogue, q *Query) (*Resolved, []metamodel.FieldError) {
	r := &Resolved{Query: q, Cat: cat, Params: map[string]any{}}
	var problems []metamodel.FieldError
	add := func(ptr, message string) {
		problems = append(problems, metamodel.FieldError{Path: ptr, Message: message})
	}

	// Parameters first: a predicate that feeds one has to know its type.
	paramTypes := map[string]metamodel.FieldType{}
	for i, p := range q.Params {
		paramTypes[p.Key] = metamodel.FieldType(p.Type)
		if p.Default == nil {
			continue
		}
		value, err := coerceParam(metamodel.FieldType(p.Type), p.Default)
		if err != nil {
			add(pointer("params", i, "default"), err.Error())
			continue
		}
		r.Params[p.Key] = value
	}

	entityType := func(ptr, key string) *dbq.EntityType {
		row, ok := cat.EntityTypes[strings.ToLower(key)]
		if !ok {
			add(ptr, fmt.Sprintf("no entity type %q in this game: declare it with types.upsert, "+
				"or use types.list to see what this game has", key))
			r.Refs = append(r.Refs, TypeRef{Kind: KindEntityType, Key: key, Pointer: ptr})
			return nil
		}
		id := row.ID
		r.Refs = append(r.Refs, TypeRef{Kind: KindEntityType, Key: key, ID: &id, Pointer: ptr})
		return &row
	}
	relationType := func(ptr, key string) *dbq.RelationType {
		row, ok := cat.RelationTypes[strings.ToLower(key)]
		if !ok {
			add(ptr, fmt.Sprintf("no relation type %q in this game: declare it with "+
				"relation_types.upsert, or use relation_types.list to see what this game has", key))
			r.Refs = append(r.Refs, TypeRef{Kind: KindRelationType, Key: key, Pointer: ptr})
			return nil
		}
		id := row.ID
		r.Refs = append(r.Refs, TypeRef{Kind: KindRelationType, Key: key, ID: &id, Pointer: ptr})
		return &row
	}

	for i := range q.From {
		sel := &q.From[i]
		ptr := pointer("from", i)
		set := ResolvedSet{Name: sel.As, Selector: sel}
		if row := entityType(ptr+"/type", sel.Type); row != nil {
			id := row.ID
			set.EntityTypeID = &id
			scope := fieldScope{
				subject: fmt.Sprintf("the entity type %q", row.Key),
				names:   []string{row.Key},
				schemas: []metamodel.Schema{cat.schemas[row.ID]},
			}
			set.Where = resolvePredicate(scope, paramTypes, sel.Where, ptr+"/where", &problems)
		}
		r.Sets = append(r.Sets, set)
	}

	// The limits are settled before the steps are walked, because a step's
	// own depth is judged against them and the problems this pass reports
	// are in document order — see the sort ResolveAgainst does not do.
	r.Limits = ResolvedLimits{
		MaxDepth: DefaultMaxDepth, MaxNodes: DefaultMaxNodes, MaxEdges: DefaultMaxEdges,
	}
	if l := q.Limits; l != nil {
		if l.MaxDepth != nil {
			r.Limits.MaxDepth = *l.MaxDepth
		}
		if l.MaxNodes != nil {
			r.Limits.MaxNodes = *l.MaxNodes
		}
		if l.MaxEdges != nil {
			r.Limits.MaxEdges = *l.MaxEdges
		}
	}

	for i := range q.Traverse {
		step := &q.Traverse[i]
		ptr := pointer("traverse", i)
		rs := ResolvedStep{Step: step, Name: step.As, FromSet: step.From}
		edgeScope := fieldScope{subject: "the relation types this step follows", edge: true}
		for j, key := range step.Via {
			if row := relationType(pointer("traverse", i, "via", j), key); row != nil {
				rs.RelationTypeIDs = append(rs.RelationTypeIDs, row.ID)
				edgeScope.names = append(edgeScope.names, row.Key)
				edgeScope.schemas = append(edgeScope.schemas, cat.schemas[row.ID])
			}
		}
		// A step with no to_type reaches entities of any type, so a
		// predicate on it can only name built-ins: no schema is common to
		// every type it could land on. fieldScope says exactly that
		// instead of blaming a type the caller never named —
		// TestAStepWithoutAToTypeAdmitsOnlyBuiltins pins the wording.
		nodeScope := fieldScope{
			subject: "the entity types this step reaches",
			open:    len(step.ToType) == 0,
		}
		for j, key := range step.ToType {
			if row := entityType(pointer("traverse", i, "to_type", j), key); row != nil {
				rs.ToTypeIDs = append(rs.ToTypeIDs, row.ID)
				nodeScope.names = append(nodeScope.names, row.Key)
				nodeScope.schemas = append(nodeScope.schemas, cat.schemas[row.ID])
			}
		}
		// A step's own depth is only judgeable here: ParseQuery sees the
		// number but not the max_depth it has to fit under, which the
		// document may itself have overridden.
		if step.Depth != nil && step.Depth.Max > r.Limits.MaxDepth {
			add(ptr+"/depth",
				fmt.Sprintf("asks for depth %d and this query's max_depth is %d: "+
					"raise limits.max_depth (up to %d) or lower this step",
					step.Depth.Max, r.Limits.MaxDepth, HardMaxDepth))
		}
		rs.Where = resolvePredicate(nodeScope, paramTypes, step.Where, ptr+"/where", &problems)
		rs.EdgeWhere = resolvePredicate(edgeScope, paramTypes, step.EdgeWhere,
			ptr+"/edge_where", &problems)
		r.Steps = append(r.Steps, rs)
	}

	for i := range q.Edges {
		spec := &q.Edges[i]
		edge := ResolvedEdge{Spec: spec}
		// The relation types this entry draws, which is what its
		// label_from is judged against. The via spelling names them; the
		// from_step spelling inherits the step's, because the relations it
		// draws are exactly the ones that step walked.
		var drawn []*dbq.RelationType
		for j, key := range spec.Via {
			if row := relationType(pointer("edges", i, "via", j), key); row != nil {
				edge.RelationTypeIDs = append(edge.RelationTypeIDs, row.ID)
				drawn = append(drawn, row)
			}
		}
		if spec.FromStep != "" {
			for j := range q.Traverse {
				if q.Traverse[j].As != spec.FromStep {
					continue
				}
				for _, key := range q.Traverse[j].Via {
					// Read from the catalogue rather than through
					// relationType: the step already listed this key in
					// Refs at its own pointer, and a second TypeRef for
					// the same reference would make Task 12 report one
					// broken dependency twice.
					if row, ok := cat.RelationTypes[strings.ToLower(key)]; ok {
						drawn = append(drawn, &row)
					}
				}
			}
		}
		edge.LabelFrom = resolveEdgeLabel(cat, drawn, spec.LabelFrom,
			pointer("edges", i, "label_from"), add)
		r.Edges = append(r.Edges, edge)
	}

	// The projection's one-hop related attributes are type references too,
	// and a stale one breaks a colour rather than a filter — which is the
	// silent failure the whole view_refs table exists to make loud.
	//
	// The five positions are read off predicate.go's projectionAttrs
	// rather than listed again here, which is what makes
	// TestEveryProjectionAttributeReferenceHasALineInTheTable cover this
	// pass as well as checkProjection: a sixth *AttrRef added to
	// Projection fails that test, and the line it then gains is the line
	// this loop reads.
	r.Projection = resolveProjection(cat, q, add, entityType, relationType)

	return r, problems
}

// fieldScope is the set of declared fields a predicate position may name.
//
// It is a *set* of schemas rather than one because a step may reach
// several entity types and follow several relation types at once, and a
// declared field is only comparable there if every one of them declares
// it the same way. Taking the first schema and ignoring the rest gets
// this wrong in both directions: a field declared on one of two reached
// types compiles to a comparison that silently matches nothing on the
// other, and a key declared number on one type and enum on another is
// coerced against whichever happened to be listed first.
// TestAFieldMustBeDeclaredTheSameWayOnEveryTypeAStepReaches pins the
// not-declared-everywhere direction;
// TestAFieldDeclaredTwoWaysNamesEachTypeWithItsOwnDeclaration pins the
// other one, and pins that each type is named beside its own declaration
// in both the type and the enum-options message.
type fieldScope struct {
	// subject names what the schemas belong to, for the refusal message.
	subject string
	// names are the type keys, in the order the document lists them.
	names []string
	// schemas are their field schemas, index-aligned with names.
	schemas []metamodel.Schema
	// open marks a position that reaches entities of any type, where no
	// declared field is comparable at all because no schema applies.
	open bool
	// edge marks a position whose rows are relations rather than
	// entities. A relation has an id, a type, two endpoints, its declared
	// fields and its timestamps and nothing else (0004_metamodel.sql), so
	// @name, @key and @invalid name no column there and would compile to
	// SQL Postgres refuses.
	// TestAnEdgePredicateAdmitsOnlyTheBuiltinsARelationHas pins the
	// refusal, with @type and @created_at as its controls.
	edge bool
}

// builtin says whether an @-sigil name can be compared in this position.
func (sc fieldScope) builtin(name string) error {
	if !sc.edge {
		return nil
	}
	if name == AttrType || name == AttrCreatedAt {
		return nil
	}
	return fmt.Errorf("%s cannot be compared on a relation: an edge carries its type, its "+
		"creation time and its declared fields, and has no key, name or invalid flag of its "+
		"own — compare %s or %s here, or a field this relation type declares",
		name, AttrType, AttrCreatedAt)
}

// field finds the declaration a key has across every schema in scope,
// or says why it has none.
func (sc fieldScope) field(key string) (metamodel.Field, error) {
	if sc.open {
		return metamodel.Field{}, fmt.Errorf(
			"no declared field can be compared here: this step names no to_type, so it reaches "+
				"entities of any type and no field schema applies — name a to_type, or compare a "+
				"built-in such as %s", strings.Join(builtinNames, ", "))
	}
	if len(sc.schemas) == 0 {
		return metamodel.Field{}, fmt.Errorf(
			"no field %q can be compared here: %s resolved to nothing", key, sc.subject)
	}
	var found metamodel.Field
	// foundOn is the type that declared it first, and the message names
	// *that* type beside its declaration rather than pairing one type's
	// name with the other type's declaration — which is a message that
	// sends a designer to edit the type that is not the one being
	// described.
	var foundOn string
	var have bool
	var missing []string
	for i, schema := range sc.schemas {
		var declared *metamodel.Field
		for j := range schema {
			if schema[j].Key == key {
				declared = &schema[j]
				break
			}
		}
		if declared == nil {
			missing = append(missing, sc.names[i])
			continue
		}
		if !have {
			found, foundOn, have = *declared, sc.names[i], true
			continue
		}
		if declared.Type != found.Type {
			return metamodel.Field{}, fmt.Errorf(
				"the field %q is declared %s on %q and %s on %q: a comparison here would "+
					"have to mean two different things at once",
				key, found.Type, foundOn, declared.Type, sc.names[i])
		}
		if found.Type == metamodel.FieldEnum && !sameOptions(found.Options, declared.Options) {
			return metamodel.Field{}, fmt.Errorf(
				"the field %q is declared enum with different options on %q (%v) and on %q "+
					"(%v): a value legal for one is not legal for the other",
				key, foundOn, found.Options, sc.names[i], declared.Options)
		}
	}
	switch {
	case !have:
		return metamodel.Field{}, fmt.Errorf(
			"no field %q is declared on %s: use types.get to see its field_schema, or address a "+
				"built-in with an @ sigil", key, sc.subject)
	case len(missing) > 0:
		return metamodel.Field{}, fmt.Errorf(
			"the field %q is not declared on every type in scope: %q does not declare it, so the "+
				"comparison would silently match nothing there",
			key, strings.Join(missing, `", "`))
	}
	return found, nil
}

// sameOptions compares two enum declarations' options as **sets**, not as
// sequences. Two types declaring the same three options in a different
// order admit exactly the same values, so refusing the comparison would
// refuse a query with no ambiguity in it — and would do so with a
// justification that is false in precisely that case, since every value
// legal for one is legal for the other. The identical-Type requirement
// stays, and min, max and required are still allowed to diverge: what a
// comparison needs is that the same value means the same thing on every
// type in scope, which identical type plus identical option set gives.
// TestEnumOptionsAreComparedAsASetNotASequence pins it.
func sameOptions(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// resolvePredicate walks a predicate tree, giving every leaf the declared
// type of the field it names and coercing its value to that type.
func resolvePredicate(scope fieldScope, paramTypes map[string]metamodel.FieldType,
	p *Predicate, ptr string, problems *[]metamodel.FieldError) *ResolvedPredicate {
	if p == nil {
		return nil
	}
	add := func(at, message string) {
		*problems = append(*problems, metamodel.FieldError{Path: at, Message: message})
	}
	switch {
	case p.All != nil:
		out := &ResolvedPredicate{}
		for i := range p.All {
			if child := resolvePredicate(scope, paramTypes, &p.All[i],
				ptr+"/all"+pointer(i), problems); child != nil {
				out.All = append(out.All, *child)
			}
		}
		return out
	case p.Any != nil:
		out := &ResolvedPredicate{}
		for i := range p.Any {
			if child := resolvePredicate(scope, paramTypes, &p.Any[i],
				ptr+"/any"+pointer(i), problems); child != nil {
				out.Any = append(out.Any, *child)
			}
		}
		return out
	case p.Not != nil:
		return &ResolvedPredicate{Not: resolvePredicate(scope, paramTypes, p.Not,
			ptr+"/not", problems)}
	}

	leaf := &ResolvedLeaf{Field: p.FieldRef, Op: Operator(p.Op), Pointer: ptr}
	var declared metamodel.Field
	if p.FieldRef.Builtin {
		typ, ok := BuiltinType(p.FieldRef.Key)
		if !ok {
			// Unreachable from ParseQuery, which refuses an unknown sigil
			// at /…/field, but ResolveAgainst is exported and a Query a Go
			// caller built by hand has been through no such pass.
			add(ptr+"/field", fmt.Sprintf("%q is not a built-in: the built-ins are %s",
				p.FieldRef.Key, strings.Join(builtinNames, ", ")))
			return nil
		}
		if err := scope.builtin(p.FieldRef.Key); err != nil {
			add(ptr+"/field", err.Error())
			return nil
		}
		leaf.Type = typ
	} else {
		field, err := scope.field(p.FieldRef.Key)
		if err != nil {
			add(ptr+"/field", err.Error())
			return nil
		}
		declared = field
		leaf.Type = field.Type
	}

	if !AdmitsOperator(leaf.Type, leaf.Op) {
		names := make([]string, 0, len(OperatorsFor(leaf.Type)))
		for _, op := range OperatorsFor(leaf.Type) {
			names = append(names, string(op))
		}
		add(ptr+"/op", fmt.Sprintf("the field %q is declared %s, which answers %s — not %q",
			p.FieldRef.Key, leaf.Type, strings.Join(names, ", "), p.Op))
		return nil
	}

	// A parameter in a value position: its declared type has to match the
	// field's, and the mismatch names both sides because an agent holding
	// only one of them cannot tell which to change.
	if ref, ok := paramRefOf(p.Value); ok {
		paramType, declaredParam := paramTypes[ref.Key]
		switch {
		case !declaredParam:
			add(ptr+"/value", fmt.Sprintf("no parameter named %q is declared: add it to "+
				"params, or write the value out", ref.Key))
		case leaf.Type == metamodel.FieldEnum || leaf.Type == metamodel.FieldListText:
			// A parameter is text, number or bool, so no parameter can ever
			// feed a field of these two types. The refusal says that the
			// language cannot express it rather than naming two types that
			// disagree, which reads like a mistake the author could fix by
			// redeclaring one of them. Widening a text parameter to feed an
			// enum is deliberately not done: the option check lives in
			// coerceOperand and a value bound at run time never passes
			// through it, so the enum misspelling this language refuses at
			// save time would come back as an empty picture.
			add(ptr+"/value", fmt.Sprintf("the field %q is declared %s, and a parameter cannot "+
				"stand in for one: a parameter is %s, %s or %s, so write the value out",
				p.FieldRef.Key, leaf.Type,
				metamodel.FieldText, metamodel.FieldNumber, metamodel.FieldBool))
		case paramType != leaf.Type:
			add(ptr+"/value", fmt.Sprintf("the parameter %q is declared %s and the field %q "+
				"is declared %s", ref.Key, paramType, p.FieldRef.Key, leaf.Type))
		default:
			leaf.Value = ParamRef{Key: ref.Key}
		}
		return &ResolvedPredicate{Leaf: leaf}
	}

	switch shapeOf[leaf.Op] {
	case shapeList, shapePair:
		list, ok := p.Value.([]any)
		if !ok {
			// Unreachable from ParseQuery, whose checkValueShape refuses a
			// non-list for these operators; reachable from a hand-built
			// Query, and a panic is not a refusal a caller can act on.
			add(ptr+"/value", fmt.Sprintf("must be a list of values, because the operator is %q",
				leaf.Op))
			return nil
		}
		for i, raw := range list {
			value, err := coerceOperand(leaf.Type, declared, raw)
			if err != nil {
				add(ptr+pointer("value", i), err.Error())
				continue
			}
			leaf.Values = append(leaf.Values, value)
		}
	case shapeBool:
		// exists and empty compare against the operand's own type, not the
		// field's. The guard is the same one its two neighbours carry and
		// for the same reason: ParseQuery's checkValueShape has judged the
		// shape of a parsed document, and ResolveAgainst is exported, so a
		// Query a Go caller built by hand has been through no such pass.
		value, ok := p.Value.(bool)
		if !ok {
			add(ptr+"/value", fmt.Sprintf("must be true or false, because the operator is %q",
				leaf.Op))
			return nil
		}
		leaf.Value = value
	case shapeNumber:
		// The length operators count elements, so their operand is a
		// number beside a list: `tags length_gte 2`. Guarded like the bool
		// arm above, and for the same reason.
		value, ok := p.Value.(float64)
		if !ok {
			add(ptr+"/value", fmt.Sprintf("must be a number, because the operator is %q",
				leaf.Op))
			return nil
		}
		leaf.Value = value
	default:
		value, err := coerceOperand(leaf.Type, declared, p.Value)
		if err != nil {
			add(ptr+"/value", err.Error())
			return nil
		}
		leaf.Value = value
	}
	return &ResolvedPredicate{Leaf: leaf}
}

// coerceOperand checks one literal against the declared type it will be
// compared with, and is where an enum option outside its declaration is
// refused rather than becoming an empty result.
//
// It reuses metamodel.Schema.Validate rather than reimplementing the
// coercion: a single-field schema whose one field is the declared one,
// validated against a single-value map, gives exactly the metamodel's own
// judgement and exactly its wording. A second copy of that judgement is
// how a query starts accepting values the write path refuses.
//
// **It takes that judgement minus the declared range**, and that
// exception is deliberate rather than an oversight. Min and Max are rules
// about what may be *written*; a stored value can sit outside them, since
// the metamodel flags an entity invalid on a schema change and leaves its
// values in place, and the query language has an include_invalid switch
// for exactly those rows. Keeping the bounds would refuse `difficulty
// gte 0` on a field declared min 1 — an ordinary "everything" query, and
// no mistake at all. An enum's options are kept, and the asymmetry is the
// point: a value outside them is a misspelling the refusal can name,
// while a number outside a range is a legitimate question.
// TestADeclaredRangeDoesNotRefuseAComparisonOutsideIt pins both halves.
func coerceOperand(typ metamodel.FieldType, declared metamodel.Field, raw any) (any, error) {
	if typ == TypeTimestamp {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected an RFC 3339 timestamp, got %T", raw)
		}
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("must be an RFC 3339 timestamp such as "+
				"2026-09-02T10:00:00Z (got %q)", s)
		}
		return at, nil
	}
	field := declared
	field.Key, field.Type, field.Required, field.HasDefault = "value", typ, false, false
	field.Min, field.Max = nil, nil
	// list<text> is compared element-wise by every operator it admits, so
	// an operand is one element and is judged as text.
	if typ == metamodel.FieldListText {
		field.Type = metamodel.FieldText
	}
	out, err := metamodel.Schema{field}.Validate(map[string]any{"value": raw})
	if err != nil {
		var ve *metamodel.ValidationError
		if errors.As(err, &ve) && len(ve.Fields) > 0 {
			return nil, errors.New(ve.Fields[0].Message)
		}
		return nil, err
	}
	return out["value"], nil
}

// coerceParam checks a parameter's default, and a value bound at run
// time, against the parameter's own declared type.
func coerceParam(typ metamodel.FieldType, raw any) (any, error) {
	return coerceOperand(typ, metamodel.Field{Type: typ}, raw)
}
