package views

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/neverbot/maestro/internal/metamodel"
)

// Operator is one comparison. The set is closed and typed: an operator is
// admitted or refused against the *declared* field type, at save time,
// because an operator a type cannot answer produces an empty result, and
// an empty result that is really a typo is the most expensive failure
// mode in a query language whose author is not in the room.
type Operator string

const (
	OpEq         Operator = "eq"
	OpNeq        Operator = "neq"
	OpIn         Operator = "in"
	OpContains   Operator = "contains"
	OpStartsWith Operator = "starts_with"
	// OpMatches is a **literal-anchored glob**, not a regular expression:
	// `*` and `?` are the only metacharacters and everything else matches
	// itself. There is no regex operator and TestThereIsNoRegexOperator
	// pins that: a regex over jsonb is unbounded work inside a statement
	// timeout this design keeps small.
	OpMatches     Operator = "matches"
	OpExists      Operator = "exists"
	OpLt          Operator = "lt"
	OpLte         Operator = "lte"
	OpGt          Operator = "gt"
	OpGte         Operator = "gte"
	OpBetween     Operator = "between"
	OpContainsAny Operator = "contains_any"
	OpContainsAll Operator = "contains_all"
	OpEmpty       Operator = "empty"
	OpLengthEq    Operator = "length_eq"
	OpLengthGte   Operator = "length_gte"
	OpLengthLte   Operator = "length_lte"
)

// TypeTimestamp is this package's own pseudo field type, for @created_at.
//
// It is declared here and not in internal/metamodel because a game cannot
// declare a timestamp field: the metamodel's six types are text, longtext,
// number, bool, enum and list<text>, and @created_at is a *column*, not a
// declared field. Giving it a type of its own rather than folding it onto
// number is what keeps `@created_at gt 20` from being a legal comparison
// between a timestamptz and an integer.
const TypeTimestamp metamodel.FieldType = "timestamp"

// operatorsByType is the spec's §2.3 table, and it is the contract: the
// tool description is generated from it (Task 15), so an addition here is
// an addition to the language and shows up on the wire in the same
// commit.
//
// **The table is data, and every other structure that has an opinion
// about an operator is derived from it rather than written beside it.**
// knownOperators, allOperatorNames and — the one that would fail
// silently — shapeOf all read this map, so an operator added to a row
// here and nowhere else is caught by TestAnOperatorCannotBeHalfAdded
// rather than being judged as a scalar by the zero value of valueShape.
// A switch over spellings is what this replaces: a switch admits a
// half-implemented arm, and the arm that is missing is always the one
// nobody thought of.
//
// list<text> is the whole list family, deliberately: the committed
// validator (internal/metamodel/schema.go) declares exactly six field
// types and there is no list<number> or list<enum> for these operators to
// apply to. contains_any over a list of numbers is therefore a string
// comparison today, and the tool description says so rather than leaving
// an agent to discover it.
var operatorsByType = map[metamodel.FieldType][]Operator{
	metamodel.FieldText:     {OpEq, OpNeq, OpIn, OpContains, OpStartsWith, OpMatches, OpExists},
	metamodel.FieldLongText: {OpEq, OpNeq, OpIn, OpContains, OpStartsWith, OpMatches, OpExists},
	metamodel.FieldNumber:   {OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpBetween, OpIn, OpExists},
	metamodel.FieldBool:     {OpEq, OpExists},
	metamodel.FieldEnum:     {OpEq, OpNeq, OpIn, OpExists},
	metamodel.FieldListText: {OpContains, OpContainsAny, OpContainsAll, OpEmpty,
		OpLengthEq, OpLengthGte, OpLengthLte},
	TypeTimestamp: {OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpBetween, OpExists},
}

// OperatorsFor returns the operators a declared field type admits, in the
// table's own order, which is the order the tool description prints. A
// type the table does not name admits nothing, which is what makes an
// unknown type a refusal rather than a free pass.
func OperatorsFor(typ metamodel.FieldType) []Operator { return operatorsByType[typ] }

