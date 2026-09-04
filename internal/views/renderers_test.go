package views

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// resolveFor resolves a document the test asserts is well-formed, so that
// a test meaning to exercise the renderer catalogue is not silently
// exercising ParseQuery or resolution instead.
func resolveFor(t *testing.T, g *game, doc string) *Resolved {
	t.Helper()
	r, err := g.views.Resolve(context.Background(), g.projectID, mustParse(t, doc))
	if err != nil {
		t.Fatalf("this query must resolve; the test means to exercise the renderer: %v", err)
	}
	return r
}

// checkFails asserts a refusal of the given code, addressed at the given
// pointer, whose message contains want.
func checkFails(t *testing.T, err error, sentinel error, ptr, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal at %s saying %q, got none", ptr, want)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected %v, got %v", sentinel, err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %T: %v", err, err)
	}
	for _, f := range qe.Fields {
		if f.Path == ptr && strings.Contains(f.Message, want) {
			return
		}
	}
	t.Fatalf("expected a problem at %s containing %q, got %v", ptr, want, qe.Fields)
}

// checkPasses is the positive control every refusal test in this file
// carries: a refusal that always fires passes a test that only asserts
// refusals.
func checkPasses(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("this combination must be accepted: %v", err)
	}
}

// The two documents most tests here are built on: a plain quest query,
// and one that draws `requires` edges between quests.
const (
	questQuery = `{"v":1,"from":[{"type":"quest","as":"q"}]}`
	chainQuery = `{"v":1,"from":[{"type":"quest","as":"q"}],
		"edges":[{"via":"requires","between":["q","q"]}]}`
)

func TestTheCatalogueIsClosedAndNamesItselfBack(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, questQuery)
	// The positive control: every name in the catalogue is accepted, so
	// a CheckRenderer that refused everything could not pass this test.
	for _, name := range RendererNames() {
		params := map[string]any{}
		switch name {
		case RendererNested:
			// nested and timeline have required parameters of their own,
			// which the next test is about; here they are given legal
			// ones so this test is about the *name*.
			params["contain_via"] = "requires"
		case RendererTimeline:
			params["axis_field"] = "min_level"
		}
		query := questQuery
		if name == RendererLayered || name == RendererNested {
			query = chainQuery
		}
		if err := CheckRenderer(name, params, resolveFor(t, g, query)); err != nil {
			t.Errorf("%q is in the catalogue and must be accepted: %v", name, err)
		}
	}
	err := CheckRenderer("sankey", nil, r)
	checkFails(t, err, ErrQueryInvalid, "/renderer",
		`"sankey" is not a renderer this catalogue has`)
	for _, name := range RendererNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal must list %q: an agent that misspelled a renderer is "+
				"told the catalogue, got %v", name, err)
		}
	}
}

// TestARendererParameterItDoesNotDeclareIsRefused is the reason
// renderer_params is not a free jsonb blob in practice: a typo'd
// rank_dircetion that is stored, returned and silently ignored forever is
// the most expensive shape of bug this sub-project has, and the refusal
// has to name what *was* accepted or an agent cannot find its typo.
func TestARendererParameterItDoesNotDeclareIsRefused(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, chainQuery)
	// The control: the spelling it meant is taken.
	checkPasses(t, CheckRenderer(RendererLayered,
		map[string]any{"rank_direction": "LR"}, r))

	err := CheckRenderer(RendererLayered, map[string]any{"rank_dircetion": "LR"}, r)
	checkFails(t, err, ErrQueryInvalid, "/renderer_params/rank_dircetion",
		`is not a parameter "layered" takes`)
	for _, p := range rendererByName[RendererLayered].Params {
		if !strings.Contains(err.Error(), p.Name) {
			t.Errorf("the refusal must name %q, which layered does take: %v", p.Name, err)
		}
	}
	// A parameter of *another* renderer is not a parameter of this one:
	// the catalogue is closed per renderer, not per union.
	checkFails(t, CheckRenderer(RendererLayered, map[string]any{"axis_field": "min_level"}, r),
		ErrQueryInvalid, "/renderer_params/axis_field", `is not a parameter "layered" takes`)
}

