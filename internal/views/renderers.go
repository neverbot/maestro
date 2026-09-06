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
	// RequiresDoc is what Requires enforces, in prose, printed under this
	// renderer in the generated description.
	//
	// **The header of that description promises "what a query has to
	// produce for a view to be saveable", and for one round the body said
	// it for no renderer.** An agent read the parameters and nothing
	// told it that nested needs edges of contain_via, that map's x_field
	// is refused in manual mode, or that a timeline axis has to be
	// declared identically across the whole scope — the half of the
	// contract a query is actually written against.
	//
	// It is a second place the same rule is written, which is the risk
	// this file refuses everywhere else, so it is guarded the way the
	// parameter table is: TestEveryRequirementIsDocumentedAndEveryDoc
	// HasARequirement asserts both directions, and a Requires added,
	// changed or deleted with this line left alone is a failing test
	// rather than a sentence that has quietly started to lie.
	RequiresDoc string
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
	// ReadsBackground says whether this renderer draws a background
	// image, and is the one source for that fact.
	//
	// **The background is a column of the view, not a renderer
	// parameter**, and that is a correction rather than an accident of
	// naming. The catalogue carried background_asset_id, background_scale
	// and background_offset as `map` parameters while 0008_views.sql
	// carried the same three as columns, which is two stores for one
	// value and therefore a drift waiting to be found. The columns win
	// and the parameters are gone: only a column can carry
	// FOREIGN KEY (background_asset_id, project_id) into view_assets,
	// which is what refuses another game's image, and only a column can
	// carry ON DELETE SET NULL, which is what detaches every view's
	// background when the image is deleted. A uuid in a jsonb blob gets
	// neither, and a deleted asset would leave a dangling id in every map
	// view in the game.
	//
	// So this flag is what assets.go's SetBackground reads to apply this
	// file's first rule — a stored value no renderer reads is a lie a
	// designer will believe — to a value stored one table over.
	// TestOnlyARendererThatDrawsABackgroundAcceptsOne drives every
	// renderer in this catalogue through that setter, so the flag cannot
	// disagree with what the product actually does.
	ReadsBackground bool
}

// RendererReadsBackground answers whether a renderer draws a background
// image. An unknown name reads as false: CheckRenderer is what refuses a
// name this catalogue does not hold, and answering "yes" for a renderer
// that does not exist would let a background be stored under it.
func RendererReadsBackground(name string) bool {
	for _, r := range renderers {
		if r.Name == name {
			return r.ReadsBackground
		}
	}
	return false
}

// movedParams names the parameters this catalogue used to declare and
// where they went, so that the refusal is a direction rather than a dead
// end.
//
// It changes the *wording* of a refusal and never the decision: a name
// in here is not a parameter of any renderer, so it is refused by the
// unknown-parameter arm exactly as any other misspelling is, and adding
// a name here cannot make one acceptable. That is the same ordering
// assets.go's svgLooking follows for the same reason.
//
// The three below moved from renderer_params to columns of the view (see
// Renderer.ReadsBackground). An agent working from the spec's own §5.1
// table, or from a saved document written before the change, will send
// one; being told "map takes coordinate_source, x_field, y_field, snap"
// and nothing else leaves it with no idea a background is still
// possible.
var movedParams = map[string]string{
	"background_asset_id": backgroundMoved,
	"background_scale":    backgroundMoved,
	"background_offset":   backgroundMoved,
}