// AdmitsOperator reports whether a field of this declared type can answer
// this operator.
func AdmitsOperator(typ metamodel.FieldType, op Operator) bool {
	for _, candidate := range operatorsByType[typ] {
		if candidate == op {
			return true
		}
	}
	return false
}

// knownOperators is every spelling any type admits, for the "is this an
// operator at all" check, which is a different refusal from "this type
// cannot answer it" and must say a different thing. It is derived from
// the table rather than listed again, so the two cannot disagree.
var knownOperators = func() map[Operator]bool {
	out := map[Operator]bool{}
	for _, ops := range operatorsByType {
		for _, op := range ops {
			out[op] = true
		}
	}
	return out
}()

// The built-in attributes, addressed with an @ sigil. The sigil exists
// because a game is free to declare a field literally called `name`, and
// an ambiguity there would be discovered by a designer looking at a wrong
// diagram. TestAFieldKeyIsAFieldKeyOrASigil pins both readings of `name`.
//
// AttrName is declared in query.go rather than here: Task 2's
// applyDefaults labels a node with it, and it is one constant with one
// definition wherever it sits.
const (
	AttrKey       = "@key"
	AttrType      = "@type"
	AttrInvalid   = "@invalid"
	AttrCreatedAt = "@created_at"
)

// builtins is the @-sigil vocabulary as one ordered table, giving every
// built-in the declared type its operators are judged against, exactly as
// a declared field's schema entry would. Ordered, and read in order, so
// the list a refusal prints is the list a reader learns the vocabulary
// from: @name first because it is the one an agent reaches for, not @
// created_at first because the alphabet says so.
//
// The two structures below are derived from this slice for the same
// reason the operator table derives its own: a built-in that is printed
// but has no type would admit no operator at all, and one that has a type
// but is not printed is invisible to the agent that mistyped it.
// TestTheBuiltinVocabularyIsListedInTheOrderItIsDeclared pins both halves.
//
// **This list is complete for the row a predicate compares against, and
// it is not everything an envelope node carries.** Node.Set and Node.Role
// (execute.go) say which selector or step a node came back through, and
// they have no sigil here. Nothing depends on that today — a predicate
// filters entities and a set is not a column of one — but a table drawn
// over a multi-set query therefore cannot show which set a row came from,
// because Task 10's `columns` admits exactly the sigils in this table.
// Recorded here rather than there because this is where a reader comes to
// learn what the vocabulary holds, and "@set" is the first thing they
// will look for and not find.
var builtins = []struct {
	Name string
	Type metamodel.FieldType
}{
	{AttrName, metamodel.FieldText},
	{AttrKey, metamodel.FieldText},
	{AttrType, metamodel.FieldText},
	{AttrInvalid, metamodel.FieldBool},
	{AttrCreatedAt, TypeTimestamp},
}

var builtinTypes = func() map[string]metamodel.FieldType {
	out := make(map[string]metamodel.FieldType, len(builtins))
	for _, b := range builtins {
		out[b.Name] = b.Type
	}
	return out
}()

// builtinNames is the list the refusal message prints, so an agent that
// mistyped one is told the whole vocabulary rather than that its spelling
// was wrong.
var builtinNames = func() []string {
	out := make([]string, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, b.Name)
	}
	return out
}()

// BuiltinType returns the declared type a built-in attribute is judged
// against, and whether the name is a built-in at all. Task 4 needs the
// same answer this package's own checks need, and a second copy of the
// table is exactly the drift internal/paging exists to prevent.
func BuiltinType(name string) (metamodel.FieldType, bool) {
	typ, ok := builtinTypes[name]
	return typ, ok
}

