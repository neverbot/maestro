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
	// The document walks as well as selects, because the walk's text is
	// written by internal/graph rather than by this package's builder:
	// the seed, the edge predicate and every bound the walk carries reach
	// that text through builder.adopt, and this is what says none of them
	// carries a caller's value.
	sql, args := compileOf(t, g, `{"v":1,"from":[{"type":"quest","keys":["hogger"],"as":"q",
		"where":{"field":"rank","op":"eq","value":"rare"}}],
		"traverse":[{"from":"q","via":"requires","direction":"out","to_type":"quest",
		             "depth":{"min":1,"max":3},"as":"chain",
		             "edge_where":{"field":"@type","op":"eq","value":"requires"}}]}`)
	for _, sentinel := range []string{"hogger", "rare", "quest", "min_level", "requires", "chain"} {
		if strings.Contains(sql, sentinel) {
			t.Errorf("the caller value %q reached the statement text:\n%s", sentinel, sql)
		}
	}
	if len(args) < 3 {
		t.Fatalf("the values must have gone somewhere: %d bind arguments", len(args))
	}
}

// projectScopedTables are the four tables this compiler reads that carry
// a project_id. Every reference to one of them, in every block, has to
// filter on it.
var projectScopedTables = []string{"entity_types", "relation_types", "entities", "relations"}

// tableReference matches a FROM or a JOIN of one of those four and
// captures the alias it was given. The longer names come first in the
// alternation because Go's regexp is leftmost-first, not leftmost-longest.
var tableReference = regexp.MustCompile(
	`(?i)\b(?:FROM|JOIN)\s+(entity_types|relation_types|entities|relations)\b\s*(?:AS\s+)?([a-z_][a-z0-9_]*)`)

// TestEveryTableReferenceIsProjectFiltered walks the emitted SQL rather
// than one query's behaviour, so a clause added by a later task cannot
// quietly drop the filter.
//
// **It asserts per table reference, not per block.** Asking whether
// `project_id = $1` appears *somewhere* in a block passes with a filter
// deleted, because another table in the same block still carries one: the
// entity-type join's filter can be removed from nodeUnion and a
// block-level check stays green. So each reference's own alias has to
// appear filtered, and all four project-scoped tables have to be
// exercised by the query below — the earlier vacuity check named two of
// them, which left entity_types and relation_types outside the test
// entirely.
//
// **Why this text test is the only real guard.** Every project filter
// this compiler emits is, in the current build, redundant: the selector
// filters on an entity_type_id resolved in *this* game, the step on a
// relation_type_id and a to_type resolved the same way, the between arm
// on its own relation_type_id, and the join-backs join to rows those
// filters already isolated. So `TestARunFromAnotherGameSeesNothing`
// cannot fail on a lost project filter under any shape the compiler emits
// today — the filters are defence in depth against the shapes Tasks 7, 8
// and 9 add, and this test is what defends them.
func TestEveryTableReferenceIsProjectFiltered(t *testing.T) {
	g, _ := newGame(t)
	// The query carries a **multi-hop** step as well as a one-hop one,
	// because the recursion internal/graph emits is the first shape in
	// this package whose project filters are not redundant: a one-hop step
	// finds its rows by an id resolved in this game, and a walk finds them
	// by walking. Its three filters — the anchor's, the relation's and the
	// far entity's — are counted here like every other.
	sql, _ := compileOf(t, g, `{"v":1,"from":[{"type":"quest","as":"q"}],
		"traverse":[{"from":"q","via":"available_to","direction":"out","to_type":"class","as":"c"},
		            {"from":"q","via":"requires","direction":"out","to_type":"quest",
		             "depth":{"min":1,"max":3},"as":"chain"}],
		"edges":[{"from_step":"c"},{"from_step":"chain"},
		         {"via":"requires","between":["q","q"]}]}`)
	seen := map[string]int{}
	for _, block := range strings.Split(sql, "SELECT") {
		for _, ref := range tableReference.FindAllStringSubmatch(block, -1) {
			table, alias := strings.ToLower(ref[1]), ref[2]
			seen[table]++
			switch strings.ToUpper(alias) {
			case "AS", "ON", "WHERE", "JOIN", "UNION":
				t.Errorf("the reference %q has no alias, so this test cannot name its filter:\n%s",
					ref[0], block)
				continue
			}
			if !strings.Contains(block, alias+".project_id = $1") {
				t.Errorf("%s is read as %q without %s.project_id = $1 in the same block:\n%s",
					table, ref[0], alias, block)
			}
		}
	}
	for _, table := range projectScopedTables {
		if seen[table] == 0 {
			t.Errorf("the query above reads no %s, so nothing above asserted a filter on it; "+
				"the assertion is vacuous for that table:\n%s", table, sql)
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
// not declared here. What each keeps is the structure the golden file
// exists to freeze: a query with only between-edges and no traversal at
// all, and a query whose steps branch from one seed into three sets with
// three edge entries, one of them §3.2's own `depth: {min:1,max:4}` —
// which is the only golden file holding a recursion, its edge predicate
// and the renumbering that splices both into the statement around them.
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
	    { "from": "quests", "via": "requires", "direction": "out",
	      "to_type": "quest", "depth": { "min": 1, "max": 4 }, "as": "chain",
	      "edge_where": { "field": "@type", "op": "eq", "value": "requires" } },
	    { "from": "quests", "via": "takes_place_in", "direction": "out",
	      "to_type": "zone", "depth": 1, "as": "zones" }
	  ],
	  "nodes": [ { "set": "start", "role": "seed" }, { "set": "quests" },
	             { "set": "chain" }, { "set": "zones" } ],
	  "edges": [ { "from_step": "quests" }, { "from_step": "chain" },
	             { "from_step": "zones" } ],
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
//
// **These files are load-bearing, not a convenience, and -update is not
// how a failure is resolved.** Three invariants used to be red here and
// in no other test — the selector's project filter, a step's invalid-row
// exclusion and its destination-type filter — so regenerating rather than
// reading the diff erased three guarantees in one keystroke. Each now has
// a test of its own (TestEveryTableReferenceIsProjectFiltered and
// TestAStepDrawsOnlyItsDestinationTypeAndOnlyValidRows), but the next
// clause a task adds arrives here first and unaccompanied, which is why
// the failure message says read the diff before it names the flag.
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
				t.Errorf("the emitted statement changed. **Read this diff before you "+
					"regenerate it.** -update makes any change to the emitter agree with "+
					"itself, including a filter that was dropped: this file is the only "+
					"place a clause no other test names is visible.\n"+
					"--- want ---\n%s\n--- got ---\n%s", want, got)
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
// caller cannot reach, plus the one that adopts internal/graph's own
// statement.
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
		// The one route by which text this package did not write becomes
		// statement text: internal/graph's own walk, spliced in. It takes
		// a graph.Walk rather than a string, so what it converts is
		// WalkCTE's output and nothing else — see builder.adopt.
		"adopt": true,
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