const backgroundMoved = "is not a renderer parameter: a background image, its scale " +
	"and its offset are columns of the view and are written with views.set_background, " +
	"because a reference to a stored asset needs a foreign key to stay honest and a " +
	"value in renderer_params cannot have one"

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
		RequiresDoc: "nothing of the query on its own — a scatter of unconnected " +
			"nodes is a picture this renderer draws. edge_labels, when it is on, " +
			"needs at least one edges[] entry declaring label_from.",
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
		RequiresDoc: "at least one edges[] entry: the layers are the edges, and a " +
			"query that draws none is one layer. rank_by naming a field rather " +
			`than "edges" needs a number field every entity type in scope declares ` +
			"and project.fields carries.",
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
		RequiresDoc: "at least one edges[] entry drawing contain_via's relation " +
			`type, named either by "via" or by a from_step whose step walks it: ` +
			"the nesting is that relation, and without it the picture is one flat " +
			"row of boxes.",
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
		Name:            RendererMap,
		Consumes:        "nodes, edges optional, coordinates required",
		ReadsBackground: true,
		// No plan reference in a description an agent reads: "Task 13" is
		// a heading in a document no caller has, and it reached the wire
		// inside the generated tool description — 6447 characters of it,
		// carrying that string. It came from Task 10 and survived this
		// task's own rewrite of the sentence around it.
		Doc: "Nodes at coordinates, optionally over a background image. The " +
			"coordinates are either the ones designers dragged and saved with " +
			"views.set_positions, or two number fields the game declares. The background " +
			"image is not a renderer parameter: it is a column of the view, " +
			"written with views.set_background, because it is a reference to a " +
			"stored asset and only a foreign key can keep that reference honest.",
		Params: []RendererParam{
			{Name: "coordinate_source", Kind: kindEnum, Values: []string{"manual", "fields"},
				Doc: `"manual" reads the positions designers dragged, "fields" reads ` +
					"two declared number fields off each node"},
			{Name: "x_field", Kind: kindNumberField,
				Doc: `the number field holding x, in "fields" mode`},
			{Name: "y_field", Kind: kindNumberField,
				Doc: `the number field holding y, in "fields" mode`},
			{Name: "snap", Kind: kindNumber,
				Doc: "grid size a dragged node snaps to; 0 for no grid"},
		},
		RequiresDoc: `in "fields" mode, both x_field and y_field, each naming a ` +
			"number field every entity type in scope declares and project.fields " +
			`carries; in "manual" mode — which is the default — neither, because ` +
			"nothing would read them; snap is the grid a dragged node lands on " +
			"and is read in manual mode only. A background image, its scale and " +
			"its offset are not parameters here and are set with " +
			"views.set_background, which applies the same rules to them.",
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
			// **snap is decided rather than left to the next reader**, the
			// way x_field's manual arm was. It is the grid a *dragged*
			// node lands on, and dragging is what manual mode reads; in
			// fields mode a node's coordinates come off its declared
			// fields, nothing is dragged, and a grid size changes no
			// picture. So it is refused there, and this sentence is why.
			if _, set := rc.params["snap"]; set && source == "fields" {
				problems = append(problems, rc.problem("snap",
					`is the grid a dragged node lands on and coordinate_source is `+
						`"fields", where a node's coordinates come off its declared `+
						`fields and nothing is dragged: remove it, or set `+
						`coordinate_source to "manual"`)...)
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
		RequiresDoc: "axis_field naming a number or enum field that every entity " +
			"type in scope declares the same way — the same type, and for an enum " +
			"the same options in the same order — and that project.fields carries. " +
			"axis_end_field, when given, has to land on that same axis.",
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
			// **The same hole one step along, and the answer is the same
			// answer.** Comparing the two ends' field *types* makes every
			// pair of enums one axis, so a race declaring
			// start_stage [heat, semi, final] and end_stage
			// [bronze, silver, gold] spans from "semi" to "gold", which
			// means nothing. An enum axis is its option sequence — the
			// rule requireDeclaredAs already applies across types — so
			// the two ends of one span are one axis exactly when their
			// options are the same list in the same order.
			startField, haveStart := rc.declaredFieldOf(start)
			endField, haveEnd := rc.declaredFieldOf(end)
			if haveStart && haveEnd && startField.Type == metamodel.FieldEnum &&
				!sameSequence(startField.Options, endField.Options) {
				return rc.problem("axis_end_field", fmt.Sprintf(
					"is an enum over [%s] and axis_field %q is an enum over [%s]: "+
						"those are two different axes, and a span from one to the "+
						"other has no length. Declare the same options in the same "+
						"order on both ends, or use number fields",
					strings.Join(endField.Options, ", "), start,
					strings.Join(startField.Options, ", ")))
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

// RendererParamKind is one parameter's declared kind, as the wire spells
// it, or false when this catalogue has no such parameter.
//
// It exists for a guard that lives outside this package:
// internal/web/static_render_test.go joins a renderer module's controls
// to the catalogue, and a control's *kind* is as much a part of that
// contract as its name is. The kind never reaches the generated
// description — that prints a phrase written for an agent — so the join
// cannot be made over the prose, and retyping the kinds in the test
// would be the third spelling this file exists to prevent.
func RendererParamKind(renderer, param string) (string, bool) {
	r, ok := rendererByName[renderer]
	if !ok {
		return "", false
	}
	for _, p := range r.Params {
		if p.Name == param {
			return string(p.Kind), true
		}
	}
	return "", false
}

// RendererDescription is the catalogue as prose, **generated from the
// table above and from nothing else**, so that views.upsert's tool
// description cannot drift from what CheckRenderer enforces. A hand-written sentence
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
		if r.RequiresDoc != "" {
			fmt.Fprintf(&b, "  requires: %s\n", r.RequiresDoc)
		}
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

// kindPhrases is how each kind reads in the description an agent is
// handed. The identifiers themselves are this package's own vocabulary and
// were being printed straight into agent-facing prose — "rank_by:
// rank_by.", "sort: column.", "x_field: number_field." — which names a Go
// constant at a reader who has never seen one and says nothing about what
// to send. kindEnum is the exception and prints its own admitted
// spellings instead.
// TestAParameterKindCannotBeHalfAdded requires a phrase for every kind and
// a kind for every phrase, the same both-arms guard the checkers get.
var kindPhrases = map[ParamKind]string{
	kindBool:         "true or false",
	kindNumber:       "a number",
	kindCount:        "a whole number of at least 1",
	kindText:         "a line of text",
	kindSlot:         "the name of a projection slot this query declares",
	kindNumberField:  "a declared number field key this query carries in project.fields",
	kindAxisField:    "a declared number or enum field key this query carries in project.fields",
	kindRankBy:       `either "edges" or a declared number field key this query carries`,
	kindRelationType: "the key of a relation type this game declares",
	kindColumn:       "one column reference: a built-in, a projection slot, or a carried field key",
	kindColumns:      "a list of column references, in the order they are drawn",
}

// kindPhrase is how a parameter's kind reads in the description. An enum
// prints its own values, so the admitted spellings are in the prose an
// agent reads rather than only in the refusal it gets afterwards.
func (p RendererParam) kindPhrase() string {
	if p.Kind == kindEnum {
		return fmt.Sprintf(`one of "%s"`, strings.Join(p.Values, `", "`))
	}
	if phrase, ok := kindPhrases[p.Kind]; ok {
		return phrase
	}
	// Unreachable while the guard holds, and this is what it would print
	// if it ever did not: the identifier, which is at least true.
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
	// nil: a renderer is checked at save time, against a query the caller
	// is writing now, so there is no stored dependency index to resolve a
	// renamed type by and nothing has moved under it yet.
	rc.scope = nodeScopeOf(r.Cat, r.Query, nil)

	var shape, requirements []metamodel.FieldError
	for given, value := range params {
		p, ok := renderer.param(given)
		if !ok {
			message := fmt.Sprintf("is not a parameter %q takes: it takes %s",
				renderer.Name, strings.Join(renderer.paramNames(), ", "))
			if moved, gone := movedParams[given]; gone {
				message = moved
			}
			shape = append(shape, metamodel.FieldError{
				Path: pointer("renderer_params", given), Message: message,
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
		for _, fault := range check(rc, p, value) {
			// A fault about one element of a list-valued parameter is
			// addressed at that element. pointerLess orders the indices
			// numerically, so /columns/10 comes back after /columns/2.
			path := pointer("renderer_params", given)
			if fault.index >= 0 {
				path = pointer("renderer_params", given, fault.index)
			}
			problem := metamodel.FieldError{Path: path, Message: fault.message}
			if fault.requirement {
				requirements = append(requirements, problem)
			} else {
				shape = append(shape, problem)
			}
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
	// st is the saved view's dependency index when this check is a *run*
	// of a stored view rather than a save, and nil when it is a save.
	//
	// A save has no recorded past — the caller is writing the document
	// now — so the type-naming parameters resolve by key, exactly as they
	// did before staleness existed. A run resolves them by id first,
	// through the same two methods every other reference in this package
	// goes through, which is what lets a renamed type keep its parameter
	// working and report the spelling instead of losing the type.
	st *staleness
}

// rendererPointer addresses one renderer parameter of a saved view.
//
// **It is not a pointer into the query document**, which every other
// pointer in this package is, and it does not have to be: a view's
// renderer and its parameters are stored beside the query rather than
// inside it, and CheckRenderer has always refused a bad parameter at this
// same address. A staleness diagnostic reported here is therefore
// addressed exactly where the repair is made.
func rendererPointer(p RendererParam) string {
	return pointer("renderer_params", p.Name)
}

// typeNamingKinds are the parameter kinds whose value *is* a declared
// type of this game, and therefore a dependency of the view exactly as a
// type named in the query is: deleting the type breaks the view, and a
// rename has to be carried by an id rather than by the spelling.
//
// It is a map from kind to the ref kind it records, rather than a switch
// inside the two functions that need it, because those two — the refs a
// save writes and the resolution a run performs — must agree about which
// parameters are references. A kind added to one and not the other is a
// parameter whose type deletion reports nothing, which is the defect
// Task 12's finding 2 already fixed once for @type operands.
// TestEveryTypeNamingParameterKindIsARecordedReference asserts the
// membership against the checkers that consult the catalogue.
var typeNamingKinds = map[ParamKind]string{
	kindRelationType: KindRelationType,
}

// rendererTypeRefs is the dependency index a view's renderer parameters
// contribute, written beside the query's own by the same transaction.
//
// It is read for its ids: without a row here, a run of a saved view whose
// relation type was renamed resolves the parameter by key, finds nothing,
// and reports the type *missing* — refusing a view whose picture the
// rename did not change. With one, the id resolves and the run reports
// the spelling, which is what the query's own references have always
// done.
//
// It is called after CheckRenderer, so every parameter it looks at has
// already been judged: a key that resolves to nothing here is a parameter
// the check refused, and the view is not being written at all.
func rendererTypeRefs(name string, params map[string]any, cat *Catalogue) []TypeRef {
	renderer, ok := rendererByName[name]
	if !ok || cat == nil {
		return nil
	}
	var refs []TypeRef
	for _, p := range renderer.Params {
		if typeNamingKinds[p.Kind] != KindRelationType {
			continue
		}
		key, ok := params[p.Name].(string)
		if !ok {
			continue
		}
		row, ok := cat.RelationTypes[strings.ToLower(key)]
		if !ok {
			continue
		}
		id := row.ID
		refs = append(refs, TypeRef{
			Kind: KindRelationType, Key: key, ID: &id, Pointer: rendererPointer(p),
		})
	}
	return refs
}

// rendererStaleness resolves a saved view's renderer parameters against
// the game as it stands now, and answers with the pointers a run cannot
// act on.
//
// **This is the fourth position that turns a key into a declared thing**,
// after the closures resolveInto hands itself, the projection's own scope
// and an edges[] entry's inherited relation types — and, until this, the
// one position none of them covered. CheckRenderer was called only from
// the upsert, so a run said nothing about a parameter whose type or field
// had moved, and the rename diagnostic's own repair instruction —
// re-save with the new spelling — was refused at a pointer the designer
// had never been told about, with advice that would recreate the type
// they had just renamed away from.
//
// It runs **the same checkers** the save runs, over the run's catalogue
// and this view's dependency index, and keeps only the faults that carry
// a diagnostic code. That is what stops it being a second implementation
// of the rules: a shape fault, a slot this query does not declare and a
// key `project.fields` does not carry are all real refusals of a save and
// none of them is something the game did, so a run reports none of them.
// The rename diagnostics are emitted by the resolution itself, inside
// kindRelationType, exactly as every other position emits its own.
func rendererStaleness(st *staleness, r *Resolved, name string,
	params map[string]any,
) []string {
	renderer, ok := rendererByName[name]
	if !ok || r == nil || r.Cat == nil || r.Query == nil {
		return nil
	}
	rc := &rendererCheck{renderer: renderer, params: params, r: r, st: st}
	rc.scope = nodeScopeOf(r.Cat, r.Query, st)
	var broken []string
	// Over the renderer's declared parameters rather than over the map,
	// so the order a run reports two stale parameters in is the order the
	// catalogue declares them and not Go's map iteration.
	for _, p := range renderer.Params {
		value, given := params[p.Name]
		if !given {
			continue
		}
		check, ok := paramCheckers[p.Kind]
		if !ok {
			// CheckRenderer refuses this loudly at save time, so a stored
			// view cannot hold one. A run says nothing rather than
			// failing over a parameter it cannot judge.
			continue
		}
		at := rendererPointer(p)
		for _, fault := range check(rc, p, value) {
			if fault.code == "" {
				continue
			}
			was := p.Name
			if key, ok := value.(string); ok {
				was = key
			}
			st.note(fault.code, at, was, fault.now)
			broken = append(broken, at)
		}
	}
	return broken
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

// declaredFieldOf is declaredTypesOf's companion for the checks that need
// more of a declaration than its type — an enum's option sequence. It
// returns the first declaration in scope order, which is the whole
// declaration when requireDeclaredAs has already agreed the scope declares
// the key one way, and it draws the same open/unresolved line for the same
// reason.
func (rc *rendererCheck) declaredFieldOf(key string) (metamodel.Field, bool) {
	if rc.scope.open || rc.scope.unresolved {
		return metamodel.Field{}, false
	}
	for _, schema := range rc.scope.schemas {
		for i := range schema {
			if schema[i].Key == key {
				return schema[i], true
			}
		}
	}
	return metamodel.Field{}, false
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
	// index addresses one element of a list-valued parameter — one
	// column of `columns` — and is -1 for a fault about the parameter as
	// a whole. **A list is exactly where the index is the address**: an
	// agent told "/renderer_params/columns" over a table of twelve
	// columns has to re-read all twelve, and the pointer this package
	// addresses every other refusal with was the one thing that would
	// have saved it that.
	index       int
	message     string
	requirement bool
	// code and now are the staleness diagnostic this fault *is*, when it
	// is one, carried the same way scopeError carries its own: the
	// judgement is made once, where the rule lives, and the run-time pass
	// reads the code off it rather than deciding a second time from a
	// pointer or a message. A code of "" is a fault that is not staleness
	// — a shape, a slot this query does not declare, a field
	// `project.fields` does not carry — and a run reports none of those,
	// because none of them is something the game did.
	code string
	now  string
}

// paramFaults is what a checker returns: **every** fault it found, and
// not the first. A checker over a list would otherwise stop at the first
// bad element, against this package's standing rule that every problem of
// one call comes back at once — and, worse, the *class* of the answer
// would depend on which bad element happened to be written first.
type paramFaults []*paramFault

func badShape(format string, args ...any) paramFaults {
	return paramFaults{{index: -1, message: fmt.Sprintf(format, args...)}}
}

func unmet(format string, args ...any) paramFaults {
	return paramFaults{{index: -1, message: fmt.Sprintf(format, args...), requirement: true}}
}

// unmetStale is unmet for a requirement a *game* can stop meeting: the
// type or the field this parameter names moved. The refusal is the same
// one a save answers with; what it also carries is the diagnostic code a
// run reports it as.
func unmetStale(code, now, format string, args ...any) paramFaults {
	return paramFaults{{
		index: -1, message: fmt.Sprintf(format, args...), requirement: true,
		code: code, now: now,
	}}
}

// at readdresses these faults to element i of a list-valued parameter.
func (f paramFaults) at(i int) paramFaults {
	for _, fault := range f {
		fault.index = i
	}
	return f
}

// message is the first fault's prose, for the one caller that quotes
// another checker's refusal inside its own.
func (f paramFaults) message() string { return f[0].message }

// paramCheckers is the kind table: one checker per kind, and the reason
// the kinds are data rather than a switch.
// TestAParameterKindCannotBeHalfAdded asserts both directions — a kind
// used by a parameter and missing here would panic on the first value
// sent, and a checker no parameter uses is a kind somebody meant to
// declare.
var paramCheckers = map[ParamKind]func(rc *rendererCheck, p RendererParam, v any) paramFaults{
	kindBool: func(_ *rendererCheck, _ RendererParam, v any) paramFaults {
		if _, ok := v.(bool); !ok {
			return badShape("must be true or false, got %s", jsonTypeOf(v))
		}
		return nil
	},
	kindNumber: func(_ *rendererCheck, _ RendererParam, v any) paramFaults {
		if _, ok := numberOf(v); !ok {
			return badShape("must be a number, got %s", jsonTypeOf(v))
		}
		return nil
	},
	kindCount: func(_ *rendererCheck, _ RendererParam, v any) paramFaults {
		n, ok := numberOf(v)
		if !ok {
			return badShape("must be a whole number of at least 1, got %s", jsonTypeOf(v))
		}
		if n != math.Trunc(n) || n < 1 {
			return badShape("must be a whole number of at least 1, got %v", v)
		}
		return nil
	},
	kindText: func(_ *rendererCheck, _ RendererParam, v any) paramFaults {
		s, ok := v.(string)
		if !ok {
			return badShape("must be a string, got %s", jsonTypeOf(v))
		}
		// Bytes, and it says bytes: MaxStringLen is a byte bound and
		// query.go's message over the same constant reads the same way.
		if len(s) > MaxStringLen {
			return badShape("must be at most %d bytes, and this one is %d", MaxStringLen, len(s))
		}
		return nil
	},
	kindEnum: func(_ *rendererCheck, p RendererParam, v any) paramFaults {
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
	kindSlot: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
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
	kindNumberField: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return fault
		}
		return rc.requireFieldKey(key, metamodel.FieldNumber)
	},
	kindAxisField: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return fault
		}
		return rc.requireFieldKey(key, metamodel.FieldNumber, metamodel.FieldEnum)
	},
	kindRankBy: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
		s, ok := v.(string)
		if !ok {
			return badShape(`must be "edges" or a number field key, got %s`, jsonTypeOf(v))
		}
		if s == "edges" {
			return nil
		}
		key, fault := fieldKeyValue(v)
		if fault != nil {
			return badShape(`must be "edges" or a number field key: %s`, fault.message())
		}
		return rc.requireFieldKey(key, metamodel.FieldNumber)
	},
	kindRelationType: func(rc *rendererCheck, p RendererParam, v any) paramFaults {
		key, ok := v.(string)
		if !ok {
			return badShape("must name a relation type, got %s", jsonTypeOf(v))
		}
		// The same id-then-key road every other reference in this package
		// takes. On a save rc.st is nil and this is the plain catalogue
		// lookup it has always been; on a run of a saved view the stored
		// reference resolves a renamed type by its id and reports the
		// spelling at this parameter's own pointer, so the run keeps
		// working and the designer is told both positions to repair.
		if _, ok := rc.st.relationTypeAt(rc.r.Cat, rendererPointer(p), key, true); !ok {
			return unmetStale(DiagRelationTypeMissing, "",
				"no relation type %q in this game: declare it with "+
					"relation_types.upsert, or use relation_types.list to see what this "+
					"game has", key)
		}
		return nil
	},
	kindColumn: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
		return rc.column(v)
	},
	kindColumns: func(rc *rendererCheck, _ RendererParam, v any) paramFaults {
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
		// **Every column's fault, each addressed by its own index.**
		// Stopping at the first was three defects in one line: an agent
		// with two wrong columns learned about one, the pointer named the
		// list rather than the element that was wrong, and — worst — the
		// *code* of the whole call depended on which of the two happened
		// to be written first, because a shape fault and a requirement
		// fault cannot both travel and the loop returned whichever it
		// reached. The class of an answer must not depend on the order an
		// agent typed its columns in; CheckRenderer's own shape-wins rule
		// is what arbitrates, and it can only do that if it is handed
		// both.
		var faults paramFaults
		for i, item := range list {
			faults = append(faults, rc.column(item).at(i)...)
		}
		return faults
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
func (rc *rendererCheck) column(v any) paramFaults {
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
	return rc.requireCarried(name)
}

// requireCarried is the *saved query* half of every parameter that names
// a declared field key, and it is one function because the rule is one
// rule.
//
// **A node comes back with its identity and the projection's attrs, and
// with declared fields only when a run asks for include_fields.**
// include_fields is a per-run option (RunRequest) and a saved view cannot
// turn it on, so a parameter naming a declared field key is legal exactly
// when project.fields asked for that key. Anything else is read from a
// member the envelope does not carry.
//
// It lives here rather than inside column() because column() was where
// the rule was first written and the *only* place it was enforced:
// axis_field, axis_end_field, x_field, y_field and rank_by all checked
// the schema and never the projection, so a timeline saved with an axis
// the run would not carry drew every node at the origin and a map in
// fields mode had no coordinates at all — the exact failure this file
// exists to refuse, reached through five parameters that had each been
// written as if the rule were column()'s alone. Duplication is why it
// drifted, so there is now one copy and six callers.
func (rc *rendererCheck) requireCarried(key string) paramFaults {
	for _, carried := range rc.r.Projection.Fields {
		if carried == key {
			return nil
		}
	}
	return unmet("names the field %q and this query does not carry it: add it to "+
		"project.fields. A run's include_fields cannot answer for a saved view, "+
		"because it is a per-run option and this view is saved without one", key)
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
func fieldKeyValue(v any) (string, paramFaults) {
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

// requireFieldKey is the whole requirement half of a parameter that names
// a declared field key, and every such parameter goes through it: the key
// has to be usably declared where this query draws, **and** the saved
// query has to carry it.
//
// The second half was the one that went missing. column() had it and no
// other field-key parameter did, so a timeline could be saved with an
// axis_field the run would never carry — every node at the origin — and a
// map in fields mode with neither coordinate. Two halves of one rule in
// one function is what stops that happening a third time.
func (rc *rendererCheck) requireFieldKey(key string, admitted ...metamodel.FieldType) paramFaults {
	if faults := rc.requireDeclaredAs(key, admitted...); faults != nil {
		return faults
	}
	return rc.requireCarried(key)
}

// requireDeclaredAs is the schema half of a field-key parameter: the key
// has to be declared on **every** type this query draws, and every one of
// them has to declare it the same, usable way.
//
// The failures it refuses are all one failure — the silent-empty picture
// this language refuses everywhere else — reached by four routes:
//
//   - A key nothing in scope declares is a typo, answered with an axis
//     every node sits at the origin of.
//   - A key some types declare and others do not is the same picture for
//     the types that do not: the quests are placed and the regions
//     vanish, and what is left looks right. **Declaring the key nowhere
//     is the rarer mistake**; drawing two types and remembering only one
//     of them is the common one, and this arm is the one that catches it.
//   - A key declared `number` on quests and `enum` on regions passes "is
//     it number or enum" while having no single axis to draw at all.
//   - An enum declared with the same options in another order is a second
//     axis wearing the first one's name, because an enum axis *is* its
//     option sequence.
//
// This is deliberately stricter than projectionScope.declares, which
// admits a key declared on at least one type in scope: a projection slot
// that finds nothing on some nodes leaves those nodes without an
// attribute, which a renderer can draw honestly, and an axis that finds
// nothing has nowhere to put them. That sentence is the whole reason the
// "declared on every type" arm exists, and for one round it was a
// sentence the code did not keep.
func (rc *rendererCheck) requireDeclaredAs(key string, admitted ...metamodel.FieldType) paramFaults {
	if rc.scope.unresolved {
		// The missing type is already a reported problem, and adding "and
		// its fields are not declared" to it is one refusal twice.
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
				return unmetStale(DiagFieldTypeChanged, string(schema[j].Type),
					"is declared %s on %s and this parameter needs %s: "+
						"a node whose value for it is not one of those cannot be placed, "+
						"and would be drawn at the origin or not at all",
					schema[j].Type, on, joinTypes(admitted))
			}
			// An enum with no options is an axis with no order and no
			// admitted value, so every node on it is unplaceable.
			// metamodel.Schema.Validate refuses one at upsert, which is
			// what made this rule *unreachable* through the API rather
			// than true: nothing revalidates a schema on the way back out
			// of the database, so a row written straight into the schema
			// column loads and is accepted. One arm here makes the rule
			// hold on the read side as well.
			// TestAnOptionlessEnumIsNoAxisEvenIfTheSchemaColumnHoldsOne
			// pins it.
			if schema[j].Type == metamodel.FieldEnum && len(schema[j].Options) == 0 {
				return unmetStale(DiagFieldTypeChanged, string(metamodel.FieldEnum),
					"is an enum declared on %s with no options: an enum axis "+
						"is ordered by its options, so one with none is an axis with no "+
						"order and no place to put a node. Declare its options with "+
						"types.upsert", on)
			}
			// Two types declaring the key differently are two different
			// axes, and a picture drawn over both would place a node by
			// whichever type it happens to have. This is the failure one
			// step along from the one above: number and enum are each
			// admitted, and a scope holding one of each passes the check
			// above without agreeing on anything.
			if firstOn != "" && firstField.Type != schema[j].Type {
				return unmetStale(DiagFieldTypeChanged, "",
					"is declared %s on %s and %s on %s: those are two "+
						"different axes, and a node would be placed by whichever type it "+
						"happens to have",
					firstField.Type, firstOn, schema[j].Type, on)
			}
			if firstOn != "" && schema[j].Type == metamodel.FieldEnum &&
				!sameSequence(firstField.Options, schema[j].Options) {
				return unmetStale(DiagFieldTypeChanged, "",
					"is an enum declared [%s] on %s and [%s] on %s: an enum "+
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
	if rc.scope.open {
		// A traverse step with no to_type reaches entities of any type,
		// so no schema covers what this query draws and the two arms
		// below — "declared nowhere" and "declared on some" — would
		// refuse a key the untyped step was written to reach. The
		// permission costs what projectionScope.open records it costing.
		//
		// **It stops here and not one line earlier.** The loop above has
		// already run, so a key a *named* type declares unusably is still
		// refused: `tags` is list<text> on `quest`, `quest` is written
		// right there in `from`, and "nothing can judge it" was never
		// true of that. The projection's justification for the wider
		// permission — a node carrying no attribute is still drawable —
		// does not transfer to an axis, which has nowhere to put it.
		return nil
	}
	switch {
	case len(declared) == 0:
		return unmetStale(DiagFieldMissing, "",
			"no field %q is declared on %s (%s): use types.get to see a "+
				"type's field_schema", key, rc.scope.subject, strings.Join(rc.scope.names, ", "))
	case len(declared) < len(rc.scope.names):
		return unmetStale(DiagFieldMissing, "",
			"is declared on %s and not on %s, and this query draws all of "+
				"them: a node of a type that does not declare %q has no place on this "+
				"axis and would be drawn at the origin or not at all. Narrow the query "+
				"to the types that declare it, or name a field all of them do",
			strings.Join(declared, ", "), strings.Join(missing(rc.scope.names, declared), ", "),
			key)
	}
	return nil
}

// missing lists the names of all that are not in declared, in scope order,
// so a refusal names the types to fix rather than the types that are fine.
func missing(all, declared []string) []string {
	var out []string
	for _, name := range all {
		found := false
		for _, on := range declared {
			found = found || on == name
		}
		if !found {
			out = append(out, name)
		}
	}
	return out
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