// fieldKeyPattern is internal/metamodel's own declared-field-key rule,
// restated rather than imported because that package keeps it unexported
// and a *field* key is not a *row* key: a row key is
// ^[A-Za-z0-9][A-Za-z0-9_-]*$ and case-insensitive, a declared field key
// is lower_snake_case and case-sensitive. Applying the wrong one refuses
// a legal query or accepts an illegal one, which is why this is spelled
// out here with the difference named.
//
// **If internal/metamodel ever exports its keyPattern, delete this and
// use it.** Two copies of a grammar is the drift internal/paging exists
// to prevent; this one is tolerated only because the alternative today is
// an exported symbol added to another package for one caller.
var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxFieldKeyLen = 64

// valueShape says what an operator's right-hand side must look like,
// before anything knows the field's declared type.
type valueShape int

const (
	shapeScalar valueShape = iota
	shapeList
	shapePair
	shapeBool
	shapeNumber
)

// shapeOf is the second half of the operator table, and it is data for
// the same reason the first half is. Its domain is asserted to be exactly
// knownOperators, in both directions, by TestAnOperatorCannotBeHalfAdded:
// a missing entry here reads as shapeScalar — the zero value — which
// would let `in` take a bare number and reach the compiler as one.
var shapeOf = map[Operator]valueShape{
	OpEq: shapeScalar, OpNeq: shapeScalar, OpContains: shapeScalar,
	OpStartsWith: shapeScalar, OpMatches: shapeScalar,
	OpLt: shapeScalar, OpLte: shapeScalar, OpGt: shapeScalar, OpGte: shapeScalar,
	OpIn: shapeList, OpContainsAny: shapeList, OpContainsAll: shapeList,
	OpBetween: shapePair,
	OpExists:  shapeBool, OpEmpty: shapeBool,
	OpLengthEq: shapeNumber, OpLengthGte: shapeNumber, OpLengthLte: shapeNumber,
}

// ParamRef is a {"param": "key"} in a value position, recognised after
// decoding because `value` is `any`.
type ParamRef struct{ Key string }

// paramRefOf reports whether a decoded value is a parameter reference.
// The shape is exact: an object with one member called `param` whose
// value is a string. An object with a `param` member and anything else is
// a value the language does not have, not a lenient parameter reference.
func paramRefOf(value any) (ParamRef, bool) {
	obj, ok := value.(map[string]any)
	if !ok || len(obj) != 1 {
		return ParamRef{}, false
	}
	key, ok := obj["param"].(string)
	if !ok {
		return ParamRef{}, false
	}
	return ParamRef{Key: key}, true
}

// checkPredicate is the structural pass over one predicate tree. It knows
// nothing about a game: which declared type `min_level` has is Task 4's
// lookup, and the split is what lets views.validate answer a structural
// mistake without a database round trip.
//
// ptr addresses the predicate itself, and is where the two *tree* bounds
// report: "this tree is too deep" and "this tree has too many conditions"
// are statements about the whole predicate, and reporting them at the
// leaf that happened to trip them would hand an agent a pointer forty
// segments long that names no mistake it made.
func checkPredicate(p *Predicate, ptr string) []metamodel.FieldError {
	if p == nil {
		return nil
	}
	w := &predicateWalk{root: ptr}
	w.at(p, ptr, 1)
	return w.problems
}

// predicateWalk carries the state one pass over one tree needs: the
// pointer the tree bounds report at, the running node count, and the two
// "already said this" flags. The flags are what keep a 500-condition
// document from answering with 300 copies of one sentence.
type predicateWalk struct {
	root     string
	nodes    int
	tooDeep  bool
	tooMany  bool
	problems []metamodel.FieldError
}

func (w *predicateWalk) add(at, message string) {
	w.problems = append(w.problems, metamodel.FieldError{Path: at, Message: message})
}

