package web_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/views"
)

// The source-shape guards over internal/web/static/layout — the three
// properties of that directory that no harness can see at runtime,
// because each of them is about the *arrangement* of the code rather
// than about what it computes.

const layoutDir = "static/layout"

// layoutModules returns every module in the layout directory, and fails
// if it finds none: a guard whose glob matched nothing passes forever,
// which is the failure Task 5 caught in the component scan.
func layoutModules(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	entries, err := os.ReadDir(layoutDir)
	assert.Must(t, err == nil, "read %s: %v", layoutDir, err)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".js" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(layoutDir, entry.Name()))
		assert.Must(t, err == nil, "read %s: %v", entry.Name(), err)
		found[entry.Name()] = string(raw)
	}
	assert.Must(t, len(found) != 0, "no modules found under %s: this guard read nothing", layoutDir)
	return found
}

// importSpecifier finds the specifier of every static import in a module.
var importSpecifier = regexp.MustCompile(`(?m)^\s*(?:import|export)[^;]*?from\s*["']([^"']+)["']`)

// TestTheLayoutModulesResolveWithoutAnImportMap is the worker rule.
func TestTheLayoutModulesResolveWithoutAnImportMap(t *testing.T) {
	t.Parallel()
	modules := layoutModules(t)

	var names []string
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"budget.js", "compose.js", "engine.js", "worker.js"}
	assert.Must(t, strings.Join(names, ",") == strings.Join(want, ","), "layout modules = %v, want %v; a new module here needs the same relative-import rule", names, want)

	vendored := 0
	for _, name := range names {
		for _, match := range importSpecifier.FindAllStringSubmatch(modules[name], -1) {
			specifier := match[1]
			assert.Should(t, strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../"), "%s/%s imports %q by bare specifier: a module worker has no import map, "+
				"so this resolves to nothing at load and the page sees a worker that never answers",
				layoutDir, name, specifier)
			if strings.Contains(specifier, "/vendor/") {
				vendored++
			}
		}
	}
	// And the reason the rule exists is really exercised: this directory
	// does reach the vendored runtime, so the guard above is guarding
	// something rather than describing a directory that imports nothing.
	assert.Should(t, vendored != 0, "no layout module imports the vendored runtime; the bare-specifier guard above tests nothing")
}

// TestTheLayoutBudgetIsOneNumberAndItsSentenceIsGenerated pins O10's
// "one exported constant, and the banner's sentence is generated from
// the number".
func TestTheLayoutBudgetIsOneNumberAndItsSentenceIsGenerated(t *testing.T) {
	t.Parallel()
	modules := layoutModules(t)
	budget, ok := modules["budget.js"]
	assert.Must(t, ok, "static/layout/budget.js is missing")

	for _, name := range []string{"LAYOUT_BUDGET_MS", "LAYOUT_RETRY_MS"} {
		declarations := strings.Count(budget, "export const "+name+" =")
		assert.Should(t, declarations == 1, "%s is declared %d times in budget.js, want exactly 1", name, declarations)
		// And nowhere else under static/: a second declaration is a
		// second budget, and the one a designer waits on would be
		// whichever module the canvas happened to import.
		for path, src := range everyOwnModule(t) {
			if path == layoutDir+"/budget.js" {
				continue
			}
			assert.Should(t, !strings.Contains(src, "const "+name), "%s declares %s as well; the budget lives in budget.js and nowhere else", path, name)
		}
	}

	// The sentence must be a function of the number. A literal "2
	// seconds" — or any digit-and-unit spelled out in the prose — is
	// exactly the drift the generated sentence exists to prevent, and no
	// runtime assertion catches it until somebody tunes the constant.
	prose := regexp.MustCompile(`\d+(\.\d+)?\s*(second|seconds|ms|milliseconds)\b`)
	for _, line := range strings.Split(budget, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if hit := prose.FindString(line); hit != "" {
			t.Errorf("budget.js writes %q into its own source: the banner's sentence must be computed "+
				"from LAYOUT_BUDGET_MS, or the prose and the timeout drift apart on the next tuning commit",
				hit)
		}
	}
	assert.Should(t, strings.Contains(budget, "${ms / 1000} seconds"), "budgetSentence no longer derives its number from its argument")
}

// everyOwnModule is the static tree minus the vendored subtree, which is
// the same perimeter static_sinks_test.go polices and is defined here by
// a walk rather than by a list for the same reason.
func everyOwnModule(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.ToSlash(path) == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs":
		default:
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(path)] = string(raw)
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Must(t, len(out) != 0, "walked no own modules under static/")
	return out
}

// TestTheComposerReadsAStoredPositionInTheSpellingTheServerWrites is the
// seam between Go and JavaScript that nothing else joins.
func TestTheComposerReadsAStoredPositionInTheSpellingTheServerWrites(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(views.Position{})
	assert.Must(t, err == nil, "marshal a position: %v", err)
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal a position: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join("static", "positions.js"))
	assert.Must(t, err == nil, "read static/positions.js: %v", err)
	code := withoutComments(string(raw))

	// UpdatedAt is the one field nothing renders — internal/views'
	// positions.go says so — and the composer has no business reading it.
	read := 0
	for name := range fields {
		if name == "updated_at" {
			continue
		}
		if !reads(code, name) {
			t.Errorf("a run marshals a stored position with a %q member and positions.js never "+
				"reads it: every saved arrangement would be silently ignored", name)
			continue
		}
		read++
	}
	assert.Must(t, read != 0, "positions.js reads none of the members a stored position marshals to")
	// The Go field names are the spelling the envelope used before
	// Position was tagged, and reading them was compose.js's workaround
	// for it. One spelling crosses the wire now, so a second reader is
	// not a belt and braces — it is the thing that would let the tags be
	// lost again without this file noticing.
	for _, gone := range []string{"EntityType", "EntityKey", "Pinned"} {
		assert.Should(t, !reads(code, gone), "positions.js still reads %q: the envelope speaks one spelling now, and a "+
			"reader of the old one hides the day it stops", gone)
	}
}

// reads says whether the module names a wire member as a member: either
// dotted (`row.entity_key`) or subscripted with a quoted string.
func reads(code, name string) bool {
	return regexp.MustCompile(`(?:\.` + regexp.QuoteMeta(name) + `\b|["']` + regexp.QuoteMeta(name) + `["'])`).
		MatchString(code)
}

// withoutComments removes `//` line comments and `/* */` blocks, so a
// source-shape guard asserts over code rather than over prose that
// happens to name what it is looking for.
func withoutComments(code string) string {
	blocks := regexp.MustCompile(`(?s)/\*.*?\*/`)
	lines := regexp.MustCompile(`(?m)//.*$`)
	return lines.ReplaceAllString(blocks.ReplaceAllString(code, " "), " ")
}

// TestTheCommentStripperRemovesCommentsAndKeepsCode pins the helper the
// guard above leans on: a stripper that returned its input unchanged
// would make that assertion pass over a comment, which is the failure it
// was rewritten to close.
func TestTheCommentStripperRemovesCommentsAndKeepsCode(t *testing.T) {
	t.Parallel()
	const src = "// row.entity_key in a comment\nconst a = row.entity_key;\n/* row.pinned */\nconst b = 1;"
	stripped := withoutComments(src)
	assert.Should(t, !strings.Contains(stripped, "comment") && !strings.Contains(stripped, "row.pinned"), "comments survived stripping: %q", stripped)
	assert.Should(t, strings.Contains(stripped, "const a = row.entity_key;") && strings.Contains(stripped, "const b = 1;"), "code did not survive stripping: %q", stripped)
}
