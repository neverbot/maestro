package views

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/metamodel"
)

// This file is the renderer catalogue.
//
// **It describes a contract, not an appearance.** What a `graph` looks
// like — its palette, its node shapes, its typography, its density, its
// empty state, the affordances a designer clicks — is sub-project 5's and
// /impeccable's, and nothing here decides any of it. What lives here is
// what a renderer *consumes*, what it can be told, and **what a query has
// to produce for a view to be saveable against it**, so that views.upsert
// refuses a broken view when it is written rather than leaving a designer
// to discover an empty picture when they open it (spec §5.1).
//
// Three rules this file holds itself to, each of them a failure this
// sub-project has already made once:
//
//   - **A knob nothing reads is a lie.** renderer_params is a free jsonb
//     blob on the wire and must not be one in practice: a typo'd
//     `rank_dircetion` that is stored, returned and silently ignored
//     forever is the most expensive shape of bug this project has. Every
//     parameter here is declared with a kind, every kind has a checker,
//     and TestEveryDeclaredParameterIsCheckedNotJustStored drives every
//     parameter of every renderer through a wrong value and requires a
//     refusal that names it.
//   - **The catalogue is closed and names itself back.** An unknown
//     renderer is refused with the six spellings, an unknown parameter
//     with the ones this renderer declares.
//   - **A requirement is judged against the *saved* query.** Not against
//     a run: `include_fields` is a per-run option, so a column that would
//     only exist when a caller asks for it is not a column a saved view
//     may name.

// The six renderer names, as constants so that Task 11's storage, Task
// 15's tool surface and this package's own tests spell them once.
const (
	RendererGraph    = "graph"
	RendererLayered  = "layered"
	RendererNested   = "nested"
	RendererMap      = "map"
	RendererTable    = "table"
	RendererTimeline = "timeline"
)

// ParamKind is what a renderer parameter's value has to be. Every kind
// has exactly one checker in paramCheckers, and every checker's kind is
// used by at least one parameter: TestAParameterKindCannotBeHalfAdded
// asserts both directions, the way the operator table's own guard does.
type ParamKind string

// The parameter kinds.
//
// The three that read the query — kindSlot, kindNumberField and
// kindAxisField — are the reason CheckRenderer takes a *Resolved at all:
// "is this a legal value" and "can this query produce it" are different
// questions with different recoveries, and this file answers both.
const (
	// kindBool, kindNumber, kindCount and kindText are values judged on
	// their own shape and nothing else.
	kindBool   ParamKind = "bool"
	kindNumber ParamKind = "number"
	kindCount  ParamKind = "count"
	kindText   ParamKind = "text"
	// kindEnum is one of the spellings the parameter declares.
	kindEnum ParamKind = "enum"
	// kindUUID is an id of a row in another table — a background asset
	// (Task 14). It is checked for shape here and for existence there:
	// this package cannot read that table, and a checker that pretended
	// to would be a second, drifting copy of Task 14's own lookup.
	kindUUID ParamKind = "uuid"
	// kindPoint is an [x, y] pair of numbers.
	kindPoint ParamKind = "point"
	// kindSlot names a projection slot — a key of Node.Attrs — and
	// therefore requires the query to declare that slot. A renderer
	// channel names a slot rather than a field key because the query
	// already decides what a slot reads, and two places to choose a field
	// is one place too many: the document's `project.color_by` may take
	// the one hop to a neighbouring zone, which no renderer can do for
	// itself.
	kindSlot ParamKind = "slot"
	// kindNumberField and kindAxisField name a declared field key and
	// require a usable declared type on every type in scope that declares
	// it — number for a coordinate, number or enum for an axis.
	kindNumberField ParamKind = "number_field"
	kindAxisField   ParamKind = "axis_field"
	// kindRankBy is the literal "edges" or a number field key.
	kindRankBy ParamKind = "rank_by"
	// kindRelationType names a relation type this game declares.
	kindRelationType ParamKind = "relation_type"
	// kindColumn and kindColumns name what a table draws: a built-in the
	// envelope carries, a projection slot, or a key `project.fields`
	// asked for.
	kindColumn  ParamKind = "column"
	kindColumns ParamKind = "columns"
)

// RendererParam is one knob a renderer exposes.
type RendererParam struct {
	Name string
	Kind ParamKind
	// Required parameters are missing-checked by CheckRenderer itself,
	// which is why no renderer's Requires has to repeat the check.
	Required bool
	// Values are the admitted spellings, and are declared by kindEnum
	// parameters and by no others.
	// TestAnEnumParameterDeclaresItsValuesAndNothingElseDoes pins both
	// directions.
	Values []string
	// Doc is the one line the generated description carries for this
	// parameter. It is prose for an agent, and it is the only place a
	// parameter's meaning is written down.
	Doc string
}

