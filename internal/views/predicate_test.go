package views

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// TestTheOperatorTableIsTheSpecsTable pins the table itself, type by
// type. It is a table test over data rather than behaviour on purpose:
// the table *is* the contract, the tool description is generated from it
// (Task 15), and a silent addition to it is a silent change to the
// language.
func TestTheOperatorTableIsTheSpecsTable(t *testing.T) {
	want := map[metamodel.FieldType][]Operator{
		metamodel.FieldText:     {OpEq, OpNeq, OpIn, OpContains, OpStartsWith, OpMatches, OpExists},
		metamodel.FieldLongText: {OpEq, OpNeq, OpIn, OpContains, OpStartsWith, OpMatches, OpExists},
		metamodel.FieldNumber:   {OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpBetween, OpIn, OpExists},
		metamodel.FieldBool:     {OpEq, OpExists},
		metamodel.FieldEnum:     {OpEq, OpNeq, OpIn, OpExists},
		metamodel.FieldListText: {OpContains, OpContainsAny, OpContainsAll, OpEmpty,
			OpLengthEq, OpLengthGte, OpLengthLte},
		TypeTimestamp: {OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpBetween, OpExists},
	}
	for typ, ops := range want {
		got := OperatorsFor(typ)
		if len(got) != len(ops) {
			t.Fatalf("%s admits %v, want %v", typ, got, ops)
		}
		for _, op := range ops {
			if !AdmitsOperator(typ, op) {
				t.Errorf("%s must admit %q", typ, op)
			}
		}
	}
	// Every type the table names is a type this engine can be asked
	// about, and no type outside `want` is in it: an operator row added
	// for a seventh type would otherwise be invisible to this test.
	for typ := range operatorsByType {
		if _, named := want[typ]; !named {
			t.Errorf("the table admits operators for %q, which this test does not name: a row "+
				"added here is a change to the language and belongs in the spec's table too", typ)
		}
	}
	// The negative half, which is the half that matters: a text field
	// must not admit a range, and a number must not admit a substring.
	if AdmitsOperator(metamodel.FieldText, OpGt) {
		t.Error("text must not admit gt: a lexicographic > on a jsonb text is not the comparison anybody meant")
	}
	if AdmitsOperator(metamodel.FieldNumber, OpContains) {
		t.Error("number must not admit contains")
	}
	if AdmitsOperator(metamodel.FieldListText, OpEq) {
		t.Error("list<text> must not admit eq: equality on a list is a question the language does not answer")
	}
	// A type nobody declared admits nothing, rather than admitting
	// everything by falling off the end of a lookup.
	if len(OperatorsFor(metamodel.FieldType("colour"))) != 0 {
		t.Error("an undeclared field type must admit no operator at all")
	}
}