// TestNestedRefusesAQueryWithNoContainmentEdges carries its positive
// control in the same test: a refusal that always fired would pass
// without one, and the control is a query that *does* draw the relation
// contain_via names.
func TestNestedRefusesAQueryWithNoContainmentEdges(t *testing.T) {
	g, _ := newGame(t)
	params := map[string]any{"contain_via": "requires"}

	// The control, via spelling: the query draws requires edges.
	checkPasses(t, CheckRenderer(RendererNested, params, resolveFor(t, g, chainQuery)))
	// The control, from_step spelling: the same edges, drawn by naming
	// the step that walked them.
	checkPasses(t, CheckRenderer(RendererNested, params, resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		 "traverse":[{"from":"q","via":"requires","to_type":"quest","as":"pre"}],
		 "edges":[{"from_step":"pre"}]}`)))

	// No edges at all.
	checkFails(t, CheckRenderer(RendererNested, params, resolveFor(t, g, questQuery)),
		ErrRendererRequirements, "/renderer_params/contain_via",
		`names "requires" and this query draws no edges of it`)
	// Edges, but of another relation type: the hole one step along from
	// "the query draws no edges".
	checkFails(t, CheckRenderer(RendererNested, params, resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone","as":"z"}],
		  "edges":[{"via":"takes_place_in","between":["q","z"]}]}`)),
		ErrRendererRequirements, "/renderer_params/contain_via",
		`names "requires" and this query draws no edges of it`)
	// A relation type this game does not declare is a different failure
	// with a different recovery, and says so.
	checkFails(t, CheckRenderer(RendererNested, map[string]any{"contain_via": "contains"},
		resolveFor(t, g, chainQuery)),
		ErrRendererRequirements, "/renderer_params/contain_via",
		`no relation type "contains" in this game`)
}

func TestTimelineRefusesAnAxisFieldThatIsNotNumberOrOrderedEnum(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, questQuery)

	// Both controls: a number axis and an enum axis are what this
	// renderer is for. An enum's options are its order, and the metamodel
	// refuses an enum with none, so "ordered enum" is every enum.
	checkPasses(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "min_level"}, quests))
	checkPasses(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "rank"}, quests))

	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "tags"}, quests),
		ErrRendererRequirements, "/renderer_params/axis_field",
		"is declared list<text> on quest and this parameter needs number and enum")
	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "no_such_field"}, quests),
		ErrRendererRequirements, "/renderer_params/axis_field",
		`no field "no_such_field" is declared on the entity types this query draws`)
	// A spelling that is not a field key at all is the *other* code: the
	// recovery is to fix the argument, not to change the query.
	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "Min Level"}, quests),
		ErrQueryInvalid, "/renderer_params/axis_field", "must be lower_snake_case")
}

// TestAnAxisDeclaredTwoWaysIsTwoAxes is the failure one step along from
// the type check: number and enum are each admitted, so a scope holding
// one of each passes "is it number or enum" while having no single axis
// to draw. The fixture declares min_level as a number on quest and as an
// enum on region, and rank as the same three options in two different
// orders — which is a set a predicate compares happily and a sequence an
// axis is drawn from.
func TestAnAxisDeclaredTwoWaysIsTwoAxes(t *testing.T) {
	g, _ := newGame(t)
	both := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"},{"type":"region","as":"g"}]}`)
	// The control: rank is declared on both, and difficulty on neither
	// twice — a scope of two types is not itself a refusal.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level"}, resolveFor(t, g, questQuery)))

	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "min_level"}, both),
		ErrRendererRequirements, "/renderer_params/axis_field",
		"is declared number on quest and enum on region")
	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "rank"}, both),
		ErrRendererRequirements, "/renderer_params/axis_field",
		"an enum axis is ordered by its declared options")
}

// TestATimelineSpanHasBothEndsOnOneAxis pins the Requires closure that
// compares the two ends of a span: a number start with an enum end is two
// axes, and one of them would be placed arbitrarily.
func TestATimelineSpanHasBothEndsOnOneAxis(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, questQuery)
	// The control: two number fields are a span.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "difficulty"}, quests))

	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "rank"}, quests),
		ErrRendererRequirements, "/renderer_params/axis_end_field",
		`is declared enum and axis_field "min_level" is declared number`)
}

