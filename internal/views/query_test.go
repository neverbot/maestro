package views

import (
	"errors"
	"strings"
	"testing"
)

// parseFails is the assertion every negative case below uses: the parse
// is refused, with query_invalid, at exactly this pointer, and the
// message says this. Asserting the pointer as well as the code is what
// makes these tests useful to the agent that reads the error.
func parseFails(t *testing.T, doc, wantPointer, wantMessageContains string) {
	t.Helper()
	_, err := ParseQuery([]byte(doc))
	if err == nil {
		t.Fatalf("this document must be refused: %s", doc)
	}
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("must be query_invalid, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	for _, f := range qe.Fields {
		if f.Path == wantPointer && strings.Contains(f.Message, wantMessageContains) {
			return
		}
	}
	t.Fatalf("no problem at %q containing %q; got %v", wantPointer, wantMessageContains, qe.Fields)
}

// TestTheSimplestUsefulQueryParses is the positive control every negative
// test below leans on. Without it, a ParseQuery that refused everything
// would pass this whole file.
func TestTheSimplestUsefulQueryParses(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}]}`))
	if err != nil {
		t.Fatalf("must parse: %v", err)
	}
	if len(q.From) != 1 || q.From[0].Type != "quest" {
		t.Fatalf("from did not survive the parse: %+v", q.From)
	}
	// `as` defaults to the type key, and the default is applied at parse
	// time rather than at compile time so that every later stage sees one
	// spelling of the set name.
	if q.From[0].As != "quest" {
		t.Fatalf("as must default to the type key, got %q", q.From[0].As)
	}
}

func TestAQueryMustDeclareItsVersion(t *testing.T) {
	parseFails(t, `{"from":[{"type":"quest"}]}`, "/v", "must be 1")
	parseFails(t, `{"v":2,"from":[{"type":"quest"}]}`, "/v", "must be 1")
}

// TestAnUnknownKeyIsRefusedRatherThanIgnored is load-bearing beyond
// tidiness: it is what reserves the extension point for the analysis
// seam (this plan's open question O8). A document with a "source" key is
// refused today, so adding "source" later is additive rather than a
// reinterpretation of a document that used to succeed silently.
func TestAnUnknownKeyIsRefusedRatherThanIgnored(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest"}],"source":{"analysis":"unreachable"}}`,
		"", `unknown key "source"`)
	parseFails(t, `{"v":1,"from":[{"type":"quest","tyep":"typo"}]}`, "", `unknown key "tyep"`)
}

func TestAQueryMustSeedFromSomething(t *testing.T) {
	parseFails(t, `{"v":1,"from":[]}`, "/from",
		"must name at least one entity type to start from")
	parseFails(t, `{"v":1}`, "/from",
		"must name at least one entity type to start from")
}

func TestATypeKeyObeysTheRowKeyGrammar(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"has space"}]}`, "/from/0/type",
		"must be letters, digits, underscores or hyphens")
	parseFails(t, `{"v":1,"from":[{"type":""}]}`, "/from/0/type", "is required")
}

// TestASetNameIsDeclaredOnceAndReferencedByName pins the two halves of
// the naming rule. There is deliberately no implicit "previous step":
// the spec's §2.2 argues that an implicit chain makes a two-branch query
// impossible to read.
func TestASetNameIsDeclaredOnceAndReferencedByName(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone","as":"q"}]}`,
		"/from/1/as", `the set name "q" is already used`)
	parseFails(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],`+
			`"traverse":[{"from":"nope","via":"requires","as":"r"}]}`,
		"/traverse/0/from", `no set named "nope" is declared before this step`)
	parseFails(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],`+
			`"traverse":[{"from":"r","via":"requires","as":"r"}]}`,
		"/traverse/0/from", `no set named "r" is declared before this step`)
}

