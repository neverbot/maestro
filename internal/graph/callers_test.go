package graph_test

import (
	"go/parser"
	"go/token"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const selfPath = "github.com/neverbot/maestro/internal/graph"

// packageNames is every internal/<name> this package's own doc mentions
// inside the paragraph beginning with marker.
//
// A paragraph and not a sentence: "see builder.adopt there" ends in a
// period that is not a full stop, and a guard that mis-parses its own
// input reads as green for the wrong reason.
func packageNames(t *testing.T, marker string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "walk.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse walk.go: %v", err)
	}
	if f.Doc == nil {
		t.Fatal("walk.go carries no package comment at all")
	}
	for _, para := range strings.Split(f.Doc.Text(), "\n\n") {
		flat := strings.Join(strings.Split(para, "\n"), " ")
		if !strings.Contains(flat, marker) {
			continue
		}
		var out []string
		for _, m := range regexp.MustCompile(`internal/[a-z]+`).FindAllString(flat, -1) {
			out = append(out, "github.com/neverbot/maestro/"+m)
		}
		sort.Strings(out)
		return out
	}
	t.Fatalf("the package comment has no paragraph containing %q", marker)
	return nil
}

// importers is every package in this module whose *production* imports
// include this one. It is read from the build graph with `go list`, not
// from anybody's prose: a guard a comment can satisfy is not a guard.
// Test imports are deliberately excluded -- this package's own external
// test package imports it, and a caller is a package that compiles it
// into the product.
func importers(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}|{{join .Imports " "}}`, "github.com/neverbot/maestro/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, imports, ok := strings.Cut(line, "|")
		if !ok || path == selfPath {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if imp == selfPath {
				found = append(found, path)
			}
		}
	}
	sort.Strings(found)
	return found
}

// TestThePackageCommentNamesItsCallersAndOnlyItsCallers is the guard that
// makes the caller sentence a fact rather than a claim, and it is
// bidirectional: an importer the comment does not name fails it, a name
// the comment claims that imports nothing fails it, and a package listed
// as "not a caller yet" that has since become one fails it too.
//
// It exists because the sentence it guards is this repository's cheapest
// instance of its standing defect -- a rule established correctly and not
// carried one step along. The commit that makes internal/analysis import
// this package is red until the comment says so.
func TestThePackageCommentNamesItsCallersAndOnlyItsCallers(t *testing.T) {
	t.Parallel()
	named := packageNames(t, "Callers as of this commit:")
	notYet := packageNames(t, "Not a caller yet:")
	actual := importers(t)

	if len(actual) == 0 {
		t.Fatal("no package in this module imports internal/graph, so nothing below is " +
			"being compared: either go list failed to see the module or the walk has lost " +
			"its only caller")
	}
	if strings.Join(named, ",") != strings.Join(actual, ",") {
		t.Errorf("the package comment names the callers %v and the module's import graph says "+
			"%v; a package comment claiming callers it cannot point at, or missing one it has, "+
			"is the defect the sentence exists to prevent", named, actual)
	}
	have := map[string]bool{}
	for _, p := range actual {
		have[p] = true
	}
	for _, p := range notYet {
		if have[p] {
			t.Errorf("the package comment says %s is not a caller yet, and it imports this "+
				"package: move it to the callers line in the commit that added the import", p)
		}
	}
}