func TestMapInFieldsModeRefusesNonNumericCoordinates(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, questQuery)
	fields := func(x, y string) map[string]any {
		return map[string]any{"coordinate_source": "fields", "x_field": x, "y_field": y}
	}
	// The control: two number fields are coordinates.
	checkPasses(t, CheckRenderer(RendererMap, fields("min_level", "difficulty"), quests))

	checkFails(t, CheckRenderer(RendererMap, fields("min_level", "rank"), quests),
		ErrRendererRequirements, "/renderer_params/y_field",
		"is declared enum on quest and this parameter needs number")
	checkFails(t, CheckRenderer(RendererMap, fields("tags", "difficulty"), quests),
		ErrRendererRequirements, "/renderer_params/x_field",
		"is declared list<text> on quest and this parameter needs number")
	// A coordinate field nothing declares is the silent-empty failure
	// this language refuses everywhere else: every node at the origin.
	checkFails(t, CheckRenderer(RendererMap, fields("x", "y"), quests),
		ErrRendererRequirements, "/renderer_params/x_field", `no field "x" is declared`)
}

// TestMapAsksForTheCoordinatesItsModeReads pins the two halves of the
// mode switch: a coordinate field is required in fields mode and read by
// nothing in manual mode, and a parameter read by nothing is this
// sub-project's most-repeated defect.
func TestMapAsksForTheCoordinatesItsModeReads(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, questQuery)
	// Both controls: manual mode with no coordinate fields, and the
	// default mode, which is manual.
	checkPasses(t, CheckRenderer(RendererMap, map[string]any{"coordinate_source": "manual"}, quests))
	checkPasses(t, CheckRenderer(RendererMap, map[string]any{"snap": 10}, quests))

	checkFails(t, CheckRenderer(RendererMap, map[string]any{"coordinate_source": "fields"}, quests),
		ErrRendererRequirements, "/renderer_params/x_field",
		`is required when coordinate_source is "fields"`)
	checkFails(t, CheckRenderer(RendererMap, map[string]any{"coordinate_source": "fields"}, quests),
		ErrRendererRequirements, "/renderer_params/y_field",
		`is required when coordinate_source is "fields"`)
	checkFails(t, CheckRenderer(RendererMap,
		map[string]any{"coordinate_source": "manual", "x_field": "min_level"}, quests),
		ErrRendererRequirements, "/renderer_params/x_field",
		`is read by nothing when coordinate_source is "manual"`)
	// The default is manual, so an unspoken mode ignores it just as
	// loudly as a spoken one — the hole one step along from checking the
	// written value only.
	checkFails(t, CheckRenderer(RendererMap, map[string]any{"x_field": "min_level"}, quests),
		ErrRendererRequirements, "/renderer_params/x_field",
		`is read by nothing when coordinate_source is "manual"`)
}

// TestLayeredNeedsTheEdgesItRanksBy is the requirement addressed at the
// renderer rather than at a parameter: no one argument is to blame.
func TestLayeredNeedsTheEdgesItRanksBy(t *testing.T) {
	g, _ := newGame(t)
	// The control: a query that draws edges.
	checkPasses(t, CheckRenderer(RendererLayered, nil, resolveFor(t, g, chainQuery)))
	// And the control for the other renderer's silence: `graph` draws a
	// scatter of unconnected nodes happily, so this requirement is
	// layered's and not every renderer's.
	checkPasses(t, CheckRenderer(RendererGraph, nil, resolveFor(t, g, questQuery)))

	checkFails(t, CheckRenderer(RendererLayered, nil, resolveFor(t, g, questQuery)),
		ErrRendererRequirements, "/renderer",
		"ranks nodes by the edges between them and this query draws no edges")
}

