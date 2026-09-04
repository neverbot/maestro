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

// TestAMultiHopStepIsRefusedUntilTheWalkArrives keeps this build from
// answering a depth-3 question with a depth-1 answer, which is the
// wrong-answer-without-an-error class this repository keeps producing.
// Task 8 replaces the refusal with graph.WalkCTE and deletes this test.
func TestAMultiHopStepIsRefusedUntilTheWalkArrives(t *testing.T) {
	g, _ := newGame(t)
	r, err := g.views.Resolve(t.Context(), g.projectID,
		mustParse(t, `{"v":1,"from":[{"type":"quest","as":"q"}],
			"traverse":[{"from":"q","via":"requires","depth":3,"as":"chain"}]}`))
	if err != nil {
		t.Fatalf("a multi-hop query resolves; it is the compiler that cannot emit it: %v", err)
	}
	_, _, err = Compile(r, g.projectID)
	oneProblem(t, err, "/traverse/0/depth", "multi-hop traversal is not implemented yet")

	// The control: one hop compiles.
	compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"requires","depth":1,"as":"chain"}]}`)
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