func TestADirectionIsOneOfThree(t *testing.T) {
	parseFails(t,
		`{"v":1,"from":[{"type":"quest","as":"q"}],`+
			`"traverse":[{"from":"q","via":"requires","direction":"sideways","as":"r"}]}`,
		"/traverse/0/direction", `must be "out", "in" or "any"`)
}

// TestDepthTakesBothSpellings pins that a bare integer and a {min,max}
// object mean the same thing, and that the object's halves are checked
// against each other.
func TestDepthTakesBothSpellings(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"q"}],` +
		`"traverse":[{"from":"q","via":"requires","depth":3,"as":"r"}]}`))
	if err != nil {
		t.Fatalf("a scalar depth must parse: %v", err)
	}
	if q.Traverse[0].Depth.Min != 1 || q.Traverse[0].Depth.Max != 3 {
		t.Fatalf("depth 3 must mean {min:1,max:3}, got %+v", q.Traverse[0].Depth)
	}
	q, err = ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"q"}],` +
		`"traverse":[{"from":"q","via":"requires","depth":{"min":2,"max":4},"as":"r"}]}`))
	if err != nil {
		t.Fatalf("an object depth must parse: %v", err)
	}
	if q.Traverse[0].Depth.Min != 2 || q.Traverse[0].Depth.Max != 4 {
		t.Fatalf("got %+v", q.Traverse[0].Depth)
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"traverse":[{"from":"q","via":"requires","depth":{"min":5,"max":2},"as":"r"}]}`,
		"/traverse/0/depth", "min 5 is above max 2, so no depth is legal")
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"traverse":[{"from":"q","via":"requires","depth":0,"as":"r"}]}`,
		"/traverse/0/depth", "must be at least 1")
}

// TestViaTakesBothSpellings pins the scalar-or-list decoding, which is
// the other place a document has two legal shapes for one meaning.
func TestViaTakesBothSpellings(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"q"}],` +
		`"traverse":[{"from":"q","via":"requires","as":"r"}]}`))
	if err != nil {
		t.Fatalf("a scalar via must parse: %v", err)
	}
	if len(q.Traverse[0].Via) != 1 || q.Traverse[0].Via[0] != "requires" {
		t.Fatalf(`via "requires" must decode to one element, got %v`, q.Traverse[0].Via)
	}
	q, err = ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"q"}],` +
		`"traverse":[{"from":"q","via":["requires","unlocks"],"as":"r"}]}`))
	if err != nil {
		t.Fatalf("a list via must parse: %v", err)
	}
	if len(q.Traverse[0].Via) != 2 {
		t.Fatalf("got %v", q.Traverse[0].Via)
	}
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"traverse":[{"from":"q","via":7,"as":"r"}]}`,
		"", "must be a string or a list of strings")
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"traverse":[{"from":"q","as":"r"}]}`,
		"/traverse/0/via", "is required")
}

// TestTheStepCapIsRefusedAtParseTime is the CTE bound from the spec's
// §4.3 table: eight steps per query, refused rather than truncated,
// because an agent that asked for nine should learn it cannot have them.
func TestTheStepCapIsRefusedAtParseTime(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[{"type":"quest","as":"s0"}],"traverse":[`)
	for i := 0; i < MaxSteps+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"from":"s0","via":"requires","as":"s` + string(rune('a'+i)) + `"}`)
	}
	b.WriteString(`]}`)
	parseFails(t, b.String(), "/traverse", "at most 8 traversal steps")
}

func TestAnOversizeDocumentIsRefusedBeforeItIsParsed(t *testing.T) {
	doc := `{"v":1,"from":[{"type":"quest","as":"` + strings.Repeat("a", MaxQueryBytes) + `"}]}`
	parseFails(t, doc, "", "is too large")
}

