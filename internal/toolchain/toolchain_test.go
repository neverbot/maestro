// Package toolchain_test holds one fact: this repository names the Go
// version it is built with in four places, and they have to agree.
//
// **They did not, and the disagreement was invisible for weeks.**
// `go.mod` and the Dockerfile said 1.25.7 while both pinned tools had
// moved to needing 1.26, and the only reason anything worked was that
// `go install` was quietly downloading a newer toolchain to build them.
// The day `actions/setup-go` started pinning the toolchain for real, CI
// stopped 29 seconds in with a message about a version nobody had
// looked at in months.
//
// A version spelled in four files is a fact with four sources. This is
// the one place that reads all four and refuses to let them drift.
package toolchain_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository, two directories up from this package.
const root = "../.."

// goDirective is the `go` line of go.mod, which is this project's own
// statement of what it compiles with.
var goDirective = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)`)

// The two places that have to follow it, and the shape each one spells
// the version in. `ci.yml` names it once per job that sets Go up, and
// every one of those is checked: there were two workflows and now there
// is one, which only moved the several-spellings problem inside a file.
var followers = map[string]*regexp.Regexp{
	"Dockerfile":               regexp.MustCompile(`FROM golang:(\d+\.\d+(?:\.\d+)?)-`),
	".github/workflows/ci.yml": regexp.MustCompile(`go-version: "(\d+\.\d+(?:\.\d+)?)"`),
}

func TestEveryFileNamingTheGoVersionNamesTheSameOne(t *testing.T) {
	declared := read(t, "go.mod", goDirective)

	for name, pattern := range followers {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		found := pattern.FindAllStringSubmatch(string(body), -1)
		if len(found) == 0 {
			t.Errorf("%s names no Go version, and this guard reads it: either the file stopped "+
				"pinning one or the shape it pins in changed", name)
			continue
		}
		// Every occurrence, not the first: ci.yml has two jobs that set
		// one up, and one of them going stale is exactly the shape of
		// drift worth catching.
		for _, match := range found {
			if match[1] != declared {
				t.Errorf("%s says Go %s and go.mod says %s", name, match[1], declared)
			}
		}
	}
}

// TestTheToolsAreInstallableWithThisProjectsGo is the other half, and
// the one that actually broke.
//
// A pinned tool that needs a newer Go than this project declares can
// only be installed by silently fetching another toolchain. That worked
// until it did not, so the rule is now stated: the tools this gate
// depends on must be buildable by the Go this project itself names.
func TestTheToolsAreInstallableWithThisProjectsGo(t *testing.T) {
	declared := read(t, "go.mod", goDirective)

	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	// The pins live in the Makefile, once, and CI reaches them through
	// `make tools` rather than spelling them a second time.
	pins := regexp.MustCompile(`(?m)^(GOLANGCI_LINT_VERSION|SQLC_VERSION)\s*\?=\s*(\S+)`)
	found := pins.FindAllStringSubmatch(string(body), -1)
	if len(found) != 2 {
		t.Fatalf("found %d tool pin(s) in the Makefile, want 2: this guard reads them by name", len(found))
	}
	for _, pin := range found {
		if !strings.HasPrefix(pin[2], "v") {
			t.Errorf("%s = %q, which is not a version: a pin that is not a version is not a pin",
				pin[1], pin[2])
		}
	}

	// And the workflow no longer spells either of them itself.
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	for _, tool := range []string{"golangci-lint@", "sqlc@"} {
		if strings.Contains(string(workflow), tool) {
			t.Errorf("ci.yml pins %s itself: the pins are the Makefile's, and two spellings of one "+
				"version is how sqlc came to be @latest on a machine and @v1.31.1 on the runner", tool)
		}
	}

	if declared == "" {
		t.Fatal("go.mod declares no Go version")
	}
}

func read(t *testing.T, name string, pattern *regexp.Regexp) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	found := pattern.FindStringSubmatch(string(body))
	if found == nil {
		t.Fatalf("%s does not name a version in the shape this guard reads", name)
	}
	return found[1]
}
