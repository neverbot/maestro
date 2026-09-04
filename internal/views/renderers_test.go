package views

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
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
	// carryingQuery is questQuery with the declared fields in
	// project.fields, and it is what every test of a field-key parameter
	// is built on: a saved view may only name a field its own query
	// carries, because include_fields is a per-run option no saved view
	// can turn on. A test built on questQuery would be asserting against
	// a view whose axis the run would not carry.
	carryingQuery = `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"fields":["min_level","difficulty","rank","tags"]}}`
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
		switch name {
		case RendererLayered, RendererNested:
			query = chainQuery
		case RendererTimeline:
			query = carryingQuery
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
	quests := resolveFor(t, g, carryingQuery)

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
		`{"v":1,"from":[{"type":"quest","as":"q"},{"type":"region","as":"g"}],
		  "project":{"fields":["min_level","rank"]}}`)
	// The control: rank is declared on both, and difficulty on neither
	// twice — a scope of two types is not itself a refusal.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level"}, resolveFor(t, g, carryingQuery)))

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
	quests := resolveFor(t, g, carryingQuery)
	// The control: two number fields are a span.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "difficulty"}, quests))

	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "rank"}, quests),
		ErrRendererRequirements, "/renderer_params/axis_end_field",
		`is declared enum and axis_field "min_level" is declared number`)

	// The same hole one step along: comparing the two ends' *types*
	// makes every pair of enums one axis, so a span runs from an option
	// of one enum to an option of an unrelated one and has no length.
	// region declares two enums with different options, which is the only
	// place in this fixture that shape exists.
	regions := resolveFor(t, g,
		`{"v":1,"from":[{"type":"region","as":"g"}],
		  "project":{"fields":["min_level","rank"]}}`)
	// The control: one enum is a span with itself's options — the same
	// key at both ends is degenerate, so the control is the number pair
	// above and this one asserts only the refusal it is about.
	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "rank"}, regions),
		ErrRendererRequirements, "/renderer_params/axis_end_field",
		"is an enum over [epic, common, rare] and axis_field \"min_level\" is an "+
			"enum over [low, high]")
}