// TestThereIsNoRegexOperator pins a deliberate absence. A regex over
// jsonb is unbounded work inside a statement timeout this design keeps
// small, and nothing in the worked examples needs one. `matches` is a
// literal-anchored glob and says so.
func TestThereIsNoRegexOperator(t *testing.T) {
	for _, typ := range []metamodel.FieldType{metamodel.FieldText, metamodel.FieldLongText} {
		for _, op := range OperatorsFor(typ) {
			if op == "regex" || op == "matches_regex" {
				t.Fatalf("%s admits %q, and this language has no regular expressions", typ, op)
			}
		}
	}
	// And the spelling is refused as an operator at all, rather than
	// being quietly accepted and then never admitted by any type.
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"title","op":"regex","value":"^H"}}]}`,
		"/from/0/where/op", `"regex" is not an operator this language has`)
}

// TestAnOperatorCannotBeHalfAdded is the reason the table is data rather
// than a switch, stated as an assertion. Three derived structures read
// from `operatorsByType` — the known-spelling set, the printed
// vocabulary, and the value-shape table — and an operator that reaches
// one of them without reaching the others is an operator whose value is
// judged by the zero value of `valueShape`, silently, as a scalar.
//
// Both directions are asserted: a row added to the table with no shape
// fails here, and a shape declared for a spelling no type admits fails
// here too.
func TestAnOperatorCannotBeHalfAdded(t *testing.T) {
	for op := range knownOperators {
		if _, ok := shapeOf[op]; !ok {
			t.Errorf("%q is in the operator table and has no value shape: it would be judged as "+
				"a scalar by the zero value of valueShape, which is the half-implemented "+
				"operator this test exists to refuse", op)
		}
	}
	for op := range shapeOf {
		if !knownOperators[op] {
			t.Errorf("%q has a value shape and no type admits it: it is unreachable, and an "+
				"unreachable arm is a spelling somebody meant to add to the table", op)
		}
	}
	// The printed vocabulary is the third reader, and an agent that
	// mistyped an operator is told the whole of it.
	names := allOperatorNames()
	for op := range knownOperators {
		if !strings.Contains(names, string(op)) {
			t.Errorf("%q is admitted by a type and is not in the vocabulary the refusal prints", op)
		}
	}
}

// TestEveryOperatorNameIsEitherKnownOrNamedBack drives the whole table
// through the public surface: every spelling any type admits parses, and
// a spelling no type admits is named back rather than falling off the end
// of a default that says nothing.
func TestEveryOperatorNameIsEitherKnownOrNamedBack(t *testing.T) {
	// One legal right-hand side per value shape, so an operator is
	// exercised with the shape it declares rather than with a scalar that
	// half of them would refuse.
	value := map[valueShape]string{
		shapeScalar: `"x"`,
		shapeList:   `["x"]`,
		shapePair:   `[1,2]`,
		shapeBool:   `true`,
		shapeNumber: `2`,
	}
	for op := range knownOperators {
		doc := `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"` + string(op) +
			`","value":` + value[shapeOf[op]] + `}}]}`
		if _, err := ParseQuery([]byte(doc)); err != nil {
			t.Errorf("%q is in the operator table and must parse with a %v value: %v",
				op, shapeOf[op], err)
		}
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"approx","value":1}}]}`,
		"/from/0/where/op", `"approx" is not an operator this language has`)
}

func TestAPredicateIsExactlyOneOfFourShapes(t *testing.T) {
	// The positive control: all four shapes parse, so a checkPredicate
	// that refused everything could not pass the rest of this test.
	for _, where := range []string{
		`{"field":"rank","op":"eq","value":1}`,
		`{"all":[{"field":"rank","op":"eq","value":1}]}`,
		`{"any":[{"field":"rank","op":"eq","value":1}]}`,
		`{"not":{"field":"rank","op":"eq","value":1}}`,
	} {
		if _, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","where":` + where + `}]}`)); err != nil {
			t.Fatalf("%s must parse: %v", where, err)
		}
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{}}]}`, "/from/0/where",
		`must be one of "all", "any", "not" or a field test`)
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"all":[],"field":"x","op":"eq","value":1}}]}`,
		"/from/0/where", "combines a combinator with a field test")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"all":[]}}]}`, "/from/0/where/all",
		"must hold at least one condition")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"all":[{"field":"a","op":"eq","value":1}],`+
		`"any":[{"field":"b","op":"eq","value":1}]}}]}`, "/from/0/where",
		`sets more than one of "all", "any" and "not"`)
}

