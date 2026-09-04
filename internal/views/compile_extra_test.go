package views

import (
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// problemsOf returns the field problems of a refusal, failing the test if
// the error is not one of this package's own.
func problemsOf(t *testing.T, err error) []metamodel.FieldError {
	t.Helper()
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected a *QueryError, got %T: %v", err, err)
	}
	return qe.Fields
}

// oneProblem asserts a refusal carries exactly one problem, at the
// pointer given, whose message contains the fragment given. Every
// negative test in this package names the path *and* the message, never
// merely that an error happened.
func oneProblem(t *testing.T, err error, path, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal at %s", path)
	}
	fields := problemsOf(t, err)
	if len(fields) != 1 {
		t.Fatalf("expected one problem, got %d: %v", len(fields), fields)
	}
	if fields[0].Path != path {
		t.Fatalf("expected the problem at %s, got %s (%s)", path, fields[0].Path,
			fields[0].Message)
	}
	if !strings.Contains(fields[0].Message, contains) {
		t.Fatalf("expected a message mentioning %q, got %q", contains, fields[0].Message)
	}
}

func resolveOnly(t *testing.T, g *game, doc string) error {
	t.Helper()
	_, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t, doc))
	return err
}

// TestAnEdgePredicateAdmitsOnlyTheBuiltinsARelationHas is the refusal a
// relation's missing columns need. A relation has an id, a type, two
// endpoints, its declared fields and its timestamps — 0004_metamodel.sql
// — so @name, @key and @invalid compile to columns Postgres does not
// have, and without this refusal the whole view fails with a SQL error
// instead of a pointer.
func TestAnEdgePredicateAdmitsOnlyTheBuiltinsARelationHas(t *testing.T) {
	g, _ := newGame(t)
	for _, name := range []string{"@name", "@key", "@invalid"} {
		err := resolveOnly(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"traverse":[{"from":"q","via":"requires","as":"needs",
			  "edge_where":{"field":"`+name+`","op":"eq","value":"x"}}]}`)
		oneProblem(t, err, "/traverse/0/edge_where/field", "cannot be compared on a relation")
	}
	// The controls: the two built-ins a relation does carry, in the same
	// position, both of which must resolve *and* compile.
	for _, doc := range []string{
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"requires","as":"needs",
		    "edge_where":{"field":"@type","op":"eq","value":"requires"}}]}`,
		`{"v":1,"from":[{"type":"quest","as":"q"}],
		  "traverse":[{"from":"q","via":"requires","as":"needs",
		    "edge_where":{"field":"@created_at","op":"lt","value":"2030-01-01T00:00:00Z"}}]}`,
	} {
		if _, _ = compileOf(t, g, doc); t.Failed() {
			t.Fatalf("the control must resolve and compile: %s", doc)
		}
	}
}

// TestAnEdgeEntrysRelationTypeIsResolvedAndListed pins that the
// via/between spelling of an edges entry goes through the same resolution
// every other type key does — so a key this game does not have is a
// refusal with a pointer, and a key it does have is a dependency Task 12
// can report on.
func TestAnEdgeEntrysRelationTypeIsResolvedAndListed(t *testing.T) {
	g, _ := newGame(t)
	err := resolveOnly(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"edges":[{"via":"reqiures","between":["q","q"]}]}`)
	oneProblem(t, err, "/edges/0/via/0", `no relation type "reqiures"`)

	r, err := g.views.Resolve(t.Context(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"edges":[{"via":"requires","between":["q","q"]}]}`))
	if err != nil {
		t.Fatalf("the control must resolve: %v", err)
	}
	var listed bool
	for _, ref := range r.Refs {
		if ref.Pointer == "/edges/0/via/0" && ref.Key == "requires" && ref.ID != nil {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("the relation type an edges entry draws must be a listed reference: %+v", r.Refs)
	}
	if len(r.Edges) != 1 || len(r.Edges[0].RelationTypeIDs) != 1 {
		t.Fatalf("the entry must carry its resolved type id: %+v", r.Edges)
	}
}

// TestAMisspelledTypeNameIsRefusedRatherThanDrawnAsNothing is the
// decision Task 4 left to the compiler: @type is compared against the id
// a row actually holds, and a key this game does not declare is a
// refusal, not a picture with nothing in it.
func TestAMisspelledTypeNameIsRefusedRatherThanDrawnAsNothing(t *testing.T) {
	g, _ := newGame(t)
	r, err := g.views.Resolve(t.Context(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"traverse":[{"from":"q","via":"available_to","as":"c",
			  "where":{"field":"@type","op":"eq","value":"clsas"}}]}`))
	if err != nil {
		t.Fatalf("resolution passes @type through as text: %v", err)
	}
	_, _, err = Compile(r, g.projectID)
	oneProblem(t, err, "/traverse/0/where/value", `no type "clsas" in this game`)

	// The control: the spelling this game does declare compiles, and the
	// value it binds is an id rather than the key.
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","as":"c",
		  "where":{"field":"@type","op":"eq","value":"class"}}]}`)
	if !strings.Contains(sql, "far.entity_type_id =") {
		t.Fatalf("@type must compare against the column the row holds:\n%s", sql)
	}
}