// Renderer is one entry of the fixed catalogue.
//
// **This describes a contract, not an appearance** — see this file's
// header. What lives here is what a renderer consumes and what it can be
// told, so that views.upsert can refuse a view whose query cannot feed
// the renderer it names.
type Renderer struct {
	Name string
	// Consumes is the envelope this renderer reads, in the catalogue's
	// own words ("nodes, edges").
	Consumes string
	// Doc is what the renderer is for, in one or two sentences, generated
	// into the tool description an agent reads.
	Doc string
	// Params are this renderer's knobs, in the order the description
	// prints them.
	Params []RendererParam
	// Requires is checked against a *resolved* query at upsert time. It
	// returns the problems that make this query unfeedable to this
	// renderer, each addressed by its own pointer, or nothing.
	//
	// **It returns problems rather than the plan's single string**, and
	// the reason is this package's own rule: every refusal it makes is
	// addressed by a JSON pointer, because an agent that is told
	// "/renderer_params/x_field" knows which of seven parameters to
	// rewrite and no amount of prose gets it there as reliably. A bare
	// sentence would have been the one refusal in this sub-project with
	// no address.
	Requires func(rc *rendererCheck) []metamodel.FieldError
}

// renderers is the catalogue, in the order the description prints it and
// the order the refusal lists it.
var renderers = []Renderer{
	{
		Name:     RendererGraph,
		Consumes: "nodes, edges",
		Doc: "A node-link diagram laid out by the client. The general case: " +
			"it draws whatever the query produced and tolerates an edge whose " +
			"endpoints are not both in nodes.",
		Params: []RendererParam{
			{Name: "color_by", Kind: kindSlot,
				Doc: "the projection slot that colours a node"},
			{Name: "group_by", Kind: kindSlot,
				Doc: "the projection slot that groups nodes"},
			{Name: "size_by", Kind: kindSlot,
				Doc: "the projection slot that sizes a node"},
			{Name: "cluster_by", Kind: kindSlot,
				Doc: "the projection slot whose value clusters nodes together in the layout"},
			{Name: "edge_labels", Kind: kindBool,
				Doc: "draw each edge's label; the query has to ask for one with " +
					"edges[].label_from"},
			{Name: "arrows", Kind: kindBool,
				Doc: "draw an edge's direction"},
		},
		Requires: func(rc *rendererCheck) []metamodel.FieldError {
			// edge_labels asks for text that only the query can produce.
			// Turning it on over a query that labels nothing is a knob
			// that draws nothing and says nothing — this file's first
			// rule, in the one position where a boolean can break it.
			if on, _ := rc.params["edge_labels"].(bool); on && !rc.anyEdgeLabelled() {
				return rc.problem("edge_labels",
					"asks for a label on every edge and no edges[] entry declares "+
						"label_from: add label_from to an edges[] entry, or turn "+
						"edge_labels off")
			}
			return nil
		},
	},
	{
		Name:     RendererLayered,
		Consumes: "nodes, edges (expected mostly acyclic)",
		Doc: "A ranked diagram: nodes in layers, edges running between them. " +
			"For progressions and prerequisite chains, where the direction of " +
			"the graph is the point.",
		Params: []RendererParam{
			{Name: "rank_direction", Kind: kindEnum, Values: []string{"TB", "LR"},
				Doc: "top-to-bottom or left-to-right"},
			{Name: "rank_by", Kind: kindRankBy,
				Doc: `"edges" for the longest path through the edges, or a number ` +
					"field key that gives each node its rank directly"},
			{Name: "layer_labels", Kind: kindBool,
				Doc: "label each layer"},
			{Name: "align", Kind: kindEnum, Values: []string{"start", "center", "end"},
				Doc: "where a node sits within its layer"},
		},
		Requires: func(rc *rendererCheck) []metamodel.FieldError {
			// Layers are the edges: a layered view of a query that draws
			// none is one layer, which is a list drawn expensively.
			// `graph` deliberately has no such requirement — a scatter of
			// unconnected nodes is a picture it can draw.
			if !rc.drawsEdges() {
				return rc.rendererProblem(
					"ranks nodes by the edges between them and this query draws no " +
						"edges: add an edges[] entry, or use the table renderer")
			}
			return nil
		},
	},
	{
		Name:     RendererNested,
		Consumes: "nodes, edges of one containment relation type",
		Doc: "Boxes inside boxes: one relation type read as containment. " +
			"For a place inside a place, or a mission inside a chapter.",
		Params: []RendererParam{
			{Name: "contain_via", Kind: kindRelationType, Required: true,
				Doc: "the relation type read as containment, source contained in target"},
			{Name: "max_depth", Kind: kindCount,
				Doc: "how many levels of nesting to draw"},
			{Name: "leaf_label", Kind: kindSlot,
				Doc: "the projection slot that labels a box with no children"},
		},
		Requires: func(rc *rendererCheck) []metamodel.FieldError {
			via, _ := rc.params["contain_via"].(string)
			row, ok := rc.r.Cat.RelationTypes[strings.ToLower(via)]
			if !ok {
				// The kind checker already refused the key; saying so a
				// second time is one refusal twice.
				return nil
			}
			if !rc.drawnRelationTypes()[row.ID] {
				return rc.problem("contain_via", fmt.Sprintf(
					"names %q and this query draws no edges of it: nesting is that "+
						"relation, so the picture would be one flat row of boxes. Add "+
						"an edges[] entry with \"via\": %q, or one drawing the step "+
						"that walks it", row.Key, row.Key))
			}
			return nil
		},
	},
	{
		Name:     RendererMap,
		Consumes: "nodes, edges optional, coordinates required",
		Doc: "Nodes at coordinates, optionally over a background image. The " +
			"coordinates are either the ones designers dragged (Task 13's " +
			"positions) or two number fields the game declares.",
		Params: []RendererParam{
			{Name: "coordinate_source", Kind: kindEnum, Values: []string{"manual", "fields"},
				Doc: `"manual" reads the positions designers dragged, "fields" reads ` +
					"two declared number fields off each node"},
			{Name: "x_field", Kind: kindNumberField,
				Doc: `the number field holding x, in "fields" mode`},
			{Name: "y_field", Kind: kindNumberField,
				Doc: `the number field holding y, in "fields" mode`},
			{Name: "background_asset_id", Kind: kindUUID,
				Doc: "the background image, an asset of this game"},
			{Name: "background_scale", Kind: kindNumber,
				Doc: "how many coordinate units one pixel of the background is"},
			{Name: "background_offset", Kind: kindPoint,
				Doc: "[x, y] the background's top-left corner sits at"},
			{Name: "snap", Kind: kindNumber,
				Doc: "grid size a dragged node snaps to; 0 for no grid"},
		},
		Requires: func(rc *rendererCheck) []metamodel.FieldError {
			source, _ := rc.params["coordinate_source"].(string)
			if source == "" {
				source = "manual"
			}
			var problems []metamodel.FieldError
			for _, name := range []string{"x_field", "y_field"} {
				_, given := rc.params[name]
				switch {
				case source == "fields" && !given:
					problems = append(problems, rc.problem(name,
						`is required when coordinate_source is "fields": name the `+
							"declared number field that holds this coordinate")...)
				case source == "manual" && given:
					// Read by nothing in manual mode, which is this
					// file's first rule: a stored parameter that changes
					// no picture is a lie a designer will believe.
					problems = append(problems, rc.problem(name,
						`is read by nothing when coordinate_source is "manual", which `+
							"reads the positions designers dragged: remove it, or set "+
							`coordinate_source to "fields"`)...)
				}
			}
			return problems
		},
	},
	{
		Name: RendererTable,
		// "nodes only", and it does **not** refuse a query that draws
		// edges. One envelope for every renderer is the property that
		// lets a saved view swap graph for table without rewriting its
		// query (Result's own doc comment), so edges a table ignores are
		// a renderer choice rather than dead weight in the document.
		// That is the line this catalogue draws between the two: a
		// *parameter* a renderer's own mode ignores is refused, because
		// nothing else will ever read it; a *query* member this renderer
		// ignores is read by the next renderer the view is switched to.
		// TestATableDoesNotRefuseAQueryThatDrawsEdges pins it.
		Consumes: "nodes only",
		Doc: "Rows and columns. Probably the most-used renderer: \"every quest " +
			"in Elwynn with its level and its rewards\" is a question designers " +
			"ask far more often than anything graph-shaped, and the same query " +
			"language answers it. It draws no edges, and it does not page over " +
			"the server: views.run has no cursor, because a page of a graph is " +
			"not a graph and half an edge set is a wrong picture rather than a " +
			"partial one. A result too large for one response is truncated and " +
			"flagged by max_nodes; page_size below pages the rows a client " +
			"already holds. When that stops being enough the answer is a " +
			"separate views.run_table tool with its own cursor and its own " +
			"row-shaped envelope, not a cursor bolted onto this one.",
		Params: []RendererParam{
			{Name: "columns", Kind: kindColumns,
				Doc: "the columns to draw, in order: a built-in the envelope carries " +
					"(@name, @key, @type), a projection slot, or a key project.fields " +
					"asked for"},
			{Name: "sort", Kind: kindColumn,
				Doc: "the column the rows are ordered by when the view opens"},
			{Name: "group_by", Kind: kindSlot,
				Doc: "the projection slot that groups rows under a heading"},
			{Name: "page_size", Kind: kindCount,
				Doc: "rows per page, client-side; the server returns the whole result"},
		},
	},
	{
		Name:     RendererTimeline,
		Consumes: "nodes with a numeric or ordinal axis",
		Doc: "Nodes along one axis, optionally in lanes. For a progression " +
			"read as a sequence: levels, chapters, championship rounds.",
		Params: []RendererParam{
			{Name: "axis_field", Kind: kindAxisField, Required: true,
				Doc: "the declared field the axis reads: number, or enum, whose " +
					"declared options are the order"},
			{Name: "axis_end_field", Kind: kindAxisField,
				Doc: "the field holding the end of a span, for nodes that occupy a " +
					"range rather than a point"},
			{Name: "lane_by", Kind: kindSlot,
				Doc: "the projection slot that puts a node in a lane"},
			{Name: "axis_label", Kind: kindText,
				Doc: "what to call the axis"},
		},
		Requires: func(rc *rendererCheck) []metamodel.FieldError {
			// A span's two ends have to be the same kind of thing. A
			// number start with an enum end is two axes, and the picture
			// would place one of them arbitrarily.
			start, _ := rc.params["axis_field"].(string)
			end, ok := rc.params["axis_end_field"].(string)
			if !ok || start == "" || end == "" {
				return nil
			}
			startTypes, endTypes := rc.declaredTypesOf(start), rc.declaredTypesOf(end)
			if len(startTypes) == 0 || len(endTypes) == 0 {
				// Undeclared, or a scope that cannot judge: the kind
				// checker has already spoken, or nothing can.
				return nil
			}
			if !sameTypeSet(startTypes, endTypes) {
				return rc.problem("axis_end_field", fmt.Sprintf(
					"is declared %s and axis_field %q is declared %s: the two ends of "+
						"a span have to be the same kind of axis",
					joinTypes(endTypes), start, joinTypes(startTypes)))
			}
			return nil
		},
	},
}

