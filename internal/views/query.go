package views

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/neverbot/maestro/internal/metamodel"
)

// The bounds a query document obeys before anything else looks at it.
// Every one of them is a refusal, never a clamp: a clamp would answer a
// question the agent did not ask, and a saved view is a thing a designer
// will trust. The one place this sub-project clamps rather than refuses
// is a *page limit* on the saved-view listing (Task 11), which is a
// caller's opinion about response size rather than about content.
const (
	// MaxQueryBytes bounds the whole document. 64 KiB is roughly two
	// hundred times the largest worked example in the spec, and it is
	// here so that an accidental paste of a game's content into a query
	// is refused before json.Decoder allocates it.
	// TestAnOversizeDocumentIsRefusedBeforeItIsParsed pins it.
	MaxQueryBytes = 64 << 10

	// MaxSteps is the spec's "CTE steps per query" cap (§4.3). Each step
	// becomes a CTE, and a query with nine of them is one whose planner
	// cost nobody has looked at.
	MaxSteps = 8

	// MaxSelectors bounds `from`. A seed set per entity type in a large
	// game is a plausible query; sixteen is above any of them.
	MaxSelectors = 16

	// MaxKeys bounds a selector's `keys` shortcut, which compiles to an
	// `= ANY($n)` over a uuid array.
	MaxKeys = 500

	// MaxPredicateNodes and MaxPredicateDepth bound one predicate tree.
	// Without the depth bound a deeply nested `not` is a stack overflow
	// in the walker before it is a bad query. Task 3 is what applies
	// them; they live here with the other document bounds.
	MaxPredicateNodes = 200
	MaxPredicateDepth = 8

	// MaxValueList bounds the right-hand side of `in`, `contains_any` and
	// `contains_all`. Task 3 applies it.
	MaxValueList = 500

	// MaxValueDepth bounds how deeply a predicate's `value` nests, and it
	// is a separate bound from MaxPredicateDepth on purpose: that one
	// bounds the predicate *tree*, and a `value` is an `any` the tree
	// bound never looks inside. Two is the deepest a legal value goes — a
	// list of scalars, or a {"param": "…"} reference — so anything deeper
	// is a shape this language has no meaning for.
	MaxValueDepth = 2

	// MaxStringLen bounds every individual string anywhere in the
	// document that is not covered by a narrower rule (a type key is 64
	// by metamodel.RowKeyProblems; a field key is 64 by Task 3's
	// fieldKeyProblems). It is what stops a megabyte of text arriving as
	// one enum value. TestAnOverlongStringIsRefusedWhereverItSits pins it.
	MaxStringLen = 4096

	// MaxParams bounds `params`.
	MaxParams = 16
)

// Direction spellings. Three, not the metamodel's two: a traversal that
// follows an edge either way is a real question ("what is this room
// connected to"), and the metamodel's one-hop listing deliberately has no
// "both" only because a paged listing cannot order the union stably.
const (
	DirectionOut = "out"
	DirectionIn  = "in"
	DirectionAny = "any"
)

// Query is a parsed, bounded, still-unresolved query document. Nothing in
// it has been looked up against a game yet: that is Task 4's job, and
// keeping the two apart is what lets views.validate answer without a
// round trip to content the caller may not have written yet.
type Query struct {
	V              int         `json:"v"`
	Params         []ParamDecl `json:"params,omitempty"`
	From           []Selector  `json:"from"`
	Traverse       []Step      `json:"traverse,omitempty"`
	Nodes          []NodeSet   `json:"nodes,omitempty"`
	Edges          []EdgeSpec  `json:"edges,omitempty"`
	Project        *Projection `json:"project,omitempty"`
	Limits         *Limits     `json:"limits,omitempty"`
	IncludeInvalid bool        `json:"include_invalid,omitempty"`
}

// ParamDecl is one declared parameter. Type is one of the metamodel's
// scalar field types — text, number, bool — and is what a value bound at
// run time is checked against, and what the operator that consumes the
// param is checked against at save time.
type ParamDecl struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Default any    `json:"default,omitempty"`
}

// Selector is one seed set.
type Selector struct {
	Type  string     `json:"type"`
	As    string     `json:"as,omitempty"`
	Keys  []string   `json:"keys,omitempty"`
	Where *Predicate `json:"where,omitempty"`
}