func (w *predicateWalk) at(p *Predicate, ptr string, depth int) {
	if p == nil {
		return
	}
	if depth > MaxPredicateDepth {
		if !w.tooDeep {
			w.tooDeep = true
			w.add(w.root, fmt.Sprintf("is nested more than %d deep: a predicate this deep is a "+
				"traversal written sideways, and a traversal belongs in \"traverse\" where it is "+
				"bounded and visible", MaxPredicateDepth))
		}
		return
	}
	w.nodes++
	if w.nodes > MaxPredicateNodes {
		if !w.tooMany {
			w.tooMany = true
			w.add(w.root, fmt.Sprintf("has more than %d conditions in one tree: a predicate this "+
				"wide is a set of keys, and a selector's \"keys\" says that directly",
				MaxPredicateNodes))
		}
		return
	}

	combinators := 0
	if p.All != nil {
		combinators++
	}
	if p.Any != nil {
		combinators++
	}
	if p.Not != nil {
		combinators++
	}
	leaf := p.Field != "" || p.Op != "" || p.Value != nil

	switch {
	case combinators == 0 && !leaf:
		w.add(ptr, `must be one of "all", "any", "not" or a field test with "field" and "op"`)
		return
	case combinators > 1:
		w.add(ptr, `sets more than one of "all", "any" and "not": a predicate is exactly one of `+
			`them, and a document setting two has a meaning decided by whichever arm the `+
			`compiler read first`)
		return
	case combinators == 1 && leaf:
		w.add(ptr, `combines a combinator with a field test: a predicate is either a combinator `+
			`over other predicates or one field test, never both`)
		return
	}

	if combinators == 1 {
		if p.All != nil {
			w.children(p.All, ptr, "all", depth)
		}
		if p.Any != nil {
			w.children(p.Any, ptr, "any", depth)
		}
		if p.Not != nil {
			w.at(p.Not, ptr+"/not", depth+1)
		}
		return
	}

	// A field test.
	switch {
	case p.Field == "":
		w.add(ptr+"/field", "is required: a field test names the field it compares")
	case strings.HasPrefix(p.Field, "@"):
		if _, ok := builtinTypes[p.Field]; !ok {
			w.add(ptr+"/field", fmt.Sprintf(
				"%q is not a built-in: the built-ins are %s, and a declared field is named "+
					"without the sigil", p.Field, strings.Join(builtinNames, ", ")))
		}
	case len(p.Field) > maxFieldKeyLen:
		w.add(ptr+"/field", fmt.Sprintf("must be at most %d characters", maxFieldKeyLen))
	case !fieldKeyPattern.MatchString(p.Field):
		w.add(ptr+"/field", "must be lower_snake_case: a letter, then letters, digits or "+
			"underscores — the same rule a field_schema declaration obeys")
	}

	op := Operator(p.Op)
	switch {
	case p.Op == "":
		w.add(ptr+"/op", "is required: a field test names the comparison it makes")
	case !knownOperators[op]:
		w.add(ptr+"/op", fmt.Sprintf("%q is not an operator this language has: the operators are "+
			"%s, and which of them a field admits depends on its declared type", p.Op, allOperatorNames()))
	default:
		w.problems = append(w.problems, checkValueShape(op, p.Value, ptr+"/value")...)
	}
}

// children walks one combinator's children, addressing each by its index
// under the combinator's own name.
func (w *predicateWalk) children(children []Predicate, ptr, name string, depth int) {
	if len(children) == 0 {
		w.add(ptr+"/"+name, "must hold at least one condition: an empty combinator has no truth "+
			"value, and guessing one would silently widen or narrow the query")
		return
	}
	for i := range children {
		w.at(&children[i], ptr+"/"+name+pointer(i), depth+1)
	}
}