// rendererByName is the lookup, derived from the catalogue so a renderer
// cannot be reachable by name and absent from the description, or the
// other way round.
var rendererByName = func() map[string]*Renderer {
	out := make(map[string]*Renderer, len(renderers))
	for i := range renderers {
		out[renderers[i].Name] = &renderers[i]
	}
	return out
}()

// RendererNames lists the catalogue in its declared order, which is what
// an unknown-renderer refusal prints: an agent that misspelled one is
// told the whole catalogue rather than that its spelling was wrong.
func RendererNames() []string {
	out := make([]string, 0, len(renderers))
	for _, r := range renderers {
		out = append(out, r.Name)
	}
	return out
}

// RendererDescription is the catalogue as prose, **generated from the
// table above and from nothing else**, so that Task 15's tool description
// cannot drift from what CheckRenderer enforces. A hand-written sentence
// naming a knob that does not exist is a knob an agent will send and this
// package will refuse; a knob added to the table and left out of the
// prose is one no agent will ever find.
// TestEveryRendererDeclaresItsParametersAndTheDescriptionIsGeneratedFromThem
// reads this text back and compares it with the table in both directions.
func RendererDescription() string {
	var b strings.Builder
	b.WriteString("The renderer catalogue. Each renderer declares what a query has to " +
		"produce for a view to be saveable against it; views.upsert refuses the " +
		"combination otherwise with renderer_requirements.\n")
	for _, r := range renderers {
		fmt.Fprintf(&b, "\n- %s (consumes %s): %s\n", r.Name, r.Consumes, r.Doc)
		for _, p := range r.Params {
			fmt.Fprintf(&b, "  - %s: %s. %s", p.Name, p.kindPhrase(), p.Doc)
			if p.Required {
				b.WriteString(" (required)")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// kindPhrase is how a parameter's kind reads in the description. An enum
// prints its own values, so the admitted spellings are in the prose an
// agent reads rather than only in the refusal it gets afterwards.
func (p RendererParam) kindPhrase() string {
	if p.Kind == kindEnum {
		return fmt.Sprintf(`one of "%s"`, strings.Join(p.Values, `", "`))
	}
	return string(p.Kind)
}

// CheckRenderer judges a renderer name, its parameters and the query they
// are saved with, and is what views.upsert calls (Task 11).
//
// **Two codes, because the recoveries differ.** query_invalid is "fix
// this argument": an unknown renderer, an unknown parameter name, a value
// of the wrong shape. renderer_requirements is "pick another renderer, or
// change the query": a well-formed value this query cannot feed — a slot
// the projection does not declare, a coordinate field that is not a
// number, a containment relation the query draws no edges of.
//
// Shape problems win when both are present, and every problem of the
// winning class comes back at once: an agent fixing a typo'd parameter
// name has not yet learned anything from being told, in the same breath,
// that a different parameter names a slot its query lacks.
func CheckRenderer(name string, params map[string]any, r *Resolved) error {
	renderer, ok := rendererByName[name]
	if !ok {
		return invalidQuery(pointer("renderer"), fmt.Sprintf(
			"%q is not a renderer this catalogue has: the renderers are %s",
			name, strings.Join(RendererNames(), ", ")))
	}
	if r == nil || r.Query == nil || r.Cat == nil {
		// Every requirement below reads the query and the game's
		// vocabulary. A nil one would make this function answer "fine"
		// for a document nothing had judged — the check that is not a
		// check — and the three are checked rather than the first,
		// because Resolved is exported and a caller may hand back one it
		// built by hand: a nil Query panics one line further on, which is
		// the same hole one step along.
		return fmt.Errorf("views: CheckRenderer needs a resolved query and its catalogue "+
			"to judge %q against", name)
	}
	rc := &rendererCheck{renderer: renderer, params: params, r: r}
	rc.scope = nodeScopeOf(r.Cat, r.Query)

	var shape, requirements []metamodel.FieldError
	for given, value := range params {
		p, ok := renderer.param(given)
		if !ok {
			shape = append(shape, metamodel.FieldError{
				Path: pointer("renderer_params", given),
				Message: fmt.Sprintf("is not a parameter %q takes: it takes %s",
					renderer.Name, strings.Join(renderer.paramNames(), ", ")),
			})
			continue
		}
		check, ok := paramCheckers[p.Kind]
		if !ok {
			// A kind with no checker would otherwise be a parameter
			// stored and never judged, which is the failure this whole
			// file is built against. TestAParameterKindCannotBeHalfAdded
			// catches it in the test suite; this catches it in
			// production, loudly, rather than by accepting anything.
			return fmt.Errorf("views: renderer %q declares parameter %q with kind %q "+
				"and no checker", renderer.Name, p.Name, p.Kind)
		}
		fault := check(rc, p, value)
		if fault == nil {
			continue
		}
		problem := metamodel.FieldError{
			Path: pointer("renderer_params", given), Message: fault.message,
		}
		if fault.requirement {
			requirements = append(requirements, problem)
		} else {
			shape = append(shape, problem)
		}
	}
	// The map is a map, so the problems above arrive in no order at all.
	// Sorting them is what makes a refusal reproducible and a test able to
	// assert more than the first line.
	sortProblems(shape)
	sortProblems(requirements)
	if len(shape) > 0 {
		return invalidQueryProblems(shape)
	}
	for _, p := range renderer.Params {
		if _, given := params[p.Name]; p.Required && !given {
			requirements = append(requirements, rc.problem(p.Name,
				"is required by this renderer: "+p.Doc)...)
		}
	}
	if renderer.Requires != nil {
		requirements = append(requirements, renderer.Requires(rc)...)
	}
	if len(requirements) > 0 {
		return &QueryError{Code: CodeRendererRequirements, Fields: requirements}
	}
	return nil
}

func sortProblems(problems []metamodel.FieldError) {
	sort.SliceStable(problems, func(i, j int) bool {
		return pointerLess(problems[i].Path, problems[j].Path)
	})
}

func (r *Renderer) param(name string) (RendererParam, bool) {
	for _, p := range r.Params {
		if p.Name == name {
			return p, true
		}
	}
	return RendererParam{}, false
}

func (r *Renderer) paramNames() []string {
	out := make([]string, 0, len(r.Params))
	for _, p := range r.Params {
		out = append(out, p.Name)
	}
	return out
}

// rendererCheck is one call's worth of context: the parameters, the
// resolved query, and the derived answers a checker or a Requires needs.
type rendererCheck struct {
	renderer *Renderer
	params   map[string]any
	r        *Resolved
	scope    projectionScope
	drawn    map[uuid.UUID]bool
}

// problem addresses one parameter of this call.
func (rc *rendererCheck) problem(param, message string) []metamodel.FieldError {
	return []metamodel.FieldError{{Path: pointer("renderer_params", param), Message: message}}
}

// rendererProblem addresses the renderer itself, for a requirement no one
// parameter is to blame for.
func (rc *rendererCheck) rendererProblem(message string) []metamodel.FieldError {
	return []metamodel.FieldError{{
		Path: pointer("renderer"), Message: rc.renderer.Name + " " + message,
	}}
}

// slots are the projection slots this query declares, which is what a
// kindSlot parameter may name. It reads the *resolved* projection rather
// than the document, so a slot whose attribute did not resolve is not a
// slot a renderer may read.
func (rc *rendererCheck) slots() map[string]bool {
	out := make(map[string]bool, len(rc.r.Projection.Slots))
	for _, slot := range rc.r.Projection.Slots {
		out[slot.Name] = true
	}
	return out
}

// drawsEdges says whether this query draws any edge at all.
func (rc *rendererCheck) drawsEdges() bool { return len(rc.r.Query.Edges) > 0 }

// anyEdgeLabelled says whether any edges[] entry asked for a label.
func (rc *rendererCheck) anyEdgeLabelled() bool {
	for _, edge := range rc.r.Edges {
		if edge.LabelFrom != "" {
			return true
		}
	}
	return false
}

// drawnRelationTypes are the relation types this query actually draws
// edges of.
//
// It reads both spellings of an edges[] entry, because they are equally
// good ways to draw a containment edge and a check that saw only `via`
// would refuse a legitimate view: the `from_step` spelling draws exactly
// the relations its step walked, so its types are that step's.
func (rc *rendererCheck) drawnRelationTypes() map[uuid.UUID]bool {
	if rc.drawn != nil {
		return rc.drawn
	}
	rc.drawn = map[uuid.UUID]bool{}
	for _, edge := range rc.r.Edges {
		for _, id := range edge.RelationTypeIDs {
			rc.drawn[id] = true
		}
		if edge.Spec == nil || edge.Spec.FromStep == "" {
			continue
		}
		for _, step := range rc.r.Steps {
			if step.Name != edge.Spec.FromStep {
				continue
			}
			for _, id := range step.RelationTypeIDs {
				rc.drawn[id] = true
			}
		}
	}
	return rc.drawn
}

// declaredTypesOf lists the declared types a field key has across the
// entity types this query draws, deduplicated and in declaration order.
//
// An empty answer means one of two things and the caller has to know
// which: nothing in scope declares the key, or the scope cannot judge —
// a traverse step with no to_type reaches entities of any type, so no
// schema applies. projectionScope.declares draws the same line and this
// reads the same flags rather than a second copy of the rule.
func (rc *rendererCheck) declaredTypesOf(key string) []metamodel.FieldType {
	if rc.scope.open || rc.scope.unresolved {
		return nil
	}
	var out []metamodel.FieldType
	for _, schema := range rc.scope.schemas {
		for i := range schema {
			if schema[i].Key != key {
				continue
			}
			if !containsType(out, schema[i].Type) {
				out = append(out, schema[i].Type)
			}
		}
	}
	return out
}

// sameSequence compares two option lists in order, which is what an
// ordered axis needs and what sameOptions deliberately does not do.
func sameSequence(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsType(list []metamodel.FieldType, t metamodel.FieldType) bool {
	for _, have := range list {
		if have == t {
			return true
		}
	}
	return false
}

func sameTypeSet(a, b []metamodel.FieldType) bool {
	if len(a) != len(b) {
		return false
	}
	for _, t := range a {
		if !containsType(b, t) {
			return false
		}
	}
	return true
}

func joinTypes(list []metamodel.FieldType) string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, string(t))
	}
	return strings.Join(out, " and ")
}

// paramFault is one parameter's refusal, plus which of the two codes it
// belongs to: a value that is wrong in itself is query_invalid, and a
// well-formed value this query cannot feed is renderer_requirements.
type paramFault struct {
	message     string
	requirement bool
}

func badShape(format string, args ...any) *paramFault {
	return &paramFault{message: fmt.Sprintf(format, args...)}
}

func unmet(format string, args ...any) *paramFault {
	return &paramFault{message: fmt.Sprintf(format, args...), requirement: true}
}

// paramCheckers is the kind table: one checker per kind, and the reason
// the kinds are data rather than a switch.
// TestAParameterKindCannotBeHalfAdded asserts both directions — a kind
// used by a parameter and missing here would panic on the first value
// sent, and a checker no parameter uses is a kind somebody meant to
// declare.
var paramCheckers = map[ParamKind]func(rc *rendererCheck, p RendererParam, v any) *paramFault{
	kindBool: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		if _, ok := v.(bool); !ok {
			return badShape("must be true or false, got %s", jsonTypeOf(v))
		}
		return nil
	},
	kindNumber: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		if _, ok := numberOf(v); !ok {
			return badShape("must be a number, got %s", jsonTypeOf(v))
		}
		return nil
	},
	kindCount: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		n, ok := numberOf(v)
		if !ok {
			return badShape("must be a whole number of at least 1, got %s", jsonTypeOf(v))
		}
		if n != math.Trunc(n) || n < 1 {
			return badShape("must be a whole number of at least 1, got %v", v)
		}
		return nil
	},
	kindText: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		s, ok := v.(string)
		if !ok {
			return badShape("must be a string, got %s", jsonTypeOf(v))
		}
		if len(s) > MaxStringLen {
			return badShape("must be at most %d characters", MaxStringLen)
		}
		return nil
	},
	kindEnum: func(_ *rendererCheck, p RendererParam, v any) *paramFault {
		s, ok := v.(string)
		if !ok {
			return badShape(`must be one of "%s", got %s`,
				strings.Join(p.Values, `", "`), jsonTypeOf(v))
		}
		for _, admitted := range p.Values {
			if s == admitted {
				return nil
			}
		}
		return badShape(`%q is not one of "%s"`, s, strings.Join(p.Values, `", "`))
	},
	kindUUID: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		s, ok := v.(string)
		if !ok {
			return badShape("must be a uuid, got %s", jsonTypeOf(v))
		}
		if _, err := uuid.Parse(s); err != nil {
			return badShape("must be a uuid: %v", err)
		}
		return nil
	},
	kindPoint: func(_ *rendererCheck, _ RendererParam, v any) *paramFault {
		list, ok := v.([]any)
		if !ok || len(list) != 2 {
			return badShape("must be a pair of numbers, [x, y], got %s", jsonTypeOf(v))
		}
		for _, item := range list {
			if _, ok := numberOf(item); !ok {
				return badShape("must be a pair of numbers, [x, y], and %s is %s",
					describe(item), jsonTypeOf(item))
			}
		}
		return nil
	},
	kindSlot: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		name, ok := v.(string)
		if !ok {
			return badShape("must name a projection slot, got %s", jsonTypeOf(v))
		}
		if !isSlotName(name) {
			return badShape("%q is not a projection slot: the slots are %s",
				name, strings.Join(slotNames(), ", "))
		}
		if !rc.slots()[name] {
			return unmet("names the %q slot and this query's project does not declare "+
				"one: add %q to project, or point this parameter at a slot it declares",
				name, name)
		}
		return nil
	},
	kindNumberField: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return fault
		}
		return rc.requireDeclaredAs(key, metamodel.FieldNumber)
	},
	kindAxisField: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return fault
		}
		return rc.requireDeclaredAs(key, metamodel.FieldNumber, metamodel.FieldEnum)
	},
	kindRankBy: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		s, ok := v.(string)
		if !ok {
			return badShape(`must be "edges" or a number field key, got %s`, jsonTypeOf(v))
		}
		if s == "edges" {
			return nil
		}
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return badShape(`must be "edges" or a number field key: %s`, fault.message)
		}
		return rc.requireDeclaredAs(key, metamodel.FieldNumber)
	},
	kindRelationType: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		key, ok := v.(string)
		if !ok {
			return badShape("must name a relation type, got %s", jsonTypeOf(v))
		}
		if _, ok := rc.r.Cat.RelationTypes[strings.ToLower(key)]; !ok {
			return unmet("no relation type %q in this game: declare it with "+
				"relation_types.upsert, or use relation_types.list to see what this "+
				"game has", key)
		}
		return nil
	},
	kindColumn: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		return rc.column(v)
	},
	kindColumns: func(rc *rendererCheck, _ RendererParam, v any) *paramFault {
		list, ok := v.([]any)
		if !ok {
			return badShape("must be a list of column references, got %s", jsonTypeOf(v))
		}
		if len(list) == 0 {
			return badShape("is empty: name at least one column, or leave the " +
				"parameter out")
		}
		if len(list) > MaxColumns {
			return badShape("must name at most %d columns", MaxColumns)
		}
		for _, item := range list {
			if fault := rc.column(item); fault != nil {
				return fault
			}
		}
		return nil
	},
}

