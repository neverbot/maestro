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
// into a statement is an explicit `frag(...)`. This test reads the whole
// package's syntax tree and refuses that conversion outside the four
// helpers that build placeholders, names and formats from things a
// caller cannot reach.
//
// **It reads every non-test file in the package, not compile.go alone**,
// and it closes the four routes a one-file walk over function bodies
// left open, each of which compiled and left the suite green:
//
//  1. a conversion in another file of this package — frag is unexported
//     but package-scoped, so predicate.go could spell one;
//  2. no conversion at all — the builder's buffer used to be a bare
//     strings.Builder, so `b.sql.WriteString(v)` needed no frag; the
//     sqlText wrapper is what closes this one by construction, and the
//     `.raw` check below is what keeps the wrapper honest;
//  3. a parenthesised conversion, `(frag)(v)`, whose call function is not
//     an *ast.Ident;
//  4. a local type alias, `type t = frag`, whose conversions do not
//     mention frag at all.
//
// The walk is over each declaration rather than over function bodies, so
// a package-level variable's initialiser — or a function literal assigned
// to one — is scanned too.
func TestTheOnlyStringToFragmentConversionsAreTheOnesNamedHere(t *testing.T) {
	allowed := map[string]bool{
		"bind":      true, // "$3" from an argument count
		"sprintf":   true, // a fragment format over fragment arguments
		"joinFrags": true, // fragments joined by a fragment
		"cteName":   true, // a constant prefix and an int
	}
	// The two methods of sqlText, which are the only code that may touch
	// the raw strings.Builder underneath a statement.
	bufferHolders := map[string]bool{"append": true, "String": true}

	found := 0
	for _, file := range packageFiles(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		// Route 4: an alias or a defined type over frag would give a
		// second spelling of the conversion, which the walk below does
		// not know to look for. There is no legitimate one.
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec)
				if ident, ok := unparen(ts.Type).(*ast.Ident); ok && ident.Name == "frag" {
					t.Errorf("%s declares %q over frag: a second name for the conversion is a "+
						"second route for a caller's value into the statement text",
						file, ts.Name.Name)
				}
			}
		}
		for _, decl := range parsed.Decls {
			where := "a package-level declaration in " + file
			if fn, ok := decl.(*ast.FuncDecl); ok {
				where = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "raw" {
					// Route 2: writing to the statement's buffer directly
					// needs no frag conversion at all.
					if !bufferHolders[where] {
						t.Errorf("%s reaches a statement's raw buffer; only %v may, and a "+
							"strings.Builder takes a plain string", where,
							sortedNames(bufferHolders))
					}
					return true
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				// Route 3: (frag)(v) is a conversion whose Fun is an
				// *ast.ParenExpr rather than an *ast.Ident.
				ident, ok := unparen(call.Fun).(*ast.Ident)
				if !ok || ident.Name != "frag" {
					return true
				}
				found++
				if !allowed[where] {
					t.Errorf("%s converts a string to statement text; only %v may, and a "+
						"caller's value has no other route into the SQL", where,
						sortedNames(allowed))
				}
				return true
			})
		}
	}
	if found != len(allowed) {
		t.Fatalf("expected one conversion in each of %v, found %d — if a helper stopped "+
			"converting, this test is no longer watching what it names",
			sortedNames(allowed), found)
	}
}

// packageFiles is every non-test Go file of this package, which is the
// unit the guard above has to hold over: frag is unexported, and
// unexported means visible to all of them.
func packageFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) < 2 {
		t.Fatalf("found %d source files; the walk below would be watching one file again", len(files))
	}
	return files
}

// unparen strips the parentheses a conversion may be wrapped in, so that
// `(frag)(v)` is the same node to this test as `frag(v)`.
func unparen(e ast.Expr) ast.Expr {
	for {
		paren, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = paren.X
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