// checkValueShape judges the right-hand side against the operator alone.
// The *type* check — is this number legal for a field declared text — is
// Task 4's, because it needs the schema.
func checkValueShape(op Operator, value any, ptr string) []metamodel.FieldError {
	problem := func(message string) []metamodel.FieldError {
		return []metamodel.FieldError{{Path: ptr, Message: message}}
	}
	// The depth bound runs before the shape checks because it is the more
	// fundamental fault: a list whose elements are objects passes every
	// shape check `in` makes, since a list operator counts its list and
	// never looks inside it.
	if got := valueDepth(value); got > MaxValueDepth {
		return problem(fmt.Sprintf(
			"is nested %d deep, and a value in this language is at most %d deep: a value is one "+
				"scalar, a list of scalars, a pair of bounds, or a {\"param\": \"…\"} reference",
			got, MaxValueDepth))
	}
	if _, isParam := paramRefOf(value); isParam {
		// A parameter stands in for a scalar. It cannot stand in for a
		// list or a pair, because the parameter types are the three
		// scalars, and a list-valued parameter is a feature this version
		// deliberately does not have.
		if shapeOf[op] == shapeList || shapeOf[op] == shapePair {
			return problem(fmt.Sprintf(
				"is a parameter, and %q takes a list of values: a parameter stands in for one "+
					"scalar, so a list must be written out", op))
		}
		return nil
	}
	switch shapeOf[op] {
	case shapeList:
		list, ok := value.([]any)
		if !ok {
			return problem(fmt.Sprintf("must be a list of values, because the operator is %q", op))
		}
		if len(list) == 0 {
			return problem(fmt.Sprintf("must hold at least one value: %q over an empty list "+
				"matches nothing, which is a query that says nothing", op))
		}
		if len(list) > MaxValueList {
			return problem(fmt.Sprintf("holds %d values, and the most an operator takes is %d",
				len(list), MaxValueList))
		}
	case shapePair:
		list, ok := value.([]any)
		if !ok || len(list) != 2 {
			return problem(`must be a list of exactly two values, the lower bound and the upper`)
		}
	case shapeBool:
		if _, ok := value.(bool); !ok {
			return problem(fmt.Sprintf("must be true or false, because the operator is %q", op))
		}
	case shapeNumber:
		if _, ok := value.(float64); !ok {
			return problem(fmt.Sprintf("must be a number, because the operator is %q", op))
		}
	case shapeScalar:
		switch value.(type) {
		case string, float64, bool:
		case nil:
			return problem(fmt.Sprintf("is required, because the operator is %q", op))
		default:
			return problem("must be a single value: a list belongs with in, contains_any, " +
				"contains_all or between")
		}
	}
	return nil
}

// valueDepth measures how deeply a decoded `value` nests. A scalar is 1,
// a list of scalars or a {"param": …} reference is 2, and anything deeper
// is a shape this language has no meaning for.
//
// It is a *language* bound and not a stack guard, and the difference is
// worth stating because the two are easy to confuse: encoding/json
// already refuses nesting past ten thousand, so nothing here overflows.
// What the decoder's bound does not do is report the caller's own
// argument — it answers at pointer "" in the decoder's wording, saying
// nothing about a value — and it does not stop a document with a
// four-deep object inside `in` from being *stored* as a saved view and
// re-walked by every later stage. MaxValueDepth is what makes such a
// document a refusal an agent can act on.
func valueDepth(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		deepest := 0
		for _, item := range typed {
			if d := valueDepth(item); d > deepest {
				deepest = d
			}
		}
		return deepest + 1
	case []any:
		deepest := 0
		for _, item := range typed {
			if d := valueDepth(item); d > deepest {
				deepest = d
			}
		}
		return deepest + 1
	default:
		return 1
	}
}