// MaxColumns bounds a table's `columns`. It is the same kind of bound as
// the query document's own: a refusal, never a truncation, because a
// table silently missing its last column is a wrong answer that looks
// like a right one.
const MaxColumns = 64

// column judges one column reference against what the envelope carries.
//
// **The rule is the envelope, not the database.** A node comes back with
// its identity, the projection's attrs and — only when a run asks for
// include_fields — its declared fields. include_fields is a per-run
// option and a saved view cannot turn it on, so a column naming a
// declared field key is legal exactly when project.fields asked for that
// key. Anything else is a column that would be blank in the picture and
// nowhere in the answer, and the recovery is named in the refusal.
//
// The @-built-ins it admits are the ones Node actually carries: @name,
// @key and @type. @invalid and @created_at are legal in a *predicate*,
// where they compare against columns of the row, and they are not in the
// envelope, so a table cannot draw them.
func (rc *rendererCheck) column(v any) *paramFault {
	name, ok := v.(string)
	if !ok {
		return badShape("every column must be a string naming a built-in, a projection "+
			"slot or a key project.fields asked for, got %s. A one-hop related "+
			"attribute is drawn by projecting it into a slot and naming the slot: "+
			"the renderer reads the envelope and cannot take the hop itself",
			jsonTypeOf(v))
	}
	if strings.HasPrefix(name, "@") {
		for _, admitted := range envelopeBuiltins {
			if name == admitted {
				return nil
			}
		}
		return badShape("%q is not a built-in a table can draw: the envelope carries %s",
			name, strings.Join(envelopeBuiltins, ", "))
	}
	if isSlotName(name) {
		if !rc.slots()[name] {
			return unmet("names the %q slot and this query's project does not declare "+
				"one: add %q to project, or drop the column", name, name)
		}
		return nil
	}
	if _, fault := fieldKeyValue(v); fault != nil {
		return fault
	}
	for _, key := range rc.r.Projection.Fields {
		if key == name {
			return nil
		}
	}
	return unmet("names the field %q and this query does not carry it: add it to "+
		"project.fields. A run's include_fields cannot answer for a saved view, "+
		"because it is a per-run option and this view is saved without one", name)
}