// TestAFieldKeyParameterNamesAFieldTheSavedQueryCarries is this file's
// third rule — a requirement is judged against the *saved* query — applied
// to every parameter that names a declared field key, and not only to the
// one it was first written on.
//
// A node comes back with its identity and the projection's attrs;
// declared fields only when a run asks for include_fields, which is a
// per-run option no saved view can turn on. A timeline saved with an
// axis_field the query does not carry has no axis at all: every node at
// the origin. A map in fields mode has no coordinates. Both were accepted
// while `columns` alone enforced the rule.
func TestAFieldKeyParameterNamesAFieldTheSavedQueryCarries(t *testing.T) {
	g, _ := newGame(t)
	carrying, bare := resolveFor(t, g, carryingQuery), resolveFor(t, g, questQuery)
	chain := `{"v":1,"from":[{"type":"quest","as":"q"}],
		"edges":[{"via":"requires","between":["q","q"]}],
		"project":{"fields":["min_level"]}}`
	// The controls: the same parameters over a query that carries the
	// keys they name.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level", "axis_end_field": "difficulty"}, carrying))
	checkPasses(t, CheckRenderer(RendererMap, map[string]any{"coordinate_source": "fields",
		"x_field": "min_level", "y_field": "difficulty"}, carrying))
	checkPasses(t, CheckRenderer(RendererLayered,
		map[string]any{"rank_by": "min_level"}, resolveFor(t, g, chain)))
	// And "edges" is not a field key at all, so it needs nothing carried.
	checkPasses(t, CheckRenderer(RendererLayered,
		map[string]any{"rank_by": "edges"}, resolveFor(t, g, chainQuery)))

	for _, c := range []struct {
		renderer string
		params   map[string]any
		r        *Resolved
		ptr      string
	}{
		{RendererTimeline, map[string]any{"axis_field": "min_level"}, bare,
			"/renderer_params/axis_field"},
		{RendererTimeline,
			map[string]any{"axis_field": "min_level", "axis_end_field": "difficulty"},
			resolveFor(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
				"project":{"fields":["min_level"]}}`),
			"/renderer_params/axis_end_field"},
		{RendererMap, map[string]any{"coordinate_source": "fields",
			"x_field": "min_level", "y_field": "difficulty"}, bare,
			"/renderer_params/x_field"},
		{RendererMap, map[string]any{"coordinate_source": "fields",
			"x_field": "min_level", "y_field": "difficulty"}, bare,
			"/renderer_params/y_field"},
		{RendererLayered, map[string]any{"rank_by": "min_level"},
			resolveFor(t, g, chainQuery), "/renderer_params/rank_by"},
	} {
		checkFails(t, CheckRenderer(c.renderer, c.params, c.r),
			ErrRendererRequirements, c.ptr, "this query does not carry it")
		checkFails(t, CheckRenderer(c.renderer, c.params, c.r),
			ErrRendererRequirements, c.ptr, "include_fields cannot answer for a saved view")
	}
}

// TestAnAxisSomeTypeInScopeDoesNotDeclareIsRefused is requireDeclaredAs's
// own doc comment, which for one round said the code refused this and did
// not. Declaring the key as a different type on a second type is the rare
// mistake; drawing two types and remembering the fields of one of them is
// the common one, and it makes exactly the picture the comment describes —
// the quests placed, the zones vanished, and what is left looking right.
func TestAnAxisSomeTypeInScopeDoesNotDeclareIsRefused(t *testing.T) {
	g, _ := newGame(t)
	// The control: one type, which declares it.
	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "min_level"}, resolveFor(t, g, carryingQuery)))
	// And the control for the *other* rule this one must not swallow: a
	// projection slot over the same two types is legal, because a node
	// with no attribute is still drawable and an axis has nowhere to put
	// it. The two rules disagree deliberately.
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"color_by": "color_by"},
		resolveFor(t, g, `{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone","as":"z"}],
			"project":{"color_by":"min_level"}}`)))

	mixed := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"},{"type":"zone","as":"z"}],
		  "project":{"fields":["min_level","difficulty"]}}`)
	for _, c := range []struct {
		renderer string
		params   map[string]any
		ptr      string
	}{
		{RendererTimeline, map[string]any{"axis_field": "min_level"},
			"/renderer_params/axis_field"},
		{RendererMap, map[string]any{"coordinate_source": "fields",
			"x_field": "min_level", "y_field": "difficulty"}, "/renderer_params/x_field"},
		{RendererMap, map[string]any{"coordinate_source": "fields",
			"x_field": "min_level", "y_field": "difficulty"}, "/renderer_params/y_field"},
	} {
		checkFails(t, CheckRenderer(c.renderer, c.params, mixed),
			ErrRendererRequirements, c.ptr, "is declared on quest and not on zone")
	}
}

// TestEveryBadColumnComesBackWithItsIndex is three assertions the list
// shape needs and no scalar parameter does: every bad column is reported,
// each at its own index, and the *code* of the answer does not depend on
// the order the columns were written in. The last is the one that matters
// most — two wrong columns returning query_invalid or renderer_requirements
// depending on which was typed first is this package's shape-wins rule
// breaking at the one place a list could break it.
func TestEveryBadColumnComesBackWithItsIndex(t *testing.T) {
	g, _ := newGame(t)
	carrying := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "project":{"color_by":"rank","fields":["min_level"]}}`)
	// The control: three good columns in any order.
	checkPasses(t, CheckRenderer(RendererTable,
		map[string]any{"columns": []any{"@name", "color_by", "min_level"}}, carrying))

	// Two columns of one class: both come back, each at its own index.
	err := CheckRenderer(RendererTable,
		map[string]any{"columns": []any{"difficulty", "tags"}}, carrying)
	checkFails(t, err, ErrRendererRequirements, "/renderer_params/columns/0",
		`names the field "difficulty"`)
	checkFails(t, err, ErrRendererRequirements, "/renderer_params/columns/1",
		`names the field "tags"`)

	// The same two wrong columns in either order answer with the same
	// code and the same class of message: shape wins, and it wins from
	// whichever position the shape problem was written in.
	for _, columns := range [][]any{
		{"@invalid", "difficulty"},
		{"difficulty", "@invalid"},
	} {
		err := CheckRenderer(RendererTable, map[string]any{"columns": columns}, carrying)
		var qe *QueryError
		if !errors.As(err, &qe) {
			t.Fatalf("%v: expected a *QueryError, got %v", columns, err)
		}
		if qe.Code != CodeQueryInvalid {
			t.Errorf("%v answered %s: the code of a refusal must not depend on the "+
				"order the columns were written in", columns, qe.Code)
		}
	}

	// Ten indices, so the pointer order is the numeric one rather than
	// the string one: /columns/10 after /columns/2, not before it.
	var many []any
	for i := 0; i < 11; i++ {
		many = append(many, "difficulty")
	}
	err = CheckRenderer(RendererTable, map[string]any{"columns": many}, carrying)
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %v", err)
	}
	if len(qe.Fields) != 11 {
		t.Fatalf("expected all eleven columns to be reported, got %d: %v",
			len(qe.Fields), qe.Fields)
	}
	for i, f := range qe.Fields {
		if want := pointer("renderer_params", "columns", i); f.Path != want {
			t.Errorf("problem %d is at %s, want %s", i, f.Path, want)
		}
	}
}

// TestSnapIsReadInManualModeAndRefusedInFields is the "read by nothing"
// rule on the last map parameter that is still a renderer parameter.
//
// **The background knobs used to be tested here and are not renderer
// parameters any more.** background_asset_id, background_scale and
// background_offset were declared in this catalogue *and* carried as
// columns on views by 0008_views.sql — two stores for one value. The
// columns won (only a column can carry the composite foreign key that
// refuses another game's image, and the ON DELETE SET NULL that detaches
// a deleted one), the parameters are gone, and the same rule is applied
// to the columns by assets.go's SetBackground:
// TestABackgroundKnobWithNoBackgroundIsRefused is where that half of
// this test went.
func TestSnapIsReadInManualModeAndRefusedInFields(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, carryingQuery)
	// The control: snap is the grid a dragged node lands on, and manual
	// mode — the default — is what drags.
	checkPasses(t, CheckRenderer(RendererMap, map[string]any{"snap": 10}, quests))
	// And in fields mode a node's coordinates come off its declared
	// fields, nothing is dragged, and a grid size changes no picture.
	checkFails(t, CheckRenderer(RendererMap, map[string]any{
		"coordinate_source": "fields", "x_field": "min_level",
		"y_field": "difficulty", "snap": 10,
	}, quests), ErrRendererRequirements, "/renderer_params/snap",
		"is the grid a dragged node lands on")
}

// TestABackgroundIsNotARendererParameter pins the correction above from
// the other side: the three names are refused as unknown parameters, so
// an agent that read a stale table and sent one is told, rather than
// having a background silently stored where nothing reads it.
func TestABackgroundIsNotARendererParameter(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, carryingQuery)
	for _, name := range []string{
		"background_asset_id", "background_scale", "background_offset",
	} {
		checkFails(t, CheckRenderer(RendererMap, map[string]any{name: 1}, quests),
			ErrQueryInvalid, pointer("renderer_params", name),
			"is not a renderer parameter")
	}
	// The description an agent reads must say where it went, or the
	// refusal above is a dead end.
	description := RendererDescription()
	if !strings.Contains(description, "views.set_background") {
		t.Errorf("the generated description must name views.set_background, since " +
			"the background is refused as a parameter and set nowhere else")
	}
}

func TestMapInFieldsModeRefusesNonNumericCoordinates(t *testing.T) {
	g, _ := newGame(t)
	quests := resolveFor(t, g, carryingQuery)
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
	quests := resolveFor(t, g, carryingQuery)
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
		ErrQueryInvalid, "/renderer_params/columns/0",
		`"@invalid" is not a built-in a table can draw`)
	// A declared field the query does not carry: legal to ask for, and
	// the recovery is in the query.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"difficulty"}},
		carrying),
		ErrRendererRequirements, "/renderer_params/columns/0",
		`names the field "difficulty" and this query does not carry it`)
	// A run's include_fields cannot answer for a saved view, and the
	// refusal says so rather than leaving an agent to try it.
	checkFails(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"difficulty"}},
		carrying),
		ErrRendererRequirements, "/renderer_params/columns/0",
		"include_fields cannot answer for a saved view")
	// The spec's "columns may be attribute references, including one-hop
	// related" is not a shape a renderer can consume: the hop is the
	// query's to take. The refusal says where it belongs.
	checkFails(t, CheckRenderer(RendererTable,
		map[string]any{"columns": []any{map[string]any{"related": map[string]any{}}}}, carrying),
		ErrQueryInvalid, "/renderer_params/columns/0",
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
	// Five problems in one pass: an agent fixing five parameters should
	// learn about five.
	err := CheckRenderer(RendererGraph, map[string]any{
		"size_by": 1, "arrows": "yes", "color_by": 1,
		"edge_labels": "yes", "cluster_by": 1,
	}, resolveFor(t, g, questQuery))
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %v", err)
	}
	if len(qe.Fields) != 5 {
		t.Fatalf("expected all five problems in one pass, got %v", qe.Fields)
	}
	// And in a stable order rather than the map's, which has none.
	//
	// **The mechanism, measured rather than assumed.** The previous
	// version of this comment claimed three keys survive an unsorted
	// implementation one run in six and five one in a hundred and twenty,
	// as though Go permuted a map's keys. It does not: a small map is
	// iterated from a random start slot, so what comes back is a
	// *rotation* of the literal's insertion order, not a permutation.
	// Measured on this toolchain over 10,000 rebuilds of the literal
	// below, five keys give five distinct orders and 0 of 10,000 came
	// back already sorted — because the literal happens to begin with a
	// key that is not first alphabetically. The same five keys written
	// alphabetically came back sorted 4,984 times in 10,000. **The key
	// count is irrelevant, and the guard was standing on the order
	// somebody happened to type.**
	//
	// So the order is asserted deterministically instead: the same call
	// twenty times over, every run identical and every run equal to the
	// sorted expectation. Reordering the literal cannot break it and
	// cannot silently weaken it, and no database is touched per
	// iteration.
	want := []string{
		"/renderer_params/arrows",
		"/renderer_params/cluster_by",
		"/renderer_params/color_by",
		"/renderer_params/edge_labels",
		"/renderer_params/size_by",
	}
	r := resolveFor(t, g, questQuery)
	for run := 0; run < 20; run++ {
		err := CheckRenderer(RendererGraph, map[string]any{
			"size_by": 1, "arrows": "yes", "color_by": 1,
			"edge_labels": "yes", "cluster_by": 1,
		}, r)
		var qe *QueryError
		if !errors.As(err, &qe) {
			t.Fatalf("run %d: expected a *QueryError, got %v", run, err)
		}
		var got []string
		for _, f := range qe.Fields {
			got = append(got, f.Path)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("run %d came back as %v, want %v: sortProblems is what makes a "+
				"refusal reproducible, and a refusal that is only usually in order "+
				"is one a test cannot assert past its first line", run, got, want)
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

// TestATableDoesNotRefuseAQueryThatDrawsEdges pins a deliberate
// non-refusal, which is worth a test for the same reason a refusal is:
// the next reader will ask why the "reads nothing" rule that refuses
// map's x_field does not refuse a table's edges. Because one envelope
// serves every renderer, and swapping graph for table must never require
// rewriting the query.
func TestATableDoesNotRefuseAQueryThatDrawsEdges(t *testing.T) {
	g, _ := newGame(t)
	checkPasses(t, CheckRenderer(RendererTable, map[string]any{"columns": []any{"@name"}},
		resolveFor(t, g, chainQuery)))
}

// TestARendererIsJudgedAgainstAWholeResolvedQuery is the nil check one
// step along: a *Resolved is exported, a Go caller can build one by hand,
// and a missing Query or Catalogue panics rather than refusing.
func TestARendererIsJudgedAgainstAWholeResolvedQuery(t *testing.T) {
	g, _ := newGame(t)
	full := resolveFor(t, g, questQuery)
	// The control: the same call with everything filled is accepted.
	checkPasses(t, CheckRenderer(RendererGraph, map[string]any{"color_by": "label"}, full))
	for _, half := range []*Resolved{
		{Cat: full.Cat},
		{Query: full.Query},
	} {
		err := CheckRenderer(RendererGraph, map[string]any{"color_by": "label"}, half)
		if err == nil || !strings.Contains(err.Error(), "needs a resolved query") {
			t.Fatalf("a half-built resolved query must be refused, got %v", err)
		}
	}
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
		// The same both-arms shape for the prose half. A kind with no
		// phrase prints its own identifier into the description an agent
		// reads — "x_field: number_field." names a Go constant at a
		// reader who has never seen one — and a phrase for no kind is a
		// sentence nothing prints.
		if kind == kindEnum {
			continue // prints its own admitted spellings instead
		}
		if _, ok := kindPhrases[kind]; !ok {
			t.Errorf("kind %q has no phrase, so the description prints the "+
				"identifier %q at an agent", kind, kind)
		}
	}
	for kind := range kindPhrases {
		switch {
		case kind == kindEnum:
			t.Errorf("kindEnum prints its own values and must not have a phrase")
		case !used[kind]:
			t.Errorf("kind %q has a phrase and no parameter declares it: the "+
				"sentence is printed nowhere", kind)
		}
	}
}

// TestEveryRequirementIsDocumentedAndEveryDocHasARequirement is the guard
// F6 asks for, and it is bidirectional for the reason every other guard
// over this table is.
//
// The generated description promises, in its own first sentence, to say
// what a query has to produce for a view to be saveable against each
// renderer — and for one round it said it for none of them. Worse than
// the omission is the drift it left open: a Requires could be added,
// changed or deleted and no generated sentence would change and no test
// would notice, in the half of the contract a query is actually written
// against. So a renderer that checks the query has to say what it checks,
// and a renderer that says it checks something has to check it.
func TestEveryRequirementIsDocumentedAndEveryDocHasARequirement(t *testing.T) {
	documented := 0
	for _, r := range renderers {
		switch {
		case r.Requires != nil && r.RequiresDoc == "":
			t.Errorf("%q checks the query and says nowhere what it checks: an agent "+
				"reads the description and discovers the rule by being refused", r.Name)
		case r.Requires == nil && r.RequiresDoc != "":
			t.Errorf("%q documents a requirement it does not enforce, which is the "+
				"one kind of sentence this catalogue must never print", r.Name)
		case r.Requires != nil:
			documented++
			if !strings.Contains(RendererDescription(), r.RequiresDoc) {
				t.Errorf("%q's requirement is written on the table and printed "+
					"nowhere", r.Name)
			}
		}
	}
	// The vacuity check: this test asserts nothing over a catalogue whose
	// Requires closures have all been deleted.
	if documented < 5 {
		t.Fatalf("the catalogue declares %d requirements and this test needs the "+
			"five it has (graph, layered, nested, map, timeline)", documented)
	}
	// And the three sentences an agent cannot get anywhere else, named
	// individually so that deleting a requirement quietly is a failure
	// here as well as above.
	for _, want := range []string{
		"drawing contain_via's relation type",
		`in "manual" mode — which is the default — neither`,
		"the same options in the same order",
	} {
		if !strings.Contains(RendererDescription(), want) {
			t.Errorf("the description must say %q", want)
		}
	}
}

// TestAParameterNameCarriesOneKindAcrossTheCatalogue is the guard over the
// one thing the per-renderer tables cannot see. `group_by` is a projection
// slot on graph and on table; if one of them were changed to text, the
// description would print two kinds under one prose line, and a misspelt
// slot name on a table would be stored, returned and read by nothing —
// which is the defect this whole file exists to refuse, reached through
// the one door none of the other guards watch.
//
// Divergence is not forbidden, it is declared: a name that genuinely means
// two things goes in the exception list with a reason, and this test then
// stops asserting about it.
func TestAParameterNameCarriesOneKindAcrossTheCatalogue(t *testing.T) {
	// Empty on purpose. Nothing in this catalogue needs to diverge, and a
	// name added here needs a sentence saying why an agent should expect
	// the same word to mean two things.
	allowedToDiffer := map[string]string{}

	kinds := map[string]ParamKind{}
	declaredBy := map[string]string{}
	shared := 0
	for _, renderer := range renderers {
		for _, p := range renderer.Params {
			if _, ok := allowedToDiffer[p.Name]; ok {
				continue
			}
			if was, seen := kinds[p.Name]; seen {
				shared++
				if was != p.Kind {
					t.Errorf("%q is %q on %s and %q on %s: one name means two kinds, "+
						"so the description prints two under one line and one of the "+
						"two values is read by nothing. Give it two names, or add it "+
						"to allowedToDiffer with a reason",
						p.Name, was, declaredBy[p.Name], p.Kind, renderer.Name)
				}
				continue
			}
			kinds[p.Name], declaredBy[p.Name] = p.Kind, renderer.Name
		}
	}
	// The vacuity check: a catalogue where no name is shared makes this
	// test assert nothing at all.
	if shared == 0 {
		t.Fatal("no parameter name is declared by two renderers, so this guard " +
			"compared nothing (group_by is graph's and table's)")
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
	// project.fields carries the key in both, because an open scope
	// switches off the check on project.fields too — which is the whole
	// permission this test records, seen from the other side.
	open := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","as":"z"}],
		  "project":{"fields":["no_such_field","tags"]}}`)
	typed := resolveFor(t, g,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"takes_place_in","to_type":"zone","as":"z"}],
		  "project":{"fields":["tags"]}}`)

	checkPasses(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "no_such_field"}, open))
	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "no_such_field"}, typed),
		ErrRendererRequirements, "/renderer_params/axis_field", `no field "no_such_field"`)

	// **The permission stops where its justification stops.** "Nothing
	// can judge this key" is true of a key no named type declares; it is
	// not true of `tags`, which quest declares list<text> and which is
	// written right there in `from`. The projection's own reason for the
	// wider permission — a node carrying no attribute is still drawable —
	// does not transfer to an axis, which has nowhere to put that node.
	checkFails(t, CheckRenderer(RendererTimeline,
		map[string]any{"axis_field": "tags"}, open),
		ErrRendererRequirements, "/renderer_params/axis_field",
		"is declared list<text> on quest and this parameter needs number and enum")
}

// TestAnOptionlessEnumIsNoAxisEvenIfTheSchemaColumnHoldsOne closes the
// gap between "refused at upsert" and "true". metamodel.Schema.Validate
// refuses an enum with no options, which made "an enum axis is ordered by
// its options" unreachable through the API rather than a rule the axis
// held — nothing revalidates a schema on the way *out* of the database,
// so a row written straight into the schema column loads and is believed.
// The catalogue mutated here is exactly what such a load produces.
func TestAnOptionlessEnumIsNoAxisEvenIfTheSchemaColumnHoldsOne(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, carryingQuery)
	// The control: with its options, the same key is an axis.
	checkPasses(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "rank"}, r))

	quest := r.Cat.EntityTypes["quest"]
	r.Cat.schemas[quest.ID] = metamodel.Schema{
		{Key: "rank", Type: metamodel.FieldEnum},
	}
	checkFails(t, CheckRenderer(RendererTimeline, map[string]any{"axis_field": "rank"}, r),
		ErrRendererRequirements, "/renderer_params/axis_field",
		"is an enum declared on quest with no options")
}

// TestEveryTypeNamingParameterKindIsARecordedReference is the both-arms
// guard for typeNamingKinds, which two things have to agree on: the refs
// a save writes, and the resolution a run performs. A kind in one and not
// the other is a parameter whose type deletion reports nothing and whose
// rename reports the type missing — the defect Task 12's finding 2 fixed
// once already, for `@type` operands.
//
// The second arm is behavioural rather than a second list: a checker that
// answers "this game has no such type" is a checker that turns a key into
// a declared type, whatever its kind is called, and every one of those is
// a reference. It is asked with a string naming nothing, which is the one
// input every such checker refuses.
func TestEveryTypeNamingParameterKindIsARecordedReference(t *testing.T) {
	g, _ := newGame(t)
	r := resolveFor(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"project":{"fields":["min_level"]}}`)
	for kind, check := range paramCheckers {
		rc := &rendererCheck{renderer: &renderers[0], params: map[string]any{}, r: r}
		rc.scope = nodeScopeOf(r.Cat, r.Query, nil)
		names := false
		for _, fault := range check(rc, RendererParam{Name: "p", Kind: kind},
			"nothing_in_this_game_is_called_this") {
			names = names || fault.code == DiagRelationTypeMissing ||
				fault.code == DiagEntityTypeMissing
		}
		if names != (typeNamingKinds[kind] != "") {
			t.Errorf("kind %q resolves a declared type = %v, and typeNamingKinds says "+
				"%v: a type-naming parameter that is not a recorded reference loses "+
				"its id, and a reference whose kind names no type is a row about "+
				"nothing", kind, names, typeNamingKinds[kind] != "")
		}
	}
}
