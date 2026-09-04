package views

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func compileOf(t *testing.T, g *game, doc string) (string, []any) {
	t.Helper()
	r, err := g.views.Resolve(t.Context(), g.projectID, mustParse(t, doc))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sql, args, err := Compile(r, g.projectID)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return sql, args
}

// TestNoCallerValueEverReachesTheStatementText is the injection question,
// answered by construction rather than by care. Every string the caller
// controls is a distinctive sentinel; none may appear in the SQL.
func TestNoCallerValueEverReachesTheStatementText(t *testing.T) {
	g, _ := newGame(t)
	// The sentinels have to be legal for the document to resolve, so they
	// are the seeded keys themselves — which is the strongest form of the
	// test: even a *valid* key must travel as a bind parameter, because
	// the compiler cannot tell a valid key from a crafted one.
	sql, args := compileOf(t, g, `{"v":1,"from":[{"type":"quest","keys":["hogger"],
		"where":{"field":"rank","op":"eq","value":"rare"}}]}`)
	for _, sentinel := range []string{"hogger", "rare", "quest", "min_level"} {
		if strings.Contains(sql, sentinel) {
			t.Errorf("the caller value %q reached the statement text:\n%s", sentinel, sql)
		}
	}
	if len(args) < 3 {
		t.Fatalf("the values must have gone somewhere: %d bind arguments", len(args))
	}
}

// TestEveryTableReferenceIsProjectFiltered walks the emitted SQL rather
// than one query's behaviour, so a clause added by a later task cannot
// quietly drop the filter.
func TestEveryTableReferenceIsProjectFiltered(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","direction":"out","to_type":"class","as":"c"}],
		"edges":[{"from_step":"c"}]}`)
	refs := regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(entities|relations)\b`).FindAllString(sql, -1)
	if len(refs) == 0 {
		t.Fatalf("no table references found; the assertion below would be vacuous:\n%s", sql)
	}
	for _, block := range strings.Split(sql, "SELECT") {
		if !strings.Contains(block, "entities") && !strings.Contains(block, "relations") {
			continue
		}
		if !strings.Contains(block, "project_id = $1") {
			t.Errorf("a select over entities or relations does not filter on the project:\n%s", block)
		}
	}
}

func TestATypedPredicateGuardsItsCastByJsonbType(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g,
		`{"v":1,"from":[{"type":"quest","where":{"field":"min_level","op":"between","value":[20,30]}}]}`)
	if !strings.Contains(sql, "jsonb_typeof") {
		t.Fatalf("a number predicate must guard its cast with jsonb_typeof:\n%s", sql)
	}
	if !strings.Contains(sql, "::numeric") {
		t.Fatalf("a number predicate must compare numerically:\n%s", sql)
	}
}

func TestInvalidRowsAreExcludedUnlessAskedFor(t *testing.T) {
	g, _ := newGame(t)
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest"}]}`)
	if !strings.Contains(sql, "invalid = false") {
		t.Fatalf("invalid rows must be excluded by default:\n%s", sql)
	}
	sql, _ = compileOf(t, g, `{"v":1,"from":[{"type":"quest"}],"include_invalid":true}`)
	if strings.Contains(sql, "invalid = false") {
		t.Fatalf("include_invalid must lift the filter:\n%s", sql)
	}
}