// Step is one traversal.
type Step struct {
	From      string     `json:"from"`
	Via       Strings    `json:"via"`
	Direction string     `json:"direction,omitempty"`
	Depth     *Depth     `json:"depth,omitempty"`
	ToType    Strings    `json:"to_type,omitempty"`
	Where     *Predicate `json:"where,omitempty"`
	EdgeWhere *Predicate `json:"edge_where,omitempty"`
	As        string     `json:"as,omitempty"`
}

// NodeSet names a set to draw, and optionally the role a renderer should
// give it ("seed" anchors a graph).
type NodeSet struct {
	Set  string `json:"set"`
	Role string `json:"role,omitempty"`
}

// EdgeSpec is one of two things, and exactly one: the relations a step
// actually walked (FromStep), or relations of a type drawn *between* nodes
// already in the result (Via + Between), which is how a prerequisite
// chain appears inside an otherwise level-filtered set without dragging
// in the entities outside the filter.
type EdgeSpec struct {
	FromStep  string   `json:"from_step,omitempty"`
	Via       Strings  `json:"via,omitempty"`
	Between   []string `json:"between,omitempty"`
	Direction string   `json:"direction,omitempty"`
	LabelFrom string   `json:"label_from,omitempty"`
}

// Projection is how a node presents itself. Every field is an attribute
// reference (§2.4): a declared field key, an @-sigil built-in, or a
// one-hop related attribute.
type Projection struct {
	Label   *AttrRef `json:"label,omitempty"`
	ColorBy *AttrRef `json:"color_by,omitempty"`
	GroupBy *AttrRef `json:"group_by,omitempty"`
	SizeBy  *AttrRef `json:"size_by,omitempty"`
	SortBy  *AttrRef `json:"sort_by,omitempty"`
	Fields  []string `json:"fields,omitempty"`
}

// Limits are per-query overrides of the defaults, always judged against
// the hard caps. A query asking for more than a cap is refused at save
// time with limit_exceeded, not clamped: an agent that asked for depth 20
// should learn that it cannot have it. Task 3's checkLimits is what
// applies the caps.
type Limits struct {
	MaxDepth *int `json:"max_depth,omitempty"`
	MaxNodes *int `json:"max_nodes,omitempty"`
	MaxEdges *int `json:"max_edges,omitempty"`
}

