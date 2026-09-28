package web_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// needsAFixture is run by the test that builds what it reads, not here.
var needsAFixture = map[string]bool{"seeded_game_page_test.mjs": true}

// TestTheBrowserHarnesses runs every harness under jstest/, discovered
// rather than listed: a harness added to that directory and to no Go
// test would run nowhere, which is how this repository's front end used
// to lose coverage silently.
func TestTheBrowserHarnesses(t *testing.T) {
	t.Parallel()
	nodeOrSkip(t)
	scripts, err := filepath.Glob("jstest/*_test.mjs")
	assert.NoErr(t, err, "glob jstest")
	assert.Must(t, len(scripts) > 20, "found %d harness(es) under jstest/: the directory moved and this test did not", len(scripts))

	for _, script := range scripts {
		if needsAFixture[filepath.Base(script)] {
			continue
		}
		t.Run(strings.TrimSuffix(filepath.Base(script), "_test.mjs"), func(t *testing.T) {
			t.Parallel()
			runJSTest(t, script)
		})
	}
}

// runJSTest runs one harness under Node and reports its output.
func runJSTest(t *testing.T, script string) {
	t.Helper()
	out, err := exec.Command("node", script).CombinedOutput()
	assert.Must(t, err == nil, "%s failed: %v\n%s", script, err, out)
	t.Log(string(out))
}

// nodeOrSkip skips rather than fails when Node is not on PATH: this
// product has no JavaScript build step, so `go test ./internal/web/`
// must still run without it — it just does not get these checks.
func nodeOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
}