func TestAFieldTestNamesAFieldAndAnOperator(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"op":"eq","value":1}}]}`,
		"/from/0/where/field", "is required")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","value":1}}]}`,
		"/from/0/where/op", "is required")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"approximately","value":1}}]}`,
		"/from/0/where/op", `is not an operator this language has`)
}

// TestAFieldKeyIsAFieldKeyOrASigil pins the ambiguity the sigil exists to
// resolve: a game is free to declare a field literally called `name`, and
// `@name` is how a query asks for the row's own name instead.
func TestAFieldKeyIsAFieldKeyOrASigil(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","where":` +
		`{"field":"@name","op":"contains","value":"Hogger"}}]}`))
	if err != nil {
		t.Fatalf("a sigil must parse: %v", err)
	}
	if !q.From[0].Where.FieldRef.Builtin {
		t.Fatal("@name must parse as a built-in, not as a declared field key")
	}
	// The other half of the ambiguity: a declared field called `name` is
	// a different reference, and is not marked as a built-in.
	q, err = ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","where":` +
		`{"field":"name","op":"contains","value":"Hogger"}}]}`))
	if err != nil {
		t.Fatalf("a game may declare a field called name: %v", err)
	}
	if q.From[0].Where.FieldRef.Builtin {
		t.Fatal("a declared field called name must not be read as the built-in @name")
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"@nmae","op":"eq","value":"x"}}]}`,
		"/from/0/where/field", `is not a built-in: the built-ins are @name, @key, @type, @invalid, @created_at`)
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"Min Level","op":"eq","value":1}}]}`,
		"/from/0/where/field", "must be lower_snake_case")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"`+strings.Repeat("a", maxFieldKeyLen+1)+
		`","op":"eq","value":1}}]}`,
		"/from/0/where/field", "must be at most 64 characters")
}

// TestTheBuiltinVocabularyIsListedInTheOrderItIsDeclared pins the two
// halves of the built-in table against each other. The printed list is
// what an agent that mistyped one reads, and it is deliberately *not*
// sorted alphabetically: @name first is the one an agent reaches for.
func TestTheBuiltinVocabularyIsListedInTheOrderItIsDeclared(t *testing.T) {
	want := []string{AttrName, AttrKey, AttrType, AttrInvalid, AttrCreatedAt}
	if len(builtinNames) != len(want) {
		t.Fatalf("the printed vocabulary is %v, want %v", builtinNames, want)
	}
	for i, name := range want {
		if builtinNames[i] != name {
			t.Fatalf("builtinNames[%d] = %q, want %q", i, builtinNames[i], name)
		}
		if _, ok := builtinTypes[name]; !ok {
			t.Fatalf("%q is printed as a built-in and has no declared type: the operators it "+
				"admits would be the operators of the empty type, which is none of them", name)
		}
	}
	if len(builtinTypes) != len(want) {
		t.Fatalf("a built-in has a type and is not printed: %v against %v", builtinTypes, want)
	}
	// Every built-in's type admits at least one operator, which is what
	// makes it usable in a field test at all.
	for name, typ := range builtinTypes {
		if len(OperatorsFor(typ)) == 0 {
			t.Errorf("the built-in %s is declared %q, which admits no operator", name, typ)
		}
	}
}

// TestAnOperatorsValueShapeIsCheckedBeforeItsType is what stops a
// `between` with three numbers reaching the compiler, where it would
// either panic or silently drop one.
func TestAnOperatorsValueShapeIsCheckedBeforeItsType(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"between","value":[20]}}]}`,
		"/from/0/where/value", "must be a list of exactly two values")
	// The case a `len(list) < 2` check would miss: a pair is exactly two
	// bounds, and a third value is one the compiler would drop in silence.
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"between","value":[20,30,40]}}]}`,
		"/from/0/where/value", "must be a list of exactly two values")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"in","value":20}}]}`,
		"/from/0/where/value", "must be a list")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"in","value":[]}}]}`,
		"/from/0/where/value", "must hold at least one value")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"exists","value":"yes"}}]}`,
		"/from/0/where/value", "must be true or false")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"tags","op":"length_eq","value":"two"}}]}`,
		"/from/0/where/value", "must be a number")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"eq","value":[1,2]}}]}`,
		"/from/0/where/value", "must be a single value")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"eq"}}]}`,
		"/from/0/where/value", "is required")

	// The positive control: the same shapes, correct, all parse.
	for _, where := range []string{
		`{"field":"min_level","op":"between","value":[20,30]}`,
		`{"field":"min_level","op":"in","value":[20,30]}`,
		`{"field":"min_level","op":"exists","value":true}`,
		`{"field":"tags","op":"length_eq","value":2}`,
		`{"field":"min_level","op":"eq","value":1}`,
	} {
		if _, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","where":` + where + `}]}`)); err != nil {
			t.Fatalf("%s must parse: %v", where, err)
		}
	}
}