// TestGraphRefusesEdgeLabelsNoEdgeCarries pins the one boolean in this
// catalogue that asks the query for something: edge_labels over a query
// that labels nothing draws nothing and says nothing.
func TestGraphRefusesEdgeLabelsNoEdgeCarries(t *testing.T) {
	g, _ := newGame(t)
	labelled := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "edges":[{"via":"requires","between":["q","q"],"label_from":"@type"}]}`)
	// Two controls: the labels exist, and asking for none over the same
	// unlabelled query is fine.
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"edge_labels": true}, labelled))
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"edge_labels": false},
		resolveFor(t, g, chainQuery)))

	checkFails(t, CheckRenderer(RendererGraph, map[string]any{"edge_labels": true},
		resolveFor(t, g, chainQuery)),
		ErrRendererRequirements, "/renderer_params/edge_labels",
		"no edges[] entry declares label_from")
}

// TestASlotParameterNamesASlotTheQueryDeclares pins the split this
// catalogue makes for a channel: a renderer names a *slot*, because the
// query already decides what a slot reads — including the one hop to a
// neighbour, which no renderer can take for itself.
func TestASlotParameterNamesASlotTheQueryDeclares(t *testing.T) {
	g, _ := newGame(t)
	coloured := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],"project":{"color_by":"rank"}}`)
	// The control: the query declares color_by, so the renderer may read
	// it — and label is always declared, because applyDefaults writes it.
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"color_by": "color_by"}, coloured))
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"size_by": "label"}, coloured))

	checkFails(t, CheckRenderer(RendererGraph, map[string]any{"color_by": "group_by"}, coloured),
		ErrRendererRequirements, "/renderer_params/color_by",
		`names the "group_by" slot and this query's project does not declare one`)
	// A spelling that is no slot at all is the other code, and names the
	// vocabulary.
	err := CheckRenderer(RendererGraph, map[string]any{"color_by": "rank"}, coloured)
	checkFails(t, err, ErrQueryInvalid, "/renderer_params/color_by",
		`"rank" is not a projection slot`)
	for _, slot := range slotNames() {
		if !strings.Contains(err.Error(), slot) {
			t.Errorf("the refusal must list the %q slot: %v", slot, err)
		}
	}
}