// envelopeBuiltins are the @-built-ins a node actually comes back with.
var envelopeBuiltins = []string{AttrName, AttrKey, AttrType}

// slotNames is the projection's slot vocabulary, read off predicate.go's
// table so a sixth slot is a slot renderers can name on the day it is
// added rather than on the day somebody remembers this list.
func slotNames() []string {
	out := make([]string, 0, len(projectionAttrs))
	for _, attr := range projectionAttrs {
		out = append(out, attr.Name)
	}
	return out
}

func isSlotName(name string) bool {
	for _, attr := range projectionAttrs {
		if attr.Name == name {
			return true
		}
	}
	return false
}

// fieldKeyValue judges a value as a declared field key's *spelling*,
// which is a shape question and is judged by the query document's own
// rule rather than by a second one.
func fieldKeyValue(v any) (string, *paramFault) {
	key, ok := v.(string)
	if !ok {
		return "", badShape("must name a declared field, got %s", jsonTypeOf(v))
	}
	switch {
	case key == "":
		return "", badShape("is empty: name a declared field key")
	case len(key) > maxFieldKeyLen:
		return "", badShape("must be at most %d characters", maxFieldKeyLen)
	case !fieldKeyPattern.MatchString(key):
		return "", badShape("must be lower_snake_case: %q is not a declared field key", key)
	}
	return key, nil
}

