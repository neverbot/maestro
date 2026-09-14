package testutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryTestDatabaseNameCarriesItsStamp is a source guard, and it is
// here because the defect it prevents cannot be seen from inside any one
// package.
//
// internal/testutil's sweep identifies an abandoned test database by the
// millisecond stamp in its name and drops anything without one, on the
// reasoning that a name with no stamp predates the stamp and is
// therefore older than any run in flight. `go test ./...` runs one
// process per package, so that sweep runs while other packages' tests
// are using their own databases — and one package built its name without
// a stamp. Another package's sweep force-dropped it mid-migration, and
// the failure arrived as "terminating connection due to administrator
// command" inside a migration, in a package that had nothing to do with
// the sweep, only under a full-suite run.
//
// No test can catch that from where it happens, so the rule is held over
// the source: **every name that begins maestro_test_ is built with the
// stamp**. The exception is this package's own files, where the
// unstamped spelling is a fixture for the parser that reads the stamp.
//
// Mutation: drop the `%d` from the name in internal/db's
// migrate_internal_test.go and this fails naming that file.
func TestEveryTestDatabaseNameCarriesItsStamp(t *testing.T) {
	const prefix = `"maestro_test_`
	const stamped = `"maestro_test_%d_`

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
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
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	if scanned < 100 {
		t.Fatalf("this guard read %d Go files; the repository has many more, so it asserted nothing", scanned)
	}
}