// TestATableColumnIsSomethingTheEnvelopeCarries is the contract in its
// purest form: a renderer consumes the envelope, so a column naming a
// field the run will not carry is a blank column in a saved view.
func TestATableColumnIsSomethingTheEnvelopeCarries(t *testing.T) {
	g, _ := newGame(t)
	carrying := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "project":{"color_by":"rank","fields":["min_level"]}}`)
	// The control: a built-in the node carries, a slot the projection
	// declares, and a key project.fields asked for.
	checkPasses(t, CheckRenderer(RendererTable,
		map[string]any{"columns": []any{"@name", "color_by", "min_level"}}, carrying))

	// A built-in that is legal in a predicate and absent from the
	// envelope.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"@invalid"}},
		carrying),
		ErrQueryInvalid, "/renderer_params/columns",
		`"@invalid" is not a built-in a table can draw`)
	// A declared field the query does not carry: legal to ask for, and
	// the recovery is in the query.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"difficulty"}},
		carrying),
		ErrRendererRequirements, "/renderer_params/columns",
		`names the field "difficulty" and this query does not carry it`)
	// A run's include_fields cannot answer for a saved view, and the
	// refusal says so rather than leaving an agent to try it.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"difficulty"}},
		carrying),
		ErrRendererRequirements, "/renderer_params/columns",
		"include_fields cannot answer for a saved view")
	// The spec's "columns may be attribute references, including one-hop
	// related" is not a shape a renderer can consume: the hop is the
	// query's to take. The refusal says where it belongs.
	checkFails(t, CheckRenderer(RendererTable,
		map[string]any{"columns": []any{map[string]any{"related": map[string]any{}}}}, carrying),
		ErrQueryInvalid, "/renderer_params/columns",
		"drawn by projecting it into a slot and naming the slot")
	// sort is one column reference and obeys the same rule, which is the
	// "same hole one step along" this file's second parameter would
	// otherwise be.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"sort": "difficulty"}, carrying),
		ErrRendererRequirements, "/renderer_params/sort",
		`names the field "difficulty" and this query does not carry it`)
}

// TestAShapeProblemIsAnsweredBeforeARequirement pins the ordering of the
// two codes. They cannot travel in one error — a QueryError carries one
// code — and answering the requirement first would tell an agent its
// query is wrong when what is wrong is its typing.
func TestAShapeProblemIsAnsweredBeforeARequirement(t *testing.T) {
	g, _ := newGame(t)
	err := CheckRenderer(RendererTimeline, map[string]any{
		"axis_field": "no_such_field", // a requirement
		"lane_by":    17,              // a shape
	}, resolveFor(t, g, questQuery))
	checkFails(t, err, ErrQueryInvalid, "/renderer_params/lane_by",
		"must name a projection slot, got a number")
	if strings.Contains(err.Error(), "no_such_field") {
		t.Fatalf("the shape problem is answered on its own: %v", err)
	}
	// And with the shape fixed, the requirement is what comes back.
	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{
		"axis_field": "no_such_field", "lane_by": "label",
	}, resolveFor(t, g, questQuery)),
		ErrRendererRequirements, "/renderer_params/axis_field", `no field "no_such_field"`)
}

// TestEveryProblemOfOneClassComesBackAtOnce is this package's standing
// rule applied here: an agent fixing three parameters should learn about
// three, not discover them one call apart.
func TestEveryProblemOfOneClassComesBackAtOnce(t *testing.T) {
	g, _ := newGame(t)
	err := CheckRenderer(RendererMap, map[string]any{
		"snap": "close", "background_scale": "big", "background_offset": "0,0",
	}, resolveFor(t, g, questQuery))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %v", err)
	}
	if len(qe.Fields) != 3 {
		t.Fatalf("expected all three problems in one pass, got %v", qe.Fields)
	}
	// And in document order rather than the map's, which has none:
	// pointerLess is what makes a refusal reproducible.
	want := []string{
		"/renderer_params/background_offset",
		"/renderer_params/background_scale",
		"/renderer_params/snap",
	}
	for i, ptr := range want {
		if qe.Fields[i].Path != ptr {
			t.Fatalf("problem %d must be %s, got %s (%v)", i, ptr, qe.Fields[i].Path, qe.Fields)
		}
	}
}

// TestARendererIsJudgedAgainstAResolvedQuery refuses the nil that would
// otherwise make every requirement in this file silently pass. A check
// that cannot see the query is not a check.
func TestARendererIsJudgedAgainstAResolvedQuery(t *testing.T) {
	err := CheckRenderer(RendererGraph, map[string]any{"color_by": "label"}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a resolved query") {
		t.Fatalf("a nil resolved query must be refused, got %v", err)
	}
	// The unknown-renderer refusal is deliberately answerable without
	// one: it reads nothing but the name.
	checkFails(t, CheckRenderer("sankey", nil, nil), ErrQueryInvalid, "/renderer",
		"is not a renderer this catalogue has")
}

// wrongValueFor is one value guaranteed to be refused for each kind, so
// that TestEveryDeclaredParameterIsCheckedNotJustStored can drive every
// declared parameter through the checker its kind names.
var wrongValueFor = map[ParamKind]any{
	kindBool:         "yes",
	kindNumber:       "big",
	kindCount:        "many",
	kindText:         17,
	kindEnum:         "nope",
	kindUUID:         "not-a-uuid",
	kindPoint:        "0,0",
	kindSlot:         "nonesuch",
	kindNumberField:  "no_such_field",
	kindAxisField:    "no_such_field",
	kindRankBy:       7,
	kindRelationType: "no_such_relation",
	kindColumn:       "@invalid",
	kindColumns:      "not a list",
}

// TestEveryDeclaredParameterIsCheckedNotJustStored is the assertion
// behind this file's first rule. renderer_params is a free jsonb blob on
// the wire; a parameter declared here and judged by nothing would be
// stored, returned and ignored forever, which is the failure this whole
// catalogue exists to prevent. Every parameter of every renderer is
// driven through a value its kind cannot accept, and the refusal has to
// name that parameter.
func TestEveryDeclaredParameterIsCheckedNotJustStored(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, chainQuery)
	for kind := range paramCheckers {
		if _, ok := wrongValueFor[kind]; !ok {
			t.Errorf("kind %q has no wrong value in this test's table, so every "+
				"parameter declaring it is exercised by nothing", kind)
		}
	}
	for _, renderer := range renderers {
		for _, p := range renderer.Params {
			wrong, ok := wrongValueFor[p.Kind]
			if !ok {
				continue // already reported above
			}
			ptr := pointer("renderer_params", p.Name)
			err := CheckRenderer(renderer.Name, map[string]any{p.Name: wrong}, r)
			var qe *QueryError
			if !errors.As(err, &qe) {
				t.Errorf("%s.%s (%s) took %v and was refused by nothing: a parameter "+
					"nothing reads is a knob that lies", renderer.Name, p.Name, p.Kind,
					wrong)
				continue
			}
			found := false
			for _, f := range qe.Fields {
				found = found || f.Path == ptr
			}
			if !found {
				t.Errorf("%s.%s (%s) took %v and the refusal named %v instead",
					renderer.Name, p.Name, p.Kind, wrong, qe.Fields)
			}
		}
	}
}

// TestAParameterKindCannotBeHalfAdded is the reason the kinds are a table
// rather than a switch, stated as an assertion, and it is bidirectional
// for the reason the operator table's own guard is: a kind used by a
// parameter and missing from the checkers is a parameter production
// refuses to judge, and a checker no parameter uses is a kind somebody
// meant to declare.
func TestAParameterKindCannotBeHalfAdded(t *testing.T) {
	used := map[ParamKind]bool{}
	for _, renderer := range renderers {
		for _, p := range renderer.Params {
			used[p.Kind] = true
			if _, ok := paramCheckers[p.Kind]; !ok {
				t.Errorf("%s.%s declares kind %q and no checker judges it: the value "+
					"would be stored and never read", renderer.Name, p.Name, p.Kind)
			}
		}
	}
	for kind := range paramCheckers {
		if !used[kind] {
			t.Errorf("kind %q is judged by a checker no parameter declares: it is "+
				"unreachable, and an unreachable arm is a kind somebody meant to use",
				kind)
		}
	}
}

// TestAnEnumParameterDeclaresItsValuesAndNothingElseDoes keeps Values and
// kindEnum in step in both directions: values on a parameter of another
// kind are read by nothing, and an enum with none admits nothing at all.
func TestAnEnumParameterDeclaresItsValuesAndNothingElseDoes(t *testing.T) {
	for _, renderer := range renderers {
		for _, p := range renderer.Params {
			switch {
			case p.Kind == kindEnum && len(p.Values) == 0:
				t.Errorf("%s.%s is an enum with no values: it would refuse every "+
					"value there is", renderer.Name, p.Name)
			case p.Kind != kindEnum && len(p.Values) > 0:
				t.Errorf("%s.%s is a %s and declares values, which nothing reads",
					renderer.Name, p.Name, p.Kind)
			}
		}
	}
}

// TestEveryRequiredParameterIsRefusedWhenMissing drives the Required flag
// itself, which is otherwise a boolean read by one line nobody exercises.
func TestEveryRequiredParameterIsRefusedWhenMissing(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, chainQuery)
	required := 0
	for _, renderer := range renderers {
		for _, p := range renderer.Params {
			if !p.Required {
				continue
			}
			required++
			checkFails(t, CheckRenderer(renderer.Name, map[string]any{}, r),
				ErrRendererRequirements, pointer("renderer_params", p.Name),
				"is required by this renderer")
		}
	}
	// The vacuity check: an arm that matches nothing asserts nothing, and
	// this test would pass over a catalogue whose Required flags had all
	// been deleted.
	if required < 2 {
		t.Fatalf("the catalogue declares %d required parameters and this test needs "+
			"the two it has (nested's contain_via, timeline's axis_field)", required)
	}
}

var (
	// The consumes clause is matched greedily: layered's own says
	// "nodes, edges (expected mostly acyclic)", so a lazy match ends at
	// the wrong parenthesis and reports the whole catalogue missing.
	descRenderer = regexp.MustCompile(`^- ([a-z_]+) \(consumes (.+)\): `)
	descParam    = regexp.MustCompile(`^  - ([a-z_]+): `)
)

// TestEveryRendererDeclaresItsParametersAndTheDescriptionIsGeneratedFromThem
// is the guard over the prose an agent reads. Task 15's tool description
// is built from this catalogue, so the two must not be able to disagree:
// a renderer added with no parameters ships an undocumented knob, a
// parameter left out of the description is one no agent will find, and a
// sentence naming a knob that does not exist is one every agent will
// send and this package will refuse.
//
// It is bidirectional, the way the operator table's guard is: the
// description is parsed back into renderers and parameters and compared
// with the table both ways.
func TestEveryRendererDeclaresItsParametersAndTheDescriptionIsGeneratedFromThem(t *testing.T) {
	for _, r := range renderers {
		switch {
		case len(r.Params) == 0:
			t.Errorf("%q declares no parameters: a renderer with no knobs is either "+
				"undocumented or unfinished", r.Name)
		case r.Consumes == "" || r.Doc == "":
			t.Errorf("%q must say what it consumes and what it is for", r.Name)
		}
		seen := map[string]bool{}
		for _, p := range r.Params {
			if p.Doc == "" {
				t.Errorf("%s.%s has no documentation: its meaning is written nowhere",
					r.Name, p.Name)
			}
			if seen[p.Name] {
				t.Errorf("%s declares %q twice", r.Name, p.Name)
			}
			seen[p.Name] = true
		}
	}

	// Read the generated description back into the same shape as the
	// table, then compare in both directions.
	printed := map[string][]string{}
	var order []string
	current := ""
	for _, line := range strings.Split(RendererDescription(), "\n") {
		if m := descRenderer.FindStringSubmatch(line); m != nil {
			current = m[1]
			order = append(order, current)
			printed[current] = nil
			if want := rendererByName[current]; want != nil && m[2] != want.Consumes {
				t.Errorf("%q is printed as consuming %q and the table says %q",
					current, m[2], want.Consumes)
			}
			continue
		}
		if m := descParam.FindStringSubmatch(line); m != nil {
			if current == "" {
				t.Fatalf("parameter %q is printed under no renderer", m[1])
			}
			printed[current] = append(printed[current], m[1])
		}
	}
	if fmt.Sprint(order) != fmt.Sprint(RendererNames()) {
		t.Errorf("the description prints %v and the catalogue is %v", order, RendererNames())
	}
	for _, r := range renderers {
		if fmt.Sprint(printed[r.Name]) != fmt.Sprint(r.paramNames()) {
			t.Errorf("%q declares %v and the description prints %v",
				r.Name, r.paramNames(), printed[r.Name])
		}
		for _, p := range r.Params {
			for _, value := range p.Values {
				if !strings.Contains(RendererDescription(), value) {
					t.Errorf("%s.%s admits %q and the description never prints it",
						r.Name, p.Name, value)
				}
			}
		}
	}
	for name, params := range printed {
		r, ok := rendererByName[name]
		if !ok {
			t.Errorf("the description names %q, which is not a renderer: an agent "+
				"would send it and be refused", name)
			continue
		}
		for _, param := range params {
			if _, ok := r.param(param); !ok {
				t.Errorf("the description gives %q a %q parameter, which it does not "+
					"take", name, param)
			}
		}
	}
	// The description is what Task 15 hands an agent, so the decision
	// open question O4 settled has to be in it rather than in this plan
	// alone: no cursor on views.run, and the escape hatch named so it is
	// not invented twice.
	for _, want := range []string{"views.run has no cursor", "views.run_table"} {
		if !strings.Contains(RendererDescription(), want) {
			t.Errorf("the table renderer's description must say %q", want)
		}
	}
}

// TestAnUntypedStepSwitchesOffTheFieldChecks records, as an assertion,
// the permission Task 9 wrote up: a traverse step with no to_type reaches
// entities of any type, so no schema applies and no field key can be
// judged — for a renderer parameter exactly as for a projection. The
// control is the same query with the step typed, where the same value is
// refused.
func TestAnUntypedStepSwitchesOffTheFieldChecks(t *testing.T) {
	g, _ := newGame(t)
	open := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","as":"z"}]}`)
	typed := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","to_type":"zone","as":"z"}]}`)

	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "no_such_field"}, open))
	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "no_such_field"}, typed),
		ErrRendererRequirements, "/renderer_params/axis_field", `no field "no_such_field"`)
}