// allOperatorNames is the whole vocabulary, sorted, for the refusal that
// says a spelling is not an operator at all. Sorted here and *not* in the
// per-type listing: this one is a lookup table an agent scans for a
// spelling, while OperatorsFor's order is the table's own and is what the
// tool description prints.
func allOperatorNames() string {
	names := make([]string, 0, len(knownOperators))
	for op := range knownOperators {
		names = append(names, string(op))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// projectionAttrs is the five attribute-reference positions a projection
// has, paired with the member name each is written under. It is a slice
// rather than a map so the pass is deterministic, and it is the one place
// in this task that enumerates fields rather than walking them — a
// projection's references are five *named* members with five different
// pointers, not a homogeneous collection.
//
// Two tests hold this enumeration to the type, because one of them alone
// cannot. TestEveryProjectionAttributeIsChecked drives all five names
// through ParseQuery, which proves the five that exist are checked and
// nothing about a sixth. TestEveryProjectionAttributeReferenceHasALineInTheTable
// asks Projection itself which of its members are *AttrRef and refuses
// one this table does not name, so a sixth reference added to Projection
// without a line here fails a test rather than reaching the compiler
// unchecked.
var projectionAttrs = []struct {
	Name string
	Of   func(*Projection) *AttrRef
}{
	{"label", func(p *Projection) *AttrRef { return p.Label }},
	{"color_by", func(p *Projection) *AttrRef { return p.ColorBy }},
	{"group_by", func(p *Projection) *AttrRef { return p.GroupBy }},
	{"size_by", func(p *Projection) *AttrRef { return p.SizeBy }},
	{"sort_by", func(p *Projection) *AttrRef { return p.SortBy }},
}

// checkProjection bounds the projection's own shapes: every attribute
// reference it names, and the declared field keys in `fields`.
func checkProjection(p *Projection) []metamodel.FieldError {
	if p == nil {
		return nil
	}
	var problems []metamodel.FieldError
	for _, attr := range projectionAttrs {
		problems = append(problems, checkAttrRef(attr.Of(p), pointer("project", attr.Name))...)
	}
	for i, key := range p.Fields {
		switch {
		case len(key) > maxFieldKeyLen:
			problems = append(problems, metamodel.FieldError{
				Path:    pointer("project", "fields", i),
				Message: fmt.Sprintf("must be at most %d characters", maxFieldKeyLen),
			})
		case !fieldKeyPattern.MatchString(key):
			problems = append(problems, metamodel.FieldError{
				Path: pointer("project", "fields", i),
				Message: "must be lower_snake_case: a declared field key, without an @ sigil — " +
					"a built-in is not a declared field and is already returned with every node",
			})
		}
	}
	return problems
}

// checkAttrRef judges one attribute reference, in either spelling.
//
// The scalar spelling reports at the reference's own pointer, because
// that is where the caller wrote the string: `"color_by": "@nmae"` has no
// member of its own to blame. The related spelling reports one segment
// deeper, at `/project/color_by/related/via`, because there it does.
func checkAttrRef(ref *AttrRef, ptr string) []metamodel.FieldError {
	if ref == nil {
		return nil
	}
	var problems []metamodel.FieldError
	add := func(at, message string) {
		problems = append(problems, metamodel.FieldError{Path: at, Message: message})
	}
	if ref.Related == nil {
		switch {
		case ref.Attr == "":
			add(ptr, "is empty: name a declared field key, a built-in such as @name, or a "+
				"one-hop related attribute")
		case strings.HasPrefix(ref.Attr, "@"):
			if _, ok := builtinTypes[ref.Attr]; !ok {
				add(ptr, fmt.Sprintf("%q is not a built-in: the built-ins are %s",
					ref.Attr, strings.Join(builtinNames, ", ")))
			}
		case len(ref.Attr) > maxFieldKeyLen:
			add(ptr, fmt.Sprintf("must be at most %d characters", maxFieldKeyLen))
		case !fieldKeyPattern.MatchString(ref.Attr):
			add(ptr, "must be lower_snake_case, a built-in with an @ sigil, or a one-hop "+
				"related attribute")
		}
		return problems
	}
	hop := ref.Related
	problems = append(problems, prefixed(ptr+"/related/via",
		metamodel.RowKeyProblems("via", hop.Via))...)
	if hop.Type != "" {
		problems = append(problems, prefixed(ptr+"/related/type",
			metamodel.RowKeyProblems("type", hop.Type))...)
	}
	switch hop.Direction {
	case "", DirectionOut, DirectionIn, DirectionAny:
	default:
		add(ptr+"/related/direction", fmt.Sprintf("must be %q, %q or %q (got %q)",
			DirectionOut, DirectionIn, DirectionAny, hop.Direction))
	}
	switch {
	case hop.Attr == "":
	case strings.HasPrefix(hop.Attr, "@"):
		if _, ok := builtinTypes[hop.Attr]; !ok {
			add(ptr+"/related/attr", fmt.Sprintf("%q is not a built-in: the built-ins are %s",
				hop.Attr, strings.Join(builtinNames, ", ")))
		}
	case len(hop.Attr) > maxFieldKeyLen:
		add(ptr+"/related/attr", fmt.Sprintf("must be at most %d characters", maxFieldKeyLen))
	case !fieldKeyPattern.MatchString(hop.Attr):
		add(ptr+"/related/attr", "must be lower_snake_case or a built-in with an @ sigil")
	}
	return problems
}

// checkLabelFrom bounds an edge entry's label source. It is the scalar
// half of checkAttrRef and nothing else: label_from names a field of the
// relation or a built-in it has, and there is no related spelling — a
// label read one hop away is a property of a node, and a node is where
// the projection puts it.
//
// Which built-ins a relation actually has is resolution's judgement
// (fieldScope.builtin), not this one: the shape of the string is a
// document question and what a relation carries is a metamodel question.
func checkLabelFrom(label, ptr string) []metamodel.FieldError {
	var problems []metamodel.FieldError
	add := func(message string) {
		problems = append(problems, metamodel.FieldError{Path: ptr, Message: message})
	}
	switch {
	case label == "":
	case strings.HasPrefix(label, "@"):
		if _, ok := builtinTypes[label]; !ok {
			add(fmt.Sprintf("%q is not a built-in: the built-ins are %s",
				label, strings.Join(builtinNames, ", ")))
		}
	case len(label) > maxFieldKeyLen:
		add(fmt.Sprintf("must be at most %d characters", maxFieldKeyLen))
	case !fieldKeyPattern.MatchString(label):
		add("must be lower_snake_case: a field the relation type declares, or a built-in " +
			"with an @ sigil")
	}
	return problems
}

// The bounds every query is judged against, and the caps no query may
// raise them past. The spec's §4.3 table, in one place, because the tool
// description prints it and a second copy would drift.
const (
	DefaultMaxDepth = 4
	HardMaxDepth    = 12
	DefaultMaxNodes = 1000
	HardMaxNodes    = 5000
	DefaultMaxEdges = 4000
	HardMaxEdges    = 20000
)

// checkLimits refuses a declared limit above its hard cap, with
// limit_exceeded and the cap, **rather than clamping it down to the cap**.
//
// That is the opposite of what the saved-view listing does with a page
// limit (Task 11 clamps), and the difference is deliberate rather than an
// inconsistency. A page limit is a caller's opinion about *response
// size*: clamping it returns the same rows in more calls, so a clamped
// listing still answers the question that was asked. A query limit is
// part of the *question* — max_depth 20 asks "what is reachable in twenty
// hops", and answering with four hops is a different question, returned
// with no indication that it was substituted. An agent that asked for
// depth 20 should learn that it cannot have it and decide what to do:
// lower it, or split the view in two.
//
// This is also the one refusal in ParseQuery that is not query_invalid,
// and that is the point of a separate code — the recovery is "lower this
// number to at most N", which is different advice from "this document is
// malformed". TestALimitAboveItsHardCapIsRefusedWithTheCap pins the code,
// the pointer and the cap in the message; TestEveryLimitIsJudgedAgainstItsOwnCap
// pins that all three limits are judged and reported in one pass; and
// TestEveryLimitInTheDocumentIsJudged reads the members off Limits
// itself, so a fourth limit added there without a line below is a bound
// the engine promises and never applies, and is caught rather than
// shipped.
func checkLimits(l *Limits) error {
	if l == nil {
		return nil
	}
	var problems []metamodel.FieldError
	check := func(name string, value *int, hardCap int, what string) {
		if value == nil {
			return
		}
		if *value < 1 {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("limits", name),
				Message: fmt.Sprintf("must be at least 1 (got %d): a bound of zero is a query "+
					"that returns nothing, which is not what any caller means by a limit", *value),
			})
			return
		}
		if *value > hardCap {
			problems = append(problems, metamodel.FieldError{
				Path: pointer("limits", name),
				Message: fmt.Sprintf("asks for %d and %s: lower it, or split the question "+
					"into two views", *value, what),
			})
		}
	}
	check("max_depth", l.MaxDepth, HardMaxDepth,
		fmt.Sprintf("the most this engine walks is %d", HardMaxDepth))
	check("max_nodes", l.MaxNodes, HardMaxNodes,
		fmt.Sprintf("the most this engine returns is %d", HardMaxNodes))
	check("max_edges", l.MaxEdges, HardMaxEdges,
		fmt.Sprintf("the most this engine returns is %d", HardMaxEdges))
	if len(problems) == 0 {
		return nil
	}
	sort.SliceStable(problems, func(i, j int) bool {
		return pointerLess(problems[i].Path, problems[j].Path)
	})
	return &QueryError{Code: CodeLimitExceeded, Fields: problems}
}