// TestEveryProblemWithOneQueryIsReportedInOnePass is the promise
// ParseQuery's doc comment makes: an agent fixes a five-step traversal in
// one round trip rather than five. The document below is wrong in four
// independent places, and all four must come back together.
func TestEveryProblemWithOneQueryIsReportedInOnePass(t *testing.T) {
	doc := `{"v":9,"from":[{"type":"has space","as":"q"}],` +
		`"traverse":[{"from":"missing","via":"requires","direction":"sideways","as":"r"}]}`
	_, err := ParseQuery([]byte(doc))
	if err == nil {
		t.Fatal("this document must be refused")
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	want := []string{"/v", "/from/0/type", "/traverse/0/from", "/traverse/0/direction"}
	got := map[string]bool{}
	for _, f := range qe.Fields {
		got[f.Path] = true
	}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("the problem at %s was not reported in the same pass; got %v", ptr, qe.Fields)
		}
	}
	// The problems are ordered by pointer so two runs over the same
	// document read the same way in a diff.
	for i := 1; i < len(qe.Fields); i++ {
		if qe.Fields[i-1].Path > qe.Fields[i].Path {
			t.Fatalf("problems must be ordered by pointer, got %v", qe.Fields)
		}
	}
}

// TestATrailingDocumentIsRefused pins that a body holding two JSON
// documents is refused rather than silently reduced to the first: the
// second one would otherwise be a query nobody ran and nobody was told
// about.
func TestATrailingDocumentIsRefused(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest"}]}{"v":1,"from":[{"type":"zone"}]}`,
		"", "carries more than one JSON document")
}

// TestAnEdgeEntryIsOneShapeOrTheOther pins the exclusivity the EdgeSpec
// doc comment claims: an entry draws the relations a step walked, or
// relations of a type between two sets already in the result, never both
// and never neither.
func TestAnEdgeEntryIsOneShapeOrTheOther(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"edges":[{"from_step":"q","via":"requires","between":["q","q"]}]}`,
		"/edges/0", `names both "from_step" and "via"/"between"`)
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],"edges":[{}]}`,
		"/edges/0", `must name either "from_step" or "via" with "between"`)
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"edges":[{"via":"requires","between":["q"]}]}`,
		"/edges/0/between", "must name exactly two sets")
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],`+
		`"edges":[{"via":"requires","between":["q","nope"]}]}`,
		"/edges/0/between/1", `no set named "nope" is declared`)
}

// TestANodeEntryNamesADeclaredSet is the same rule for `nodes`.
func TestANodeEntryNamesADeclaredSet(t *testing.T) {
	parseFails(t, `{"v":1,"from":[{"type":"quest","as":"q"}],"nodes":[{"set":"nope"}]}`,
		"/nodes/0/set", `no set named "nope" is declared`)
}

// TestNodesDefaultToEverySetInDeclarationOrder pins applyDefaults: a
// document that draws nothing explicitly draws everything it declared,
// which is what makes the simplest useful query useful.
func TestNodesDefaultToEverySetInDeclarationOrder(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone"}],` +
		`"traverse":[{"from":"q","via":"requires","as":"r"}]}`))
	if err != nil {
		t.Fatalf("must parse: %v", err)
	}
	want := []string{"q", "zone", "r"}
	if len(q.Nodes) != len(want) {
		t.Fatalf("nodes must default to every declared set, got %+v", q.Nodes)
	}
	for i, name := range want {
		if q.Nodes[i].Set != name {
			t.Fatalf("nodes[%d] = %q, want %q", i, q.Nodes[i].Set, name)
		}
	}
	// A step with no direction walks outgoing edges, and a step with no
	// depth is one hop: both are what an agent that omitted them meant.
	if q.Traverse[0].Direction != DirectionOut {
		t.Fatalf("direction must default to %q, got %q", DirectionOut, q.Traverse[0].Direction)
	}
	if q.Traverse[0].Depth.Min != 1 || q.Traverse[0].Depth.Max != 1 {
		t.Fatalf("depth must default to one hop, got %+v", q.Traverse[0].Depth)
	}
}

