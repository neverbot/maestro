package web_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// **Every module this repository ships must parse as a module.**
func TestEveryModuleParsesAsAModule(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; this guard needs the runtime the browser is closest to")
	}
	modules := ownModules(t)
	assert.Must(t, len(modules) != 0, "no module was examined, so this guard holds nothing")
	for _, module := range modules {
		source, err := os.ReadFile(module)
		assert.Must(t, err == nil, "read %s: %v", module, err)
		cmd := exec.Command("node", "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(string(source))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse as a module:\n%s", module, out)
		}
	}
}

// vendorDir is the one subtree of internal/web/static that is not ours.
const vendorDir = "static/vendor"

// ownModules lists every JavaScript file this project ships, vendored
// ones excluded.
func ownModules(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir("static", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if d.IsDir() {
			if slashed == vendorDir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".js", ".mjs":
			out = append(out, slashed)
		}
		return nil
	})
	assert.Must(t, err == nil, "walk static: %v", err)
	assert.Must(t, len(out) != 0, "found no own module under internal/web/static: this test would pass on an empty tree")
	return out
}

// TestOnlyTheRenderedProseIsInsertedAsMarkup is the policy the jstest
// stub used to hold by throwing: `innerHTML` parses markup, so a module
// that reaches for it is a module where a name an agent wrote could
// become a tag. Two do, and both insert exactly what internal/markdown
// rendered — doc.js a document body, pages/entity.js a longtext field —
// which is goldmark with no html.WithUnsafe and the scheme filter on
// every destination. A third would be a decision, and this is where it
// gets made.
func TestOnlyTheRenderedProseIsInsertedAsMarkup(t *testing.T) {
	t.Parallel()
	inserters := map[string]bool{
		"static/doc.js":          true,
		"static/pages/entity.js": true,
	}
	found := map[string]bool{}
	for _, module := range ownModules(t) {
		source, err := os.ReadFile(module)
		assert.Must(t, err == nil, "read %s: %v", module, err)
		// `.innerHTML`, with the dot: two modules say "never innerHTML" in
		// a comment about themselves, and a guard that read those as uses
		// would report the opposite of what they are.
		if !strings.Contains(string(source), ".innerHTML") {
			continue
		}
		found[module] = true
		assert.Should(t, inserters[module], "%s inserts markup, and only a module that inserts what internal/markdown "+
			"rendered may: either render it there or build the DOM with createElement and textContent", module)
	}
	for module := range inserters {
		assert.Should(t, found[module], "%s is listed as one of this front end's two inserters and does not touch "+
			"innerHTML: the list is stale, and a stale allow-list admits the next module that does", module)
	}
}