// fieldTypeOrder is the order the generated description prints the
// operator table in, and it is the declaration order of
// internal/metamodel's own six types with this package's pseudo-type
// last. A slice rather than a range over the map, because Go randomises
// map iteration and a tool description that comes back in a different
// order on every start is a description a client cannot diff.
//
// TestTheOperatorDescriptionIsGeneratedFromTheTable requires every key of
// operatorsByType to appear here and every entry here to be a key of it,
// so a seventh field type cannot be printed without a row or given a row
// without being printed.
var fieldTypeOrder = []metamodel.FieldType{
	metamodel.FieldText, metamodel.FieldLongText, metamodel.FieldNumber,
	metamodel.FieldBool, metamodel.FieldEnum, metamodel.FieldListText,
	TypeTimestamp,
}

// OperatorDescription is the operator table as prose, **generated from
// operatorsByType and from the built-in table and from nothing else**.
//
// It exists for the reason RendererDescription does, and the risk is the
// same one: the table *is* the contract — an operator a field type does
// not admit is a refusal, and one it does admit and nobody documents is
// an operator no agent will ever send — so a hand-written paragraph
// beside it is a paragraph that will be wrong the first time a row moves.
// TestTheOperatorDescriptionIsGeneratedFromTheTable reads this text back
// and compares it with the table in both directions.
//
// The two sentences that are not rows are here because they are
// properties of the table that no row can state. `list<text>` is the
// whole list family, because the metamodel declares no list<number> and
// no list<enum> for these operators to apply to, so `contains_any` over
// a list of numbers is a string comparison today. And the built-ins are
// judged as though they were declared fields of the type printed beside
// them, which is what makes `@created_at gt 20` a comparison between a
// timestamp and an integer rather than a legal one.
func OperatorDescription() string {
	var b strings.Builder
	b.WriteString("The operator table. Which operators a condition may use is decided by " +
		"the *declared type* of the field it names, and an operator that type does not " +
		"answer is refused at its own pointer rather than compiled into something that " +
		"draws nothing.\n")
	for _, typ := range fieldTypeOrder {
		ops := operatorsByType[typ]
		names := make([]string, 0, len(ops))
		for _, op := range ops {
			names = append(names, string(op))
		}
		fmt.Fprintf(&b, "\n- %s: %s", typ, strings.Join(names, ", "))
	}
	b.WriteString("\n\nlist<text> is the whole list family: the metamodel declares no " +
		"list<number> and no list<enum>, so contains_any over a list of numbers is a " +
		"string comparison.\n")
	b.WriteString("\nThe built-in attributes are written with an @ sigil and are judged " +
		"as fields of the type named here")
	for _, builtin := range builtins {
		fmt.Fprintf(&b, "\n- %s: %s", builtin.Name, builtin.Type)
	}
	b.WriteString("\n\nA declared field is named without a sigil. A field this game does " +
		"not declare is refused, and so is an operator its declared type does not " +
		"answer — both at the pointer of the condition that wrote it.\n")
	return b.String()
}