// requireDeclaredAs is the requirement half of a field-key parameter: the
// key has to be declared somewhere this query draws, and **every** type
// that declares it has to declare it usably.
//
// The two halves are two different failures and both are the silent-empty
// one this language refuses everywhere else. A key nothing declares is a
// typo, answered with an axis every node sits at the origin of. A key
// declared `number` on quests and `text` on regions is worse: the quests
// are placed and the regions vanish, and the picture looks right.
//
// This is deliberately stricter than projectionScope.declares, which
// admits a key declared on at least one type in scope: a projection slot
// that finds nothing on some nodes leaves those nodes without an
// attribute, which a renderer can draw honestly, and an axis that finds
// nothing has nowhere to put them.
func (rc *rendererCheck) requireDeclaredAs(key string, admitted ...metamodel.FieldType) *paramFault {
	if rc.scope.open || rc.scope.unresolved {
		// A traverse step with no to_type reaches entities of any type,
		// so no schema applies and nothing here can judge. The permission
		// costs what projectionScope.open records it costing.
		return nil
	}
	var declared []string
	// An enum axis is ordered by its declared options, so two types
	// declaring the same key with the same options in a different order
	// are two different axes. This is the one place the *sequence*
	// matters: resolve.go's sameOptions compares an enum's options as a
	// set, because a predicate over them asks whether a value is one of
	// them, and order changes no answer there.
	var firstOn string
	var firstField metamodel.Field
	for i, schema := range rc.scope.schemas {
		for j := range schema {
			if schema[j].Key != key {
				continue
			}
			on := rc.scope.names[i]
			if !containsType(admitted, schema[j].Type) {
				return unmet("is declared %s on %s and this parameter needs %s: "+
					"a node whose value for it is not one of those cannot be placed, "+
					"and would be drawn at the origin or not at all",
					schema[j].Type, on, joinTypes(admitted))
			}
			// Two types declaring the key differently are two different
			// axes, and a picture drawn over both would place a node by
			// whichever type it happens to have. This is the failure one
			// step along from the one above: number and enum are each
			// admitted, and a scope holding one of each passes the check
			// above without agreeing on anything.
			if firstOn != "" && firstField.Type != schema[j].Type {
				return unmet("is declared %s on %s and %s on %s: those are two "+
					"different axes, and a node would be placed by whichever type it "+
					"happens to have",
					firstField.Type, firstOn, schema[j].Type, on)
			}
			if firstOn != "" && schema[j].Type == metamodel.FieldEnum &&
				!sameSequence(firstField.Options, schema[j].Options) {
				return unmet("is an enum declared [%s] on %s and [%s] on %s: an enum "+
					"axis is ordered by its declared options, so those are two "+
					"different axes. Declare the same options in the same order, or "+
					"use a number field. (A predicate over the same field compares "+
					"the options as a set, where the order changes no answer; an "+
					"axis is the one place the sequence is the meaning)",
					strings.Join(firstField.Options, ", "), firstOn,
					strings.Join(schema[j].Options, ", "), on)
			}
			firstOn, firstField = on, schema[j]
			declared = append(declared, on)
		}
	}
	if len(declared) == 0 {
		return unmet("no field %q is declared on %s (%s): use types.get to see a "+
			"type's field_schema", key, rc.scope.subject, strings.Join(rc.scope.names, ", "))
	}
	return nil
}

// numberOf reads a JSON number in every spelling this package can be
// handed one: a jsonb value decoded by encoding/json is a float64 or a
// json.Number depending on the decoder, and a Go caller building
// parameters by hand writes an int.
func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// jsonTypeOf names a value's type the way the document spells it, so a
// refusal reads "got a string" rather than naming a Go type an agent
// never wrote.
func jsonTypeOf(v any) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case json.Number, float64, float32, int, int64:
		return "a number"
	default:
		return fmt.Sprintf("a %T", value)
	}
}

// describe is jsonTypeOf's companion for an item inside a list, naming
// the value itself where it is short enough to be worth quoting.
func describe(v any) string {
	if s, ok := v.(string); ok && len(s) <= 32 {
		return fmt.Sprintf("%q", s)
	}
	return "an item"
}