// workedExamples are the spec's §3 examples, as far as this game's
// vocabulary can express them.
//
// §3.1 is verbatim: the fixture is that example's own game. The other two
// are the *shapes* of §3.2 and §3.3 mapped onto the same vocabulary,
// because their own types — licence, championship, room, ability — are
// not declared here and §3.2's `depth: {min:1,max:4}` is Task 8's, not
// this build's. What each keeps is the structure the golden file exists
// to freeze: a query with only between-edges and no traversal at all, and
// a query whose steps branch from one seed into two sets with three edge
// entries.
var workedExamples = []struct {
	name  string
	query string
}{
	{"mmorpg_quests", `{
	  "v": 1,
	  "params": [ { "key": "class_key", "type": "text", "default": "mage" } ],
	  "from": [
	    { "type": "class", "as": "cls",
	      "where": { "field": "@key", "op": "eq", "value": { "param": "class_key" } } }
	  ],
	  "traverse": [
	    { "from": "cls", "via": "available_to", "direction": "in",
	      "to_type": "quest", "depth": 1, "as": "reachable",
	      "where": { "all": [
	        { "field": "min_level", "op": "gte", "value": 20 },
	        { "field": "min_level", "op": "lte", "value": 30 } ] } }
	  ],
	  "nodes": [ { "set": "cls", "role": "seed" }, { "set": "reachable" } ],
	  "edges": [
	    { "from_step": "reachable" },
	    { "via": "requires", "between": ["reachable", "reachable"], "direction": "out" }
	  ],
	  "project": {
	    "label": "@name",
	    "color_by": { "related": { "via": "takes_place_in", "direction": "out",
	                               "type": "zone", "attr": "@name" } },
	    "fields": ["min_level"]
	  },
	  "limits": { "max_nodes": 500 }
	}`},
	{"mmorpg_between_edges", `{
	  "v": 1,
	  "from": [ { "type": "quest", "as": "quests" } ],
	  "traverse": [],
	  "nodes": [ { "set": "quests" } ],
	  "edges": [ { "via": "requires", "between": ["quests", "quests"], "direction": "out" } ],
	  "project": { "label": "@name", "color_by": "rank" },
	  "limits": { "max_nodes": 1200, "max_edges": 4000 }
	}`},
	{"mmorpg_progression", `{
	  "v": 1,
	  "from": [ { "type": "class", "as": "start",
	              "where": { "field": "@key", "op": "eq", "value": "mage" } } ],
	  "traverse": [
	    { "from": "start", "via": "available_to", "direction": "in",
	      "to_type": "quest", "depth": 1, "as": "quests" },
	    { "from": "quests", "via": "takes_place_in", "direction": "out",
	      "to_type": "zone", "depth": 1, "as": "zones" }
	  ],
	  "nodes": [ { "set": "start", "role": "seed" }, { "set": "quests" }, { "set": "zones" } ],
	  "edges": [ { "from_step": "quests" }, { "from_step": "zones" },
	             { "via": "requires", "between": ["quests", "quests"] } ],
	  "project": { "label": "@name", "color_by": "@type", "group_by": "@type" },
	  "limits": { "max_depth": 4, "max_nodes": 800 }
	}`},
}

var updateGolden = flag.Bool("update", false, "rewrite the golden statements in testdata")

// TestTheWorkedExamplesCompileToTheseStatements freezes the emitted SQL.
// A golden file is what makes a change to the emitter a diff a reviewer
// reads rather than a behaviour they infer — and the ids are already $n
// by construction, because every value the compiler handles is a bind
// parameter.
func TestTheWorkedExamplesCompileToTheseStatements(t *testing.T) {
	g, _ := newGame(t)
	for _, example := range workedExamples {
		t.Run(example.name, func(t *testing.T) {
			sql, _ := compileOf(t, g, example.query)
			path := filepath.Join("testdata", example.name+".sql")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(sql+"\n"), 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s (run go test -run TestTheWorkedExamplesCompileToTheseStatements -update): %v", path, err)
			}
			if got := sql + "\n"; got != string(want) {
				t.Errorf("the emitted statement changed.\n--- want ---\n%s\n--- got ---\n%s",
					want, got)
			}
		})
	}
}

// TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere is the
// construction half of the injection answer, and the half a behavioural
// test cannot give.
//
// TestNoCallerValueEverReachesTheStatementText proves that the queries it
// compiles put nothing in the text; it cannot prove that a query nobody
// wrote will not. What can is the type: a `string` variable does not
// convert to frag implicitly, so the only way to spell a caller's value
// into a statement is an explicit `frag(...)`. This test reads compile.go
// itself and refuses that conversion outside the four helpers that build
// placeholders, names and formats from things a caller cannot reach.
func TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere(t *testing.T) {
	allowed := map[string]bool{
		"bind":      true, // "$3" from an argument count
		"sprintf":   true, // a fragment format over fragment arguments
		"joinFrags": true, // fragments joined by a fragment
		"cteName":   true, // a constant prefix and an int
	}
	file, err := parser.ParseFile(token.NewFileSet(), "compile.go", nil, 0)
	if err != nil {
		t.Fatalf("parse compile.go: %v", err)
	}
	found := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "frag" {
				return true
			}
			found++
			if !allowed[fn.Name.Name] {
				t.Errorf("%s converts a string to statement text; only %v may, and a caller's "+
					"value has no other route into the SQL", fn.Name.Name, sortedNames(allowed))
			}
			return true
		})
	}
	if found != len(allowed) {
		t.Fatalf("expected one conversion in each of %v, found %d — if a helper stopped "+
			"converting, this test is no longer watching what it names",
			sortedNames(allowed), found)
	}
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