// Depth is `1`, an integer, or {"min":1,"max":4}, normalised to the pair
// at parse time so that every later stage reads one shape.
type Depth struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// UnmarshalJSON accepts both spellings. A bare integer n means
// {min: 1, max: n}: the seed is depth 0 and one hop is depth 1, so the
// natural reading of "depth 3" is "up to three hops", not "exactly three".
// TestDepthTakesBothSpellings pins both.
func (d *Depth) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] != '{' {
		var n int
		if err := json.Unmarshal(trimmed, &n); err != nil {
			return fmt.Errorf("must be an integer or an object with min and max")
		}
		d.Min, d.Max = 1, n
		return nil
	}
	var obj struct {
		Min *int `json:"min"`
		Max *int `json:"max"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("must be an integer or an object with min and max")
	}
	d.Min, d.Max = 1, 1
	if obj.Min != nil {
		d.Min = *obj.Min
	}
	if obj.Max != nil {
		d.Max = *obj.Max
	}
	return nil
}

// Strings is a scalar-or-list: "requires" and ["requires","unlocks"] both
// decode to the same slice, because a union of one is the common case and
// making an agent write a one-element array for it is friction with no
// payoff. TestViaTakesBothSpellings pins both.
type Strings []string

func (s *Strings) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return fmt.Errorf("must be a string or a list of strings")
		}
		*s = list
		return nil
	}
	var one string
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return fmt.Errorf("must be a string or a list of strings")
	}
	*s = Strings{one}
	return nil
}

// ParseQuery decodes, bounds and structurally validates a query document.
func ParseQuery(raw []byte) (*Query, error) {
	if len(raw) > MaxQueryBytes {
		return nil, invalidQuery("", fmt.Sprintf(
			"is too large (%d bytes, the most a query takes is %d): a query names types and "+
				"fields, so a document this size is content that belongs in entities",
			len(raw), MaxQueryBytes))
	}
	// The encoding is judged on the bytes the caller sent, before
	// encoding/json sees them, and that ordering is the whole point of
	// this check rather than a style: **encoding/json substitutes U+FFFD
	// for every byte that does not decode**, so an invalid sequence
	// arrives at the far side of Decode as a valid string that is no
	// longer what the caller wrote. Checking after the decode would
	// therefore pass a document Postgres would later refuse with SQLSTATE
	// 22021 over the caller's own bytes — the failure
	// metamodel.CheckText's own comment records — and would silently
	// change authored content on the way. It is whole-document rather
	// than per-string for the same reason: after the decode the evidence
	// is gone. TestAnInvalidUTF8ByteIsRefusedBeforeItBecomesAReplacementCharacter
	// pins it, control characters and lengths being bounded per string by
	// checkAllText below.
	if !utf8.Valid(raw) {
		return nil, invalidQuery("", "is not valid UTF-8: a byte in it does not decode as any "+
			"character, and a JSON decoder would silently replace it rather than refuse it, so "+
			"the query stored would not be the query written")
	}
	var q Query
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		return nil, invalidQuery("", decodeProblem(err))
	}
	if dec.More() {
		return nil, invalidQuery("", "carries more than one JSON document")
	}
	problems := checkQuery(&q)
	if len(problems) > 0 {
		sort.SliceStable(problems, func(i, j int) bool {
			return pointerLess(problems[i].Path, problems[j].Path)
		})
		return nil, invalidQueryProblems(problems)
	}
	// The limits are judged in their own pass because they answer with
	// their own code: a limit above its cap is limit_exceeded, and a
	// []metamodel.FieldError cannot carry a second code alongside
	// query_invalid. It runs after the structural pass, so a document that
	// is both malformed and over-cap reports the malformation first —
	// limit_exceeded's advice is "lower this number", which is only
	// actionable on a document that is otherwise well formed.
	if err := checkLimits(q.Limits); err != nil {
		return nil, err
	}
	applyDefaults(&q)
	return &q, nil
}

// decodeProblem turns encoding/json's own wording into this package's.
// The one shape worth translating is the unknown-field error, because it
// is the extension-point reservation this plan's open question O8 leans
// on and an agent must be able to tell it from a syntax error.
func decodeProblem(err error) string {
	msg := err.Error()
	if _, name, found := strings.Cut(msg, "unknown field "); found {
		return fmt.Sprintf("has an unknown key %s: this version of the query language is v1, "+
			"and a key it does not know is refused rather than ignored so that a typo is not "+
			"silently a different query", strings.TrimSpace(name))
	}
	return "is not a JSON object this query language recognises: " + msg
}

// applyDefaults fills the defaults every later stage may then assume.
// It runs only after checkQuery has passed, so it never defaults its way
// past a problem: a selector with no type gets no name.
func applyDefaults(q *Query) {
	for i := range q.From {
		if q.From[i].As == "" {
			q.From[i].As = q.From[i].Type
		}
		q.From[i].Where.normalise()
	}
	for i := range q.Traverse {
		if q.Traverse[i].Direction == "" {
			q.Traverse[i].Direction = DirectionOut
		}
		if q.Traverse[i].Depth == nil {
			q.Traverse[i].Depth = &Depth{Min: 1, Max: 1}
		}
		q.Traverse[i].Where.normalise()
		q.Traverse[i].EdgeWhere.normalise()
	}
	// An edge entry defaults its direction the way a step does. It used
	// to be the one place an empty direction was read as "out" further
	// down instead, which made the same empty string a refusal in a step
	// and a silent default in an edge.
	for i := range q.Edges {
		if q.Edges[i].Direction == "" {
			q.Edges[i].Direction = DirectionOut
		}
	}
	if len(q.Nodes) == 0 {
		for _, name := range declaredSets(q) {
			q.Nodes = append(q.Nodes, NodeSet{Set: name})
		}
	}
	if q.Project == nil {
		q.Project = &Projection{}
	}
	if q.Project.Label == nil {
		q.Project.Label = &AttrRef{Attr: AttrName}
	}
	// A one-hop related attribute defaults its direction and what it
	// reads, in the one place every other default is filled. `out` is the
	// direction a step and an edge entry default to; @name is what
	// "coloured by zone" means, and a hop that named a type but no attr
	// wants the far entity's name rather than an error. Defaulting them
	// further down instead is what made an empty direction a refusal in
	// one position and a silent "out" in another.
	for _, attr := range projectionAttrs {
		ref := attr.Of(q.Project)
		if ref == nil || ref.Related == nil {
			continue
		}
		if ref.Related.Direction == "" {
			ref.Related.Direction = DirectionOut
		}
		if ref.Related.Attr == "" {
			ref.Related.Attr = AttrName
		}
	}
}

// declaredSets lists every set name in declaration order: the selectors
// first, then the steps.
func declaredSets(q *Query) []string {
	names := make([]string, 0, len(q.From)+len(q.Traverse))
	for _, sel := range q.From {
		name := sel.As
		if name == "" {
			name = sel.Type
		}
		names = append(names, name)
	}
	for _, step := range q.Traverse {
		if step.As != "" {
			names = append(names, step.As)
		}
	}
	return names
}

// checkQuery is the whole structural pass, collecting rather than
// returning on the first problem.
func checkQuery(q *Query) []metamodel.FieldError {
	var problems []metamodel.FieldError
	add := func(ptr, message string) {
		problems = append(problems, metamodel.FieldError{Path: ptr, Message: message})
	}

	// Every string in the document, bounded before anything reads any of
	// them for meaning. See checkAllText: it walks, it does not enumerate.
	problems = append(problems, checkAllText(q)...)

	if q.V != 1 {
		add(pointer("v"), fmt.Sprintf(
			"must be 1: this is the only version of the query language, and every stored "+
				"query carries it so the language can change without rewriting authored content "+
				"(got %d)", q.V))
	}
	if len(q.From) == 0 {
		add(pointer("from"), "must name at least one entity type to start from: a query with "+
			"no seed set selects nothing, and an empty picture is indistinguishable from a "+
			"correct one")
	}
	if len(q.From) > MaxSelectors {
		add(pointer("from"), fmt.Sprintf("has %d seed selectors, and a query takes at most %d",
			len(q.From), MaxSelectors))
	}
	if len(q.Traverse) > MaxSteps {
		add(pointer("traverse"), fmt.Sprintf(
			"has %d steps, and a query takes at most %d traversal steps: each one becomes a "+
				"CTE, and beyond this the planner's cost is nobody's estimate",
			len(q.Traverse), MaxSteps))
	}
	if len(q.Params) > MaxParams {
		add(pointer("params"), fmt.Sprintf("declares %d parameters, and a query takes at most %d",
			len(q.Params), MaxParams))
	}

	seen := map[string]string{} // set name -> the pointer that declared it
	declare := func(ptr, name string) {
		if name == "" {
			return
		}
		if was, dup := seen[name]; dup {
			add(ptr, fmt.Sprintf("the set name %q is already used, at %s: a name addresses one "+
				"set, and a second use would make every later reference ambiguous", name, was))
			return
		}
		seen[name] = ptr
	}

	// A parameter key addresses one parameter, exactly as a set name
	// addresses one set. Two declarations of the same key are last-wins
	// everywhere that reads them — the type a predicate is checked
	// against, the default that is bound — so the document would mean one
	// thing and read as another. It is refused here for the same reason
	// and in the same shape as a duplicate set name.
	declaredParams := map[string]string{} // key -> the pointer that declared it
	for i, p := range q.Params {
		ptr := pointer("params", i)
		problems = append(problems, prefixed(ptr+"/key", metamodel.RowKeyProblems("key", p.Key))...)
		if p.Key != "" {
			if was, dup := declaredParams[p.Key]; dup {
				add(ptr+"/key", fmt.Sprintf("the parameter %q is already declared, at %s: a key "+
					"addresses one parameter, and a second declaration would silently replace "+
					"its type and its default", p.Key, was))
			} else {
				declaredParams[p.Key] = ptr + "/key"
			}
		}
		switch metamodel.FieldType(p.Type) {
		case metamodel.FieldText, metamodel.FieldNumber, metamodel.FieldBool:
		default:
			add(ptr+"/type", fmt.Sprintf(
				"must be %q, %q or %q: a parameter substitutes for one literal value, and the "+
					"three scalar types are the ones an operator takes (got %q)",
				metamodel.FieldText, metamodel.FieldNumber, metamodel.FieldBool, p.Type))
		}
		// A default is an `any`, exactly like a predicate's value, and the
		// storage argument for MaxValueDepth applies to it word for word:
		// nothing overflows, but an arbitrarily deep blob here becomes a
		// *stored* saved view that every later stage re-walks, and the
		// only refusal without this is encoding/json's, at ten thousand,
		// reported at pointer "" in the decoder's own wording. A declared
		// parameter is one of three scalars, so the bound refuses nothing
		// legal — it refuses a shape this language has no meaning for,
		// here rather than in the row that stores it. Task 4's coercion
		// is what then refuses a default that is not a scalar at all, and
		// what checks it against the declared type.
		if got := valueDepth(p.Default); got > MaxValueDepth {
			add(ptr+"/default", fmt.Sprintf(
				"is nested %d deep, and a value in this language is at most %d deep: a "+
					"parameter's default is one scalar of its declared type", got, MaxValueDepth))
		}
	}

	for i, sel := range q.From {
		ptr := pointer("from", i)
		problems = append(problems, prefixed(ptr+"/type",
			metamodel.RowKeyProblems("type", sel.Type))...)
		if sel.As != "" {
			problems = append(problems, prefixed(ptr+"/as",
				metamodel.RowKeyProblems("as", sel.As))...)
		}
		name := sel.As
		nameAt := ptr + "/as"
		if name == "" {
			name, nameAt = sel.Type, ptr+"/type"
		}
		declare(nameAt, name)
		if len(sel.Keys) > MaxKeys {
			add(ptr+"/keys", fmt.Sprintf("names %d keys, and a selector takes at most %d",
				len(sel.Keys), MaxKeys))
		}
		for j, key := range sel.Keys {
			problems = append(problems, prefixed(pointer("from", i, "keys", j),
				metamodel.RowKeyProblems("keys", key))...)
		}
		problems = append(problems, checkPredicate(sel.Where, ptr+"/where")...)
	}

	for i, step := range q.Traverse {
		ptr := pointer("traverse", i)
		if step.From == "" {
			add(ptr+"/from", "is required: a step reads from a set declared before it, and "+
				"there is deliberately no implicit previous step")
		} else if _, ok := seen[step.From]; !ok {
			add(ptr+"/from", fmt.Sprintf(
				"no set named %q is declared before this step: a step may only read a set that "+
					"an earlier selector or step named", step.From))
		}
		if len(step.Via) == 0 {
			add(ptr+"/via", "is required: a step walks along at least one relation type")
		}
		for j, key := range step.Via {
			problems = append(problems, prefixed(pointer("traverse", i, "via", j),
				metamodel.RowKeyProblems("via", key))...)
		}
		for j, key := range step.ToType {
			problems = append(problems, prefixed(pointer("traverse", i, "to_type", j),
				metamodel.RowKeyProblems("to_type", key))...)
		}
		switch step.Direction {
		case "", DirectionOut, DirectionIn, DirectionAny:
		default:
			add(ptr+"/direction", fmt.Sprintf(
				"must be %q, %q or %q, relative to the set this step reads from (got %q)",
				DirectionOut, DirectionIn, DirectionAny, step.Direction))
		}
		if step.Depth != nil {
			switch {
			// Both halves are checked against 1, not only Min: the scalar
			// spelling `"depth": 0` normalises to {min:1, max:0}, so a
			// Min-only check would report it as "min 1 is above max 0" —
			// an error about a number the caller never wrote.
			case step.Depth.Min < 1 || step.Depth.Max < 1:
				add(ptr+"/depth", fmt.Sprintf("must be at least 1: depth 0 is the set this step "+
					"already reads from (got min %d, max %d)", step.Depth.Min, step.Depth.Max))
			case step.Depth.Max < step.Depth.Min:
				add(ptr+"/depth", fmt.Sprintf("min %d is above max %d, so no depth is legal",
					step.Depth.Min, step.Depth.Max))
			}
		}
		if step.As != "" {
			problems = append(problems, prefixed(ptr+"/as",
				metamodel.RowKeyProblems("as", step.As))...)
		}
		declare(ptr+"/as", step.As)
		// Every *Predicate member of Step needs its own call here, and
		// TestEveryPredicateInAStepIsChecked reads the members off the
		// type so that a third added without one is caught: an unchecked
		// tree is an unbounded tree, stored in a saved view.
		problems = append(problems, checkPredicate(step.Where, ptr+"/where")...)
		problems = append(problems, checkPredicate(step.EdgeWhere, ptr+"/edge_where")...)
	}

	for i, n := range q.Nodes {
		if _, ok := seen[n.Set]; !ok {
			add(pointer("nodes", i, "set"), fmt.Sprintf("no set named %q is declared", n.Set))
		}
	}
	for i, e := range q.Edges {
		ptr := pointer("edges", i)
		switch {
		case e.FromStep != "" && (len(e.Via) > 0 || len(e.Between) > 0):
			add(ptr, `names both "from_step" and "via"/"between": an edge entry draws the `+
				`relations a step walked, or relations of a type between sets already in the `+
				`result, and never both`)
		case e.FromStep != "":
			if _, ok := seen[e.FromStep]; !ok {
				add(ptr+"/from_step", fmt.Sprintf("no set named %q is declared", e.FromStep))
			}
		case len(e.Via) > 0:
			if len(e.Between) != 2 {
				add(ptr+"/between", fmt.Sprintf(
					"must name exactly two sets, the source side and the target side (got %d)",
					len(e.Between)))
			}
			for j, name := range e.Between {
				if _, ok := seen[name]; !ok {
					add(pointer("edges", i, "between", j),
						fmt.Sprintf("no set named %q is declared", name))
				}
			}
			for j, key := range e.Via {
				problems = append(problems, prefixed(pointer("edges", i, "via", j),
					metamodel.RowKeyProblems("via", key))...)
			}
			switch e.Direction {
			case "", DirectionOut, DirectionIn, DirectionAny:
			default:
				add(ptr+"/direction", fmt.Sprintf("must be %q, %q or %q (got %q)",
					DirectionOut, DirectionIn, DirectionAny, e.Direction))
			}
			problems = append(problems, checkLabelFrom(e.LabelFrom, ptr+"/label_from")...)
		default:
			add(ptr, `must name either "from_step" or "via" with "between"`)
		}
	}

	problems = append(problems, checkProjection(q.Project)...)
	return problems
}

// prefixed re-addresses a set of problems produced by a helper that knows
// its own argument name but not where in the document it sits. It is what
// lets metamodel.RowKeyProblems — which reports at "type" — report at
// "/from/0/type" without a second copy of the key grammar living here.
func prefixed(ptr string, problems []metamodel.FieldError) []metamodel.FieldError {
	out := make([]metamodel.FieldError, 0, len(problems))
	for _, p := range problems {
		out = append(out, metamodel.FieldError{Path: ptr, Message: p.Message})
	}
	return out
}

// checkAllText bounds every string anywhere in a decoded query document:
// no control characters at all, and at most MaxStringLen bytes. The
// encoding itself is judged on the raw bytes in ParseQuery, before
// encoding/json can replace an invalid sequence with U+FFFD.
func checkAllText(v any) []metamodel.FieldError {
	var problems []metamodel.FieldError
	walkStrings(reflect.ValueOf(v), "", func(ptr, value string) {
		if len(value) > MaxStringLen {
			problems = append(problems, metamodel.FieldError{Path: ptr, Message: fmt.Sprintf(
				"must be at most %d bytes, and this one is %d", MaxStringLen, len(value))})
			return
		}
		fault, bad := metamodel.CheckText(value, "")
		switch {
		case !bad:
		case fault.InvalidUTF8:
			// Unreachable from ParseQuery, which refuses an invalid
			// sequence on the raw bytes and never gets here — but this
			// function is the package's one text bound and takes any
			// value, so the arm reports rather than silently classifying
			// an invalid string as clean.
			problems = append(problems, metamodel.FieldError{Path: ptr, Message: "is not valid UTF-8: " +
				"a byte in it does not decode as any character, and Postgres refuses that outright"})
		default:
			problems = append(problems, metamodel.FieldError{Path: ptr, Message: fmt.Sprintf(
				"holds a control character (%U at byte %d): every string in a query is an "+
					"identifier, an operator or one literal value, and all three are one line",
				fault.Rune, fault.Offset)})
		}
	})
	return problems
}

// walkStrings visits every string reachable from v, including strings
// inside `any` values and inside map keys and values, calling visit with
// the JSON pointer of each.
func walkStrings(v reflect.Value, ptr string, visit func(ptr, value string)) {
	if v.Kind() == reflect.Slice && v.Type() == rawMessageType && !v.IsNil() {
		var deferred any
		// Bytes rather than Interface: the latter panics on a value
		// reached through an unexported field, which the embedded case
		// below reaches on purpose.
		if err := json.Unmarshal(v.Bytes(), &deferred); err == nil {
			walkStrings(reflect.ValueOf(deferred), ptr, visit)
		}
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), ptr, visit)
		}
	case reflect.String:
		visit(ptr, v.String())
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), ptr+pointer(i), visit)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			// Formatted through the reflect.Value rather than through
			// Interface(), which panics on anything reached through an
			// unexported field — and an embedded unexported type is now
			// reached on purpose.
			name := key.String()
			if key.Kind() == reflect.String {
				visit(ptr+pointer(name), key.String())
			} else {
				name = fmt.Sprint(key)
			}
			walkStrings(v.MapIndex(key), ptr+pointer(name), visit)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			// The membership rules encoding/json itself applies: an
			// untagged anonymous struct field is promoted, and only a
			// non-anonymous unexported field is genuinely absent from the
			// document.
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			promoted := f.Anonymous && name == "" && ft.Kind() == reflect.Struct
			if !f.IsExported() && !promoted {
				continue
			}
			at := ptr + pointer(name)
			switch {
			case promoted, name == "-":
				at = ptr
			case name == "":
				at = ptr + pointer(f.Name)
			}
			walkStrings(v.Field(i), at, visit)
		}
	}
}

// rawMessageType is compared by identity rather than by assignability: a
// named type whose underlying type is []byte is base64 in a document,
// while a json.RawMessage is a document member the decode postponed.
var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// AttrName is the built-in attribute Task 2's applyDefaults needs: it
// labels a node with the entity's own name when the document said
// nothing. The rest of the @-sigil vocabulary, and the table giving each
// built-in the declared type its operators are judged against, live in
// predicate.go beside the operator table they are judged by.
const AttrName = "@name"

// FieldRef is a resolved-enough reference to something comparable: either
// a declared field key or a built-in.
type FieldRef struct {
	Key     string `json:"-"`
	Builtin bool   `json:"-"`
}

// Predicate is a boolean tree. Exactly one of the four shapes is set, and
// checkPredicate (predicate.go) refuses anything else: a document that
// set both `all` and `field` would otherwise have a meaning decided by
// whichever arm the compiler read first.
type Predicate struct {
	All []Predicate `json:"all,omitempty"`
	Any []Predicate `json:"any,omitempty"`
	Not *Predicate  `json:"not,omitempty"`

	Field string `json:"field,omitempty"`
	Op    string `json:"op,omitempty"`
	Value any    `json:"value,omitempty"`

	// FieldRef is filled by normalise, after checkPredicate has passed.
	FieldRef FieldRef `json:"-"`
}

// normalise fills FieldRef for this node and every node below it. It runs
// from applyDefaults, which runs only after checkQuery has passed, so it
// never has to decide what a malformed node means.
// TestEveryPredicateInAParsedQueryIsNormalised pins it over all three
// predicate positions and through all/any/not.
func (p *Predicate) normalise() {
	if p == nil {
		return
	}
	if p.Field != "" {
		p.FieldRef = FieldRef{Key: p.Field, Builtin: strings.HasPrefix(p.Field, "@")}
	}
	for i := range p.All {
		p.All[i].normalise()
	}
	for i := range p.Any {
		p.Any[i].normalise()
	}
	p.Not.normalise()
}

// AttrRef is an attribute reference: a field key, a built-in, or a
// one-hop related attribute.
type AttrRef struct {
	Attr    string  `json:"-"`
	Related *RelHop `json:"related,omitempty"`
}

// RelHop is the one-hop side of an AttrRef.
type RelHop struct {
	Via       string `json:"via"`
	Direction string `json:"direction,omitempty"`
	Type      string `json:"type,omitempty"`
	Attr      string `json:"attr,omitempty"`
}

func (a *AttrRef) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &a.Attr)
	}
	var obj struct {
		Related *RelHop `json:"related"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return err
	}
	a.Related = obj.Related
	return nil
}

func (a AttrRef) MarshalJSON() ([]byte, error) {
	if a.Related != nil {
		return json.Marshal(struct {
			Related *RelHop `json:"related"`
		}{a.Related})
	}
	return json.Marshal(a.Attr)
}
