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