// TestTheValueListCapIsRefusedAtParseTime is the last of the collection
// bounds, and the only one Task 3 owns: the right-hand side of `in`,
// `contains_any` and `contains_all`, each of which compiles to an
// `= ANY($n)` the planner has to materialise.
func TestTheValueListCapIsRefusedAtParseTime(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"in","value":[`)
	for i := 0; i <= MaxValueList; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("1")
	}
	b.WriteString(`]}}]}`)
	parseFails(t, b.String(), "/from/0/where/value", "the most an operator takes is 500")
}

// TestAParameterStandsInForOneScalarAndSaysSoWhereItCannot pins the one
// place a `{"param": …}` is refused by shape rather than by type: a
// parameter is one scalar, so an operator that takes a list or a pair
// cannot be given one, and the refusal says which.
func TestAParameterStandsInForOneScalarAndSaysSoWhereItCannot(t *testing.T) {
	ok := `{"v":1,"params":[{"key":"lvl","type":"number"}],"from":[{"type":"quest",` +
		`"where":{"field":"min_level","op":"gte","value":{"param":"lvl"}}}]}`
	if _, err := ParseQuery([]byte(ok)); err != nil {
		t.Fatalf("a parameter in a scalar position must parse: %v", err)
	}
	parseFails(t, `{"v":1,"params":[{"key":"lvl","type":"number"}],"from":[{"type":"quest",`+
		`"where":{"field":"min_level","op":"in","value":{"param":"lvl"}}}]}`,
		"/from/0/where/value", "is a parameter, and \"in\" takes a list of values")
	// An object that is *not* exactly {"param": "<string>"} is not a
	// lenient parameter reference: it is a value the language does not
	// have, and is refused as a shape.
	parseFails(t, `{"v":1,"from":[{"type":"quest",`+
		`"where":{"field":"min_level","op":"eq","value":{"param":"lvl","fallback":1}}}]}`,
		"/from/0/where/value", "must be a single value")
	parseFails(t, `{"v":1,"from":[{"type":"quest",`+
		`"where":{"field":"min_level","op":"eq","value":{"param":7}}}]}`,
		"/from/0/where/value", "must be a single value")
}

func TestAPredicateTreeIsBounded(t *testing.T) {
	doc := `{"v":1,"from":[{"type":"quest","where":`
	for i := 0; i <= MaxPredicateDepth; i++ {
		doc += `{"not":`
	}
	doc += `{"field":"a","op":"eq","value":1}`
	for i := 0; i <= MaxPredicateDepth; i++ {
		doc += `}`
	}
	doc += `}]}`
	parseFails(t, doc, "/from/0/where", "is nested more than 8 deep")

	// One level shallower is accepted, so the bound is the bound and not
	// an accident of the tree being nested at all.
	ok := `{"v":1,"from":[{"type":"quest","where":`
	for i := 0; i < MaxPredicateDepth-1; i++ {
		ok += `{"not":`
	}
	ok += `{"field":"a","op":"eq","value":1}`
	for i := 0; i < MaxPredicateDepth-1; i++ {
		ok += `}`
	}
	ok += `}]}`
	if _, err := ParseQuery([]byte(ok)); err != nil {
		t.Fatalf("a tree of exactly %d levels must parse: %v", MaxPredicateDepth, err)
	}
}

// TestAPredicateTreeIsBoundedByItsNodeCount is the other half of the
// tree bound: a wide tree is as expensive as a deep one, and 200
// conditions in one `all` is a query nobody wrote by hand.
//
// The problem is reported **once**, at the predicate's own root, rather
// than once per sibling past the cap: a caller that sent 500 conditions
// does not need 300 copies of the same sentence.
func TestAPredicateTreeIsBoundedByItsNodeCount(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[{"type":"quest","where":{"all":[`)
	for i := 0; i <= MaxPredicateNodes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"field":"rank","op":"eq","value":1}`)
	}
	b.WriteString(`]}}]}`)
	parseFails(t, b.String(), "/from/0/where", "has more than 200 conditions")

	_, err := ParseQuery([]byte(b.String()))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	count := 0
	for _, f := range qe.Fields {
		if strings.Contains(f.Message, "conditions in one tree") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the node cap must be reported once, got %d copies", count)
	}
}

// TestAPredicateValueHasItsOwnDepthBound pins this task's own decision
// about the hand-off Task 2's review recorded: MaxPredicateDepth bounds
// the predicate *tree*, and a `value` is an `any` that the tree bound
// never looks inside.
//
// Without this the only bound on a value is encoding/json's own, at ten
// thousand, which reports at pointer "" in the decoder's wording and says
// nothing about the language. The elements of a list are the case the
// shape checks miss entirely: `in` counts its list and never looks into
// it.
func TestAPredicateValueHasItsOwnDepthBound(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":`+
		`{"field":"tags","op":"in","value":[["a"]]}}]}`,
		"/from/0/where/value", "and a value in this language is at most 2 deep")
	parseFails(t, `{"v":1,"from":[{"type":"quest","where":`+
		`{"field":"min_level","op":"between","value":[[1],[2]]}}]}`,
		"/from/0/where/value", "and a value in this language is at most 2 deep")
	// The positive control, and the two legal shapes that sit exactly on
	// the bound: a list of scalars, and a parameter reference.
	for _, value := range []string{`["a","b"]`, `{"param":"p"}`, `"a"`, `1`, `true`} {
		doc := `{"v":1,"params":[{"key":"p","type":"text"}],"from":[{"type":"quest","where":` +
			`{"field":"tags","op":"contains","value":` + value + `}}]}`
		if value == `["a","b"]` {
			doc = strings.Replace(doc, `"op":"contains"`, `"op":"in"`, 1)
		}
		if _, err := ParseQuery([]byte(doc)); err != nil {
			t.Fatalf("the value %s sits on the bound and must parse: %v", value, err)
		}
	}
}

