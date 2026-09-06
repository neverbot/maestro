package web_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/views"
)

// The source-shape guards over internal/web/static/layout — the three
// properties of that directory that no harness can see at runtime,
// because each of them is about the *arrangement* of the code rather
// than about what it computes.
//
// internal/web/jstest/layout_test.mjs holds everything else: the
// determinism, the three degeneracies of the fit, the separation pass,
// the grid and the budget are all arithmetic over plain data, and the
// only honest way to test JavaScript is to run it. What is left here is:
//
//   - **No module in this directory imports by bare specifier.** An
//     import map is a property of a *document*; a module worker has its
//     own module map and no map at all. So `import … from "dagre"`
//     inside anything worker.js pulls in resolves to nothing, in the
//     worker, at load — and the page sees not an error but a worker that
//     never answers, which looks exactly like a slow layout and would be
//     reported to the designer as the budget running out. It works today
//     because the two vendored imports are written as paths; a habit is
//     not a guard.
//   - **The budget is one number and its sentence is generated from it.**
//     A hard-coded "2 seconds" beside a 2000 survives every runtime
//     assertion in the harness until somebody tunes the constant, at
//     which point the interface starts telling designers a number that
//     is not the one it enforced.
//   - **The spelling a stored position reads back in is the one the
//     server writes.** compose.js parses the envelope's `positions[]`,
//     and internal/views.Position carries no struct tags, so that
//     spelling is Go field names and lives nowhere anybody would think
//     to look. This is the seam between the two halves, and it is
//     asserted by marshalling the real struct rather than by quoting it.

const layoutDir = "static/layout"

// layoutModules returns every module in the layout directory, and fails
// if it finds none: a guard whose glob matched nothing passes forever,
// which is the failure Task 5 caught in the component scan.
func layoutModules(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	entries, err := os.ReadDir(layoutDir)
	if err != nil {
		t.Fatalf("read %s: %v", layoutDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".js" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(layoutDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		found[entry.Name()] = string(raw)
	}
	if len(found) == 0 {
		t.Fatalf("no modules found under %s: this guard read nothing", layoutDir)
	}
	return found
}

// importSpecifier finds the specifier of every static import in a module.
var importSpecifier = regexp.MustCompile(`(?m)^\s*(?:import|export)[^;]*?from\s*["']([^"']+)["']`)

// TestTheLayoutModulesResolveWithoutAnImportMap is the worker rule.
//
// It also asserts the directory holds the four modules the layout is
// made of, so that a fifth one added without a thought about workers is
// a diff somebody reads rather than a file this test never opened.
func TestTheLayoutModulesResolveWithoutAnImportMap(t *testing.T) {
	modules := layoutModules(t)

	var names []string
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"budget.js", "compose.js", "engine.js", "worker.js"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("layout modules = %v, want %v; a new module here needs the same relative-import rule", names, want)
	}

	vendored := 0
	for _, name := range names {
		for _, match := range importSpecifier.FindAllStringSubmatch(modules[name], -1) {
			specifier := match[1]
			if !strings.HasPrefix(specifier, "./") && !strings.HasPrefix(specifier, "../") {
				t.Errorf("%s/%s imports %q by bare specifier: a module worker has no import map, "+
					"so this resolves to nothing at load and the page sees a worker that never answers",
					layoutDir, name, specifier)
			}
			if strings.Contains(specifier, "/vendor/") {
				vendored++
			}
		}
	}
	// And the reason the rule exists is really exercised: this directory
	// does reach the vendored runtime, so the guard above is guarding
	// something rather than describing a directory that imports nothing.
	if vendored == 0 {
		t.Error("no layout module imports the vendored runtime; the bare-specifier guard above tests nothing")
	}
}

// TestTheLayoutBudgetIsOneNumberAndItsSentenceIsGenerated pins O10's
// "one exported constant, and the banner's sentence is generated from
// the number".
func TestTheLayoutBudgetIsOneNumberAndItsSentenceIsGenerated(t *testing.T) {
	modules := layoutModules(t)
	budget, ok := modules["budget.js"]
	if !ok {
		t.Fatal("static/layout/budget.js is missing")
	}

	for _, name := range []string{"LAYOUT_BUDGET_MS", "LAYOUT_RETRY_MS"} {
		declarations := strings.Count(budget, "export const "+name+" =")
		if declarations != 1 {
			t.Errorf("%s is declared %d times in budget.js, want exactly 1", name, declarations)
		}
		// And nowhere else under static/: a second declaration is a
		// second budget, and the one a designer waits on would be
		// whichever module the canvas happened to import.
		for path, src := range everyOwnModule(t) {
			if path == layoutDir+"/budget.js" {
				continue
			}
			if strings.Contains(src, "const "+name) {
				t.Errorf("%s declares %s as well; the budget lives in budget.js and nowhere else", path, name)
			}
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
	if !strings.Contains(budget, "${ms / 1000} seconds") {
		t.Error("budgetSentence no longer derives its number from its argument")
	}
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
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("walked no own modules under static/")
	}
	return out
}

// TestTheComposerReadsAStoredPositionInTheSpellingTheServerWrites is the
// seam between Go and JavaScript that nothing else joins.
//
// internal/views.Position carries **no struct tags**, so a run marshals
// a stored position as its Go field names — `EntityType`, `EntityKey`,
// `X`, `Y`, `Pinned` — while views.set_positions *takes* the snake_case
// spelling that internal/web/static/client.js sends. The asymmetry is
// real, shipped, and load-bearing: a composer reading only the
// snake_case names would find no stored position in any envelope and
// would quietly re-arrange every saved view on every load, with no error
// anywhere.
//
// The names are taken from the struct rather than quoted, so adding json
// tags to Position fails here — loudly, in the same commit — instead of
// silently unreading every arrangement in the product.
func TestTheComposerReadsAStoredPositionInTheSpellingTheServerWrites(t *testing.T) {
	encoded, err := json.Marshal(views.Position{})
	if err != nil {
		t.Fatalf("marshal a position: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal a position: %v", err)
	}

	compose, ok := layoutModules(t)["compose.js"]
	if !ok {
		t.Fatal("static/layout/compose.js is missing")
	}

	// UpdatedAt is the one field nothing renders — internal/views'
	// positions.go says so — and the composer has no business reading it.
	read := 0
	for name := range fields {
		if name == "UpdatedAt" {
			continue
		}
		if !strings.Contains(compose, `"`+name+`"`) {
			t.Errorf("a run marshals a stored position with a %q member and compose.js never names it: "+
				"every saved arrangement would be silently ignored", name)
			continue
		}
		read++
	}
	if read == 0 {
		t.Fatal("compose.js reads none of the members a stored position marshals to")
	}
	// The write's spelling is read too, because both really do occur on
	// the wire, and because a fixture in the harness spelling only one of
	// them would prove nothing about the other.
	for _, name := range []string{"entity_type", "entity_key", "pinned"} {
		if !strings.Contains(compose, `"`+name+`"`) {
			t.Errorf("compose.js does not read %q, which is the spelling views.set_positions takes "+
				"and internal/web/static/client.js sends", name)
		}
	}
}