// TestAMultiHopStepEmitsARecursionRatherThanASecondJoin replaces the
// refusal this build carried until the walk arrived. The refusal existed
// so that a depth-3 question could not be answered with a depth-1 answer
// and no error; what keeps that from happening now is that the step is
// compiled by internal/graph, and this asserts it as text — a compiler
// that quietly emitted its own one-hop join for a depth-3 step would
// still return rows, and only the shape of the statement says which
// question was asked.
func TestAMultiHopStepEmitsARecursionRatherThanASecondJoin(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"requires","depth":3,"as":"chain"}]}`)
	for _, want := range []string{
		"w0 (id, depth, path, via_relation, from_id, closed) AS (", // graph's own recursion
		"UNION ALL",     // the recursive term
		"NOT w.closed",  // its path guard
		"w0_out AS (",   // the wrapper that bounds it
		"FROM w0_out w", // and this compiler reading from it
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("a multi-hop step is walked by internal/graph, and %q is missing:\n%s",
				want, sql)
		}
	}
	// The control: one hop is still a plain join, with no recursion at
	// all. Routing a neighbour query through a recursive CTE would be a
	// cost nothing asked for.
	one, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"requires","depth":1,"as":"chain"}]}`)
	if strings.Contains(one, "w0") {
		t.Errorf("a one-hop step is a join, not a walk:\n%s", one)
	}
}

// TestAnEdgeEntryNamingASelectorIsRefused: a selector walks no relation,
// so there is nothing for the entry to draw and an empty edge set would
// say so in silence.
func TestAnEdgeEntryNamingASelectorIsRefused(t *testing.T) {
	g, _ := newGame(t)
	r, err := g.views.Resolve(t.Context(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],"edges":[{"from_step":"q"}]}`))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, _, err = Compile(r, g.projectID)
	oneProblem(t, err, "/edges/0/from_step", "walks no relations")
}

// TestAGlobPatternMapsItsWildcardsAndEscapesEverythingElse pins the
// operator table's promise for `matches`: `*` and `?` are the only
// metacharacters, and a per-cent or an underscore the caller wrote is a
// literal. The previous emitter escaped the caller's characters and then
// ran a second replacer that unescaped them again, so `matches "50%"`
// compiled to the pattern `50%` — a prefix match — and nothing tested it.
func TestAGlobPatternMapsItsWildcardsAndEscapesEverythingElse(t *testing.T) {
	for _, c := range []struct {
		op          Operator
		value, want string
	}{
		{OpMatches, "50%", `50\%`},
		{OpMatches, "a_b", `a\_b`},
		{OpMatches, "50%*", `50\%%`},
		{OpMatches, "Hogg?r", "Hogg_r"},
		{OpMatches, `back\slash`, `back\\slash`},
		{OpMatches, "*plain*", "%plain%"},
		{OpStartsWith, "50%", `50\%%`},
		{OpStartsWith, "a*b", `a*b%`},
		{OpContains, "a_b", `%a\_b%`},
	} {
		if got := likePattern(c.op, c.value); got != c.want {
			t.Errorf("%s %q compiles to the pattern %q, want %q", c.op, c.value, got, c.want)
		}
	}
}

// TestAnEdgeEntrysEmptyDirectionIsFilledOnceAndRefusedAfter closes the
// asymmetry a step and an edge entry used to have: a step refused an
// empty direction while an edge entry read it as "out" in a switch arm of
// its own. Now applyDefaults fills both, and the compiler refuses what is
// left — which is only reachable from a *Resolved a Go caller built.
func TestAnEdgeEntrysEmptyDirectionIsFilledOnceAndRefusedAfter(t *testing.T) {
	g, _ := newGame(t)
	doc := `{"v":1,"from":[{"type":"quest","as":"quests"}],
		"edges":[{"via":"requires","between":["quests","quests"]}]}`
	parsed := mustParse(t, doc)
	if got := parsed.Edges[0].Direction; got != DirectionOut {
		t.Fatalf("an edge entry's direction defaults to %q, got %q", DirectionOut, got)
	}
	resolved, err := g.views.Resolve(t.Context(), g.projectID, parsed)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The control: the filled default compiles.
	if _, _, err := Compile(resolved, g.projectID); err != nil {
		t.Fatalf("the defaulted direction must compile: %v", err)
	}
	resolved.Edges[0].Spec.Direction = ""
	_, _, err = Compile(resolved, g.projectID)
	oneProblem(t, err, "/edges/0/direction", `must be "out"`)
}