// TestAnAttributeReferenceIsAStringOrAOneHopRelated pins both spellings
// and the reason the related form is one hop and not many: a multi-hop
// colour source is a traversal, and traversals belong in `traverse`,
// where they are bounded and visible.
func TestAnAttributeReferenceIsAStringOrAOneHopRelated(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],"project":{"color_by":` +
		`{"related":{"via":"takes_place_in","direction":"out","type":"zone","attr":"@name"}}}}`))
	if err != nil {
		t.Fatalf("a related attribute must parse: %v", err)
	}
	rel := q.Project.ColorBy.Related
	if rel == nil || rel.Via != "takes_place_in" || rel.Type != "zone" || rel.Attr != "@name" {
		t.Fatalf("the related hop did not survive the parse: %+v", rel)
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":`+
		`{"related":{"via":"a","direction":"out","type":"z","attr":"@name","then":{"via":"b"}}}}}`,
		"", `unknown key "then"`)
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":`+
		`{"related":{"direction":"out","type":"z","attr":"@name"}}}}`,
		"/project/color_by/related/via", "is required")
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":`+
		`{"related":{"via":"a","direction":"sideways"}}}}`,
		"/project/color_by/related/direction", `must be "out", "in" or "any"`)
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":`+
		`{"related":{"via":"a","attr":"@nmae"}}}}`,
		"/project/color_by/related/attr", "is not a built-in")
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":`+
		`{"related":{"via":"a","type":"has space"}}}}`,
		"/project/color_by/related/type", "must be letters, digits, underscores or hyphens")
	// The scalar spelling's own refusals, at the reference's own pointer
	// because that is where the caller wrote the string.
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":"@nmae"}}`,
		"/project/color_by", "is not a built-in")
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":"Min Level"}}`,
		"/project/color_by", "must be lower_snake_case")
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"color_by":""}}`,
		"/project/color_by", "is empty")
}

// TestEveryProjectionAttributeIsChecked is the enumeration guard for the
// one place in this task that has to enumerate: a projection names five
// attribute references by their own member names, and a sixth added to
// Projection without a line in projectionAttrs would be an unchecked
// attribute reference reaching the compiler.
func TestEveryProjectionAttributeIsChecked(t *testing.T) {
	for _, name := range []string{"label", "color_by", "group_by", "size_by", "sort_by"} {
		parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"`+name+`":"@nmae"}}`,
			"/project/"+name, "is not a built-in")
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"project":{"fields":["Min Level"]}}`,
		"/project/fields/0", "must be lower_snake_case")
	// The positive control: the same five, well spelled, all parse.
	if _, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],"project":` +
		`{"label":"@name","color_by":"rank","group_by":"@type","size_by":"weight",` +
		`"sort_by":"@created_at","fields":["min_level"]}}`)); err != nil {
		t.Fatalf("a well-spelled projection must parse: %v", err)
	}
}

