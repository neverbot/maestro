package testutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestEveryTestDatabaseNameCarriesItsStamp is a source guard, and it is
// here because the defect it prevents cannot be seen from inside any one
// package.
func TestEveryTestDatabaseNameCarriesItsStamp(t *testing.T) {
	const prefix = `"maestro_test_`
	const stamped = `"maestro_test_%d_`

	root, err := filepath.Abs(filepath.Join("..", ".."))
	assert.Must(t, err == nil, "resolve the repository root: %v", err)
	// **Scanned, and counted.** The first version of this walk skipped
	// every directory whose name began with a dot, computed the root as
	// "../.." — whose base is ".." — and therefore skipped the whole
	// repository on its first step: green, scanning nothing, with a
	// deliberately broken name in internal/db sitting right there. The
	// root is absolute now, and the count below is what makes a walk
	// that reaches nothing a failure rather than a pass.
	scanned := 0
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Nothing under a dot directory, and nothing under this
			// package: its fixtures are unstamped names on purpose,
			// because they are what the stamp reader is tested with.
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			if filepath.Clean(path) == filepath.Clean(filepath.Join(root, "internal", "testutil")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		scanned++
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(source), "\n") {
			if !strings.Contains(line, prefix) || strings.Contains(line, stamped) {
				continue
			}
			// Repo-relative, so a failure reads the same on every
			// machine and names nobody's home directory.
			where, relErr := filepath.Rel(root, path)
			if relErr != nil {
				where = path
			}
			t.Errorf("%s:%d builds a test database name with no millisecond stamp:\n\t%s\n"+
				"internal/testutil's sweep drops an unstamped name as a leftover, so another "+
				"package's run will force-drop this database while it is in use",
				where, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	assert.Must(t, err == nil, "walk the repository: %v", err)
	assert.Must(t, scanned >= 100, "this guard read %d Go files; the repository has many more, so it asserted nothing", scanned)
}