func TestAParameterDeclaresOneOfTheThreeScalarTypes(t *testing.T) {
	parseFails(t, `{"v":1,"params":[{"key":"lvl","type":"quest_ref"}],`+
		`"from":[{"type":"quest"}]}`,
		"/params/0/type", `must be "text", "number" or "bool"`)
	parseFails(t, `{"v":1,"params":[{"key":"has space","type":"text"}],`+
		`"from":[{"type":"quest"}]}`,
		"/params/0/key", "must be letters, digits, underscores or hyphens")
	if _, err := ParseQuery([]byte(`{"v":1,"params":[{"key":"lvl","type":"number","default":20}],` +
		`"from":[{"type":"quest"}]}`)); err != nil {
		t.Fatalf("a well-formed parameter must parse: %v", err)
	}
}

// TestTheSelectorAndKeyCapsAreRefusedAtParseTime pins the two collection
// bounds a document carries that are not a string bound: the number of
// seed selectors and the number of keys in one selector's shortcut.
func TestTheSelectorAndKeyCapsAreRefusedAtParseTime(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[`)
	for i := 0; i <= MaxSelectors; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"quest","as":"s` + string(rune('a'+i)) + `"}`)
	}
	b.WriteString(`]}`)
	parseFails(t, b.String(), "/from", "at most 16")

	var k strings.Builder
	k.WriteString(`{"v":1,"from":[{"type":"quest","keys":[`)
	for i := 0; i <= MaxKeys; i++ {
		if i > 0 {
			k.WriteString(",")
		}
		k.WriteString(`"q` + strings.Repeat("x", i%3) + `"`)
	}
	k.WriteString(`]}]}`)
	parseFails(t, k.String(), "/from/0/keys", "at most 500")
}

// TestAParamCapIsRefusedAtParseTime is the third collection bound.
func TestAParamCapIsRefusedAtParseTime(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"params":[`)
	for i := 0; i <= MaxParams; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"key":"p` + string(rune('a'+i)) + `","type":"text"}`)
	}
	b.WriteString(`],"from":[{"type":"quest"}]}`)
	parseFails(t, b.String(), "/params", "at most 16")
}

// TestEveryPredicateInAParsedQueryIsNormalised pins the other half of
// applyDefaults, and it is deliberately spelled over *all three* predicate
// positions the document has. A selector's `where` was normalised while a
// step's `where` and `edge_where` were not, which is an asymmetry that
// costs Task 4 a nil FieldRef in exactly the branch a two-branch query
// needs; nothing pinned either the calls or what they fill, so the fix
// could be deleted and the package would stay green.
//
// It also pins the nesting: normalise recurses through `all`, `any` and
// `not`, so a leaf three levels down carries its reference too.
func TestEveryPredicateInAParsedQueryIsNormalised(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,
	  "from":[{"type":"quest","as":"q",
	           "where":{"all":[{"field":"rank","op":"eq","value":1},
	                           {"not":{"any":[{"field":"@name","op":"eq","value":"x"}]}}]}}],
	  "traverse":[{"from":"q","via":"requires","as":"r",
	               "where":{"field":"tier","op":"eq","value":2},
	               "edge_where":{"field":"weight","op":"eq","value":3}}]}`))
	if err != nil {
		t.Fatalf("must parse: %v", err)
	}

	// Every leaf reachable from every predicate position, and the
	// reference each one must carry.
	leaves := map[string]*Predicate{
		"/from/0/where/all/0":           &q.From[0].Where.All[0],
		"/from/0/where/all/1/not/any/0": &q.From[0].Where.All[1].Not.Any[0],
		"/traverse/0/where":             q.Traverse[0].Where,
		"/traverse/0/edge_where":        q.Traverse[0].EdgeWhere,
	}
	for at, leaf := range leaves {
		if leaf.FieldRef.Key != leaf.Field {
			t.Errorf("the predicate at %s was not normalised: field %q, FieldRef %+v — "+
				"Task 4 reads FieldRef and never Field", at, leaf.Field, leaf.FieldRef)
		}
		if want := strings.HasPrefix(leaf.Field, "@"); leaf.FieldRef.Builtin != want {
			t.Errorf("the predicate at %s must record that %q is a built-in: %v, want %v",
				at, leaf.Field, leaf.FieldRef.Builtin, want)
		}
	}
	// The built-in leaf is the one that makes Builtin mean something: with
	// it missing, every reference above could be a declared field key and
	// the assertion would hold with the flag hard-wired to false.
	if !q.From[0].Where.All[1].Not.Any[0].FieldRef.Builtin {
		t.Fatal("@name must be recorded as a built-in")
	}
}

// TestALabelDefaultsToTheEntityName pins the last default applyDefaults
// applies, which every renderer in this sub-project reads: a node with no
// projection is drawn with its name, not with nothing. Every sibling
// default in this file is pinned; this one was not.
func TestALabelDefaultsToTheEntityName(t *testing.T) {
	q, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}]}`))
	if err != nil {
		t.Fatalf("must parse: %v", err)
	}
	if q.Project == nil {
		t.Fatal("project must default to an empty projection rather than staying nil")
	}
	if q.Project.Label == nil || q.Project.Label.Attr != AttrName {
		t.Fatalf("label must default to %q, got %+v", AttrName, q.Project.Label)
	}
	// A document that said what it wanted keeps it: the default is a
	// default and not an overwrite.
	q, err = ParseQuery([]byte(`{"v":1,"from":[{"type":"quest"}],"project":{"label":"title"}}`))
	if err != nil {
		t.Fatalf("must parse: %v", err)
	}
	if q.Project.Label.Attr != "title" {
		t.Fatalf("an explicit label must survive the default, got %+v", q.Project.Label)
	}
}