// TestALimitAboveItsHardCapIsRefusedWithTheCap pins the one refusal in
// ParseQuery that is not query_invalid, and the direction of it: the
// limit is refused with its cap, not clamped down to it.
func TestALimitAboveItsHardCapIsRefusedWithTheCap(t *testing.T) {
	_, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],"limits":{"max_depth":20}}`))
	if err == nil {
		t.Fatal("max_depth 20 must be refused")
	}
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("must be limit_exceeded, not query_invalid: %v", err)
	}
	if !strings.Contains(err.Error(), "the most this engine walks is 12") {
		t.Fatalf("must carry the cap, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 1 || qe.Fields[0].Path != "/limits/max_depth" {
		t.Fatalf("must be reported at /limits/max_depth, got %v", qe.Fields)
	}
	// It is refused rather than clamped, which is the assertion that
	// matters: a clamp would return a query, and that query would answer
	// a different question in silence.
	if _, err := ParseQuery([]byte(
		`{"v":1,"from":[{"type":"quest"}],"limits":{"max_depth":12}}`)); err != nil {
		t.Fatalf("a limit exactly on the cap must parse: %v", err)
	}
}

// TestEveryLimitIsJudgedAgainstItsOwnCap walks all three, so a fourth
// limit added to Limits without a line in checkLimits is a limit nothing
// bounds.
func TestEveryLimitIsJudgedAgainstItsOwnCap(t *testing.T) {
	for _, tc := range []struct{ name, over, cap string }{
		{"max_depth", "13", "the most this engine walks is 12"},
		{"max_nodes", "5001", "the most this engine returns is 5000"},
		{"max_edges", "20001", "the most this engine returns is 20000"},
	} {
		_, err := ParseQuery([]byte(
			`{"v":1,"from":[{"type":"quest"}],"limits":{"` + tc.name + `":` + tc.over + `}}`))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("%s above its cap must be limit_exceeded, got %v", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.cap) {
			t.Errorf("%s must carry its cap: %v", tc.name, err)
		}
	}
	// A limit below 1 is a different mistake with a different sentence,
	// and it is still limit_exceeded rather than a silent default.
	_, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],"limits":{"max_nodes":0}}`))
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("max_nodes 0 must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "must be at least 1") {
		t.Fatalf("must say what the floor is, got %v", err)
	}
	// All three together are reported in one pass, because an agent that
	// set every bound too high fixes them in one round trip.
	_, err = ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],` +
		`"limits":{"max_depth":13,"max_nodes":5001,"max_edges":20001}}`))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 3 {
		t.Fatalf("all three limits must be reported in one pass, got %v", qe.Fields)
	}
}

// TestNoDocumentTypeCarriesCallerTextInAByteSlice verifies, rather than
// assumes, the claim Task 3 inherited: everything added under the text
// walk is bounded by construction.
//
// It lives here rather than beside the walk because the question is not
// about the walk's own behaviour — TestEveryStringInAQueryIsBounded pins
// that over the twenty-seven positions the document has — but about the
// *shapes* later tasks are allowed to add. walkStrings visits a
// json.RawMessage by decoding it and skips every other byte slice
// deliberately, because a plain []byte decodes from base64 and its bytes
// are not a string the caller wrote. That rule is stated in a comment and
// was pinned by nothing: a field declared []byte anywhere in the document
// would be caller text the bound never sees, and the walk would report
// nothing at all rather than failing.
//
// The graph is walked over reflect.Type, so it covers every type
// reachable from Query whether or not a test document happens to populate
// it, which is what makes it a guard for the fourteen tasks that add
// types under this walk rather than a restatement of today's shapes.
func TestNoDocumentTypeCarriesCallerTextInAByteSlice(t *testing.T) {
	rawMessage := reflect.TypeOf(json.RawMessage(nil))
	seen := map[reflect.Type]bool{}
	structs := map[string]bool{}

	var walk func(typ reflect.Type, at string)
	walk = func(typ reflect.Type, at string) {
		if typ == nil || seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Chan, reflect.Func, reflect.UnsafePointer:
			t.Errorf("%s is a %s, which walkStrings ignores in silence: a document member must "+
				"be a shape the walk descends into", at, typ.Kind())
			return
		case reflect.Slice:
			if typ.Elem().Kind() == reflect.Uint8 && typ != rawMessage {
				t.Errorf("%s is a byte slice (%s): walkStrings skips one deliberately, because a "+
					"plain []byte decodes from base64 and its bytes are not a string the caller "+
					"wrote — so caller text here would escape the text bound entirely. Use a "+
					"string, or json.RawMessage for a deferred document member", at, typ)
				return
			}
			walk(typ.Elem(), at+"[]")
		case reflect.Array:
			walk(typ.Elem(), at+"[]")
		case reflect.Pointer:
			walk(typ.Elem(), at)
		case reflect.Map:
			walk(typ.Key(), at+"{key}")
			walk(typ.Elem(), at+"{}")
		case reflect.Struct:
			structs[typ.Name()] = true
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				walk(f.Type, at+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(Query{}), "Query")

	// The sanity half: a walk that reached nothing would pass every
	// assertion above. The three types this task owns must be in the
	// graph, along with the document's own root shapes.
	for _, name := range []string{"Query", "Selector", "Step", "Projection", "Limits",
		"Predicate", "FieldRef", "AttrRef", "RelHop"} {
		if !structs[name] {
			t.Errorf("the type graph reachable from Query does not include %s: this test is "+
				"passing over a graph it never walked", name)
		}
	}
}

// TestEveryProjectionAttributeReferenceHasALineInTheTable is the half of
// the projection guard that a hardcoded list cannot be: it asks
// Projection itself which of its members are attribute references, and
// refuses one that projectionAttrs does not name.
//
// TestEveryProjectionAttributeIsChecked above drives five *known* names
// through ParseQuery, which proves the five that exist are checked and
// nothing about a sixth — a member added to Projection with no line in
// projectionAttrs leaves that test, and the whole suite, green while the
// reference reaches the compiler unchecked. Task 9 adds projection
// attributes. This is what fails for it.
//
// The walk is over reflect.Type, like
// TestNoDocumentTypeCarriesCallerTextInAByteSlice, so it covers a shape
// no test document populates.
func TestEveryProjectionAttributeReferenceHasALineInTheTable(t *testing.T) {
	named := map[string]bool{}
	for _, attr := range projectionAttrs {
		named[attr.Name] = true
	}
	attrRef := reflect.TypeOf((*AttrRef)(nil))
	projection := reflect.TypeOf(Projection{})
	found := 0
	for i := 0; i < projection.NumField(); i++ {
		f := projection.Field(i)
		if f.Type != attrRef {
			continue
		}
		found++
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			t.Errorf("Projection.%s is an attribute reference with no json tag: the pointer a "+
				"refusal reports is the member name the caller wrote", f.Name)
			continue
		}
		if !named[name] {
			t.Errorf("Projection.%s (json %q) is an attribute reference with no line in "+
				"projectionAttrs, so checkProjection never sees it and a misspelled built-in "+
				"in it reaches the compiler unchecked: add {%q, func(p *Projection) *AttrRef "+
				"{ return p.%s }} to the table", f.Name, name, name, f.Name)
		}
	}
	// The sanity half, for the same reason the type-graph test has one: a
	// walk that matched nothing would pass every assertion above.
	if found != len(projectionAttrs) {
		t.Errorf("Projection declares %d attribute references and projectionAttrs has %d lines: "+
			"either a member has no line, or the table names one that no longer exists",
			found, len(projectionAttrs))
	}
}

// TestEveryLimitInTheDocumentIsJudged is the same guard for Limits, and
// it exists for the same reason: TestEveryLimitIsJudgedAgainstItsOwnCap
// drives three known names, so a fourth *int added to Limits without a
// line in checkLimits is a limit nothing bounds and nothing notices.
// Task 12 adds limits.
//
// It drives each member ParseQuery rather than reading checkLimits, so
// what it pins is the observable behaviour — an over-cap limit is
// refused — rather than the shape of the function that produces it.
func TestEveryLimitInTheDocumentIsJudged(t *testing.T) {
	limits := reflect.TypeOf(Limits{})
	intPtr := reflect.TypeOf((*int)(nil))
	found := 0
	for i := 0; i < limits.NumField(); i++ {
		f := limits.Field(i)
		if f.Type != intPtr {
			continue
		}
		found++
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			t.Errorf("Limits.%s has no json tag", f.Name)
			continue
		}
		// Above every cap this engine could plausibly declare, so the
		// test does not have to know which cap belongs to this member.
		_, err := ParseQuery([]byte(
			`{"v":1,"from":[{"type":"quest"}],"limits":{"` + name + `":2000000000}}`))
		if !errors.Is(err, ErrLimitExceeded) {
			t.Errorf("Limits.%s (json %q) set to 2000000000 parses: checkLimits has no line for "+
				"it, so it is a bound the engine promises and never applies (got %v)",
				f.Name, name, err)
			continue
		}
		if _, err := ParseQuery([]byte(
			`{"v":1,"from":[{"type":"quest"}],"limits":{"` + name + `":0}}`)); !errors.Is(
			err, ErrLimitExceeded) {
			t.Errorf("Limits.%s (json %q) set to 0 parses: a bound of zero returns nothing, "+
				"which is not what any caller means by a limit (got %v)", f.Name, name, err)
		}
	}
	if found != 3 {
		t.Errorf("Limits declares %d int members and this test expected 3: if a limit was added, "+
			"give it a line in checkLimits and update this count", found)
	}
}

// TestEveryPredicateInAStepIsChecked is the third of the same guard. A
// Step carries two predicate trees under two different pointers, each
// with its own checkPredicate call written out by hand in checkQuery, and
// a third added without a call would be an unbounded, unvalidated tree
// stored in a saved view. Tasks 7 and 8 add step members.
func TestEveryPredicateInAStepIsChecked(t *testing.T) {
	step := reflect.TypeOf(Step{})
	predicate := reflect.TypeOf((*Predicate)(nil))
	found := 0
	for i := 0; i < step.NumField(); i++ {
		f := step.Field(i)
		if f.Type != predicate {
			continue
		}
		found++
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			t.Errorf("Step.%s has no json tag", f.Name)
			continue
		}
		// A field key no grammar admits, so the refusal can only come
		// from checkPredicate having walked this member.
		parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],"traverse":[{"from":"q",`+
			`"via":"leads_to","`+name+`":{"field":"Min Level","op":"eq","value":1}}]}`,
			"/traverse/0/"+name+"/field", "must be lower_snake_case")
	}
	if found != 2 {
		t.Errorf("Step declares %d predicate members and this test expected 2: if one was added, "+
			"give it a checkPredicate call in checkQuery and update this count", found)
	}
}