// TestASetOfProblemsIsOrderedByIndexNotByPointerString pins the order
// this pass reports in. Unlike resolution, checkQuery cannot simply walk
// and not sort — its whole-document text bounds run before the per-member
// checks — so it sorts, and sorting pointers as plain strings puts
// /from/10 before /from/2.
func TestASetOfProblemsIsOrderedByIndexNotByPointerString(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"v":1,"from":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		switch i {
		case 2, 10:
			// An empty type is refused by the key grammar, at /from/N/type.
			b.WriteString(`{"type":"","as":"s` + string(rune('a'+i)) + `"}`)
		default:
			b.WriteString(`{"type":"quest","as":"s` + string(rune('a'+i)) + `"}`)
		}
	}
	b.WriteString(`]}`)
	_, err := ParseQuery([]byte(b.String()))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("want a *QueryError, got %v", err)
	}
	if len(qe.Fields) != 2 {
		t.Fatalf("want two problems, got %v", qe.Fields)
	}
	if qe.Fields[0].Path != "/from/2/type" || qe.Fields[1].Path != "/from/10/type" {
		t.Fatalf("want /from/2/type before /from/10/type, got %v", qe.Fields)
	}
}

// TestAParameterKeyIsDeclaredOnce pins the rule a duplicate set name
// already has, one member along: two declarations of the same parameter
// key are last-wins in every pass that reads them — the type a predicate
// is checked against, the default that is bound — so the document means
// one thing and reads as another.
func TestAParameterKeyIsDeclaredOnce(t *testing.T) {
	parseFails(t, `{"v":1,"params":[{"key":"floor","type":"number","default":1},
		{"key":"floor","type":"text","default":"x"}],"from":[{"type":"quest"}]}`,
		"/params/1/key", `the parameter "floor" is already declared, at /params/0/key`)
	// Positive control: two different keys are ordinary.
	if _, err := ParseQuery([]byte(`{"v":1,"params":[{"key":"floor","type":"number"},
		{"key":"ceiling","type":"number"}],"from":[{"type":"quest"}]}`)); err != nil {
		t.Fatalf("two distinct parameters must parse: %v", err)
	}
}
